// Package restic wraps restic CLI with JSON/JSONL parsing.
package restic

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"backupmanagementcenter/internal/agent/backup"
	"backupmanagementcenter/internal/model"
)

// Options configures restic wrapper.
type Options struct {
	Exe            string // absolute path to restic binary
	RepoPath       string // repository path (e.g., rclone:gdrive:path)
	PasswordFile   string // path to 0600 password file
	CacheDir       string // optional cache directory
	RcloneConfFile string // 0600 rclone.conf path; required for rclone: repos
	WorkingDir     string // optional working directory for relative backup paths
}

// Snapshot represents a restic snapshot from --json output.
type Snapshot struct {
	ID    string   `json:"id"`
	Time  string   `json:"time"`
	Host  string   `json:"host"`
	Tags  []string `json:"tags"`
	Paths []string `json:"paths"`
}

// ProgressCallback is called for status updates during backup.
type ProgressCallback func(model.Progress)

// Backup runs `restic backup` with the given paths.
// Returns (summary, snapshotID, error).
func Backup(ctx context.Context, exec backup.Executor, opts Options, paths []string, excludeFile string, tags []string, oneFS bool, onProgress ProgressCallback) (string, string, error) {
	if opts.Exe == "" {
		return "", "", fmt.Errorf("restic exe not set")
	}
	args := []string{"backup"}
	if opts.RepoPath != "" {
		args = append(args, "--repo", opts.RepoPath)
	}
	if opts.PasswordFile != "" {
		args = append(args, "--password-file", opts.PasswordFile)
	}
	if opts.CacheDir != "" {
		args = append(args, "--cache-dir", opts.CacheDir)
	}
	if excludeFile != "" {
		args = append(args, "--exclude-file", excludeFile)
	}
	for _, t := range tags {
		args = append(args, "--tag", t)
	}
	if oneFS {
		args = append(args, "--one-file-system")
	}
	args = append(args, "--json")
	args = append(args, paths...)

	env := buildEnv(opts)
	if opts.CacheDir != "" {
		env = append(env, "RESTIC_CACHE_DIR="+opts.CacheDir)
	}
	var snapshotID string
	var lastSummary string
	var outputTail strings.Builder
	var outputMu sync.Mutex
	appendOutput := func(line string) {
		outputMu.Lock()
		defer outputMu.Unlock()
		const maxOutput = 4 << 10
		if outputTail.Len() >= maxOutput {
			return
		}
		remaining := maxOutput - outputTail.Len()
		if len(line)+1 > remaining {
			line = line[:remaining-1]
		}
		outputTail.WriteString(line)
		outputTail.WriteByte('\n')
	}
	exitCode, err := exec.Run(ctx, backup.Cmd{Exe: opts.Exe, Args: args, Env: env, Dir: opts.WorkingDir},
		func(line string) {
			// Parse JSONL output
			var msg jsonMessage
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				return
			}
			switch msg.MessageType {
			case "status":
				if onProgress != nil && msg.Data != nil {
					var prog model.Progress
					if err := json.Unmarshal(msg.Data, &prog); err == nil {
						onProgress(prog)
					}
				}
			case "summary":
				if msg.Data != nil {
					var summary backupSummary
					if err := json.Unmarshal(msg.Data, &summary); err == nil {
						snapshotID = summary.SnapshotID
						lastSummary = string(msg.Data)
					}
				}
			case "error", "exit_error":
				appendOutput(line)
			}
		}, appendOutput)
	if err != nil || exitCode != 0 {
		return "", "", enriched(mapResticError(exitCode, err), outputTail.String())
	}
	return lastSummary, snapshotID, nil
}

// CatConfig runs `restic cat config` to check if repo exists and password is correct.
func CatConfig(ctx context.Context, exec backup.Executor, opts Options) error {
	if opts.Exe == "" {
		return fmt.Errorf("restic exe not set")
	}
	args := []string{"cat", "config"}
	if opts.RepoPath != "" {
		args = append(args, "--repo", opts.RepoPath)
	}
	if opts.PasswordFile != "" {
		args = append(args, "--password-file", opts.PasswordFile)
	}
	if opts.CacheDir != "" {
		args = append(args, "--cache-dir", opts.CacheDir)
	}
	args = append(args, "--json")

	env := buildEnv(opts)
	if opts.CacheDir != "" {
		env = append(env, "RESTIC_CACHE_DIR="+opts.CacheDir)
	}

	exitCode, err := exec.Run(ctx, backup.Cmd{Exe: opts.Exe, Args: args, Env: env}, func(string) {}, func(string) {})
	if err != nil || exitCode != 0 {
		return mapResticError(exitCode, err)
	}
	return nil
}

// Snapshots runs `restic snapshots --json` and returns parsed snapshots.
func Snapshots(ctx context.Context, exec backup.Executor, opts Options) ([]Snapshot, error) {
	if opts.Exe == "" {
		return nil, fmt.Errorf("restic exe not set")
	}
	args := []string{"snapshots"}
	if opts.RepoPath != "" {
		args = append(args, "--repo", opts.RepoPath)
	}
	if opts.PasswordFile != "" {
		args = append(args, "--password-file", opts.PasswordFile)
	}
	if opts.CacheDir != "" {
		args = append(args, "--cache-dir", opts.CacheDir)
	}
	args = append(args, "--json")

	env := buildEnv(opts)

	var stderrTail strings.Builder
	var snapshots []Snapshot
	exitCode, err := exec.Run(ctx, backup.Cmd{Exe: opts.Exe, Args: args, Env: env},
		func(line string) {
			var snaps []Snapshot
			if err := json.Unmarshal([]byte(line), &snaps); err == nil {
				snapshots = snaps
			}
		}, func(line string) { stderrTail.WriteString(line + "\n") })
	if exitCode != 0 {
		return nil, enriched(mapResticError(exitCode, err), stderrTail.String())
	}
	return snapshots, nil
}

// enriched appends a compact tail of restic output to an error for diagnostics.
func enriched(err error, output string) error {
	if err == nil {
		return nil
	}
	out := strings.ReplaceAll(strings.TrimSpace(output), "\n", " | ")
	if len(out) > 300 {
		out = out[len(out)-300:]
	}
	if out == "" {
		return err
	}
	return fmt.Errorf("%w; output: %s", err, out)
}

// RestoreDryRun keeps the historical API and uses restic's safe default.
func RestoreDryRun(ctx context.Context, exec backup.Executor, opts Options, snapshotID, target string, includePaths []string) (*model.Progress, error) {
	return RestoreDryRunWithOverwrite(ctx, exec, opts, snapshotID, target, includePaths, "always")
}

// RestoreDryRunWithOverwrite runs `restic restore --dry-run --verbose=2` with
// the exact overwrite policy selected by the caller.
func RestoreDryRunWithOverwrite(ctx context.Context, exec backup.Executor, opts Options, snapshotID, target string, includePaths []string, overwrite string) (*model.Progress, error) {
	if opts.Exe == "" {
		return nil, fmt.Errorf("restic exe not set")
	}
	args := []string{"restore", snapshotID, "--target", target, "--dry-run", "--verbose=2", "--overwrite", normalizeOverwrite(overwrite)}
	if opts.RepoPath != "" {
		args = append(args, "--repo", opts.RepoPath)
	}
	if opts.PasswordFile != "" {
		args = append(args, "--password-file", opts.PasswordFile)
	}
	if opts.CacheDir != "" {
		args = append(args, "--cache-dir", opts.CacheDir)
	}
	for _, p := range includePaths {
		args = append(args, "--include", p)
	}

	env := buildEnv(opts)
	if opts.CacheDir != "" {
		env = append(env, "RESTIC_CACHE_DIR="+opts.CacheDir)
	}

	var filesAdded, filesChanged, filesSkipped, filesDeleted int
	var exampleLines []string

	// Regex for restic verbose dry-run output
	reSummary := regexp.MustCompile(`(?i)\b(new|added|changed|unmodified|skipped|deleted|removed)\b[^0-9]*(\d+)`)

	exitCode, err := exec.Run(ctx, backup.Cmd{Exe: opts.Exe, Args: args, Env: env},
		func(line string) {
			// Parse verbose output for stats
			if m := reSummary.FindStringSubmatch(line); m != nil {
				switch strings.ToLower(m[1]) {
				case "new", "added":
					if n, err := strconv.Atoi(m[2]); err == nil {
						filesAdded = n
					}
				case "changed":
					if n, err := strconv.Atoi(m[2]); err == nil {
						filesChanged = n
					}
				case "unmodified", "skipped":
					if n, err := strconv.Atoi(m[2]); err == nil {
						filesSkipped = n
					}
				case "deleted", "removed":
					if n, err := strconv.Atoi(m[2]); err == nil {
						filesDeleted = n
					}
				}
			}
			// Capture example lines
			if strings.Contains(line, "would be") || strings.Contains(line, "would restore") {
				exampleLines = append(exampleLines, strings.TrimSpace(line))
			}
		}, func(string) {})
	if err != nil || exitCode != 0 {
		return nil, mapResticError(exitCode, err)
	}

	// Fallback: count lines if regex didn't catch
	if filesAdded == 0 && filesChanged == 0 {
		// Rough estimation from example lines
		for _, l := range exampleLines {
			if strings.Contains(l, "added") {
				filesAdded++
			} else if strings.Contains(l, "changed") {
				filesChanged++
			}
		}
	}

	return &model.Progress{
		Phase:        "dry_run",
		FilesDone:    int64(filesAdded + filesChanged + filesDeleted),
		FilesTotal:   int64(filesAdded + filesChanged + filesSkipped + filesDeleted),
		FilesAdded:   int64(filesAdded),
		FilesChanged: int64(filesChanged),
		FilesSkipped: int64(filesSkipped),
		FilesDeleted: int64(filesDeleted),
		Sample:       exampleLines,
	}, nil
}

// Restore runs `restic restore` to target directory with restic's default
// overwrite behavior.
func Restore(ctx context.Context, exec backup.Executor, opts Options, snapshotID, target string, includePaths []string) error {
	return RestoreWithOverwrite(ctx, exec, opts, snapshotID, target, includePaths, "always")
}

func RestoreWithOverwrite(ctx context.Context, exec backup.Executor, opts Options, snapshotID, target string, includePaths []string, overwrite string) error {
	if opts.Exe == "" {
		return fmt.Errorf("restic exe not set")
	}
	args := []string{"restore", snapshotID, "--target", target, "--overwrite", normalizeOverwrite(overwrite)}
	if opts.RepoPath != "" {
		args = append(args, "--repo", opts.RepoPath)
	}
	if opts.PasswordFile != "" {
		args = append(args, "--password-file", opts.PasswordFile)
	}
	if opts.CacheDir != "" {
		args = append(args, "--cache-dir", opts.CacheDir)
	}
	for _, p := range includePaths {
		args = append(args, "--include", p)
	}

	env := buildEnv(opts)
	if opts.CacheDir != "" {
		env = append(env, "RESTIC_CACHE_DIR="+opts.CacheDir)
	}

	exitCode, err := exec.Run(ctx, backup.Cmd{Exe: opts.Exe, Args: args, Env: env}, func(string) {}, func(string) {})
	if err != nil || exitCode != 0 {
		return mapResticError(exitCode, err)
	}
	return nil
}

// Forget runs retention and prune. It is reserved for an explicit maintenance
// run because prune locks the repository.
func Forget(ctx context.Context, exec backup.Executor, opts Options, retention model.Retention, tags []string) error {
	return forget(ctx, exec, opts, retention, tags, true)
}

// ForgetOnly applies retention without prune. Backups use this path so a
// long-running prune cannot contend with the next upload.
func ForgetOnly(ctx context.Context, exec backup.Executor, opts Options, retention model.Retention, tags []string) error {
  return forget(ctx, exec, opts, retention, tags, false)
}

// DeleteByTags 删除匹配标签的全部快照，并清理不再引用的数据。
func DeleteByTags(ctx context.Context, exec backup.Executor, opts Options, tags []string) error {
  if opts.Exe == "" {
    return fmt.Errorf("restic exe not set")
  }
  args := []string{"forget", "--group-by", "host,tags", "--prune"}
  if opts.RepoPath != "" { args = append(args, "--repo", opts.RepoPath) }
  if opts.PasswordFile != "" { args = append(args, "--password-file", opts.PasswordFile) }
  if opts.CacheDir != "" { args = append(args, "--cache-dir", opts.CacheDir) }
  for _, tag := range tags { args = append(args, "--tag", tag) }
  args = append(args, "--keep-last", "0", "--keep-daily", "0", "--keep-weekly", "0", "--keep-monthly", "0", "--json")
  env := buildEnv(opts)
  if opts.CacheDir != "" { env = append(env, "RESTIC_CACHE_DIR="+opts.CacheDir) }
  exitCode, err := exec.Run(ctx, backup.Cmd{Exe: opts.Exe, Args: args, Env: env}, func(string) {}, func(string) {})
  if err != nil || exitCode != 0 { return mapResticError(exitCode, err) }
  return nil
}

// DeleteSnapshots 删除指定 snapshot ID 的快照，并可选 prune 回收空间。
func DeleteSnapshots(ctx context.Context, exec backup.Executor, opts Options, snapshotIDs []string, prune bool) error {
  if opts.Exe == "" {
    return fmt.Errorf("restic exe not set")
  }
  if len(snapshotIDs) == 0 {
    return fmt.Errorf("no snapshot IDs provided")
  }
  args := []string{"forget"}
  args = append(args, snapshotIDs...)
  if prune {
    args = append(args, "--prune")
  }
  if opts.RepoPath != "" {
    args = append(args, "--repo", opts.RepoPath)
  }
  if opts.PasswordFile != "" {
    args = append(args, "--password-file", opts.PasswordFile)
  }
  if opts.CacheDir != "" {
    args = append(args, "--cache-dir", opts.CacheDir)
  }
  args = append(args, "--json")

  env := buildEnv(opts)
  if opts.CacheDir != "" {
    env = append(env, "RESTIC_CACHE_DIR="+opts.CacheDir)
  }

  exitCode, err := exec.Run(ctx, backup.Cmd{Exe: opts.Exe, Args: args, Env: env}, func(string) {}, func(string) {})
  if err != nil || exitCode != 0 {
    return mapResticError(exitCode, err)
  }
  return nil
}

func forget(ctx context.Context, exec backup.Executor, opts Options, retention model.Retention, tags []string, prune bool) error {
	if opts.Exe == "" {
		return fmt.Errorf("restic exe not set")
	}
	args := []string{"forget", "--group-by", "host,tags"}
	if prune {
		args = append(args, "--prune")
	}
	if opts.RepoPath != "" {
		args = append(args, "--repo", opts.RepoPath)
	}
	if opts.PasswordFile != "" {
		args = append(args, "--password-file", opts.PasswordFile)
	}
	if opts.CacheDir != "" {
		args = append(args, "--cache-dir", opts.CacheDir)
	}
	for _, t := range tags {
		args = append(args, "--tag", t)
	}
	if retention.KeepLast > 0 {
		args = append(args, "--keep-last", strconv.Itoa(retention.KeepLast))
	}
	if retention.KeepDaily > 0 {
		args = append(args, "--keep-daily", strconv.Itoa(retention.KeepDaily))
	}
	if retention.KeepWeekly > 0 {
		args = append(args, "--keep-weekly", strconv.Itoa(retention.KeepWeekly))
	}
	if retention.KeepMonthly > 0 {
		args = append(args, "--keep-monthly", strconv.Itoa(retention.KeepMonthly))
	}
	args = append(args, "--json")

	env := buildEnv(opts)
	if opts.CacheDir != "" {
		env = append(env, "RESTIC_CACHE_DIR="+opts.CacheDir)
	}

	exitCode, err := exec.Run(ctx, backup.Cmd{Exe: opts.Exe, Args: args, Env: env}, func(string) {}, func(string) {})
	if err != nil || exitCode != 0 {
		return mapResticError(exitCode, err)
	}
	return nil
}

// Check runs `restic check --json`.
func Check(ctx context.Context, exec backup.Executor, opts Options) error {
	if opts.Exe == "" {
		return fmt.Errorf("restic exe not set")
	}
	args := []string{"check", "--json"}
	if opts.RepoPath != "" {
		args = append(args, "--repo", opts.RepoPath)
	}
	if opts.PasswordFile != "" {
		args = append(args, "--password-file", opts.PasswordFile)
	}
	if opts.CacheDir != "" {
		args = append(args, "--cache-dir", opts.CacheDir)
	}
	env := buildEnv(opts)
	if opts.CacheDir != "" {
		env = append(env, "RESTIC_CACHE_DIR="+opts.CacheDir)
	}
	var stdoutTail, stderrTail strings.Builder
	appendStdout := func(line string) {
		stdoutTail.WriteString(line)
		stdoutTail.WriteByte('\n')
	}
	appendStderr := func(line string) {
		stderrTail.WriteString(line)
		stderrTail.WriteByte('\n')
	}
	exitCode, err := exec.Run(ctx, backup.Cmd{Exe: opts.Exe, Args: args, Env: env}, appendStdout, appendStderr)
	if err != nil || exitCode != 0 {
		return enriched(mapResticError(exitCode, err), stdoutTail.String()+stderrTail.String())
	}
	return nil
}

// SnapshotEntry is a single file/directory entry from `restic ls --json`.
type SnapshotEntry struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Path  string `json:"path,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Mtime string `json:"mtime,omitempty"`
}

// Ls runs `restic ls <snapshot> <path> --json` and returns that directory and
// its direct children.
func Ls(ctx context.Context, exec backup.Executor, opts Options, snapshotID, snapshotPath string) ([]SnapshotEntry, error) {
	if opts.Exe == "" {
		return nil, fmt.Errorf("restic exe not set")
	}
	if snapshotPath == "" {
		snapshotPath = "/"
	}
	args := []string{"ls", snapshotID, snapshotPath}
	if opts.RepoPath != "" {
		args = append(args, "--repo", opts.RepoPath)
	}
	if opts.PasswordFile != "" {
		args = append(args, "--password-file", opts.PasswordFile)
	}
	if opts.CacheDir != "" {
		args = append(args, "--cache-dir", opts.CacheDir)
	}
	args = append(args, "--json")

	env := buildEnv(opts)
	var entries []SnapshotEntry
	exitCode, err := exec.Run(ctx, backup.Cmd{Exe: opts.Exe, Args: args, Env: env},
		func(line string) {
			var node struct {
				Name string `json:"name"`
				Type string `json:"type"`
				Path string `json:"path"`
				Size int64 `json:"size"`
				Mtime string `json:"mtime"`
			}
			if json.Unmarshal([]byte(line), &node) != nil || node.Name == "" || node.Type == "" {
				return
			}
			entries = append(entries, SnapshotEntry{Name: node.Name, Type: node.Type, Path: node.Path, Size: node.Size, Mtime: node.Mtime})
		}, func(string) {})
	if exitCode != 0 {
		return nil, mapResticError(exitCode, err)
	}
	return entries, nil
}

// Init runs `restic init` to create a new repository. Restic does not
// support --json for init, so we parse the human-readable output.
func Init(ctx context.Context, exec backup.Executor, opts Options) error {
	if opts.Exe == "" {
		return fmt.Errorf("restic exe not set")
	}
	args := []string{"init"}
	if opts.RepoPath != "" {
		args = append(args, "--repo", opts.RepoPath)
	}
	if opts.PasswordFile != "" {
		args = append(args, "--password-file", opts.PasswordFile)
	}
	if opts.CacheDir != "" {
		args = append(args, "--cache-dir", opts.CacheDir)
	}
	env := buildEnv(opts)
	if opts.CacheDir != "" {
		env = append(env, "RESTIC_CACHE_DIR="+opts.CacheDir)
	}
	var lastLine string
	exitCode, err := exec.Run(ctx, backup.Cmd{Exe: opts.Exe, Args: args, Env: env},
		func(line string) {
			lastLine = strings.TrimSpace(line)
		}, func(string) {})
	if err != nil || exitCode != 0 {
		return mapResticError(exitCode, err)
	}
	_ = lastLine // success message
	return nil
}

// jsonMessage represents a restic JSONL message.
type jsonMessage struct {
	MessageType string          `json:"message_type"`
	Data        json.RawMessage `json:"data,omitempty"`
}

type backupSummary struct {
	SnapshotID string `json:"snapshot_id"`
}

// mapResticError maps restic exit codes to stable error codes.
func mapResticError(exitCode int, err error) error {
	code := model.MapResticExitCode(exitCode)
	if code != "" {
		return &ResticError{ExitCode: exitCode, Code: code, Err: err}
	}
	if err != nil {
		return &ResticError{ExitCode: exitCode, Code: "restic_failed", Err: err}
	}
	return &ResticError{ExitCode: exitCode, Code: "restic_failed", Err: fmt.Errorf("exit %d", exitCode)}
}

// ResticError carries exit code and mapped error code.
type ResticError struct {
	ExitCode int
	Code     string
	Err      error
}

func (e *ResticError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("restic exit %d (%s): %v", e.ExitCode, e.Code, e.Err)
	}
	return fmt.Sprintf("restic exit %d (%s)", e.ExitCode, e.Code)
}

func (e *ResticError) Unwrap() error { return e.Err }

// buildEnv assembles the restic child environment: password file, optional
// cache dir and the rclone config for rclone: backends.
func buildEnv(opts Options) []string {
	env := []string{"RESTIC_PASSWORD_FILE=" + opts.PasswordFile}
	if opts.CacheDir != "" {
		env = append(env, "RESTIC_CACHE_DIR="+opts.CacheDir)
	}
	if opts.RcloneConfFile != "" {
		env = append(env, "RCLONE_CONFIG="+opts.RcloneConfFile)
	}
	return env
}

// knownBackends are restic location prefixes that must not be re-wrapped.
var knownBackends = []string{"local:", "rclone:", "rest:", "s3:", "sftp:", "b2:", "azure:", "gs:", "swift:"}

// NormalizeRepoPath maps "<remote>:<path>" (our storage-target notation) to
// restic's "rclone:<remote>:<path>"; already-prefixed or plain paths pass
// through unchanged.
func NormalizeRepoPath(p string) string {
	if p == "" {
		return ""
	}
	for _, b := range knownBackends {
		if strings.HasPrefix(p, b) {
			return p
		}
	}
	if i := strings.Index(p, ":"); i > 0 {
		return "rclone:" + p
	}
	return p
}

func normalizeOverwrite(v string) string {
	switch v {
	case "never", "if-changed", "if-newer", "always":
		return v
	default:
		return "always"
	}
}
