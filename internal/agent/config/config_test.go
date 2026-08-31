package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAgentResticCacheDefaultsToStateDir(t *testing.T) {
	t.Setenv("BMC_SERVER_GRPC_URL", "server:9090")
	t.Setenv("BMC_AGENT_STATE_DIR", filepath.Join("/var", "lib", "bmc-agent"))
	t.Setenv("BMC_AGENT_DATA_DIR", "")
	t.Setenv("BMC_RESTIC_CACHE_DIR", "")
	cfg, err := LoadAgent()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(cfg.StateDir, ".cache", "restic")
	if cfg.ResticCacheDir != want {
		t.Fatalf("cache = %q, want %q", cfg.ResticCacheDir, want)
	}
	if cfg.DataDir != filepath.Join(cfg.StateDir, "scratch") {
		t.Fatalf("data = %q", cfg.DataDir)
	}
}

func TestLoadAgentResticCacheOverride(t *testing.T) {
	t.Setenv("BMC_SERVER_GRPC_URL", "server:9090")
	t.Setenv("BMC_AGENT_STATE_DIR", "/state")
	t.Setenv("BMC_RESTIC_CACHE_DIR", "/cache/../restic-cache")
	cfg, err := LoadAgent()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ResticCacheDir != filepath.Clean("/cache/../restic-cache") {
		t.Fatalf("cache = %q", cfg.ResticCacheDir)
	}
}

func TestLoadAgentSourcePathMappings(t *testing.T) {
	t.Setenv("BMC_SERVER_GRPC_URL", "server:9090")
	hostRoot := filepath.Join(t.TempDir(), "host")
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	raw, err := json.Marshal(map[string]string{hostRoot: runtimeRoot})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BMC_SOURCE_PATH_MAPPINGS", string(raw))
	cfg, err := LoadAgent()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.SourcePathMappings) != 1 {
		t.Fatalf("mappings = %#v", cfg.SourcePathMappings)
	}
	mapping := cfg.SourcePathMappings[0]
	if mapping.HostPath != filepath.Clean(hostRoot) || mapping.RuntimePath != filepath.Clean(runtimeRoot) || !mapping.ReadOnly {
		t.Fatalf("mapping = %#v", mapping)
	}
}

func TestLoadAgentRestorePathMappings(t *testing.T) {
	t.Setenv("BMC_SERVER_GRPC_URL", "server:9090")
	hostRoot := filepath.Join(t.TempDir(), "restore-host")
	runtimeRoot := filepath.Join(t.TempDir(), "restore-runtime")
	raw, err := json.Marshal(map[string]string{hostRoot: runtimeRoot})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BMC_RESTORE_PATH_MAPPINGS", string(raw))
	cfg, err := LoadAgent()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.RestorePathMappings) != 1 || cfg.RestorePathMappings[0].ReadOnly {
		t.Fatalf("restore mappings = %#v", cfg.RestorePathMappings)
	}
}

func TestLoadAgentRestoreRootDerivesMappingAndRoot(t *testing.T) {
	t.Setenv("BMC_SERVER_GRPC_URL", "server:9090")
	t.Setenv("BMC_RESTORE_PATH_MAPPINGS", "")
	t.Setenv("BMC_RESTORE_ROOTS", "")
	root := filepath.Join(t.TempDir(), "restore", "..", "restore-root")
	t.Setenv("BMC_RESTORE_ROOT", root)

	cfg, err := LoadAgent()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.RestorePathMappings) != 1 {
		t.Fatalf("restore mappings = %#v", cfg.RestorePathMappings)
	}
	mapping := cfg.RestorePathMappings[0]
	if mapping.HostPath != filepath.Clean(root) || mapping.RuntimePath != "/backup-restore" || mapping.ReadOnly {
		t.Fatalf("mapping = %#v", mapping)
	}
	if len(cfg.RestoreRoots) != 1 || cfg.RestoreRoots[0] != "/backup-restore" {
		t.Fatalf("restore roots = %#v", cfg.RestoreRoots)
	}
}

func TestLoadAgentRejectsInvalidRestoreRoot(t *testing.T) {
	for _, root := range []string{"/", "relative/path", "."} {
		t.Run(root, func(t *testing.T) {
			t.Setenv("BMC_SERVER_GRPC_URL", "server:9090")
			t.Setenv("BMC_RESTORE_PATH_MAPPINGS", "")
			t.Setenv("BMC_RESTORE_ROOTS", "")
			t.Setenv("BMC_RESTORE_ROOT", root)
			if _, err := LoadAgent(); err == nil || !strings.Contains(err.Error(), "BMC_RESTORE_ROOT") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestLoadAgentRestoreConfigurationExplicitValuesOverrideRoot(t *testing.T) {
	t.Setenv("BMC_SERVER_GRPC_URL", "server:9090")
	hostRoot := filepath.Join(t.TempDir(), "restore-host")
	runtimeRoot := filepath.Join(t.TempDir(), "restore-runtime")
	raw, err := json.Marshal(map[string]string{hostRoot: runtimeRoot})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BMC_RESTORE_ROOT", filepath.Join(t.TempDir(), "automatic-root"))
	t.Setenv("BMC_RESTORE_PATH_MAPPINGS", string(raw))
	t.Setenv("BMC_RESTORE_ROOTS", "/explicit-restore")

	cfg, err := LoadAgent()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.RestorePathMappings) != 1 || cfg.RestorePathMappings[0].HostPath != filepath.Clean(hostRoot) || cfg.RestorePathMappings[0].RuntimePath != filepath.Clean(runtimeRoot) {
		t.Fatalf("restore mappings = %#v", cfg.RestorePathMappings)
	}
	if len(cfg.RestoreRoots) != 1 || cfg.RestoreRoots[0] != filepath.Clean("/explicit-restore") {
		t.Fatalf("restore roots = %#v", cfg.RestoreRoots)
	}
}

func TestLoadAgentRestoreConfigurationEmptyIsCompatible(t *testing.T) {
	t.Setenv("BMC_SERVER_GRPC_URL", "server:9090")
	t.Setenv("BMC_RESTORE_ROOT", "")
	t.Setenv("BMC_RESTORE_PATH_MAPPINGS", "")
	t.Setenv("BMC_RESTORE_ROOTS", "")

	cfg, err := LoadAgent()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.RestorePathMappings) != 0 || len(cfg.RestoreRoots) != 0 {
		t.Fatalf("restore configuration = mappings %#v, roots %#v", cfg.RestorePathMappings, cfg.RestoreRoots)
	}
}

func TestLoadAgentRejectsInvalidRestorePathMappings(t *testing.T) {
	t.Setenv("BMC_SERVER_GRPC_URL", "server:9090")
	t.Setenv("BMC_RESTORE_PATH_MAPPINGS", `[]`)
	_, err := LoadAgent()
	if err == nil || !strings.Contains(err.Error(), "BMC_RESTORE_PATH_MAPPINGS") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadAgentRejectsInvalidSourcePathMappings(t *testing.T) {
	t.Setenv("BMC_SERVER_GRPC_URL", "server:9090")
	t.Setenv("BMC_SOURCE_PATH_MAPPINGS", `[]`)
	if _, err := LoadAgent(); err == nil {
		t.Fatal("expected invalid mapping error")
	}
}
