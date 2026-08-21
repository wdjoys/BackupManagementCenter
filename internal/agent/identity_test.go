package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestIdentityManager_LoadOrCreate_New(t *testing.T) {
	stateDir := t.TempDir()
	im := NewIdentityManager(stateDir)

	ident, isNew, err := im.LoadOrCreate("test-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isNew {
		t.Fatal("expected new identity")
	}
	if ident.AgentID != "" {
		t.Fatal("expected empty agent_id before enroll")
	}
	if ident.SecretHex == "" {
		t.Fatal("expected non-empty secret_hex")
	}
	if len(ident.SecretHex) != 64 {
		t.Fatalf("expected 64 hex chars (32 bytes), got %d", len(ident.SecretHex))
	}

	// Verify file exists with 0600 permissions
	info, err := os.Stat(filepath.Join(stateDir, "identity.json"))
	if err != nil {
		t.Fatalf("identity.json not found: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 permissions, got %o", info.Mode().Perm())
	}
}

func TestIdentityManager_LoadOrCreate_Existing(t *testing.T) {
	stateDir := t.TempDir()

	// Create identity file first
	ident := &Identity{
		AgentID:   "test-agent-id",
		SecretHex: "aabbccddee0011223344556677889900aabbccddee0011223344556677889900",
	}
	data, _ := json.MarshalIndent(ident, "", "  ")
	os.WriteFile(filepath.Join(stateDir, "identity.json"), data, 0o600)

	im := NewIdentityManager(stateDir)
	loaded, isNew, err := im.LoadOrCreate("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isNew {
		t.Fatal("expected existing identity, not new")
	}
	if loaded.AgentID != "test-agent-id" {
		t.Fatalf("expected agent_id 'test-agent-id', got %q", loaded.AgentID)
	}
	if loaded.SecretHex != "aabbccddee0011223344556677889900aabbccddee0011223344556677889900" {
		t.Fatalf("secret_hex mismatch")
	}
}

func TestIdentityManager_LoadOrCreate_NoTokenAndNoIdentity(t *testing.T) {
	stateDir := t.TempDir()
	im := NewIdentityManager(stateDir)

	_, _, err := im.LoadOrCreate("")
	if err == nil {
		t.Fatal("expected error when no identity and no token")
	}
}

func TestIdentityManager_SetAgentID(t *testing.T) {
	stateDir := t.TempDir()
	im := NewIdentityManager(stateDir)

	// Create identity first
	ident, _, err := im.LoadOrCreate("test-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Set agent ID
	if err := im.SetAgentID("agent-12345"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify via Get
	loaded, err := im.Get()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loaded.AgentID != "agent-12345" {
		t.Fatalf("expected agent_id 'agent-12345', got %q", loaded.AgentID)
	}
	if loaded.SecretHex != ident.SecretHex {
		t.Fatal("secret_hex should remain unchanged")
	}

	// Verify file on disk
	data, err := os.ReadFile(filepath.Join(stateDir, "identity.json"))
	if err != nil {
		t.Fatalf("identity.json not found: %v", err)
	}
	var onDisk Identity
	json.Unmarshal(data, &onDisk)
	if onDisk.AgentID != "agent-12345" {
		t.Fatalf("disk: expected agent_id 'agent-12345', got %q", onDisk.AgentID)
	}
}

func TestIdentityManager_Get_WithoutInit(t *testing.T) {
	stateDir := t.TempDir()
	im := NewIdentityManager(stateDir)

	_, err := im.Get()
	if err == nil {
		t.Fatal("expected error when no identity file exists")
	}
}

func TestIdentityManager_LoadOrCreate_SecretBytes(t *testing.T) {
	stateDir := t.TempDir()
	im := NewIdentityManager(stateDir)

	ident, _, err := im.LoadOrCreate("test-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	secretBytes, err := ident.SecretBytes()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(secretBytes) != 32 {
		t.Fatalf("expected 32 bytes, got %d", len(secretBytes))
	}
}

func TestIdentityManager_LoadOrCreate_Reentrant(t *testing.T) {
	stateDir := t.TempDir()
	im := NewIdentityManager(stateDir)

	// First call creates an un-enrolled identity (no agent id yet).
	ident1, isNew, err := im.LoadOrCreate("test-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isNew {
		t.Fatal("expected new identity on first call")
	}

	// Second call without enrollment must still report "needs enrollment"
	// (agent id empty) while keeping the same secret.
	ident2, needsEnroll, err := im.LoadOrCreate("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !needsEnroll {
		t.Fatal("expected needs-enrollment when agent id is empty")
	}
	if ident1.SecretHex != ident2.SecretHex {
		t.Fatal("secret_hex should match across load calls")
	}

	// After enrollment completes, subsequent loads are stable.
	if err := im.SetAgentID("agent-1"); err != nil {
		t.Fatalf("SetAgentID: %v", err)
	}
	im2 := NewIdentityManager(stateDir)
	ident3, needsEnroll2, err := im2.LoadOrCreate("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if needsEnroll2 {
		t.Fatal("expected enrolled identity to be stable")
	}
	if ident3.AgentID != "agent-1" {
		t.Fatal("expected agent id to persist")
	}
}
func TestIdentityManager_LoadOrCreate_NewManagerLoadsExisting(t *testing.T) {
	stateDir := t.TempDir()

	// First manager creates
	im1 := NewIdentityManager(stateDir)
	ident1, _, err := im1.LoadOrCreate("test-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Set agent ID
	im1.SetAgentID("agent-123")

	// Second manager loads
	im2 := NewIdentityManager(stateDir)
	ident2, isNew, err := im2.LoadOrCreate("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isNew {
		t.Fatal("expected existing identity for new manager")
	}
	if ident2.AgentID != "agent-123" {
		t.Fatalf("expected agent_id 'agent-123', got %q", ident2.AgentID)
	}
	if ident1.SecretHex != ident2.SecretHex {
		t.Fatal("secret_hex should match across managers")
	}
}