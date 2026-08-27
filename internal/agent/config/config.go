// Package config loads agent configuration from the environment.
package config

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Agent struct {
	ServerGRPCURL       string
	ServerTLS           bool
	EnrollToken         string
	StateDir            string
	DataDir             string
	ResticCacheDir      string
	DevInsecure         bool
	ProbeInterval       int
	SourceRoots         []string
	RestoreRoots        []string
	ScratchMinFreeBytes int64
	MaxConcurrency      int
}

func LoadAgent() (Agent, error) {
	stateDir := envOr("BMC_AGENT_STATE_DIR", "./agent-state")
	a := Agent{
		ServerGRPCURL: os.Getenv("BMC_SERVER_GRPC_URL"), ServerTLS: os.Getenv("BMC_SERVER_TLS") != "0",
		EnrollToken: os.Getenv("BMC_ENROLLMENT_TOKEN"), StateDir: stateDir, DataDir: os.Getenv("BMC_AGENT_DATA_DIR"),
		ResticCacheDir: filepath.Clean(envOr("BMC_RESTIC_CACHE_DIR", filepath.Join(stateDir, ".cache", "restic"))),
		DevInsecure: os.Getenv("BMC_DEV_INSECURE") == "1", ProbeInterval: envInt("BMC_AGENT_PROBE_INTERVAL", 600),
		SourceRoots: splitPaths(os.Getenv("BMC_SOURCE_ROOTS")), RestoreRoots: splitPaths(os.Getenv("BMC_RESTORE_ROOTS")),
		ScratchMinFreeBytes: envInt64("BMC_SCRATCH_MIN_FREE_BYTES", 0), MaxConcurrency: envInt("BMC_AGENT_MAX_CONCURRENCY", 2),
	}
	if a.ServerGRPCURL == "" { return a, errors.New("config: BMC_SERVER_GRPC_URL is required") }
	if a.DataDir == "" { a.DataDir = filepath.Join(a.StateDir, "scratch") }
	return a, nil
}

func splitPaths(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") { if p = filepath.Clean(strings.TrimSpace(p)); p != "." && p != "" { out = append(out, p) } }
	return out
}
func envOr(key, def string) string { if v := os.Getenv(key); v != "" { return v }; return def }
func envInt(key string, def int) int { if v := os.Getenv(key); v != "" { if n, err := strconv.Atoi(v); err == nil { return n } }; return def }
func envInt64(key string, def int64) int64 { if v := os.Getenv(key); v != "" { if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 { return n } }; return def }
