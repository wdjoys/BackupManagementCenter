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

func TestFingerprintIsKeyedAndScoped(t *testing.T) {
	key := make([]byte, KeyLen)
	for i := range key {
		key[i] = byte(i + 1)
	}
	s, err := NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	const scope = "restore.target_password"

	pw := s.Fingerprint(scope, "hunter2")
	if pw != s.Fingerprint(scope, "hunter2") {
		t.Fatal("same value must yield the same fingerprint")
	}
	if pw == s.Fingerprint(scope, "hunter3") {
		t.Fatal("different values must not collide")
	}
	if pw == s.Fingerprint("other.scope", "hunter2") {
		t.Fatal("scope must separate fingerprints")
	}
	// 关键性质：指纹是密钥化的，不是裸哈希；换 master key 后同一口令指纹不同。
	if pw == HashToken("hunter2") {
		t.Fatal("fingerprint must not be a bare hash")
	}
	other := make([]byte, KeyLen)
	for i := range other {
		other[i] = byte(0xff - i)
	}
	s2, err := NewSealer(other)
	if err != nil {
		t.Fatal(err)
	}
	if pw == s2.Fingerprint(scope, "hunter2") {
		t.Fatal("fingerprint must depend on the master key")
	}
}
