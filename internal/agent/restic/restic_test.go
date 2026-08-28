package restic

import (
	"context"
	"strings"
	"testing"

	"backupmanagementcenter/internal/agent/backup"
)

type checkExecutor struct { stdout, stderr string; code int; cmd *backup.Cmd }
func (e checkExecutor) Run(_ context.Context, cmd backup.Cmd, onStdout func(string), onStderr func(string)) (int, error) {
	if e.cmd != nil { *e.cmd = cmd }
	if onStdout != nil && e.stdout != "" { onStdout(e.stdout) }
	if onStderr != nil && e.stderr != "" { onStderr(e.stderr) }
	return e.code, nil
}
func contains(values []string, want string) bool { for _, value := range values { if value == want { return true } }; return false }

func TestLsFiltersByRequestedDirectory(t *testing.T) {
	var cmd backup.Cmd; cacheDir := "/var/lib/bmc-agent/.cache/restic"
	entries, err := Ls(context.Background(), checkExecutor{stdout: `{"name":"child","type":"dir","path":"/backup/child","size":0,"mtime":"2026-08-26T00:00:00Z"}`, cmd: &cmd}, Options{Exe: "restic", RepoPath: "rclone:remote:/repo", CacheDir: cacheDir}, "snapshot-id", "/backup")
	if err != nil { t.Fatalf("Ls: %v", err) }
	wantArgs := []string{"ls", "snapshot-id", "/backup", "--repo", "rclone:remote:/repo", "--cache-dir", cacheDir, "--json"}
	if len(cmd.Args) != len(wantArgs) { t.Fatalf("args = %q, want %q", cmd.Args, wantArgs) }
	for i, want := range wantArgs { if cmd.Args[i] != want { t.Fatalf("args = %q, want %q", cmd.Args, wantArgs) } }
	if !contains(cmd.Env, "RESTIC_CACHE_DIR="+cacheDir) { t.Fatalf("env = %q", cmd.Env) }
	if len(entries) != 1 || entries[0].Path != "/backup/child" { t.Fatalf("entries = %#v", entries) }
}

func TestSnapshotsUsesCacheDir(t *testing.T) {
	var cmd backup.Cmd; cacheDir := "/cache/restic"
	_, err := Snapshots(context.Background(), checkExecutor{stdout: `[]`, cmd: &cmd}, Options{Exe: "restic", RepoPath: "rclone:remote:/repo", CacheDir: cacheDir})
	if err != nil { t.Fatal(err) }
	if !contains(cmd.Args, "--cache-dir") || !contains(cmd.Args, cacheDir) { t.Fatalf("args = %q", cmd.Args) }
	if !contains(cmd.Env, "RESTIC_CACHE_DIR="+cacheDir) { t.Fatalf("env = %q", cmd.Env) }
}

func TestCheckIncludesCommandOutputOnFailure(t *testing.T) {
	err := Check(context.Background(), checkExecutor{stdout: `{"message_type":"error","message":"index is damaged"}`, stderr: "Fatal: repository check failed", code: 1}, Options{Exe: "restic", RepoPath: "rclone:remote:/repo"})
	if err == nil { t.Fatal("expected check failure") }; msg := err.Error()
	for _, want := range []string{"restic exit 1", "index is damaged", "repository check failed"} { if !strings.Contains(msg, want) { t.Fatalf("error %q does not contain %q", msg, want) } }
}
func TestBackupIncludesCommandOutputOnFailure(t *testing.T) {
	_, _, err := Backup(context.Background(), checkExecutor{stdout: `{"message_type":"exit_error","code":3,"message":"permission denied: /backup-sources/etc/shadow"}`, stderr: "unable to read source file", code: 3}, Options{Exe: "restic", RepoPath: "rclone:remote:/repo"}, []string{"/backup-sources"}, "", nil, false, nil)
	if err == nil { t.Fatal("expected backup failure") }; msg := err.Error()
	for _, want := range []string{"partial_source_read", "permission denied", "unable to read source file"} { if !strings.Contains(msg, want) { t.Fatalf("error %q does not contain %q", msg, want) } }
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
  if !contains(cmd.Env, "RESTIC_PASSWORD_FILE="+pwdFile) { t.Fatalf("env missing password file: %q", cmd.Env) }
  if !contains(cmd.Env, "RESTIC_CACHE_DIR="+cacheDir) { t.Fatalf("env missing cache dir: %q", cmd.Env) }
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
