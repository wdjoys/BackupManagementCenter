package agent

import (
	"bytes"
	"context"
	"os/exec"
	"runtime"
	"strings"
	"sync"

	"backupmanagementcenter/internal/model"
)

// ToolProbeFunc is a function that finds a tool and returns its path and version.
type ToolProbeFunc func(ctx context.Context, toolName string) (path, version string)

// Prober probes for tool capabilities.
type Prober struct {
	mu       sync.RWMutex
	cached   map[string]model.ToolInfo
	probeFn  ToolProbeFunc
}

// NewProber creates a new prober.
func NewProber() *Prober {
	return &Prober{
		cached:  make(map[string]model.ToolInfo),
		probeFn: defaultProbe,
	}
}

// NewProberWithProbeFn creates a prober with a custom probe function (for testing).
func NewProberWithProbeFn(fn ToolProbeFunc) *Prober {
	return &Prober{
		cached:  make(map[string]model.ToolInfo),
		probeFn: fn,
	}
}

// toolNames is the list of tools to probe.
var toolNames = []string{
	"restic",
	"rclone",
	"pg_dump",
	"pg_restore",
	"psql",
	"mysqldump",
	"mysql",
	"mongodump",
	"mongorestore",
	"sqlite3",
}

// Probe probes all tools and returns the results.
func (p *Prober) Probe(ctx context.Context) []model.ToolInfo {
	p.mu.Lock()
	defer p.mu.Unlock()

	results := make([]model.ToolInfo, 0, len(toolNames))
	for _, name := range toolNames {
		path, version := p.probeFn(ctx, name)
		info := model.ToolInfo{
			Name:    name,
			Path:    path,
			Version: version,
		}
		p.cached[name] = info
		results = append(results, info)
	}
	return results
}

// GetCached returns the cached tool info map.
func (p *Prober) GetCached() map[string]model.ToolInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make(map[string]model.ToolInfo, len(p.cached))
	for k, v := range p.cached {
		result[k] = v
	}
	return result
}

// defaultProbe finds a tool using exec.LookPath and runs `{tool} --version`.
func defaultProbe(ctx context.Context, toolName string) (path, version string) {
	path, err := exec.LookPath(toolName)
	if err != nil && runtime.GOOS == "windows" && !strings.Contains(toolName, ".") {
		// PATHEXT may be absent in minimal environments.
		path, err = exec.LookPath(toolName + ".exe")
	}
	if err != nil {
		return "", ""
	}

	// Run `{tool} --version` (fall back to `{tool} version` for subcommand-style
	// CLIs like restic) and capture the first line.
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		stdout.Reset()
		cmd2 := exec.CommandContext(ctx, path, "version")
		cmd2.Stdout = &stdout
		if err := cmd2.Run(); err != nil {
			return path, ""
		}
	}
	version = strings.TrimSpace(stdout.String())
	// Take only the first line
	if nl := strings.IndexByte(version, '\n'); nl >= 0 {
		version = version[:nl]
	}
	return path, version
}