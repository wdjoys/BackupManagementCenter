// Package pipeline executes one dispatched operation end-to-end on the agent:
// adapter work plus the surrounding restic invocation. The agent transport
// layer calls Execute; adapters are selected by task kind.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	bmcv1 "backupmanagementcenter/api/proto/v1"
	"backupmanagementcenter/internal/agent/backup"
	"backupmanagementcenter/internal/agent/rclone"
	"backupmanagementcenter/internal/agent/restic"
	"backupmanagementcenter/internal/model"
)

// PipelineError carries a stable error code for the agent runner.
type PipelineError struct {
	Code     string
	Message  string
	Cause    error
	ExitCode int // restic exit code when available
}

func (e *PipelineError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

func (e *PipelineError) Unwrap() error { return e.Cause }

// Deps are process-wide facilities provided by the agent runtime.
type Deps struct {
	Tools               map[string]model.ToolInfo
	Exec                backup.Executor
	Logf                func(level, format string, args ...any)
	Progress            func(model.Progress)
	SourceRoots         []string
	RestoreRoots        []string
	ScratchMinFreeBytes int64
	MaxConcurrency      int
}

// Result mirrors proto RunResult payload fields produced by successful ops.
type Result struct {
	SnapshotIDs []string
	ResultJSON  []byte
}

// Execute runs the operation synchronously. tempDir is private and already
// created; the caller wipes it afterwards regardless of outcome.
func Execute(ctx context.Context, d Deps, tempDir string, op bmcv1.ExecuteCommand_Operation, params []byte, secrets backup.SecretBundle) (*Result, error) {
	// Tell the backup package which tool paths to use
	backup.SetToolPaths(d.Tools)

	switch op {
	case bmcv1.ExecuteCommand_BACKUP:
		return runBackup(ctx, d, tempDir, params, secrets)
	case bmcv1.ExecuteCommand_RESTORE:
		return runRestore(ctx, d, tempDir, params, secrets, false)
	case bmcv1.ExecuteCommand_RESTORE_DRY_RUN:
		return runRestore(ctx, d, tempDir, params, secrets, true)
	case bmcv1.ExecuteCommand_CHECK:
		return runCheck(ctx, d, tempDir, params, secrets)
	case bmcv1.ExecuteCommand_FORGET:
		return runForget(ctx, d, tempDir, params, secrets)
	case bmcv1.ExecuteCommand_SNAPSHOTS:
		return runSnapshots(ctx, d, tempDir, params, secrets)
	case bmcv1.ExecuteCommand_SNAPSHOT_LS:
		return runSnapshotLs(ctx, d, tempDir, params, secrets)
	case bmcv1.ExecuteCommand_VERIFY_STORAGE_REMOTE:
		return runVerifyRemote(ctx, d, tempDir, params, secrets)
	case bmcv1.ExecuteCommand_VALIDATE_PATHS:
		return runValidatePaths(ctx, d, tempDir, params, secrets)
	case bmcv1.ExecuteCommand_PROBE_CAPABILITIES:
		return runProbeCapabilities(ctx, d, tempDir, params, secrets)
	default:
		return nil, ErrUnsupportedOperation
	}
}

// toolExe returns the absolute path of a tool, or the bare name as fallback.
func toolExe(d Deps, name string) string {
	info, ok := d.Tools[name]
	if ok && info.Path != "" {
		return info.Path
	}
	return name
}

// runBackup handles OPERATION_BACKUP.
func runBackup(ctx context.Context, d Deps, tempDir string, params []byte, secrets backup.SecretBundle) (*Result, error) {
	var task model.BackupTask
	if err := json.Unmarshal(params, &task); err != nil {
		return nil, &PipelineError{Code: "invalid_params", Message: "unmarshal backup task", Cause: err}
	}

	adapter, ok := backup.For(task.Kind)
	if !ok {
		return nil, &PipelineError{Code: "invalid_plan", Message: "unknown kind: " + task.Kind}
	}
	if task.Kind == "filesystem" || task.Kind == "sqlite" {
		paths := task.Source.Paths
		if task.Kind == "sqlite" {
			paths = []string{task.Source.Path}
		}
		if err := validateAllowedPaths(paths, d.SourceRoots, false); err != nil {
			return nil, &PipelineError{Code: "path_not_allowed", Message: "source path is outside configured allowlist", Cause: err}
		}
	}

	spec := backup.PlanSpec{Kind: task.Kind, Source: task.Source, AgentID: ""}
	if err := adapter.Validate(ctx, spec); err != nil {
		return nil, &PipelineError{Code: "invalid_plan", Message: "validation failed", Cause: err}
	}

	// Space check for database kinds
	if task.Kind != "filesystem" {
		const maxLogicalBackupBytes int64 = 100 << 30
		estimated := task.Source.EstimatedDumpBytes
		if task.Kind == "sqlite" && estimated <= 0 {
			if info, statErr := os.Stat(task.Source.Path); statErr == nil {
				estimated = info.Size()
			}
		}
		if estimated > maxLogicalBackupBytes {
			return nil, &PipelineError{Code: model.ErrPhysicalBackupRequired, Message: "logical backup exceeds 100 GiB; physical/incremental backup required"}
		}
		if estimated > 0 {
			required := estimated * 13 / 10
			if d.ScratchMinFreeBytes > required {
				required = d.ScratchMinFreeBytes
			}
			if err := checkTempSpace(tempDir, required); err != nil {
				return nil, err
			}
		}
	}

	rc := &backup.RunContext{
		RunID:    "",
		Task:     task,
		Secrets:  secrets,
		TempDir:  tempDir,
		Exec:     d.Exec,
		Logf:     d.Logf,
		Progress: d.Progress,
	}

	artifact, err := adapter.Backup(ctx, rc)
	if err != nil {
		return nil, &PipelineError{Code: "backup_failed", Message: "adapter backup failed", Cause: err}
	}

	resticOpts, err := newResticOpts(d, task.Repository.RepositoryPath, tempDir, secrets)
	if err != nil {
		return nil, err
	}

	var snapshotID string
	// A retry after an agent disconnect may have completed the upload but lost
	// its terminal response. The run tag makes the backup idempotent: reuse the
	// existing snapshot instead of creating a duplicate.
	for _, tag := range task.Tags {
		if strings.HasPrefix(tag, "run:") {
			if snapshots, snapErr := restic.Snapshots(ctx, d.Exec, resticOpts); snapErr == nil {
				for _, snap := range snapshots {
					for _, existingTag := range snap.Tags {
						if existingTag == tag {
							snapshotID = snap.ID
							break
						}
					}
					if snapshotID != "" {
						break
					}
				}
			}
			break
		}
	}
	if snapshotID != "" {
		if task.Kind != "filesystem" && artifact.StagingDir != "" {
			_ = os.RemoveAll(artifact.StagingDir)
		}
		return &Result{SnapshotIDs: []string{snapshotID}}, nil
	}
	if task.Kind == "filesystem" {
		_, snapshotID, err = restic.Backup(ctx, d.Exec, resticOpts, artifact.LivePaths, artifact.ExcludeFile, task.Tags, artifact.OneFileSystem, d.Progress)
	} else {
		resticOpts.WorkingDir = artifact.StagingDir
		_, snapshotID, err = restic.Backup(ctx, d.Exec, resticOpts, []string{"."}, "", task.Tags, false, d.Progress)
	}
	if err != nil {
		return nil, &PipelineError{Code: "backup_failed", Message: "restic backup failed", Cause: err}
	}

	if task.Kind != "filesystem" && artifact.StagingDir != "" {
		os.RemoveAll(artifact.StagingDir)
	}

	return &Result{SnapshotIDs: []string{snapshotID}}, nil
}

// runRestore handles OPERATION_RESTORE and OPERATION_RESTORE_DRY_RUN.
func runRestore(ctx context.Context, d Deps, tempDir string, params []byte, secrets backup.SecretBundle, dryRun bool) (*Result, error) {
	var task model.RestoreTask
	if err := json.Unmarshal(params, &task); err != nil {
		return nil, &PipelineError{Code: "invalid_params", Message: "unmarshal restore task", Cause: err}
	}

	resticOpts, err := newResticOpts(d, task.Repository.RepositoryPath, tempDir, secrets)
	if err != nil {
		return nil, err
	}

	if task.Kind == "filesystem" {
		return runFilesystemRestore(ctx, d, resticOpts, task, dryRun)
	}
	return runDatabaseRestore(ctx, d, resticOpts, task, tempDir, secrets, dryRun)
}

// runFilesystemRestore handles filesystem restore/dry-run.
func runFilesystemRestore(ctx context.Context, d Deps, opts restic.Options, task model.RestoreTask, dryRun bool) (*Result, error) {
	fs := task.Filesystem
	if fs == nil {
		return nil, &PipelineError{Code: "invalid_params", Message: "missing filesystem restore spec"}
	}
	if err := validateAllowedPaths([]string{fs.TargetPath}, d.RestoreRoots, true); err != nil {
		return nil, &PipelineError{Code: "path_not_allowed", Message: "restore target is outside configured allowlist", Cause: err}
	}

	if dryRun {
		prog, err := restic.RestoreDryRunWithOverwrite(ctx, d.Exec, opts, fs.SnapshotID, fs.TargetPath, fs.IncludePaths, fs.OverwriteMode)
		if err != nil {
			return nil, &PipelineError{Code: "restore_failed", Message: "dry run failed", Cause: err}
		}
		resultJSON, _ := json.Marshal(map[string]any{
			"add":     prog.FilesAdded,
			"changed": prog.FilesChanged,
			"skipped": prog.FilesSkipped,
			"delete":  prog.FilesDeleted,
			"sample":  prog.Sample,
		})
		return &Result{ResultJSON: resultJSON}, nil
	}

	if fs.OverwriteMode == "never" {
		if entries, err := os.ReadDir(fs.TargetPath); err == nil && len(entries) > 0 {
			return nil, &PipelineError{Code: "restore_target_not_empty", Message: "target path not empty and overwrite_mode=never"}
		}
	}

	if err := restic.RestoreWithOverwrite(ctx, d.Exec, opts, fs.SnapshotID, fs.TargetPath, fs.IncludePaths, fs.OverwriteMode); err != nil {
		return nil, &PipelineError{Code: "restore_failed", Message: "restore failed", Cause: err}
	}
	if err := validateRestoredSymlinks(fs.TargetPath); err != nil {
		return nil, &PipelineError{Code: "path_not_allowed", Message: "restored symlink escapes target root", Cause: err}
	}
	return &Result{}, nil
}

// runDatabaseRestore handles database restore/dry-run.
func runDatabaseRestore(ctx context.Context, d Deps, opts restic.Options, task model.RestoreTask, tempDir string, secrets backup.SecretBundle, dryRun bool) (*Result, error) {
	db := task.Database
	if db == nil {
		return nil, &PipelineError{Code: "invalid_params", Message: "missing database restore spec"}
	}
	if task.Kind == "sqlite" {
		if err := validateAllowedPaths([]string{db.TargetDatabase}, d.RestoreRoots, true); err != nil {
			return nil, &PipelineError{Code: "path_not_allowed", Message: "sqlite restore target is outside configured allowlist", Cause: err}
		}
	}

	adapter, ok := backup.For(task.Kind)
	if !ok {
		return nil, &PipelineError{Code: "invalid_plan", Message: "unknown kind: " + task.Kind}
	}

	stagingDir := filepath.Join(tempDir, "restore_staging")
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return nil, &PipelineError{Code: "internal", Message: "mkdir staging", Cause: err}
	}

	if err := restic.Restore(ctx, d.Exec, opts, db.SnapshotID, stagingDir, nil); err != nil {
		return nil, &PipelineError{Code: "restore_failed", Message: "restic restore snapshot failed", Cause: err}
	}

	manifestPath, artifactRoot, err := findRestoredManifest(stagingDir)
	if err != nil {
		return nil, &PipelineError{Code: "restore_verification_failed", Message: "locate manifest failed", Cause: err}
	}
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, &PipelineError{Code: "restore_verification_failed", Message: "read manifest failed", Cause: err}
	}
	var manifest backup.Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, &PipelineError{Code: "restore_verification_failed", Message: "unmarshal manifest", Cause: err}
	}
	if manifest.Adapter != task.Kind {
		return nil, &PipelineError{Code: "restore_verification_failed", Message: "manifest adapter mismatch: " + manifest.Adapter}
	}
	for _, dbe := range manifest.Databases {
		if dbe.File == "" || dbe.File == ".." || filepath.IsAbs(dbe.File) || filepath.Clean(dbe.File) != dbe.File || strings.HasPrefix(dbe.File, ".."+string(filepath.Separator)) {
			return nil, &PipelineError{Code: "restore_verification_failed", Message: "manifest contains an unsafe artifact path"}
		}
		artifactPath := filepath.Join(artifactRoot, dbe.File)
		if info, statErr := os.Stat(artifactPath); statErr != nil || !info.Mode().IsRegular() {
			return nil, &PipelineError{Code: "restore_verification_failed", Message: "manifest artifact is missing or not a regular file"}
		}
	}

	if dryRun {
		resultJSON, _ := json.Marshal(map[string]any{
			"databases": manifest.Databases,
			"adapter":   manifest.Adapter,
		})
		return &Result{ResultJSON: resultJSON}, nil
	}

	restoreSpec := &backup.RestoreSpec{
		SnapshotID: db.SnapshotID,
		Kind:       task.Kind,
		StagingDir: artifactRoot,
		Database:   db,
		Secrets:    secrets,
		Tools:      d.Tools,
		Logf:       d.Logf,
		Progress:   d.Progress,
		Exec:       d.Exec,
	}
	if err := adapter.Restore(ctx, restoreSpec); err != nil {
		return nil, &PipelineError{Code: "restore_verification_failed", Message: "adapter restore failed", Cause: err}
	}

	os.RemoveAll(stagingDir)
	return &Result{}, nil
}

func findRestoredManifest(root string) (manifestPath, artifactRoot string, err error) {
	var found string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() != "manifest.json" || !entry.Type().IsRegular() {
			return nil
		}
		if found != "" {
			return fmt.Errorf("multiple manifest.json files in restored snapshot")
		}
		found = path
		return nil
	})
	if err != nil {
		return "", "", err
	}
	if found == "" {
		return "", "", fmt.Errorf("manifest.json not found under %s", root)
	}
	return found, filepath.Dir(found), nil
}

// validateAllowedPaths rejects paths that escape the explicitly configured
// source/restore roots. An empty allowlist preserves local-development
// compatibility; production Docker deployments must configure both lists.
func validateAllowedPaths(paths, roots []string, allowMissing bool) error {
	for _, p := range paths {
		if p == "" {
			return fmt.Errorf("empty path")
		}
		abs, err := filepath.Abs(filepath.Clean(p))
		if err != nil {
			return err
		}
		clean := filepath.ToSlash(filepath.Clean(abs))
		volumeRoot := filepath.VolumeName(abs) + string(filepath.Separator)
		if clean == "/" || (volumeRoot != string(filepath.Separator) && filepath.Clean(abs) == filepath.Clean(volumeRoot)) || strings.HasSuffix(clean, "/var/run/docker.sock") || clean == "/docker.sock" {
			return fmt.Errorf("forbidden path: %s", p)
		}
	}
	if len(roots) == 0 {
		return nil
	}
	cleanRoots := make([]string, 0, len(roots))
	for _, root := range roots {
		abs, err := filepath.Abs(filepath.Clean(root))
		if err != nil {
			return err
		}
		rootClean := filepath.ToSlash(filepath.Clean(abs))
		rootVolume := filepath.VolumeName(abs) + string(filepath.Separator)
		if rootClean == "/" || (rootVolume != string(filepath.Separator) && filepath.Clean(abs) == filepath.Clean(rootVolume)) || strings.HasSuffix(rootClean, "/var/run/docker.sock") || rootClean == "/docker.sock" {
			return fmt.Errorf("forbidden allowlist root: %s", root)
		}
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			abs = real
		}
		cleanRoots = append(cleanRoots, filepath.Clean(abs))
	}
	for _, p := range paths {
		if p == "" {
			return fmt.Errorf("empty path")
		}
		abs, err := filepath.Abs(filepath.Clean(p))
		if err != nil {
			return err
		}
		resolved, err := resolvePathForCheck(abs, allowMissing)
		if err != nil {
			return err
		}
		abs = resolved
		if abs == filepath.VolumeName(abs)+string(filepath.Separator) {
			return fmt.Errorf("root path is not allowed: %s", p)
		}
		ok := false
		for _, root := range cleanRoots {
			rel, err := filepath.Rel(root, abs)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("path %q is outside configured roots", p)
		}
	}
	return nil
}

func validateRestoredSymlinks(root string) error {
	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return err
	}
	if real, evalErr := filepath.EvalSymlinks(rootAbs); evalErr == nil {
		rootAbs = real
	}
	return filepath.WalkDir(rootAbs, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink == 0 {
			return nil
		}
		target, readErr := os.Readlink(path)
		if readErr != nil {
			return readErr
		}
		resolved := target
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(filepath.Dir(path), resolved)
		}
		resolved, err = filepath.Abs(filepath.Clean(resolved))
		if err != nil {
			return err
		}
		if real, evalErr := filepath.EvalSymlinks(path); evalErr == nil {
			resolved = real
		} else {
			_ = os.Remove(path)
			return fmt.Errorf("symlink %q target cannot be resolved: %w", path, evalErr)
		}
		rel, relErr := filepath.Rel(rootAbs, resolved)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			// Remove only the newly restored link; never follow it and never
			// delete a target outside the restore root.
			_ = os.Remove(path)
			return fmt.Errorf("symlink %q points outside restore root", path)
		}
		return nil
	})
}

func resolvePathForCheck(abs string, allowMissing bool) (string, error) {
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(real), nil
	} else if !allowMissing {
		return "", err
	}
	// Resolve the nearest existing parent so a symlinked directory cannot
	// escape the allowlist even when the final restore file is new.
	missing := []string{}
	cur := abs
	for {
		if _, err := os.Lstat(cur); err == nil {
			real, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", err
			}
			for i := len(missing) - 1; i >= 0; i-- {
				real = filepath.Join(real, missing[i])
			}
			return filepath.Clean(real), nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs, nil
		}
		missing = append(missing, filepath.Base(cur))
		cur = parent
	}
}

// runCheck runs restic check.
func runCheck(ctx context.Context, d Deps, tempDir string, params []byte, secrets backup.SecretBundle) (*Result, error) {
	var task model.CheckTask
	if err := json.Unmarshal(params, &task); err != nil {
		return nil, &PipelineError{Code: "invalid_params", Message: "unmarshal check task", Cause: err}
	}
	opts, err := newResticOpts(d, task.Repository.RepositoryPath, tempDir, secrets)
	if err != nil {
		return nil, err
	}
	if err := restic.Check(ctx, d.Exec, opts); err != nil {
		return nil, &PipelineError{Code: "check_failed", Message: "restic check failed", Cause: err}
	}
	resultJSON, _ := json.Marshal(map[string]bool{"checked": true})
	return &Result{ResultJSON: resultJSON}, nil
}

// runForget runs restic forget (without prune) with retention policy, or restic init if
// params is an InitTask (ResticInit=true). InitTask uses no Tags field.
func runForget(ctx context.Context, d Deps, tempDir string, params []byte, secrets backup.SecretBundle) (*Result, error) {
	// Try InitTask first
	var initTask model.InitTask
	if err := json.Unmarshal(params, &initTask); err == nil && initTask.ResticInit {
		opts, err := newResticOpts(d, initTask.Repository.RepositoryPath, tempDir, secrets)
		if err != nil {
			return nil, err
		}
		if err := restic.Init(ctx, d.Exec, opts); err != nil {
			return nil, &PipelineError{Code: "init_failed", Message: "restic init failed", Cause: err}
		}
		resultJSON, _ := json.Marshal(map[string]bool{"initialized": true})
		return &Result{ResultJSON: resultJSON}, nil
	}

	// Regular ForgetTask (retention pruning)
	var task model.ForgetTask
	if err := json.Unmarshal(params, &task); err != nil {
		return nil, &PipelineError{Code: "invalid_params", Message: "unmarshal forget task", Cause: err}
	}
	opts, err := newResticOpts(d, task.Repository.RepositoryPath, tempDir, secrets)
	if err != nil {
		return nil, err
	}
	if err := restic.ForgetOnly(ctx, d.Exec, opts, task.Retention, nil); err != nil {
		return nil, &PipelineError{Code: "forget_failed", Message: "restic forget failed", Cause: err}
	}
	return &Result{}, nil
}

// runSnapshots runs restic snapshots --json.
func runSnapshots(ctx context.Context, d Deps, tempDir string, params []byte, secrets backup.SecretBundle) (*Result, error) {
	var task model.SnapshotsTask
	if err := json.Unmarshal(params, &task); err != nil {
		return nil, &PipelineError{Code: "invalid_params", Message: "unmarshal snapshots task", Cause: err}
	}
	opts, err := newResticOpts(d, task.Repository.RepositoryPath, tempDir, secrets)
	if err != nil {
		return nil, err
	}
	snaps, err := restic.Snapshots(ctx, d.Exec, opts)
	if err != nil {
		return nil, &PipelineError{Code: "snapshots_failed", Message: "restic snapshots failed", Cause: err}
	}
	resultJSON, _ := json.Marshal(snaps)
	return &Result{ResultJSON: resultJSON}, nil
}

// runSnapshotLs runs restic ls <snapshot> --json.
func runSnapshotLs(ctx context.Context, d Deps, tempDir string, params []byte, secrets backup.SecretBundle) (*Result, error) {
	var task model.SnapshotLsTask
	if err := json.Unmarshal(params, &task); err != nil {
		return nil, &PipelineError{Code: "invalid_params", Message: "unmarshal snapshot_ls task", Cause: err}
	}
	opts, err := newResticOpts(d, task.Repository.RepositoryPath, tempDir, secrets)
	if err != nil {
		return nil, err
	}
	entries, roots, err := restic.Ls(ctx, d.Exec, opts, task.SnapshotID)
	if err != nil {
		return nil, &PipelineError{Code: "snapshot_ls_failed", Message: "restic ls failed", Cause: err}
	}

	// Normalize helper: restic node paths look like "/D/tmp/x" on Windows and
	// "/etc/x" on Linux.
	norm := func(p string) string {
		p = strings.ReplaceAll(p, "\\", "/")
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		// Restic renders Windows drive paths as /D/tmp/... while the
		// snapshot descriptor retains D:/tmp/.... Normalize both forms.
		if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
			p = p[:2] + p[3:]
		}
		return strings.TrimSuffix(p, "/")
	}

	dir := norm(task.Path)
	var parentDirs []string
	if dir == "" || dir == "/" {
		// Snapshot root listing: children of the backup source roots.
		for _, r := range roots {
			parentDirs = append(parentDirs, norm(r))
		}
	} else {
		parentDirs = []string{dir}
	}

	filtered := make([]restic.SnapshotEntry, 0, len(entries))
	seen := map[string]bool{}
	for _, e := range entries {
		parent := path.Dir(norm(e.Path))
		ok := false
		for _, pd := range parentDirs {
			if parent == pd {
				ok = true
				break
			}
		}
		if !ok || seen[e.Path] {
			continue
		}
		seen[e.Path] = true
		e.Path = norm(e.Path)
		filtered = append(filtered, e)
	}

	resultJSON, _ := json.Marshal(map[string]any{
		"entries": filtered,
		"path":    dir,
	})
	return &Result{ResultJSON: resultJSON}, nil
}

// verifyDeadline bounds one verify-remote run (listremotes + lsd). It must
// stay below the server's verifyRemoteWait so the rclone stderr tail reaches
// the user as a storage_remote_unreachable failure rather than a wait timeout.
const verifyDeadline = 100 * time.Second

// runVerifyRemote validates rclone remote.
//
// The whole verify is bounded by verifyDeadline: a remote that black-holes
// (unreachable endpoint, DNS hang) would otherwise keep rclone running until
// the server watchdog force-fails the run with a generic timeout. Killing at
// verifyDeadline — slightly below the server's verifyRemoteWait — lets the
// captured rclone stderr surface as storage_remote_unreachable instead.
func runVerifyRemote(ctx context.Context, d Deps, tempDir string, params []byte, secrets backup.SecretBundle) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, verifyDeadline)
	defer cancel()
	var task model.VerifyRemoteTask
	if err := json.Unmarshal(params, &task); err != nil {
		return nil, &PipelineError{Code: "invalid_params", Message: "unmarshal verify remote task", Cause: err}
	}
	if !task.ConfigProvided {
		return nil, &PipelineError{Code: "invalid_params", Message: "config_provided must be true"}
	}
	confPath, err := rclone.WriteConf(tempDir, secrets.RcloneConf)
	if err != nil {
		return nil, &PipelineError{Code: "internal", Message: "write rclone conf", Cause: err}
	}
	remotes, err := rclone.ListRemotes(ctx, d.Exec, confPath)
	if err != nil {
		return nil, &PipelineError{Code: "storage_remote_unreachable", Message: "listremotes failed", Cause: err}
	}
	found := false
	remoteType := task.RemoteName
	for _, r := range remotes {
		if r == task.RemoteName {
			found = true
			break
		}
	}
	if !found {
		return nil, &PipelineError{Code: "storage_remote_unreachable", Message: "remote not found: " + task.RemoteName}
	}
	entries, err := rclone.Lsd(ctx, d.Exec, confPath, task.RemoteName)
	if err != nil {
		return nil, &PipelineError{Code: "storage_remote_unreachable", Message: "lsd failed", Cause: err}
	}
	resultJSON, _ := json.Marshal(map[string]any{
		"remote_type": remoteType,
		"entries":     len(entries),
	})
	return &Result{ResultJSON: resultJSON}, nil
}

// runValidatePaths validates filesystem paths via the filesystem adapter.
func runValidatePaths(ctx context.Context, d Deps, tempDir string, params []byte, secrets backup.SecretBundle) (*Result, error) {
	var task model.ValidatePathsTask
	if err := json.Unmarshal(params, &task); err != nil {
		return nil, &PipelineError{Code: "invalid_params", Message: "unmarshal validate paths task", Cause: err}
	}
	if err := validateAllowedPaths(task.Paths, d.SourceRoots, false); err != nil {
		return nil, &PipelineError{Code: "path_not_allowed", Message: "source path is outside configured allowlist", Cause: err}
	}
	adapter, ok := backup.For("filesystem")
	if !ok {
		return nil, &PipelineError{Code: "invalid_plan", Message: "filesystem adapter not found"}
	}
	spec := backup.PlanSpec{
		Kind: "filesystem",
		Source: model.PlanSource{
			Paths:    task.Paths,
			Excludes: task.Excludes,
		},
	}
	if err := adapter.Validate(ctx, spec); err != nil {
		return nil, &PipelineError{Code: "path_validation_failed", Message: "path validation failed", Cause: err}
	}
	return &Result{}, nil
}

// runProbeCapabilities returns the current tool info.
func runProbeCapabilities(ctx context.Context, d Deps, tempDir string, params []byte, secrets backup.SecretBundle) (*Result, error) {
	resultJSON, _ := json.Marshal(d.Tools)
	return &Result{ResultJSON: resultJSON}, nil
}

type unsupportedError struct{}

func (unsupportedError) Error() string { return "pipeline: unsupported operation" }

func unsupportedOpErr() error { return unsupportedError{} }

// ErrUnsupportedOperation is returned for ops without an adapter.
var ErrUnsupportedOperation = unsupportedOpErr()

// newResticOpts writes the per-run secret files (restic password and, when
// provided, the rclone config) into tempDir and returns ready-to-use options.
func newResticOpts(d Deps, repoPath, tempDir string, secrets backup.SecretBundle) (restic.Options, error) {
	pwPath, err := backup.WriteSecretFile(tempDir, "restic_pw", secrets.ResticPassword)
	if err != nil {
		return restic.Options{}, &PipelineError{Code: "internal", Message: "write restic password", Cause: err}
	}
	opts := restic.Options{
		Exe:          toolExe(d, "restic"),
		RepoPath:     restic.NormalizeRepoPath(repoPath),
		PasswordFile: pwPath,
	}
	if secrets.RcloneConf != "" {
		confPath, err := rclone.WriteConf(tempDir, secrets.RcloneConf)
		if err != nil {
			return restic.Options{}, &PipelineError{Code: "internal", Message: "write rclone conf", Cause: err}
		}
		opts.RcloneConfFile = confPath
	}
	return opts, nil
}
