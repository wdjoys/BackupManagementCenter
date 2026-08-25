// Package rclone wraps rclone CLI for remote validation.
package rclone

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"backupmanagementcenter/internal/agent/backup"
)

// maxStderrTail bounds the retained stderr appended to failure errors so a
// multi-KB rclone traceback cannot flood the run error message.
const maxStderrTail = 4 << 10

// WriteConf writes rclone config content to a 0600 file and returns the path.
func WriteConf(tempDir, confContent string) (string, error) {
	p := filepath.Join(tempDir, "rclone.conf")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(confContent); err != nil {
		f.Close()
		return "", err
	}
	return p, f.Close()
}

// captureStderr retains the tail of a child's stderr. rclone prints its actual
// failure reason (DNS, TLS, auth, unknown remote) there; without this the only
// observable detail is "exit status N".
func captureStderr() (sink func(string), tail func() string) {
	var lines []string
	sink = func(line string) {
		line = strings.TrimSpace(line)
		if line == "" {
			return
		}
		lines = append(lines, line)
		if len(lines) > 40 {
			lines = lines[len(lines)-40:]
		}
	}
	tail = func() string {
		s := strings.Join(lines, "; ")
		if len(s) > maxStderrTail {
			s = s[len(s)-maxStderrTail:]
		}
		return s
	}
	return sink, tail
}

// runFailed wraps a non-zero exit with the captured stderr tail.
func runFailed(op string, exitCode int, err error, stderrTail string) error {
	if err == nil {
		err = errors.New("non-zero exit")
	}
	msg := fmt.Sprintf("rclone %s failed (exit %d): %v", op, exitCode, err)
	if t := strings.TrimSpace(stderrTail); t != "" {
		msg += ": " + t
	}
	return errors.New(msg)
}

// ListRemotes runs `rclone listremotes --config <file>` and returns remote names.
func ListRemotes(ctx context.Context, exec backup.Executor, confPath string) ([]string, error) {
	if exec == nil {
		return nil, runFailed("listremotes", -1, errors.New("executor is nil"), "")
	}
	args := []string{"listremotes", "--config", confPath}
	env := []string{"RCLONE_CONFIG=" + confPath}
	var remotes []string
	onStderr, stderr := captureStderr()
	exitCode, err := exec.Run(ctx, backup.Cmd{Exe: "rclone", Args: args, Env: env},
		func(line string) {
			name := strings.TrimSpace(line)
			if name != "" && strings.HasSuffix(name, ":") {
				remotes = append(remotes, strings.TrimSuffix(name, ":"))
			}
		}, onStderr)
	if err != nil || exitCode != 0 {
		return nil, runFailed("listremotes", exitCode, err, stderr())
	}
	return remotes, nil
}

// Lsd runs `rclone lsd <remote>: --config <file>` and returns directory entries.
func Lsd(ctx context.Context, exec backup.Executor, confPath, remote string) ([]string, error) {
	if exec == nil {
		return nil, runFailed("lsd", -1, errors.New("executor is nil"), "")
	}
	args := []string{"lsd", remote + ":", "--config", confPath}
	env := []string{"RCLONE_CONFIG=" + confPath}
	var entries []string
	onStderr, stderr := captureStderr()
	exitCode, err := exec.Run(ctx, backup.Cmd{Exe: "rclone", Args: args, Env: env},
		func(line string) {
			// lsd output format: "          -1 2024-01-01 00:00:00        -1 dirname"
			fields := strings.Fields(line)
			if len(fields) >= 5 && fields[4] != "" {
				entries = append(entries, fields[4])
			}
		}, onStderr)
	if err != nil || exitCode != 0 {
		return nil, runFailed("lsd", exitCode, err, stderr())
	}
	return entries, nil
}
