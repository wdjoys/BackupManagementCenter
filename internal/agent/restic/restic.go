// Package restic wraps restic CLI with JSON/JSONL parsing.
package restic

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"backupmanagementcenter/internal/agent/backup"
	"backupmanagementcenter/internal/model"
)

// Options configures restic wrapper.
type Options struct {
	Exe         string // absolute path to restic binary
	RepoPath    string // repository path (e.g., rclone:gdrive:path)
	PasswordFile string // path to 0600 password file
	CacheDir    string // optional cache directory
}

// Snapshot represents a restic snapshot from --json output.
type Snapshot struct {
	ID        string   `json:"id"`
	Time      string   `json:"time"`
	Host      string   `json:"host"`
	Tags      []string `json:"tags"`
	Paths     []string `json:"paths"`
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

	env := []string{"RESTIC_PASSWORD_FILE=" + opts.PasswordFile}
	if opts.CacheDir != "" {
		env = append(env, "RESTIC_CACHE_DIR="+opts.CacheDir)
	}

	var snapshotID string
	var lastSummary string
	exitCode, err := exec.Run(ctx, backup.Cmd{Exe: opts.Exe, Args: args, Env: env},
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
			}
		}, func(line string) {
			// stderr logged via Logf
		})
	if err != nil || exitCode != 0 {
		return "", "", mapResticError(exitCode, err)
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

	env := []string{"RESTIC_PASSWORD_FILE=" + opts.PasswordFile}
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

	env := []string{"RESTIC_PASSWORD_FILE=" + opts.PasswordFile}
	if opts.CacheDir != "" {
		env = append(env, "RESTIC_CACHE_DIR="+opts.CacheDir)
	}

	var snapshots []Snapshot
	exitCode, err := exec.Run(ctx, backup.Cmd{Exe: opts.Exe, Args: args, Env: env},
		func(line string) {
			var snaps []Snapshot
			if err := json.Unmarshal([]byte(line), &snaps); err == nil {
				snapshots = snaps
			}
		}, func(string) {})
	if err != nil || exitCode != 0 {
		return nil, mapResticError(exitCode, err)
	}
	return snapshots, nil
}


// RestoreDryRun runs `restic restore --dry-run --verbose=2` and parses stats.
func RestoreDryRun(ctx context.Context, exec backup.Executor, opts Options, snapshotID, target string, includePaths []string) (*model.Progress, error) {
	if opts.Exe == "" {
		return nil, fmt.Errorf("restic exe not set")
	}
	args := []string{"restore", snapshotID, "--target", target, "--dry-run", "--verbose=2"}
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

	env := []string{"RESTIC_PASSWORD_FILE=" + opts.PasswordFile}
	if opts.CacheDir != "" {
		env = append(env, "RESTIC_CACHE_DIR="+opts.CacheDir)
	}

	var filesAdded, filesChanged int
	var exampleLines []string

	// Regex for restic verbose dry-run output
	reSummary := regexp.MustCompile(`(?i)(added|changed|unmodified|scanned)\s*[:=]\s*(\d+)`)

	exitCode, err := exec.Run(ctx, backup.Cmd{Exe: opts.Exe, Args: args, Env: env},
		func(line string) {
			// Parse verbose output for stats
			if m := reSummary.FindStringSubmatch(line); m != nil {
				if strings.EqualFold(m[1], "added") {
					if n, err := strconv.Atoi(m[2]); err == nil {
						filesAdded = n
					}
				} else if strings.EqualFold(m[1], "changed") {
					if n, err := strconv.Atoi(m[2]); err == nil {
						filesChanged = n
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
		Phase:      "dry_run",
		FilesDone:  int64(filesAdded + filesChanged),
		FilesTotal: int64(filesAdded + filesChanged),
	}, nil
}

// Restore runs `restic restore` to target directory.
func Restore(ctx context.Context, exec backup.Executor, opts Options, snapshotID, target string, includePaths []string) error {
	if opts.Exe == "" {
		return fmt.Errorf("restic exe not set")
	}
	args := []string{"restore", snapshotID, "--target", target}
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

	env := []string{"RESTIC_PASSWORD_FILE=" + opts.PasswordFile}
	if opts.CacheDir != "" {
		env = append(env, "RESTIC_CACHE_DIR="+opts.CacheDir)
	}

	exitCode, err := exec.Run(ctx, backup.Cmd{Exe: opts.Exe, Args: args, Env: env}, func(string) {}, func(string) {})
	if err != nil || exitCode != 0 {
		return mapResticError(exitCode, err)
	}
	return nil
}

// Forget runs `restic forget --prune --group-by host,tags` with retention.
func Forget(ctx context.Context, exec backup.Executor, opts Options, retention model.Retention, tags []string) error {
	if opts.Exe == "" {
		return fmt.Errorf("restic exe not set")
	}
	args := []string{"forget", "--prune", "--group-by", "host,tags"}
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

	env := []string{"RESTIC_PASSWORD_FILE=" + opts.PasswordFile}
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
	env := []string{"RESTIC_PASSWORD_FILE=" + opts.PasswordFile}
	if opts.CacheDir != "" {
		env = append(env, "RESTIC_CACHE_DIR="+opts.CacheDir)
	}
	exitCode, err := exec.Run(ctx, backup.Cmd{Exe: opts.Exe, Args: args, Env: env}, func(string) {}, func(string) {})
	if err != nil || exitCode != 0 {
		return mapResticError(exitCode, err)
	}
	return nil
}

// SnapshotEntry is a single file/directory entry from `restic ls --json`.
type SnapshotEntry struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Size  int64  `json:"size,omitempty"`
	Mtime string `json:"mtime,omitempty"`
}

// Ls runs `restic ls <snapshot> --json` and returns structured entries.
func Ls(ctx context.Context, exec backup.Executor, opts Options, snapshotID string) ([]SnapshotEntry, error) {
	if opts.Exe == "" {
		return nil, fmt.Errorf("restic exe not set")
	}
	args := []string{"ls", snapshotID}
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

	env := []string{"RESTIC_PASSWORD_FILE=" + opts.PasswordFile}
	if opts.CacheDir != "" {
		env = append(env, "RESTIC_CACHE_DIR="+opts.CacheDir)
	}

	var entries []SnapshotEntry
	exitCode, err := exec.Run(ctx, backup.Cmd{Exe: opts.Exe, Args: args, Env: env},
		func(line string) {
			var es []SnapshotEntry
			if err := json.Unmarshal([]byte(line), &es); err == nil {
				entries = es
			}
		}, func(string) {})
	if err != nil || exitCode != 0 {
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
	env := []string{"RESTIC_PASSWORD_FILE=" + opts.PasswordFile}
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