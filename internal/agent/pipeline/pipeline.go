// Package pipeline executes one dispatched operation end-to-end on the agent:
// adapter work plus the surrounding restic invocation. The agent transport
// layer calls Execute; adapters are selected by task kind.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	bmcv1 "backupmanagementcenter/api/proto/v1"
	"backupmanagementcenter/internal/agent/backup"
	"backupmanagementcenter/internal/agent/restic"
	"backupmanagementcenter/internal/agent/rclone"
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
	Tools    map[string]model.ToolInfo
	Exec     backup.Executor
	Logf     func(level, format string, args ...any)
	Progress func(model.Progress)
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

	spec := backup.PlanSpec{Kind: task.Kind, Source: task.Source, AgentID: ""}
	if err := adapter.Validate(ctx, spec); err != nil {
		return nil, &PipelineError{Code: "invalid_plan", Message: "validation failed", Cause: err}
	}

	// Space check for database kinds
	if task.Kind != "filesystem" && task.Source.EstimatedDumpBytes > 0 {
		required := task.Source.EstimatedDumpBytes * 12 / 10
		if err := checkTempSpace(tempDir, required); err != nil {
			return nil, err
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

	resticOpts := restic.Options{
		Exe:         toolExe(d, "restic"),
		RepoPath:    task.Repository.RepositoryPath,
		PasswordFile: filepath.Join(tempDir, "restic_pw"),
		CacheDir:    task.Repository.CacheDir,
	}
	if _, err := backup.WriteSecretFile(tempDir, "restic_pw", secrets.ResticPassword); err != nil {
		return nil, &PipelineError{Code: "internal", Message: "write restic password", Cause: err}
	}

	var snapshotID string
	if task.Kind == "filesystem" {
		_, snapshotID, err = restic.Backup(ctx, d.Exec, resticOpts, artifact.LivePaths, artifact.ExcludeFile, task.Tags, artifact.OneFileSystem, d.Progress)
	} else {
		_, snapshotID, err = restic.Backup(ctx, d.Exec, resticOpts, []string{artifact.StagingDir}, "", task.Tags, false, d.Progress)
	}
	if err != nil {
		return nil, &PipelineError{Code: "backup_failed", Message: "restic backup failed", Cause: err}
	}

	if task.Kind != "filesystem" && artifact.StagingDir != "" {
		os.RemoveAll(artifact.StagingDir)
	}

	if task.Retention.KeepLast > 0 || task.Retention.KeepDaily > 0 || task.Retention.KeepWeekly > 0 || task.Retention.KeepMonthly > 0 {
		if err := restic.Forget(ctx, d.Exec, resticOpts, task.Retention, task.Tags); err != nil {
			d.Logf("warn", "retention forget failed: %v", err)
		}
	}

	return &Result{SnapshotIDs: []string{snapshotID}}, nil
}

// runRestore handles OPERATION_RESTORE and OPERATION_RESTORE_DRY_RUN.
func runRestore(ctx context.Context, d Deps, tempDir string, params []byte, secrets backup.SecretBundle, dryRun bool) (*Result, error) {
	var task model.RestoreTask
	if err := json.Unmarshal(params, &task); err != nil {
		return nil, &PipelineError{Code: "invalid_params", Message: "unmarshal restore task", Cause: err}
	}

	resticOpts := restic.Options{
		Exe:         toolExe(d, "restic"),
		RepoPath:    task.Repository.RepositoryPath,
		PasswordFile: filepath.Join(tempDir, "restic_pw"),
		CacheDir:    task.Repository.CacheDir,
	}
	if _, err := backup.WriteSecretFile(tempDir, "restic_pw", secrets.ResticPassword); err != nil {
		return nil, &PipelineError{Code: "internal", Message: "write restic password", Cause: err}
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

	if dryRun {
		prog, err := restic.RestoreDryRun(ctx, d.Exec, opts, fs.SnapshotID, fs.TargetPath, fs.IncludePaths)
		if err != nil {
			return nil, &PipelineError{Code: "restore_failed", Message: "dry run failed", Cause: err}
		}
		resultJSON, _ := json.Marshal(prog)
		return &Result{ResultJSON: resultJSON}, nil
	}

	if fs.OverwriteMode == "never" {
		if entries, err := os.ReadDir(fs.TargetPath); err == nil && len(entries) > 0 {
			return nil, &PipelineError{Code: "restore_target_not_empty", Message: "target path not empty and overwrite_mode=never"}
		}
	}

	if err := restic.Restore(ctx, d.Exec, opts, fs.SnapshotID, fs.TargetPath, fs.IncludePaths); err != nil {
		return nil, &PipelineError{Code: "restore_failed", Message: "restore failed", Cause: err}
	}
	return &Result{}, nil
}

// runDatabaseRestore handles database restore/dry-run.
func runDatabaseRestore(ctx context.Context, d Deps, opts restic.Options, task model.RestoreTask, tempDir string, secrets backup.SecretBundle, dryRun bool) (*Result, error) {
	db := task.Database
	if db == nil {
		return nil, &PipelineError{Code: "invalid_params", Message: "missing database restore spec"}
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

	manifestPath := filepath.Join(stagingDir, "manifest.json")
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
		StagingDir: stagingDir,
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

// runCheck runs restic check.
func runCheck(ctx context.Context, d Deps, tempDir string, params []byte, secrets backup.SecretBundle) (*Result, error) {
	var task model.CheckTask
	if err := json.Unmarshal(params, &task); err != nil {
		return nil, &PipelineError{Code: "invalid_params", Message: "unmarshal check task", Cause: err}
	}
	opts := restic.Options{
		Exe:         toolExe(d, "restic"),
		RepoPath:    task.Repository.RepositoryPath,
		PasswordFile: filepath.Join(tempDir, "restic_pw"),
		CacheDir:    task.Repository.CacheDir,
	}
	if _, err := backup.WriteSecretFile(tempDir, "restic_pw", secrets.ResticPassword); err != nil {
		return nil, &PipelineError{Code: "internal", Message: "write restic password", Cause: err}
	}
	if err := restic.Check(ctx, d.Exec, opts); err != nil {
		return nil, &PipelineError{Code: "check_failed", Message: "restic check failed", Cause: err}
	}
	resultJSON, _ := json.Marshal(map[string]bool{"checked": true})
	return &Result{ResultJSON: resultJSON}, nil
}

// runForget runs restic forget with retention policy, or restic init if
// params is an InitTask (ResticInit=true). InitTask uses no Tags field.
func runForget(ctx context.Context, d Deps, tempDir string, params []byte, secrets backup.SecretBundle) (*Result, error) {
	// Try InitTask first
	var initTask model.InitTask
	if err := json.Unmarshal(params, &initTask); err == nil && initTask.ResticInit {
		opts := restic.Options{
			Exe:         toolExe(d, "restic"),
			RepoPath:    initTask.Repository.RepositoryPath,
			PasswordFile: filepath.Join(tempDir, "restic_pw"),
			CacheDir:    initTask.Repository.CacheDir,
		}
		if _, err := backup.WriteSecretFile(tempDir, "restic_pw", secrets.ResticPassword); err != nil {
			return nil, &PipelineError{Code: "internal", Message: "write restic password", Cause: err}
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
	opts := restic.Options{
		Exe:         toolExe(d, "restic"),
		RepoPath:    task.Repository.RepositoryPath,
		PasswordFile: filepath.Join(tempDir, "restic_pw"),
		CacheDir:    task.Repository.CacheDir,
	}
	if _, err := backup.WriteSecretFile(tempDir, "restic_pw", secrets.ResticPassword); err != nil {
		return nil, &PipelineError{Code: "internal", Message: "write restic password", Cause: err}
	}
	if err := restic.Forget(ctx, d.Exec, opts, task.Retention, nil); err != nil {
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
	opts := restic.Options{
		Exe:         toolExe(d, "restic"),
		RepoPath:    task.Repository.RepositoryPath,
		PasswordFile: filepath.Join(tempDir, "restic_pw"),
		CacheDir:    task.Repository.CacheDir,
	}
	if _, err := backup.WriteSecretFile(tempDir, "restic_pw", secrets.ResticPassword); err != nil {
		return nil, &PipelineError{Code: "internal", Message: "write restic password", Cause: err}
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
	opts := restic.Options{
		Exe:         toolExe(d, "restic"),
		RepoPath:    task.Repository.RepositoryPath,
		PasswordFile: filepath.Join(tempDir, "restic_pw"),
		CacheDir:    task.Repository.CacheDir,
	}
	if _, err := backup.WriteSecretFile(tempDir, "restic_pw", secrets.ResticPassword); err != nil {
		return nil, &PipelineError{Code: "internal", Message: "write restic password", Cause: err}
	}
	entries, err := restic.Ls(ctx, d.Exec, opts, task.SnapshotID)
	if err != nil {
		return nil, &PipelineError{Code: "snapshot_ls_failed", Message: "restic ls failed", Cause: err}
	}
	resultJSON, _ := json.Marshal(map[string]any{
		"entries": entries,
		"path":    "",
	})
	return &Result{ResultJSON: resultJSON}, nil
}

// runVerifyRemote validates rclone remote.
func runVerifyRemote(ctx context.Context, d Deps, tempDir string, params []byte, secrets backup.SecretBundle) (*Result, error) {
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