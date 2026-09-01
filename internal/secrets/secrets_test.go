package secrets

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadOrCreateKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "master.key")
	key, created, err := LoadOrCreateKey(path)
	if err != nil || !created || len(key) != KeyLen {
		t.Fatalf("first load: len=%d created=%v err=%v", len(key), created, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	key2, created, err := LoadOrCreateKey(path)
	if err != nil || created || string(key) != string(key2) {
		t.Fatalf("second load created=%v err=%v", created, err)
	}
}

func TestLoadOrCreateKeyRejectsInvalidExistingKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreateKey(path); err == nil {
		t.Fatal("expected invalid key error")
	}
}

func TestLoadOrCreateKeyRejectsUnwritablePath(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := LoadOrCreateKey(dir); err == nil {
		t.Fatal("expected directory path failure")
	}
}
