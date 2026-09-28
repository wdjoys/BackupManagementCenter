package agent

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"

	"backupmanagementcenter/internal/agent/backup"
)

// maxStreamLine bounds one stdout/stderr line. Single-document JSON commands
// (restic snapshots --json) print the whole array as one line, so the limit
// must clear the gRPC result payload by a wide margin.
const maxStreamLine = 16 << 20

// OSExecutor implements backup.Executor using os/exec.
type OSExecutor struct{}

// Run executes a command, streaming stdout/stderr to callbacks.
func (OSExecutor) Run(ctx context.Context, cmd backup.Cmd, onStdout func(line string), onStderr func(line string)) (exitCode int, err error) {
	// Build the command
	c := exec.CommandContext(ctx, cmd.Exe, cmd.Args...)
	c.Env = childEnv(cmd.Env)
	if cmd.Dir != "" {
		c.Dir = cmd.Dir
	}
	if cmd.StdinPath != "" {
		stdin, openErr := os.Open(cmd.StdinPath)
		if openErr != nil {
			return -1, openErr
		}
		defer stdin.Close()
		c.Stdin = stdin
	}

	// Set up stdout pipe
	stdout, err := c.StdoutPipe()
	if err != nil {
		return -1, err
	}

	// Set up stderr pipe
	stderr, err := c.StderrPipe()
	if err != nil {
		return -1, err
	}

	// Start the command
	if err := c.Start(); err != nil {
		return -1, err
	}

	// Read stdout and stderr concurrently. A read failure must be reported:
	// silently abandoning the pipe blocks the child on a full pipe forever, so
	// the run would never reach a terminal state.
	streams := make(chan error, 2)
	go func() { streams <- scanLines(stdout, onStdout) }()
	go func() { streams <- scanLines(stderr, onStderr) }()

	// Wait for both pipes to finish
	var streamErr error
	for range 2 {
		if err := <-streams; err != nil && streamErr == nil {
			streamErr = err
		}
	}

	// Wait for the process to exit
	err = c.Wait()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		// Context canceled/timeout
		if ctx.Err() != nil {
			return -1, ctx.Err()
		}
		return -1, err
	}
	if streamErr != nil {
		return -1, streamErr
	}
	return 0, nil
}

// scanLines forwards complete lines to onLine. After a read failure the pipe is
// drained to io.Discard so the child can exit instead of blocking forever on a
// full pipe, and the failure is returned to the caller.
func scanLines(r io.Reader, onLine func(line string)) error {
	scanner := bufio.NewScanner(r)
	// Single-document JSON output (restic snapshots --json) is one long line;
	// the 64 KiB scanner default would abort it mid-line.
	scanner.Buffer(make([]byte, 0, 64*1024), maxStreamLine)
	for scanner.Scan() {
		if onLine != nil {
			onLine(scanner.Text())
		}
	}
	if err := scanner.Err(); err != nil {
		_, _ = io.Copy(io.Discard, r)
		return fmt.Errorf("read command output: %w", err)
	}
	return nil
}

// CancelFunc kills the process group for the given context.
// This is used for task cancellation.
func CancelFunc(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if runtime.GOOS == "windows" {
		// On Windows, kill the process tree
		kill := exec.Command("taskkill", "/F", "/T", "/PID", itoa(cmd.Process.Pid))
		_ = kill.Run()
	} else {
		// On POSIX, send SIGTERM to the process group
		sigCmd := exec.Command("kill", "-TERM", itoa(cmd.Process.Pid))
		_ = sigCmd.Run()
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
