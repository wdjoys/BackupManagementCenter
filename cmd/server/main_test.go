package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/secrets"
	"backupmanagementcenter/internal/server/store"
)

func TestResetAdminCommand(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "bmc.db")

	seal := secrets.NewNoopSealer()
	st, err := store.NewWithSealer(dbPath, seal)
	if err != nil {
		t.Fatalf("NewWithSealer: %v", err)
	}

	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		st.Close()
		t.Fatalf("Migrate: %v", err)
	}

	admin := &model.Admin{
		ID:           "test-admin-1",
		Username:     "admin",
		PasswordHash: "$argon2id$v19$test",
		CreatedAt:    time.Now().UTC(),
	}
	if err := st.CreateAdmin(ctx, admin); err != nil {
		st.Close()
		t.Fatalf("CreateAdmin: %v", err)
	}

	has, err := st.HasAdmin(ctx)
	if err != nil || !has {
		st.Close()
		t.Fatalf("expected admin to exist, got %v, err=%v", has, err)
	}
	st.Close()

	// Set BMC_DATA_DIR to tempDir and run runResetAdmin
	t.Setenv("BMC_DATA_DIR", tempDir)
	runResetAdmin()

	// Verify admin is gone
	st2, err := store.NewWithSealer(dbPath, seal)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st2.Close()

	hasAfter, err := st2.HasAdmin(ctx)
	if err != nil {
		t.Fatalf("HasAdmin after reset: %v", err)
	}
	if hasAfter {
		t.Fatalf("expected admin to be deleted after reset-admin")
	}
}
