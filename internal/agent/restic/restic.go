// Package restic wraps restic CLI with JSON/JSONL parsing.
package restic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"backupmanagementcenter/internal/agent/backup"
	"backupmanagementcenter/internal/model"
)

// Options configures restic CLI invocation.
type Options struct {
	Exe            string       // absolute path to restic binary
	RepoPath       string       // repository path (e.g. rclone:gdrive:path)
	PasswordFile   string       // path to 0600 password file
	CacheDir       string       // optional cache directory
	RcloneConfFile string       // 0600 rclone.conf path; required for rclone: repos
	WorkingDir     string       // optional working directory for relative backup paths
	Logf           func(string) // optional stderr sink; called with a readable line, already level-tagged by the caller
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
// 返回解析后的 summary；SnapshotID 为空表示 restic 未输出 summary。
func Backup(ctx context.Context, exec backup.Executor, opts Options, paths []string, excludeFile string, tags []string, oneFS bool, onProgress ProgressCallback) (BackupSummary, error) {
	var summary BackupSummary
	if opts.Exe == "" {
		return summary, fmt.Errorf("restic exe not set")
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
	// JSON 模式下 restic 默认每秒输出 60 次 status，每条都会写 SQLite；限流为每秒 1 次。
	env = append(env, "RESTIC_PROGRESS_FPS=1")
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
	// restic --json 的每行是扁平 JSON（{"message_type":"status",...}），
	// 不是 {"message_type":...,"data":{...}} 包装格式。
	stderrLines := 0
	exitCode, err := exec.Run(ctx, backup.Cmd{Exe: opts.Exe, Args: args, Env: env, Dir: opts.WorkingDir},
		func(line string) {
			var head struct {
				MessageType string `json:"message_type"`
			}
			if json.Unmarshal([]byte(line), &head) != nil {
				return
			}
			switch head.MessageType {
			case "status":
				var s resticStatus
				if json.Unmarshal([]byte(line), &s) != nil {
					return
				}
				if onProgress != nil {
					onProgress(s.toProgress())
				}
			case "summary":
				var parsed BackupSummary
				if json.Unmarshal([]byte(line), &parsed) != nil {
					return
				}
				summary = parsed
				if onProgress != nil {
					onProgress(summary.toProgress())
				}
			case "error", "exit_error":
				appendOutput(line)
			}
		}, func(line string) {
			appendOutput(line)
			// restic 把逐文件错误写在 stderr；转成可读文本进运行日志，避免只留在错误尾巴里。
			// stderr 回调只在单个 goroutine 中执行，计数器无需加锁。
			if opts.Logf == nil {
				return
			}
			stderrLines++
			switch {
			case stderrLines <= maxLoggedStderrLines:
				opts.Logf(resticErrorText(line))
			case stderrLines == maxLoggedStderrLines+1:
				opts.Logf("restic 输出的更多错误已省略")
			}
		})
	if err != nil || exitCode != 0 {
		return summary, enriched(mapResticError(exitCode, err), outputTail.String())
	}
	return summary, nil
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
	// `restic snapshots --json` 输出单个 JSON 数组文档（可能是多行）：必须整段
	// 解析，逐行解析在换行布局下会静默丢成空列表。
	var stdout strings.Builder
	exitCode, err := exec.Run(ctx, backup.Cmd{Exe: opts.Exe, Args: args, Env: env},
		func(line string) { stdout.WriteString(line); stdout.WriteByte('\n') },
		func(line string) { stderrTail.WriteString(line + "\n") })
	if exitCode != 0 {
		return nil, resticRunError(exitCode, err, stderrTail.String())
	}
	raw := strings.TrimSpace(stdout.String())
	if raw == "" {
		return nil, enriched(errors.New("restic snapshots returned no output"), stderrTail.String())
	}
	snapshots := []Snapshot{}
	if err := json.Unmarshal([]byte(raw), &snapshots); err != nil {
		return nil, enriched(fmt.Errorf("parse restic snapshots output: %w", err), stderrTail.String())
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

// DeleteByTags 删除匹配全部给定标签的快照，并清理不再引用的数据。
//
// 不能用 restic 的 forget 策略表达"全部删除"：--keep-last 0（以及 keep-daily
// 等全为 0）会被 restic 判定为"未指定策略"并直接失败：
//
//	Fatal: no policy was specified, no snapshots will be removed
//
// 因此改为先枚举匹配的快照 ID，再按 ID 删除——与手动删除快照走同一条已验证路径。
func DeleteByTags(ctx context.Context, exec backup.Executor, opts Options, tags []string) error {
	if opts.Exe == "" {
		return fmt.Errorf("restic exe not set")
	}
	if len(tags) == 0 {
		// 无标签过滤会匹配整个仓库；拒绝而不是误删全部快照。
		return fmt.Errorf("no tags provided")
	}
	snaps, err := Snapshots(ctx, exec, opts)
	if err != nil {
		return err
	}
	var ids []string
	for _, s := range snaps {
		if snapshotHasAllTags(s, tags) {
			ids = append(ids, s.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return DeleteSnapshots(ctx, exec, opts, ids, true)
}

// snapshotHasAllTags 报告快照是否带有全部给定标签。
func snapshotHasAllTags(s Snapshot, tags []string) bool {
	have := make(map[string]struct{}, len(s.Tags))
	for _, t := range s.Tags {
		have[t] = struct{}{}
	}
	for _, want := range tags {
		if _, ok := have[want]; !ok {
			return false
		}
	}
	return true
}

func forget(ctx context.Context, exec backup.Executor, opts Options, retention model.Retention, tags []string, prune bool) error {
	if opts.Exe == "" {
		return fmt.Errorf("restic exe not set")
	}
	// --group-by 必须按 host，不能带 tags，也不能用 restic 的默认 host,paths：
	// 每条快照都带本次运行唯一的 run:<uuid> 标签，staging 路径也每次不同，
	// 于是每条快照自成一"组"，--keep-last/--keep-daily 对每组都成立，
	// 保留策略会静默地一个也不删。标签过滤已由 --tag 限定在单个计划范围内，
	// 按 host 分组即可让同一计划的快照进入同一组。
	args := []string{"forget", "--group-by", "host"}
	if prune {
		args = append(args, "--prune")
	}
	args = append(args, resticRepositoryArgs(opts)...)
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
	return runDelete(ctx, exec, opts, args)
}

// DeleteSnapshots 删除指定 snapshot ID 的快照，并可选 prune 回收空间。
func DeleteSnapshots(ctx context.Context, exec backup.Executor, opts Options, snapshotIDs []string, prune bool) error {
	if opts.Exe == "" {
		return fmt.Errorf("restic exe not set")
	}
	if len(snapshotIDs) == 0 {
		return fmt.Errorf("no snapshot IDs provided")
	}
	args := append([]string{"forget"}, snapshotIDs...)
	if prune {
		args = append(args, "--prune")
	}
	args = append(args, resticRepositoryArgs(opts)...)
	args = append(args, "--json")
	return runDelete(ctx, exec, opts, args)
}

func resticRepositoryArgs(opts Options) []string {
	args := make([]string, 0, 6)
	if opts.RepoPath != "" {
		args = append(args, "--repo", opts.RepoPath)
	}
	if opts.PasswordFile != "" {
		args = append(args, "--password-file", opts.PasswordFile)
	}
	if opts.CacheDir != "" {
		args = append(args, "--cache-dir", opts.CacheDir)
	}
	return args
}

func runDelete(ctx context.Context, exec backup.Executor, opts Options, args []string) error {
	if err := runResticDeleteCommand(ctx, exec, opts, args); err == nil {
		return nil
	} else {
		var resticErr *ResticError
		if !errors.As(err, &resticErr) || resticErr.Code != model.ErrRepositoryLocked {
			return err
		}
		if opts.Logf != nil {
			opts.Logf("检测到仓库锁，开始执行 stale unlock")
		}
		unlockArgs := append([]string{"unlock"}, resticRepositoryArgs(opts)...)
		unlockArgs = append(unlockArgs, "--json")
		if unlockErr := runResticDeleteCommand(ctx, exec, opts, unlockArgs); unlockErr != nil {
			if opts.Logf != nil {
				opts.Logf(fmt.Sprintf("stale unlock 失败：%v", unlockErr))
			}
			return unlockErr
		}
		if opts.Logf != nil {
			opts.Logf("已清理陈旧锁，重新删除快照")
		}
		retryArgs := addRetryLock(args)
		if retryErr := runResticDeleteCommand(ctx, exec, opts, retryArgs); retryErr != nil {
			if opts.Logf != nil {
				opts.Logf(fmt.Sprintf("重试删除失败：%v", retryErr))
			}
			return retryErr
		}
		return nil
	}
}

func runResticDeleteCommand(ctx context.Context, exec backup.Executor, opts Options, args []string) error {
	env := buildEnv(opts)
	if opts.CacheDir != "" {
		env = append(env, "RESTIC_CACHE_DIR="+opts.CacheDir)
	}
	var stderrTail strings.Builder
	exitCode, err := exec.Run(ctx, backup.Cmd{Exe: opts.Exe, Args: args, Env: env}, func(string) {}, func(line string) {
		stderrTail.WriteString(line + "\n")
		if opts.Logf != nil {
			opts.Logf(line)
		}
	})
	if err != nil || exitCode != 0 {
		return enriched(mapResticError(exitCode, err), stderrTail.String())
	}
	return nil
}

func addRetryLock(args []string) []string {
	result := make([]string, 0, len(args)+2)
	for i, arg := range args {
		if arg == "--repo" && i > 0 {
			result = append(result, "--retry-lock", "5m")
		}
		result = append(result, arg)
	}
	if len(result) == len(args) {
		result = append(result, "--retry-lock", "5m")
	}
	return result
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
				Name  string `json:"name"`
				Type  string `json:"type"`
				Path  string `json:"path"`
				Size  int64  `json:"size"`
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

// maxLoggedStderrLines 限制单次 Backup 记录到运行日志的 stderr 行数，
// 避免大量损坏文件把日志刷爆；超出部分只记录一条省略提示。
const maxLoggedStderrLines = 50

// resticErrorText 把 restic --json 的错误行转成可读文本；非 JSON 行原样返回。
func resticErrorText(line string) string {
	var m struct {
		MessageType string `json:"message_type"`
		Message     string `json:"message"`
		Item        string `json:"item"`
		Error       struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(line), &m) != nil {
		return line
	}
	switch m.MessageType {
	case "error":
		if m.Item != "" {
			return m.Item + ": " + m.Error.Message
		}
		return m.Error.Message
	case "exit_error":
		return m.Message
	}
	return line
}

type resticStatus struct {
	PercentDone float64 `json:"percent_done"`
	TotalBytes  int64   `json:"total_bytes"`
	BytesDone   int64   `json:"bytes_done"`
	TotalFiles  int64   `json:"total_files"`
	FilesDone   int64   `json:"files_done"`
}

func (s resticStatus) toProgress() model.Progress {
	// restic status 的 percent_done 范围是 0.0 ~ 1.0，UI 按 0 ~ 100 百分比展示
	pct := math.Round(s.PercentDone*10000) / 100
	if pct > 100 {
		pct = 100
	}
	return model.Progress{
		Phase:      model.BackupPhaseUploading,
		Percent:    pct,
		BytesDone:  s.BytesDone,
		BytesTotal: s.TotalBytes,
		FilesDone:  s.FilesDone,
		FilesTotal: s.TotalFiles,
	}
}

// BackupSummary 是 restic backup --json 最后一行 summary 的解析结果（扁平格式）。
type BackupSummary struct {
	SnapshotID          string  `json:"snapshot_id"`
	FilesNew            int64   `json:"files_new"`
	FilesChanged        int64   `json:"files_changed"`
	FilesUnmodified     int64   `json:"files_unmodified"`
	DataAdded           int64   `json:"data_added"`
	DataAddedPacked     int64   `json:"data_added_packed"`
	TotalFilesProcessed int64   `json:"total_files_processed"`
	TotalBytesProcessed int64   `json:"total_bytes_processed"`
	TotalDuration       float64 `json:"total_duration"`
}

func (s BackupSummary) toProgress() model.Progress {
	return model.Progress{
		Phase:      model.BackupPhaseDone,
		Percent:    100,
		BytesDone:  s.TotalBytesProcessed,
		BytesTotal: s.TotalBytesProcessed,
		FilesDone:  s.TotalFilesProcessed,
		FilesTotal: s.TotalFilesProcessed,
	}
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

// missingRepositoryMarkers 是 restic 在仓库不存在时打印的稳定文本。
// 本地后端返回 exit 10，但 rclone/sftp 等后端只返回 exit 1，因此必须按文本
// 兜底分类，否则 EnsureRepository 永远走不到 init 分支。
var missingRepositoryMarkers = []string{
	"unable to open config file",
	"does not exist\nIs there a repository",
	"Is there a repository at the following location",
}

// resticRunError 把退出码与输出一起分类：先按退出码映射，再对 rclone 等
// 非本地后端用输出文本识别“仓库不存在”。
func resticRunError(exitCode int, cause error, output string) error {
	classified := mapResticError(exitCode, cause)
	if exitCode != 0 {
		var re *ResticError
		if errors.As(classified, &re) && re.Code == "restic_failed" {
			for _, marker := range missingRepositoryMarkers {
				if strings.Contains(output, marker) {
					re.Code = model.ErrRepositoryMissing
					break
				}
			}
		}
	}
	return enriched(classified, output)
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
