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
	wantArgs := []string{"ls", "snapshot-id", "/backup", "--repo", "rclone:remote:/repo", "--cache-dir", cacheDir, "--json"}
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
	_, _, err := Backup(context.Background(), checkExecutor{stdout: `{"message_type":"exit_error","code":3,"message":"permission denied: /backup-sources/etc/shadow"}`, stderr: "unable to read source file", code: 3}, Options{Exe: "restic", RepoPath: "rclone:remote:/repo"}, []string{"/backup-sources"}, "", nil, false, nil)
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
		`{"message_type":"status","data":{"percent_done":0.25,"total_bytes":104857600,"bytes_done":26214400,"total_files":100,"files_done":25}}`,
		`{"message_type":"summary","data":{"snapshot_id":"snap-123","total_bytes_processed":104857600,"total_files_processed":100}}`,
	}, "\n")

	var got []model.Progress
	onProg := func(p model.Progress) {
		got = append(got, p)
	}

	summary, snapID, err := Backup(context.Background(), checkExecutor{stdout: stdoutLines}, Options{
		Exe: "restic", RepoPath: "rclone:remote:/repo",
	}, []string{"/data"}, "", nil, false, onProg)
	if err != nil {
		t.Fatalf("Backup failed: %v", err)
	}
	if snapID != "snap-123" {
		t.Fatalf("snapID = %q, want snap-123", snapID)
	}
	if summary == "" {
		t.Fatal("expected summary")
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
	if status.Phase != "backup" {
		t.Errorf("status.Phase = %q, want backup", status.Phase)
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
	if final.Phase != "done" {
		t.Errorf("final.Phase = %q, want done", final.Phase)
	}
}

func TestDeleteByTagsDeletesSnapshotsAndPrunes(t *testing.T) {
	var cmd backup.Cmd
	err := DeleteByTags(context.Background(), checkExecutor{cmd: &cmd}, Options{
		Exe: "restic", RepoPath: "rclone:remote:/repo", CacheDir: "/cache/restic",
	}, []string{"plan:plan-1"})
	if err != nil {
		t.Fatalf("DeleteByTags: %v", err)
	}
	want := []string{"forget", "--group-by", "host,tags", "--prune", "--repo", "rclone:remote:/repo", "--cache-dir", "/cache/restic", "--tag", "plan:plan-1", "--keep-last", "0", "--keep-daily", "0", "--keep-weekly", "0", "--keep-monthly", "0", "--json"}
	if len(cmd.Args) != len(want) {
		t.Fatalf("args = %q, want %q", cmd.Args, want)
	}
	for i := range want {
		if cmd.Args[i] != want[i] {
			t.Fatalf("args = %q, want %q", cmd.Args, want)
		}
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
