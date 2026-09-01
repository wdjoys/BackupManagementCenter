package config

import (
	"os"
	"path/filepath"
	"testing"
)

func clearServerEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"BMC_DATA_DIR", "BMC_MASTER_KEY_FILE", "BMC_TLS_CERT_FILE", "BMC_TLS_KEY_FILE", "BMC_TLS_MODE", "BMC_DEV_INSECURE", "BMC_PUBLIC_URL"} {
		t.Setenv(key, "")
	}
}

func TestLoadServerDerivesMasterKeyPath(t *testing.T) {
	clearServerEnv(t)
	t.Setenv("BMC_DATA_DIR", filepath.Join("/tmp", "bmc-test"))
	t.Setenv("BMC_TLS_MODE", "none")
	cfg, err := LoadServer()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MasterKeyFile != filepath.Join(cfg.DataDir, "master.key") || cfg.MasterKeyExplicit {
		t.Fatalf("master key = %q explicit=%v", cfg.MasterKeyFile, cfg.MasterKeyExplicit)
	}
}

func TestLoadServerExplicitMasterKeyWins(t *testing.T) {
	clearServerEnv(t)
	t.Setenv("BMC_DATA_DIR", "/data")
	t.Setenv("BMC_MASTER_KEY_FILE", "/etc/bmc/master.key")
	t.Setenv("BMC_TLS_MODE", "none")
	cfg, err := LoadServer()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MasterKeyFile != "/etc/bmc/master.key" || !cfg.MasterKeyExplicit {
		t.Fatalf("master key = %q explicit=%v", cfg.MasterKeyFile, cfg.MasterKeyExplicit)
	}
}

func TestLoadServerTLSValidation(t *testing.T) {
	clearServerEnv(t)
	if _, err := LoadServer(); err == nil {
		t.Fatal("expected missing TLS error")
	}
	t.Setenv("BMC_TLS_MODE", "none")
	if _, err := LoadServer(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BMC_TLS_MODE", "invalid")
	if _, err := LoadServer(); err == nil {
		t.Fatal("expected TLS mode error")
	}
}

func TestLoadServerDevInsecureAndExplicitKey(t *testing.T) {
	clearServerEnv(t)
	t.Setenv("BMC_DEV_INSECURE", "1")
	if _, err := LoadServer(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BMC_MASTER_KEY_FILE", filepath.Join(t.TempDir(), "key"))
	cfg, err := LoadServer()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.MasterKeyExplicit {
		t.Fatal("explicit key was not retained")
	}
}

func TestLoadServerRejectsInvalidPublicURL(t *testing.T) {
	clearServerEnv(t)
	t.Setenv("BMC_TLS_MODE", "none")
	t.Setenv("BMC_PUBLIC_URL", "ftp://example.com")
	if _, err := LoadServer(); err == nil {
		t.Fatal("expected public URL error")
	}
	_ = os.ErrNotExist
}
