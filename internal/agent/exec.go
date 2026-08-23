package agent

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"runtime"

	"backupmanagementcenter/internal/agent/backup"
)

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

	// Read stdout and stderr concurrently
	done := make(chan struct{}, 2)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if onStdout != nil {
				onStdout(scanner.Text())
			}
		}
		done <- struct{}{}
	}()
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			if onStderr != nil {
				onStderr(scanner.Text())
			}
		}
		done <- struct{}{}
	}()

	// Wait for both pipes to finish
	<-done
	<-done

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
	return 0, nil
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
