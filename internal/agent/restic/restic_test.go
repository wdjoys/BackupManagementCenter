package restic

import (
	"context"
	"errors"
	"strings"
	"testing"

	"backupmanagementcenter/internal/agent/backup"
	"backupmanagementcenter/internal/model"
)

type checkExecutor struct {
	stdout, stderr string
	code           int
	cmd            *backup.Cmd
}

func (e checkExecutor) Run(_ context.Context, cmd backup.Cmd, onStdout func(string), onStderr func(string)) (int, error) {
	if e.cmd != nil {
		*e.cmd = cmd
	}
	if onStdout != nil && e.stdout != "" {
		for _, line := range strings.Split(e.stdout, "\n") {
			if line != "" {
				onStdout(line)
			}
		}
	}
	if onStderr != nil && e.stderr != "" {
		onStderr(e.stderr)
	}
	return e.code, nil
}

type scriptedExecutor struct {
	steps []struct {
		code int
		err  error
		out  string
	}
	cmds []backup.Cmd
}

func (e *scriptedExecutor) Run(_ context.Context, cmd backup.Cmd, _ func(string), onStderr func(string)) (int, error) {
	e.cmds = append(e.cmds, cmd)
	step := e.steps[len(e.cmds)-1]
	if onStderr != nil && step.out != "" {
		onStderr(step.out)
	}
	return step.code, step.err
}

func TestDeleteSnapshotsRecoversStaleLockOnce(t *testing.T) {
	exec := &scriptedExecutor{steps: []struct {
		code int
		err  error
		out  string
	}{
		{code: 11, out: "the unlock command can be used to remove stale locks"},
		{code: 0},
		{code: 0},
	}}
	err := DeleteSnapshots(context.Background(), exec, Options{Exe: "restic", RepoPath: "repo", PasswordFile: "pw", CacheDir: "cache"}, []string{"snap"}, false)
	if err != nil {
		t.Fatalf("DeleteSnapshots: %v", err)
	}
	if len(exec.cmds) != 3 {
		t.Fatalf("command count = %d, want 3", len(exec.cmds))
	}
	if exec.cmds[1].Args[0] != "unlock" || contains(exec.cmds[1].Args, "--remove-all") {
		t.Fatalf("unlock args = %q", exec.cmds[1].Args)
	}
	if !contains(exec.cmds[2].Args, "--retry-lock") || !contains(exec.cmds[2].Args, "5m") {
		t.Fatalf("retry args = %q", exec.cmds[2].Args)
	}
}

func TestDeleteSnapshotsDoesNotUnlockNonLockFailure(t *testing.T) {
	exec := &scriptedExecutor{steps: []struct {
		code int
		err  error
		out  string
	}{{code: 1, out: "permission denied"}}}
	err := DeleteSnapshots(context.Background(), exec, Options{Exe: "restic", RepoPath: "repo"}, []string{"snap"}, false)
	if err == nil || len(exec.cmds) != 1 || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v, commands = %d", err, len(exec.cmds))
	}
}

func TestDeleteSnapshotsUnlockFailureStopsRetry(t *testing.T) {
	exec := &scriptedExecutor{steps: []struct {
		code int
		err  error
		out  string
	}{
		{code: 11, out: "locked"},
		{code: 1, out: "unlock denied"},
	}}
	err := DeleteSnapshots(context.Background(), exec, Options{Exe: "restic", RepoPath: "repo"}, []string{"snap"}, false)
	if err == nil || len(exec.cmds) != 2 || !strings.Contains(err.Error(), "unlock denied") {
		t.Fatalf("err = %v, commands = %d", err, len(exec.cmds))
	}
}

func TestDeleteSnapshotsRetryLockFailureDoesNotLoop(t *testing.T) {
	exec := &scriptedExecutor{steps: []struct {
		code int
		err  error
		out  string
	}{
		{code: 11, out: "locked"},
		{code: 0},
		{code: 11, out: "still locked"},
	}}
	err := DeleteSnapshots(context.Background(), exec, Options{Exe: "restic", RepoPath: "repo"}, []string{"snap"}, false)
	if err == nil || len(exec.cmds) != 3 || !strings.Contains(err.Error(), "still locked") {
		t.Fatalf("err = %v, commands = %d", err, len(exec.cmds))
	}
}
func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestLsFiltersByRequestedDirectory(t *testing.T) {
	var cmd backup.Cmd
	cacheDir := "/var/lib/bmc-agent/.cache/restic"
	entries, err := Ls(context.Background(), checkExecutor{stdout: `{"name":"child","type":"dir","path":"/backup/child","size":0,"mtime":"2026-08-26T00:00:00Z"}`, cmd: &cmd}, Options{Exe: "restic", RepoPath: "rclone:remote:/repo", CacheDir: cacheDir}, "snapshot-id", "/backup")
	if err != nil {
		t.Fatalf("Ls: %v", err)
	}
	// 选项全部在 -- 之前、位置参数在 -- 之后：快照内路径由调用方提供，以 '-' 开头
	// 时若排在选项位置会被 restic 当作选项（实测 unknown shorthand flag 'd'）。
	wantArgs := []string{"ls", "--repo", "rclone:remote:/repo", "--cache-dir", cacheDir, "--json", "--", "snapshot-id", "/backup"}
	if len(cmd.Args) != len(wantArgs) {
		t.Fatalf("args = %q, want %q", cmd.Args, wantArgs)
	}
	for i, want := range wantArgs {
		if cmd.Args[i] != want {
			t.Fatalf("args = %q, want %q", cmd.Args, wantArgs)
		}
	}
	if !contains(cmd.Env, "RESTIC_CACHE_DIR="+cacheDir) {
		t.Fatalf("env = %q", cmd.Env)
	}
	if len(entries) != 1 || entries[0].Path != "/backup/child" {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestSnapshotsUsesCacheDir(t *testing.T) {
	var cmd backup.Cmd
	cacheDir := "/cache/restic"
	_, err := Snapshots(context.Background(), checkExecutor{stdout: `[]`, cmd: &cmd}, Options{Exe: "restic", RepoPath: "rclone:remote:/repo", CacheDir: cacheDir})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(cmd.Args, "--cache-dir") || !contains(cmd.Args, cacheDir) {
		t.Fatalf("args = %q", cmd.Args)
	}
	if !contains(cmd.Env, "RESTIC_CACHE_DIR="+cacheDir) {
		t.Fatalf("env = %q", cmd.Env)
	}
}

// lineExecutor 按真实执行器的方式逐行回调 stdout。
type lineExecutor struct{ stdout string }

func (e lineExecutor) Run(_ context.Context, _ backup.Cmd, onStdout func(string), onStderr func(string)) (int, error) {
	if onStdout != nil && e.stdout != "" {
		for _, line := range strings.Split(strings.TrimSuffix(e.stdout, "\n"), "\n") {
			onStdout(line)
		}
	}
	return 0, nil
}

// 回归：snapshots --json 是单个 JSON 文档（可能跨多行），必须整段解析；逐行解析会
// 静默返回空列表，前端于是显示“没有快照”且没有任何错误。
func TestSnapshotsParsesWholeDocument(t *testing.T) {
	stdout := "[\n  {\"id\":\"snap-a\",\"host\":\"host-1\"},\n  {\"id\":\"snap-b\",\"host\":\"host-1\"}\n]"
	snaps, err := Snapshots(context.Background(), lineExecutor{stdout: stdout}, Options{Exe: "restic", RepoPath: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 || snaps[0].ID != "snap-a" || snaps[1].ID != "snap-b" {
		t.Fatalf("snapshots = %+v", snaps)
	}
}

// 回归：命令成功但没有输出（输出被丢弃/截断）必须报错，不能当成空仓库。
func TestSnapshotsMissingOutputIsError(t *testing.T) {
	_, err := Snapshots(context.Background(), lineExecutor{}, Options{Exe: "restic", RepoPath: "repo"})
	if err == nil {
		t.Fatal("expected error when restic prints no snapshot output")
	}
}

func TestCheckIncludesCommandOutputOnFailure(t *testing.T) {
	err := Check(context.Background(), checkExecutor{stdout: `{"message_type":"error","message":"index is damaged"}`, stderr: "Fatal: repository check failed", code: 1}, Options{Exe: "restic", RepoPath: "rclone:remote:/repo"})
	if err == nil {
		t.Fatal("expected check failure")
	}
	msg := err.Error()
	for _, want := range []string{"restic exit 1", "index is damaged", "repository check failed"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q does not contain %q", msg, want)
		}
	}
}
func TestBackupIncludesCommandOutputOnFailure(t *testing.T) {
	_, err := Backup(context.Background(), checkExecutor{stdout: `{"message_type":"exit_error","code":3,"message":"permission denied: /backup-sources/etc/shadow"}`, stderr: "unable to read source file", code: 3}, Options{Exe: "restic", RepoPath: "rclone:remote:/repo"}, []string{"/backup-sources"}, "", nil, false, nil)
	if err == nil {
		t.Fatal("expected backup failure")
	}
	msg := err.Error()
	for _, want := range []string{"partial_source_read", "permission denied", "unable to read source file"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q does not contain %q", msg, want)
		}
	}
}
func TestBackupEmitsStatusAndSummaryProgress(t *testing.T) {
	stdoutLines := strings.Join([]string{
		`{"message_type":"status","percent_done":0.25,"total_files":100,"files_done":25,"total_bytes":104857600,"bytes_done":26214400}`,
		`{"message_type":"summary","files_new":3,"files_changed":1,"files_unmodified":96,"data_added":2048,"data_added_packed":1024,"total_files_processed":100,"total_bytes_processed":104857600,"total_duration":1.5,"snapshot_id":"snap-123"}`,
	}, "\n")

	var got []model.Progress
	onProg := func(p model.Progress) {
		got = append(got, p)
	}

	summary, err := Backup(context.Background(), checkExecutor{stdout: stdoutLines}, Options{
		Exe: "restic", RepoPath: "rclone:remote:/repo",
	}, []string{"/data"}, "", nil, false, onProg)
	if err != nil {
		t.Fatalf("Backup failed: %v", err)
	}
	if summary.SnapshotID != "snap-123" {
		t.Fatalf("summary.SnapshotID = %q, want snap-123", summary.SnapshotID)
	}
	if summary.FilesNew != 3 {
		t.Errorf("summary.FilesNew = %d, want 3", summary.FilesNew)
	}
	if summary.DataAdded != 2048 {
		t.Errorf("summary.DataAdded = %d, want 2048", summary.DataAdded)
	}
	if len(got) != 2 {
		t.Fatalf("got %d progress events, want 2: %+v", len(got), got)
	}

	// 验证 status 解析出的字节总量与百分比
	status := got[0]
	if status.Percent != 25.0 {
		t.Errorf("status.Percent = %v, want 25", status.Percent)
	}
	if status.BytesTotal != 104857600 || status.BytesDone != 26214400 {
		t.Errorf("status bytes = %d / %d, want 26214400 / 104857600", status.BytesDone, status.BytesTotal)
	}
	if status.FilesTotal != 100 || status.FilesDone != 25 {
		t.Errorf("status files = %d / %d, want 25 / 100", status.FilesDone, status.FilesTotal)
	}
	if status.Phase != model.BackupPhaseUploading {
		t.Errorf("status.Phase = %q, want %q", status.Phase, model.BackupPhaseUploading)
	}

	// 验证 summary 的 100% 收尾
	final := got[1]
	if final.Percent != 100 {
		t.Errorf("final.Percent = %v, want 100", final.Percent)
	}
	if final.BytesDone != 104857600 || final.BytesTotal != 104857600 {
		t.Errorf("final bytes = %d / %d, want 104857600 / 104857600", final.BytesDone, final.BytesTotal)
	}
	if final.FilesDone != 100 || final.FilesTotal != 100 {
		t.Errorf("final files = %d / %d, want 100 / 100", final.FilesDone, final.FilesTotal)
	}
	if final.Phase != model.BackupPhaseDone {
		t.Errorf("final.Phase = %q, want %q", final.Phase, model.BackupPhaseDone)
	}
}

// TestBackupLogsReadableSourceErrors 验证 restic stderr 的 JSON 错误行被转成可读文本进日志。
func TestBackupLogsReadableSourceErrors(t *testing.T) {
	var logged []string
	_, err := Backup(context.Background(), checkExecutor{
		stderr: `{"message_type":"error","error":{"message":"permission denied"},"during":"archival","item":"/data/secret"}`,
		code:   3,
	}, Options{
		Exe: "restic", RepoPath: "repo",
		Logf: func(l string) { logged = append(logged, l) },
	}, []string{"/data"}, "", nil, false, nil)
	if err == nil {
		t.Fatal("expected backup failure")
	}
	if len(logged) != 1 {
		t.Fatalf("got %d logged lines, want 1: %+v", len(logged), logged)
	}
	if logged[0] != "/data/secret: permission denied" {
		t.Errorf("logged[0] = %q, want %q", logged[0], "/data/secret: permission denied")
	}
}

// stdoutExecutor 记录命令并把预设 stdout 逐次回放（Snapshots 读 stdout）。
type stdoutExecutor struct {
	stdout []string
	cmds   []backup.Cmd
}

func (e *stdoutExecutor) Run(_ context.Context, cmd backup.Cmd, onStdout, _ func(string)) (int, error) {
	e.cmds = append(e.cmds, cmd)
	if onStdout != nil && len(e.cmds) <= len(e.stdout) && e.stdout[len(e.cmds)-1] != "" {
		onStdout(e.stdout[len(e.cmds)-1])
	}
	return 0, nil
}

// DeleteByTags 不能用 forget 策略表达"全删"：--keep-last 0 会被 restic 判为
// "未指定策略"并失败（Fatal: no policy was specified）。必须改为先枚举匹配的
// 快照 ID，再按 ID 删除，且只删匹配全部给定标签的那些。
func TestDeleteByTagsForgetsMatchingSnapshotIDs(t *testing.T) {
	list := `[{"id":"aaaa1111","time":"2026-10-08T00:00:00Z","host":"h1","tags":["plan:plan-1","kind:mysql","run:r1"]},` +
		`{"id":"bbbb2222","time":"2026-10-08T00:00:01Z","host":"h1","tags":["plan:plan-2","kind:mysql","run:r2"]}]`
	exec := &stdoutExecutor{stdout: []string{list, ""}}
	if err := DeleteByTags(context.Background(), exec, Options{
		Exe: "restic", RepoPath: "rclone:remote:/repo", CacheDir: "/cache/restic",
	}, []string{"plan:plan-1"}); err != nil {
		t.Fatalf("DeleteByTags: %v", err)
	}
	if len(exec.cmds) != 2 {
		t.Fatalf("command count = %d, want 2 (snapshots then forget)", len(exec.cmds))
	}
	if exec.cmds[0].Args[0] != "snapshots" {
		t.Fatalf("first command must be snapshots, got %q", exec.cmds[0].Args)
	}
	want := []string{"forget", "aaaa1111", "--prune", "--repo", "rclone:remote:/repo", "--cache-dir", "/cache/restic", "--json"}
	got := exec.cmds[1].Args
	if len(got) != len(want) {
		t.Fatalf("forget args = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("forget args = %q, want %q", got, want)
		}
	}
}

// 无标签时必须拒绝，避免匹配整个仓库造成误删。
func TestDeleteByTagsRefusesEmptyTags(t *testing.T) {
	exec := &stdoutExecutor{}
	if err := DeleteByTags(context.Background(), exec, Options{Exe: "restic"}, nil); err == nil {
		t.Fatal("empty tags must be refused")
	}
	if len(exec.cmds) != 0 {
		t.Fatalf("no command should run, got %d", len(exec.cmds))
	}
}

// 没有匹配快照时不应执行任何删除。
func TestDeleteByTagsNoMatchIsNoop(t *testing.T) {
	exec := &stdoutExecutor{stdout: []string{`[{"id":"aaaa1111","tags":["plan:other"]}]`}}
	if err := DeleteByTags(context.Background(), exec, Options{Exe: "restic"}, []string{"plan:plan-1"}); err != nil {
		t.Fatalf("DeleteByTags: %v", err)
	}
	if len(exec.cmds) != 1 {
		t.Fatalf("only the listing should run, got %d commands", len(exec.cmds))
	}
}

func TestDeleteSnapshotsGeneratesCorrectArgs(t *testing.T) {
	var cmd backup.Cmd
	pwdFile := "/tmp/restic-pw"
	cacheDir := "/cache/restic"
	err := DeleteSnapshots(context.Background(), checkExecutor{cmd: &cmd}, Options{
		Exe: "restic", RepoPath: "rclone:remote:/repo", PasswordFile: pwdFile, CacheDir: cacheDir,
	}, []string{"abc123def456"}, true)
	if err != nil {
		t.Fatalf("DeleteSnapshots: %v", err)
	}
	want := []string{"forget", "abc123def456", "--prune", "--repo", "rclone:remote:/repo", "--password-file", pwdFile, "--cache-dir", cacheDir, "--json"}
	if len(cmd.Args) != len(want) {
		t.Fatalf("args = %q, want %q", cmd.Args, want)
	}
	for i, w := range want {
		if cmd.Args[i] != w {
			t.Fatalf("args[%d] = %q, want %q", i, cmd.Args[i], w)
		}
	}
	if !contains(cmd.Env, "RESTIC_PASSWORD_FILE="+pwdFile) {
		t.Fatalf("env missing password file: %q", cmd.Env)
	}
	if !contains(cmd.Env, "RESTIC_CACHE_DIR="+cacheDir) {
		t.Fatalf("env missing cache dir: %q", cmd.Env)
	}
}

func TestDeleteSnapshotsWithoutPruneOmitsFlag(t *testing.T) {
	var cmd backup.Cmd
	err := DeleteSnapshots(context.Background(), checkExecutor{cmd: &cmd}, Options{
		Exe: "restic", RepoPath: "rclone:remote:/repo",
	}, []string{"abc123def456"}, false)
	if err != nil {
		t.Fatalf("DeleteSnapshots: %v", err)
	}
	if contains(cmd.Args, "--prune") {
		t.Fatalf("args should not contain --prune: %q", cmd.Args)
	}
}

func TestDeleteSnapshotsRejectsEmptyIDs(t *testing.T) {
	err := DeleteSnapshots(context.Background(), checkExecutor{code: 0}, Options{Exe: "restic"}, nil, true)
	if err == nil {
		t.Fatal("expected error for empty snapshot IDs")
	}
}

// rclone/sftp 等非本地后端在仓库不存在时只返回 exit 1（本地后端返回 exit 10），
// 必须按输出文本归类，否则 EnsureRepository 永远走不到 init 分支。
func TestSnapshotsMissingRepositoryOnRcloneBackend(t *testing.T) {
	exec := checkExecutor{
		code: 1,
		stderr: "Fatal: unable to open config file: <config/> does not exist\n" +
			"Is there a repository at the following location?\n" +
			"rclone:localtest:shared/inst/agent\n",
	}
	_, err := Snapshots(context.Background(), exec, Options{Exe: "restic", RepoPath: "rclone:localtest:shared/inst/agent"})
	if err == nil {
		t.Fatal("expected an error for a missing repository")
	}
	var re *ResticError
	if !errors.As(err, &re) {
		t.Fatalf("expected ResticError, got %T: %v", err, err)
	}
	if re.Code != model.ErrRepositoryMissing {
		t.Fatalf("expected %s, got %s", model.ErrRepositoryMissing, re.Code)
	}
}

// 其他 exit 1 失败不能被误判为“仓库不存在”（否则会触发一次危险的 restic init）。
func TestSnapshotsUnrelatedFailureStaysGeneric(t *testing.T) {
	exec := checkExecutor{code: 1, stderr: "Fatal: unexpected backend error\n"}
	_, err := Snapshots(context.Background(), exec, Options{Exe: "restic", RepoPath: "rclone:localtest:shared/inst/agent"})
	var re *ResticError
	if !errors.As(err, &re) {
		t.Fatalf("expected ResticError, got %T: %v", err, err)
	}
	if re.Code != "restic_failed" {
		t.Fatalf("expected restic_failed, got %s", re.Code)
	}
}

// 保留策略必须按 host 分组：每条快照都带本次运行唯一的 run:<uuid> 标签，
// staging 路径也每次不同，因此 --group-by host,tags（以及 restic 默认的
// host,paths）会让每条快照自成一"组"，--keep-last/--keep-daily 对每组都成立，
// forget 静默地一个也不删。这是线上实测到的"保留策略空转"缺陷。
func TestForgetGroupsByHostOnly(t *testing.T) {
	var cmd backup.Cmd
	err := ForgetOnly(context.Background(), checkExecutor{cmd: &cmd}, Options{
		Exe: "restic", RepoPath: "rclone:remote:/repo", CacheDir: "/cache/restic",
	}, model.Retention{KeepLast: 1, KeepDaily: 7}, []string{"plan:plan-1", "kind:mysql"})
	if err != nil {
		t.Fatalf("ForgetOnly: %v", err)
	}
	want := []string{"forget", "--group-by", "host", "--repo", "rclone:remote:/repo",
		"--cache-dir", "/cache/restic", "--tag", "plan:plan-1", "--tag", "kind:mysql",
		"--keep-last", "1", "--keep-daily", "7", "--json"}
	if len(cmd.Args) != len(want) {
		t.Fatalf("args = %q, want %q", cmd.Args, want)
	}
	for i := range want {
		if cmd.Args[i] != want[i] {
			t.Fatalf("args = %q, want %q", cmd.Args, want)
		}
	}
	// 分组里绝不能出现 tags 或 paths：两者都含每次运行唯一的值。
	for i := 0; i+1 < len(cmd.Args); i++ {
		if cmd.Args[i] == "--group-by" {
			if g := cmd.Args[i+1]; g != "host" {
				t.Fatalf("--group-by must be host only, got %q", g)
			}
		}
	}
}

// dryRunLineExecutor 逐行回放到 stdout（真实 OSExecutor 也按行切分）。
type dryRunLineExecutor struct{ lines []string }

func (e *dryRunLineExecutor) Run(_ context.Context, _ backup.Cmd, onStdout, _ func(string)) (int, error) {
	if onStdout != nil {
		for _, l := range e.lines {
			onStdout(l)
		}
	}
	return 0, nil
}

// restic 0.18 的 --dry-run --verbose=2 逐文件动词是 restored / updated /
// unchanged（Summary 行只给总数）。旧实现匹配 new/added/changed/skipped，
// 在逐文件行里根本不存在，导致预演恒报 add=0、changed=0，误导“无变化”。
func TestRestoreDryRunParsesResticVerbLines(t *testing.T) {
	lines := []string{
		"restoring snapshot 68980ca7 of [/backup-sources/fsdemo] at 2026-10-08 10:17:52 to /tmp/dr-target",
		"unchanged /backup-sources/fsdemo/a.txt with size 6 B",
		"restored  /backup-sources/fsdemo/blob.bin with size 19.531 KiB",
		"updated   /backup-sources/fsdemo/sub/b.txt with size 5 B",
		"restored  /backup-sources/fsdemo/sub",
		"restored  /backup-sources/fsdemo",
		"restored  /backup-sources",
		"Summary: Restored 5 files/dirs (19.536 KiB) in 0:00, skipped 1 files/dirs 6 B",
	}
	exec := &dryRunLineExecutor{lines: lines}
	progress, err := RestoreDryRunWithOverwrite(context.Background(), exec,
		Options{Exe: "restic", RepoPath: "repo"}, "snap", "/target", nil, "always")
	if err != nil {
		t.Fatalf("RestoreDryRunWithOverwrite: %v", err)
	}
	if progress.FilesAdded != 4 {
		t.Errorf("FilesAdded = %d, want 4", progress.FilesAdded)
	}
	if progress.FilesChanged != 1 {
		t.Errorf("FilesChanged = %d, want 1", progress.FilesChanged)
	}
	if progress.FilesSkipped != 1 {
		t.Errorf("FilesSkipped = %d, want 1", progress.FilesSkipped)
	}
	if len(progress.Sample) != 5 {
		t.Errorf("Sample = %d lines, want 5 (only changing entries)", len(progress.Sample))
	}
}

// 快照下载（restic restore）是数据库/文件系统恢复的共用前置步骤。此前它的
// stdout/stderr 回调都是空函数，且不做 enriched 包装：仓库损坏时运行只显示
// "restic exit 1 (restic_failed): exit 1"、日志中无任何 agent 侧信息，运维无法
// 区分数据损坏、权限不足还是快照缺失。这里锚定 stderr 必须同时进日志与错误。
func TestRestoreFailureSurfacesStderr(t *testing.T) {
	exec := &scriptedExecutor{steps: []struct {
		code int
		err  error
		out  string
	}{{code: 1, out: "Fatal: unable to find pack file ab12"}}}
	var logged []string
	opts := Options{Exe: "restic", RepoPath: "repo", Logf: func(line string) { logged = append(logged, line) }}
	err := RestoreWithOverwrite(context.Background(), exec, opts, "snap1", "/tmp/target", nil, "always")
	if err == nil {
		t.Fatal("want error from failed restore")
	}
	if !strings.Contains(err.Error(), "unable to find pack file") {
		t.Fatalf("error must carry restic stderr, got %v", err)
	}
	if len(logged) == 0 || !strings.Contains(logged[0], "unable to find pack file") {
		t.Fatalf("stderr must be logged, got %v", logged)
	}
}

// init 失败同样必须带上 stderr（仓库不存在 / 权限 / 后端错误）。
func TestInitFailureSurfacesStderr(t *testing.T) {
	exec := &scriptedExecutor{steps: []struct {
		code int
		err  error
		out  string
	}{{code: 1, out: "Fatal: create key in repository failed"}}}
	var logged []string
	opts := Options{Exe: "restic", RepoPath: "repo", Logf: func(line string) { logged = append(logged, line) }}
	if err := Init(context.Background(), exec, opts); err == nil {
		t.Fatal("want error from failed init")
	} else if !strings.Contains(err.Error(), "create key in repository failed") {
		t.Fatalf("init error must carry restic stderr, got %v", err)
	}
	if len(logged) == 0 {
		t.Fatal("init stderr must be logged")
	}
}

// 与 RestoreWithOverwrite 同类的三处调用点（预演 / 仓库文件列举 / 读取仓库配置）
// 此前也丢弃 stderr 且不做 enriched 包装：失败时运行只显示 "restic exit N"，
// 日志中无任何 agent 侧信息。这里逐个锚定 stderr 必须进入错误与日志。
func TestSiblingCallSitesSurfaceStderr(t *testing.T) {
	mk := func(out string) *scriptedExecutor {
		return &scriptedExecutor{steps: []struct {
			code int
			err  error
			out  string
		}{{code: 1, out: out}}}
	}
	opts := func(logged *[]string) Options {
		return Options{Exe: "restic", RepoPath: "repo", Logf: func(l string) { *logged = append(*logged, l) }}
	}
	t.Run("恢复预演", func(t *testing.T) {
		var logged []string
		_, err := RestoreDryRunWithOverwrite(context.Background(), mk("Fatal: unable to open repository"), opts(&logged), "snap1", "/tmp/t", nil, "always")
		if err == nil || !strings.Contains(err.Error(), "unable to open repository") {
			t.Fatalf("dry-run error must carry restic stderr, got %v", err)
		}
		if len(logged) == 0 {
			t.Fatal("dry-run stderr must be logged")
		}
	})
	t.Run("列举仓库文件", func(t *testing.T) {
		var logged []string
		_, err := Ls(context.Background(), mk("Fatal: snapshot not found"), opts(&logged), "snap1", "/")
		if err == nil || !strings.Contains(err.Error(), "snapshot not found") {
			t.Fatalf("ls error must carry restic stderr, got %v", err)
		}
		if len(logged) == 0 {
			t.Fatal("ls stderr must be logged")
		}
	})
	t.Run("读取仓库配置", func(t *testing.T) {
		var logged []string
		err := CatConfig(context.Background(), mk("Fatal: wrong password"), opts(&logged))
		if err == nil || !strings.Contains(err.Error(), "wrong password") {
			t.Fatalf("cat-config error must carry restic stderr, got %v", err)
		}
		if len(logged) == 0 {
			t.Fatal("cat-config stderr must be logged")
		}
	})
}

// 快照内路径由 API 的 path 查询参数提供（未经绝对路径校验），以 '-' 开头时必须是
// 位置参数而不是选项：实测 `restic ls snap -dash.txt` 报 unknown shorthand flag 'd'。
func TestLsEndsOptionsBeforeSnapshotPath(t *testing.T) {
	var cmd backup.Cmd
	_, err := Ls(context.Background(), checkExecutor{stdout: "", cmd: &cmd},
		Options{Exe: "restic", RepoPath: "repo"}, "snap-1", "-dash.txt")
	if err != nil {
		t.Fatalf("Ls: %v", err)
	}
	dash := -1
	for i, a := range cmd.Args {
		if a == "--" {
			dash = i
		}
	}
	if dash < 0 {
		t.Fatalf("args must contain -- separator, got %q", cmd.Args)
	}
	if dash != len(cmd.Args)-3 || cmd.Args[len(cmd.Args)-2] != "snap-1" || cmd.Args[len(cmd.Args)-1] != "-dash.txt" {
		t.Fatalf("-- must precede the positional snapshot id and path, got %q", cmd.Args)
	}
	if cmd.Args[0] != "ls" {
		t.Fatalf("first arg must be the ls subcommand, got %q", cmd.Args)
	}
}
