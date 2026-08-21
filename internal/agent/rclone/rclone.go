// Package rclone wraps rclone CLI for remote validation.
package rclone

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"backupmanagementcenter/internal/agent/backup"
)

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

// ListRemotes runs `rclone listremotes --config <file>` and returns remote names.
func ListRemotes(ctx context.Context, exec backup.Executor, confPath string) ([]string, error) {
	args := []string{"listremotes", "--config", confPath}
	env := []string{"RCLONE_CONFIG=" + confPath}
	var remotes []string
	exitCode, err := exec.Run(ctx, backup.Cmd{Exe: "rclone", Args: args, Env: env},
		func(line string) {
			name := strings.TrimSpace(line)
			if name != "" && strings.HasSuffix(name, ":") {
				remotes = append(remotes, strings.TrimSuffix(name, ":"))
			}
		}, func(string) {})
	if err != nil || exitCode != 0 {
		return nil, fmt.Errorf("rclone listremotes failed (exit %d): %w", exitCode, err)
	}
	return remotes, nil
}

// Lsd runs `rclone lsd <remote>: --config <file>` and returns directory entries.
func Lsd(ctx context.Context, exec backup.Executor, confPath, remote string) ([]string, error) {
	args := []string{"lsd", remote + ":", "--config", confPath}
	env := []string{"RCLONE_CONFIG=" + confPath}
	var entries []string
	exitCode, err := exec.Run(ctx, backup.Cmd{Exe: "rclone", Args: args, Env: env},
		func(line string) {
			// lsd output format: "          -1 2024-01-01 00:00:00        -1 dirname"
			fields := strings.Fields(line)
			if len(fields) >= 5 && fields[4] != "" {
				entries = append(entries, fields[4])
			}
		}, func(string) {})
	if err != nil || exitCode != 0 {
		return nil, fmt.Errorf("rclone lsd failed (exit %d): %w", exitCode, err)
	}
	return entries, nil
}