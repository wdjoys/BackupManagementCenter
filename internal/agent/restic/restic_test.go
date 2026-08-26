package restic

import (
	"context"
	"strings"
	"testing"

	"backupmanagementcenter/internal/agent/backup"
)

type checkExecutor struct {
	stdout string
	stderr string
	code   int
	cmd    *backup.Cmd
}

func (e checkExecutor) Run(_ context.Context, cmd backup.Cmd, onStdout func(string), onStderr func(string)) (int, error) {
	if e.cmd != nil {
		*e.cmd = cmd
	}
	if onStdout != nil && e.stdout != "" {
		onStdout(e.stdout)
	}
	if onStderr != nil && e.stderr != "" {
		onStderr(e.stderr)
	}
	return e.code, nil
}

func TestLsFiltersByRequestedDirectory(t *testing.T) {
	var cmd backup.Cmd
	entries, err := Ls(context.Background(), checkExecutor{
		stdout: `{"name":"child","type":"dir","path":"/backup/child","size":0,"mtime":"2026-08-26T00:00:00Z"}`,
		cmd:    &cmd,
	}, Options{Exe: "restic", RepoPath: "rclone:remote:/repo"}, "snapshot-id", "/backup")
	if err != nil {
		t.Fatalf("Ls: %v", err)
	}
	wantArgs := []string{"ls", "snapshot-id", "/backup", "--repo", "rclone:remote:/repo", "--json"}
	if len(cmd.Args) != len(wantArgs) {
		t.Fatalf("args = %q, want %q", cmd.Args, wantArgs)
	}
	for i, want := range wantArgs {
		if cmd.Args[i] != want {
			t.Fatalf("args = %q, want %q", cmd.Args, wantArgs)
		}
	}
	if len(entries) != 1 || entries[0].Path != "/backup/child" {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestCheckIncludesCommandOutputOnFailure(t *testing.T) {
	err := Check(context.Background(), checkExecutor{
		stdout: `{"message_type":"error","message":"index is damaged"}`,
		stderr: "Fatal: repository check failed",
		code:   1,
	}, Options{Exe: "restic", RepoPath: "rclone:remote:/repo"})
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
	_, _, err := Backup(context.Background(), checkExecutor{
		stdout: `{"message_type":"exit_error","code":3,"message":"permission denied: /backup-sources/etc/shadow"}`,
		stderr: "unable to read source file",
		code:   3,
	}, Options{Exe: "restic", RepoPath: "rclone:remote:/repo"}, []string{"/backup-sources"}, "", nil, false, nil)
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
