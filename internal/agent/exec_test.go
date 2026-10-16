package agent

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"backupmanagementcenter/internal/agent/backup"
)

// longLineBytes exceeds bufio's default 64 KiB scan limit. `restic snapshots
// --json` prints the whole snapshot array as one such line, so the executor
// must deliver it whole.
const longLineBytes = 512 * 1024

// TestExecHelperProcess re-executes this test binary to emit one oversized
// stdout line. It only does work in helper mode and is a no-op otherwise.
func TestExecHelperProcess(t *testing.T) {
	if os.Getenv("BMC_EXEC_TEST_HELPER") != "1" {
		return
	}
	fmt.Fprint(os.Stdout, strings.Repeat("x", longLineBytes), "\n")
	os.Exit(0)
}

// 回归：单行 stdout 超过 bufio 默认 64 KiB 上限时必须完整送达。旧实现遇到
// ErrTooLong 就停止读取，子进程写满管道后永久阻塞，run 永不终结（表现为快照页
// 持续核验且内容不显示）。
func TestOSExecutor_LongSingleLine(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var lines []string
	exitCode, err := OSExecutor{}.Run(ctx, backup.Cmd{
		Exe:  os.Args[0],
		Args: []string{"-test.run=TestExecHelperProcess"},
		Env:  []string{"BMC_EXEC_TEST_HELPER=1"},
	}, func(line string) {
		lines = append(lines, line)
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d", exitCode)
	}
	if len(lines) != 1 {
		t.Fatalf("expected 1 stdout line, got %d", len(lines))
	}
	if len(lines[0]) != longLineBytes {
		t.Fatalf("expected %d bytes on the line, got %d", longLineBytes, len(lines[0]))
	}
}

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

	probe := cmdForTest("printenv", "BMC_TEST_VAR")
	if runtime.GOOS == "windows" {
		probe = cmdForTest("echo", "%BMC_TEST_VAR%")
	}
	exitCode, err := exec.Run(ctx, backup.Cmd{
		Exe: probe.Exe, Args: probe.Args,
		Env: []string{"BMC_TEST_VAR=hello-world"},
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
	if len(args) == 1 && args[0] == "exit1" {
		args = []string{"exit", "1"}
	}
	return backup.Cmd{
		Exe:  shell(),
		Args: shellArgs(args...),
	}
}

func shell() string {
	if runtime.GOOS == "windows" {
		return "cmd.exe"
	}
	return "sh"
}

func shellArgs(args ...string) []string {
	flag := "-c"
	if runtime.GOOS == "windows" {
		flag = "/c"
	}
	if len(args) == 0 {
		return []string{flag, "exit"} // no-op success
	}
	return []string{flag, strings.Join(args, " ")}
}
