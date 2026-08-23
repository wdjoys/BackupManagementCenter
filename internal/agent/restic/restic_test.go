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
}

func (e checkExecutor) Run(_ context.Context, _ backup.Cmd, onStdout func(string), onStderr func(string)) (int, error) {
	if onStdout != nil && e.stdout != "" {
		onStdout(e.stdout)
	}
	if onStderr != nil && e.stderr != "" {
		onStderr(e.stderr)
	}
	return e.code, nil
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
