package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"backupmanagementcenter/internal/agent/backup"
)

func TestOSExecutor_StdoutStderrExitCode(t *testing.T) {
	exec := OSExecutor{}

	var stdoutLines, stderrLines []string
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	exitCode, err := exec.Run(ctx, backup.Cmd{
		Exe:  cmdForTest("echo stdout-line-1 && echo stdout-line-2").Exe,
		Args: cmdForTest("echo stdout-line-1 && echo stdout-line-2").Args,
		Env:  []string{},
	}, func(line string) {
		stdoutLines = append(stdoutLines, line)
	}, func(line string) {
		stderrLines = append(stderrLines, line)
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d", exitCode)
	}
	for i := range stdoutLines {
		stdoutLines[i] = strings.TrimRight(stdoutLines[i], " \t\r")
	}
	if len(stdoutLines) != 2 {
		t.Fatalf("expected 2 stdout lines, got %d: %v", len(stdoutLines), stdoutLines)
	}
	if stdoutLines[0] != "stdout-line-1" {
		t.Fatalf("expected trimmed %q", stdoutLines[0])
	}
	if stdoutLines[1] != "stdout-line-2" {
		t.Fatalf("expected 'stdout-line-2', got %q", stdoutLines[1])
	}
	if len(stderrLines) != 0 {
		t.Fatalf("expected 0 stderr lines, got %d: %v", len(stderrLines), stderrLines)
	}
}

func TestOSExecutor_ExitCode(t *testing.T) {
	exec := OSExecutor{}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	exitCode, err := exec.Run(ctx, backup.Cmd{
		Exe: cmdForTest("exit1").Exe, Args: cmdForTest("exit1").Args,
		Env: []string{},
	}, nil, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode != 1 {
		t.Fatalf("expected exit 1, got %d", exitCode)
	}
}

func TestOSExecutor_EnvInjection(t *testing.T) {
	exec := OSExecutor{}

	var captured []string
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	exitCode, err := exec.Run(ctx, backup.Cmd{
		Exe: cmdForTest("printenv", "BMC_TEST_VAR").Exe, Args: cmdForTest("printenv", "BMC_TEST_VAR").Args,
		Env:  []string{"BMC_TEST_VAR=hello-world"},
	}, func(line string) {
		captured = append(captured, line)
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d", exitCode)
	}
	if len(captured) != 1 {
		t.Fatalf("expected 1 line, got %d: %v", len(captured), captured)
	}
	if captured[0] != "hello-world" {
		t.Fatalf("expected 'hello-world', got %q", captured[0])
	}
}

func TestOSExecutor_ContextCancelled(t *testing.T) {
	exec := OSExecutor{}

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel before starting
	cancel()

	_, err := exec.Run(ctx, backup.Cmd{
		Exe: cmdForTest("sleep", "1000").Exe, Args: cmdForTest("sleep", "1000").Args,
		Env: []string{},
	}, nil, nil)

	if err == nil {
		t.Fatal("expected error on cancelled context")
	}
	if !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("expected 'canceled' in error, got %q", err.Error())
	}
}

func cmdForTest(args ...string) backup.Cmd {
	return backup.Cmd{
		Exe:  shell(),
		Args: shellArgs(args...),
	}
}

func shell() string {
	return "cmd.exe"
}

func shellArgs(args ...string) []string {
	if len(args) == 0 {
		return []string{"/c", "exit"} // no-op success
	}
	return []string{"/c", strings.Join(args, " ")}
}