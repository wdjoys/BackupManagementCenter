// Package backup implements plan-kind adapters.
package backup

import (
	"context"
	"strings"
)

// toolPaths is populated by the pipeline before invoking adapters. It maps
// tool name to its absolute path from the latest capability probe. Adapters
// look up paths here rather than hardcoding "pg_dump" etc. so the agent can
// use discovered binary locations. SetToolPaths is exported for the pipeline;
// lookups are via unexported toolPath to keep the surface small.
var toolPaths = map[string]string{}

// SetToolPaths replaces the tool-path map used by adapters.
func SetToolPaths(tools map[string]ToolInfo) {
	m := make(map[string]string, len(tools))
	for name, info := range tools {
		m[name] = info.Path
	}
	toolPaths = m
}

// toolPath returns the configured absolute path for a tool, falling back to
// the bare name if unknown so tests and minimal environments still work.
func toolPath(name string) string {
	if p, ok := toolPaths[name]; ok && p != "" {
		return p
	}
	return name
}

// WriteSecretFile writes <tempDir>/<name> with 0600 permissions and returns
// the absolute path. Exported for packages outside backup (e.g. pipeline)
// that must create secret files in the private temp dir.
func WriteSecretFile(tempDir, name, content string) (string, error) {
	return writeSecretFile(tempDir, name, content)
}

// getToolVersion runs `<exe> --version` and returns the first non-empty
// stdout line, or "" if it fails. Shared by all database adapters.
func getToolVersion(ctx context.Context, exec Executor, exe string, env []string) string {
	var version string
	_, _ = exec.Run(ctx, Cmd{Exe: exe, Args: []string{"--version"}, Env: env},
		func(line string) {
			if version == "" {
				version = strings.TrimSpace(line)
			}
		}, func(line string) {})
	return version
}