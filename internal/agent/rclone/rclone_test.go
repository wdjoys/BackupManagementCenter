package rclone

import (
	"context"
	"strings"
	"testing"

	"backupmanagementcenter/internal/agent/backup"
)

// fakeExecutor replays canned stdout/stderr and returns a fixed exit code.
type fakeExecutor struct {
	stdout []string
	stderr []string
	exit   int
}

func (f *fakeExecutor) Run(ctx context.Context, c backup.Cmd, onStdout func(string), onStderr func(string)) (int, error) {
	for _, l := range f.stdout {
		onStdout(l)
	}
	for _, l := range f.stderr {
		onStderr(l)
	}
	return f.exit, nil
}

func TestListRemotes_ParsesRemoteNames(t *testing.T) {
	exec := &fakeExecutor{stdout: []string{"gdrive:", "disk.xiaocao.im:", "", "not-a-remote"}, exit: 0}
	remotes, err := ListRemotes(context.Background(), exec, "/tmp/rclone.conf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(remotes) != 2 || remotes[0] != "gdrive" || remotes[1] != "disk.xiaocao.im" {
		t.Fatalf("unexpected remotes: %v", remotes)
	}
}
func TestListRemotes_NilExecutorReturnsError(t *testing.T) {
	_, err := ListRemotes(context.Background(), nil, "/tmp/rclone.conf")
	if err == nil {
		t.Fatal("expected nil executor error")
	}
	if !strings.Contains(err.Error(), "executor is nil") {
		t.Fatalf("unexpected error: %v", err)
	}
}


func TestListRemotes_FailureCarriesStderr(t *testing.T) {
	exec := &fakeExecutor{exit: 3, stderr: []string{"2026/08/23 10:00:00 Failed to create file system: dial tcp: lookup disk.invalid: no such host"}}
	_, err := ListRemotes(context.Background(), exec, "/tmp/rclone.conf")
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"listremotes failed (exit 3)", "no such host"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestLsd_FailureCarriesStderr(t *testing.T) {
	exec := &fakeExecutor{exit: 1, stderr: []string{"2026/08/23 10:00:00 Failed to lsd: 401 Unauthorized"}}
	_, err := Lsd(context.Background(), exec, "/tmp/rclone.conf", "gdrive")
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"lsd failed (exit 1)", "401 Unauthorized"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestLsd_NonZeroExitWithoutStderr(t *testing.T) {
	exec := &fakeExecutor{exit: 1}
	_, err := Lsd(context.Background(), exec, "/tmp/rclone.conf", "gdrive")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "lsd failed (exit 1)") {
		t.Fatalf("error %q missing exit detail", err.Error())
	}
	if strings.Contains(err.Error(), "%!w") || strings.Contains(err.Error(), "<nil>") {
		t.Fatalf("error %q leaks nil-format placeholder", err.Error())
	}
}
