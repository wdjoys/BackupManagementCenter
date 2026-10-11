// Package backup implements plan-kind adapters.
package backup

import (
	"context"
	"fmt"
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

// exitError 构造工具失败错误。OSExecutor 对非零退出返回 (exitCode, nil)，
// err 仅在进程无法启动或流读取失败时非 nil；直接 %w 会把 nil 渲染成
// "%!w(<nil>)"，因此这里按需拼上退出码。
func exitError(tool string, exitCode int, err error) error {
	if err != nil {
		return fmt.Errorf("%s failed (exit %d): %w", tool, exitCode, err)
	}
	return fmt.Errorf("%s failed (exit %d)", tool, exitCode)
}

// stderrCapture 收集工具 stderr 的关键行，同时把每一行转给日志回调。
// 保留最后 20 行：失败原因通常在末尾，而 stderr 可能包含大量进度/警告。
type stderrCapture struct {
	tail []string
	logf func(line string)
}

// line 用作 Executor 的 onStderr 回调。
func (s *stderrCapture) line(l string) {
	if trimmed := strings.TrimSpace(l); trimmed != "" {
		s.tail = append(s.tail, trimmed)
		if len(s.tail) > 20 {
			s.tail = s.tail[len(s.tail)-20:]
		}
	}
	if s.logf != nil {
		s.logf(l)
	}
}

// stderrReason 从捕获的 stderr 里挑出最能说明失败原因的一行：优先最后一条含
// error/denied/failed 的行（MySQL 的 1044/1142、PostgreSQL 的 "pg_dump: error:"
// 都命中），否则退回最后一行。返回空串表示没有可用内容。
func stderrReason(lines []string) string {
	const maxLen = 400
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.ToLower(lines[i])
		if strings.Contains(l, "error") || strings.Contains(l, "denied") || strings.Contains(l, "failed") {
			return truncateReason(lines[i], maxLen)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return truncateReason(lines[len(lines)-1], maxLen)
}

// truncateReason 截断过长的单行原因，避免整条 stderr 灌进 error_message。
func truncateReason(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// exitErrorWithStderr 在 exitError 的文案后并入工具 stderr 的关键行，使运行错误
// （GET /runs/{id}.error_message）直接可读——此前失败原因只出现在 run 日志流里，
// 列表/详情页只看到 "mysqldump failed (exit 2)"，无法区分权限错与网络错。
func exitErrorWithStderr(tool string, exitCode int, err error, stderrTail []string) error {
	base := exitError(tool, exitCode, err)
	if reason := stderrReason(stderrTail); reason != "" {
		return fmt.Errorf("%w: %s", base, reason)
	}
	return base
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
