package config

import (
	"path/filepath"
	"testing"
)

func TestLoadAgentResticCacheDefaultsToStateDir(t *testing.T) {
	t.Setenv("BMC_SERVER_GRPC_URL", "server:9090")
	t.Setenv("BMC_AGENT_STATE_DIR", filepath.Join("/var", "lib", "bmc-agent"))
	t.Setenv("BMC_AGENT_DATA_DIR", "")
	t.Setenv("BMC_RESTIC_CACHE_DIR", "")
	cfg, err := LoadAgent()
	if err != nil { t.Fatal(err) }
	want := filepath.Join(cfg.StateDir, ".cache", "restic")
	if cfg.ResticCacheDir != want { t.Fatalf("cache = %q, want %q", cfg.ResticCacheDir, want) }
	if cfg.DataDir != filepath.Join(cfg.StateDir, "scratch") { t.Fatalf("data = %q", cfg.DataDir) }
}

func TestLoadAgentResticCacheOverride(t *testing.T) {
	t.Setenv("BMC_SERVER_GRPC_URL", "server:9090")
	t.Setenv("BMC_AGENT_STATE_DIR", "/state")
	t.Setenv("BMC_RESTIC_CACHE_DIR", "/cache/../restic-cache")
	cfg, err := LoadAgent()
	if err != nil { t.Fatal(err) }
	if cfg.ResticCacheDir != filepath.Clean("/cache/../restic-cache") { t.Fatalf("cache = %q", cfg.ResticCacheDir) }
}
