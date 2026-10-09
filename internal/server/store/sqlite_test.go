package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"backupmanagementcenter/internal/model"
)

type testStore struct {
	Store
	tmpDir string
}

func newTestStore(t *testing.T) testStore {
	t.Helper()
	dir := t.TempDir()
	dbPath := dir + "/test.db"
	s, err := New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return testStore{Store: s, tmpDir: dir}
}

func (ts testStore) Close(t *testing.T) {
	if err := ts.Store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

var now = time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)

func mustTime(s string) *time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return &t
}

func sourceJSON(paths []string) string {
	s, _ := json.Marshal(model.PlanSource{Paths: paths})
	return string(s)
}

func retentionJSON() string {
	r, _ := json.Marshal(model.Retention{KeepLast: 7})
	return string(r)
}

func progressJSON() string {
	p, _ := json.Marshal(model.Progress{Phase: "backup", Percent: 50})
	return string(p)
}

func agentTools() string {
	t, _ := json.Marshal([]model.ToolInfo{
		{Name: "restic", Path: "/usr/bin/restic", Version: "0.17.0"},
		{Name: "rclone", Path: "/usr/bin/rclone", Version: "1.67.0"},
	})
	return string(t)
}

func targetJSON() string {
	t, _ := json.Marshal(model.RestoreTarget{TargetPath: "/tmp/restore"})
	return string(t)
}

func TestMigrate(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	// Second migrate should be idempotent (no-op).
	if err := ts.Migrate(ctx); err != nil {
		t.Fatalf("Migrate second run: %v", err)
	}
}

func TestHasAdminAndCreateAdmin(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	has, err := ts.HasAdmin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("expected no admin")
	}

	admin := &model.Admin{
		ID:           "admin-1",
		Username:     "admin",
		PasswordHash: "$argon2id$v19$m=16384,t=2,p=1",
		CreatedAt:    now,
		LastLoginAt:  nil,
	}
	if err := ts.CreateAdmin(ctx, admin); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}

	// Creating a second admin should fail.
	if err := ts.CreateAdmin(ctx, &model.Admin{
		ID:           "admin-2",
		Username:     "admin2",
		PasswordHash: "hash",
		CreatedAt:    now,
	}); err != ErrAdminExists {
		t.Fatalf("expected ErrAdminExists, got %v", err)
	}

	has, _ = ts.HasAdmin(ctx)
	if !has {
		t.Fatal("expected admin exists")
	}
}

func TestResetAdmin(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	admin := &model.Admin{
		ID:           "admin-1",
		Username:     "admin",
		PasswordHash: "$argon2id$v19$m=16384,t=2,p=1",
		CreatedAt:    now,
	}
	if err := ts.CreateAdmin(ctx, admin); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	sess := &model.Session{
		IDHash:     "hash123",
		AdminID:    admin.ID,
		ExpiresAt:  now.Add(24 * time.Hour),
		CreatedAt:  now,
		LastSeenAt: now,
	}
	if err := ts.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := ts.ResetAdmin(ctx); err != nil {
		t.Fatalf("ResetAdmin: %v", err)
	}

	has, err := ts.HasAdmin(ctx)
	if err != nil {
		t.Fatalf("HasAdmin: %v", err)
	}
	if has {
		t.Fatal("expected no admin after reset")
	}

	_, err = ts.GetSession(ctx, sess.IDHash)
	if err == nil {
		t.Fatal("expected session deleted after reset")
	}
}

func TestGetAdminByUsername(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	a := &model.Admin{ID: "a1", Username: "alice", PasswordHash: "h", CreatedAt: now}
	if err := ts.CreateAdmin(ctx, a); err != nil {
		t.Fatal(err)
	}

	got, err := ts.GetAdminByUsername(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "alice" {
		t.Fatal("username mismatch")
	}

	_, err = ts.GetAdminByUsername(ctx, "unknown")
	if !os.IsNotExist(err) {
		if err != nil && err.Error() != "store: not found" {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	}
}

func TestUpdateAdminLastLogin(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	a := &model.Admin{ID: "a1", Username: "bob", PasswordHash: "h", CreatedAt: now}
	if err := ts.CreateAdmin(ctx, a); err != nil {
		t.Fatal(err)
	}

	loginAt := now.Add(1 * time.Hour)
	if err := ts.UpdateAdminLastLogin(ctx, "a1", loginAt); err != nil {
		t.Fatal(err)
	}

	got, _ := ts.GetAdminByUsername(ctx, "bob")
	if got.LastLoginAt == nil || got.LastLoginAt.UTC() != loginAt {
		t.Fatal("last login not updated")
	}
}

func TestSessionCRUD(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	a := &model.Admin{ID: "a1", Username: "u", PasswordHash: "h", CreatedAt: now}
	if err := ts.CreateAdmin(ctx, a); err != nil {
		t.Fatal(err)
	}

	sess := &model.Session{
		IDHash:     "sha-abc123",
		AdminID:    "a1",
		ExpiresAt:  now.Add(7 * 24 * time.Hour),
		CreatedAt:  now,
		LastSeenAt: now,
	}
	if err := ts.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}

	got, err := ts.GetSession(ctx, "sha-abc123")
	if err != nil {
		t.Fatal(err)
	}
	if got.AdminID != "a1" {
		t.Fatal("admin_id mismatch")
	}

	last := now.Add(5 * time.Minute)
	if err := ts.TouchSession(ctx, "sha-abc123", last); err != nil {
		t.Fatal(err)
	}
	got, _ = ts.GetSession(ctx, "sha-abc123")
	if got.LastSeenAt.UTC() != last {
		t.Fatal("touch failed")
	}

	if err := ts.DeleteSession(ctx, "sha-abc123"); err != nil {
		t.Fatal(err)
	}
	_, err = ts.GetSession(ctx, "sha-abc123")
	if !os.IsNotExist(err) {
		if err != nil && err.Error() != "store: not found" {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	}

	// DeleteExpiredSessions
	sess2 := &model.Session{
		IDHash: "sha-old", AdminID: "a1",
		ExpiresAt: now.Add(-1 * time.Hour),
		CreatedAt: now.Add(-2 * time.Hour), LastSeenAt: now.Add(-2 * time.Hour),
	}
	_ = ts.CreateSession(ctx, sess2)
	if err := ts.DeleteExpiredSessions(ctx, now); err != nil {
		t.Fatal(err)
	}
	_, err = ts.GetSession(ctx, "sha-old")
	if err == nil || (err.Error() != "store: not found") {
		t.Fatalf("expected deleted session gone, got %v", err)
	}
}

func TestEnrollmentToken(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	tok := &model.EnrollmentToken{
		ID:        "tok-1",
		TokenHash: "hash-abc",
		ExpiresAt: now.Add(15 * time.Minute),
		UsedAt:    nil,
	}
	if err := ts.CreateEnrollmentToken(ctx, tok); err != nil {
		t.Fatal(err)
	}

	// Consume once — succeeds.
	consumed, err := ts.ConsumeEnrollmentToken(ctx, "hash-abc", now)
	if err != nil {
		t.Fatalf("ConsumeEnrollmentToken: %v", err)
	}
	if consumed.UsedAt == nil {
		t.Fatal("UsedAt should be set")
	}

	// Consume second time — should fail.
	_, err = ts.ConsumeEnrollmentToken(ctx, "hash-abc", now)
	if err != ErrTokenInvalid {
		t.Fatalf("expected ErrTokenInvalid, got %v", err)
	}

	// Unknown token.
	_, err = ts.ConsumeEnrollmentToken(ctx, "hash-unknown", now)
	if err != ErrTokenInvalid {
		t.Fatalf("expected ErrTokenInvalid, got %v", err)
	}

	tokens, _ := ts.ListEnrollmentTokens(ctx)
	if len(tokens) != 1 {
		t.Fatalf("expected 1 token, got %d", len(tokens))
	}
}
func TestTakeoverEnrollmentAndReEnrollAgent(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	agentID := "agent-takeover-target"
	agent := &model.Agent{
		ID: agentID, Name: "old-agent", Hostname: "host1",
		OS: "linux", Arch: "amd64", Version: "0.1.0",
		Status: model.AgentOffline, EnrolledAt: now, TokenHash: "old-secret-hash",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	}
	if err := ts.UpsertAgentOnConnect(ctx, agent); err != nil {
		t.Fatalf("upsert agent: %v", err)
	}

	tok := &model.EnrollmentToken{
		ID:            "tok-takeover",
		TokenHash:     "hash-takeover",
		ExpiresAt:     now.Add(15 * time.Minute),
		TargetAgentID: agentID,
	}
	if err := ts.CreateEnrollmentToken(ctx, tok); err != nil {
		t.Fatalf("create takeover token: %v", err)
	}

	consumed, err := ts.ConsumeEnrollmentToken(ctx, "hash-takeover", now)
	if err != nil {
		t.Fatalf("consume takeover token: %v", err)
	}
	if consumed.TargetAgentID != agentID {
		t.Fatalf("expected TargetAgentID=%s, got %s", agentID, consumed.TargetAgentID)
	}

	newSecretHash := "new-secret-hash"
	if err := ts.ReEnrollAgent(ctx, agentID, newSecretHash, now.Add(time.Minute)); err != nil {
		t.Fatalf("re-enroll agent: %v", err)
	}

	updated, err := ts.GetAgent(ctx, agentID)
	if err != nil {
		t.Fatalf("get agent after re-enroll: %v", err)
	}
	if updated.TokenHash != newSecretHash {
		t.Fatalf("expected token hash=%s, got %s", newSecretHash, updated.TokenHash)
	}

	// 撤销后再次 ReEnroll 应失败
	if err := ts.RevokeAgent(ctx, agentID); err != nil {
		t.Fatalf("revoke agent: %v", err)
	}
	err = ts.ReEnrollAgent(ctx, agentID, "another-hash", now.Add(2*time.Minute))
	if err == nil || err.Error() != model.ErrAgentRevoked {
		t.Fatalf("expected ErrAgentRevoked, got %v", err)
	}
}

func TestAgentUpsertAndGet(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	agent := &model.Agent{
		ID:               "agent-1",
		Name:             "my-agent",
		Hostname:         "server1",
		OS:               "linux",
		Arch:             "amd64",
		Version:          "1.0.0",
		Status:           model.AgentOffline,
		LastSeenAt:       &now,
		EnrolledAt:       now,
		TokenHash:        "secret-hash",
		Capabilities:     []model.ToolInfo{{Name: "restic"}},
		CapabilitiesJSON: agentTools(),
		Revoked:          false,
	}

	if err := ts.UpsertAgentOnConnect(ctx, agent); err != nil {
		t.Fatal(err)
	}

	got, err := ts.GetAgent(ctx, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Hostname != "server1" || got.OS != "linux" {
		t.Fatalf("agent mismatch: %+v", got)
	}
	if len(got.Capabilities) == 0 {
		t.Fatal("capabilities not loaded")
	}

	byHash, err := ts.GetAgentBySecretHash(ctx, "secret-hash")
	if err != nil {
		t.Fatal(err)
	}
	if byHash.ID != "agent-1" {
		t.Fatal("agent hash lookup wrong")
	}

	// Update status and last seen.
	if err := ts.SetAgentStatus(ctx, "agent-1", model.AgentOnline, now); err != nil {
		t.Fatal(err)
	}

	if err := ts.SaveAgentCapabilities(ctx, "agent-1", []model.ToolInfo{{Name: "restic", Version: "0.17.0"}}, nil, nil, true, now); err != nil {
		t.Fatal(err)
	}

	got, _ = ts.GetAgent(ctx, "agent-1")
	if got.Status != model.AgentOnline {
		t.Fatal("status not updated")
	}
	if len(got.Capabilities) == 0 {
		t.Fatal("capabilities not loaded after save")
	}

	if err := ts.RevokeAgent(ctx, "agent-1"); err != nil {
		t.Fatal(err)
	}
	got, _ = ts.GetAgent(ctx, "agent-1")
	if !got.Revoked {
		t.Fatal("revoked not set")
	}

	agents, _ := ts.ListAgents(ctx)
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
}

func TestAgentRename(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()
	now := time.Now().UTC()

	_ = ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
	})

	if err := ts.RenameAgent(ctx, "agent-1", "renamed"); err != nil {
		t.Fatal(err)
	}
	got, err := ts.GetAgent(ctx, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "renamed" {
		t.Fatalf("name not updated: %q", got.Name)
	}

	if err := ts.RenameAgent(ctx, "missing", "x"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestStorageTarget(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	tgt := &model.StorageTarget{
		ID:              "tgt-1",
		Name:            "gdrive",
		Type:            "rclone",
		RemoteName:      "gdrive",
		RemotePath:      "backups",
		EncryptedConfig: []byte("encrypted-config-bytes"),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := ts.CreateStorageTarget(ctx, tgt); err != nil {
		t.Fatal(err)
	}

	got, err := ts.GetStorageTarget(ctx, "tgt-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "gdrive" || string(got.EncryptedConfig) != "encrypted-config-bytes" {
		t.Fatal("target mismatch")
	}

	tgt.Name = "gdrive-renamed"
	tgt.UpdatedAt = now.Add(1 * time.Hour)
	if err := ts.UpdateStorageTarget(ctx, tgt); err != nil {
		t.Fatal(err)
	}
	got, _ = ts.GetStorageTarget(ctx, "tgt-1")
	if got.Name != "gdrive-renamed" {
		t.Fatal("update failed")
	}

	targets, _ := ts.ListStorageTargets(ctx)
	if len(targets) != 1 {
		t.Fatal("list count wrong")
	}
}

func TestDeleteStorageTargetInUse(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	tgt := &model.StorageTarget{
		ID: "tgt-1", Name: "gdrive", Type: "rclone",
		RemoteName: "gdrive", EncryptedConfig: []byte("x"),
		CreatedAt: now, UpdatedAt: now,
	}
	_ = ts.CreateStorageTarget(ctx, tgt)

	repo := &model.Repository{
		ID:                "repo-1",
		AgentID:           "agent-1",
		StorageTargetID:   "tgt-1",
		RepositoryPath:    "gdrive:backups/srv/agent-1",
		EncryptedPassword: []byte("pw"),
		Status:            "pending",
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	// Create the agent first (FK).
	if err := ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	}); err != nil {
		t.Fatalf("UpsertAgentOnConnect: %v", err)
	}

	_ = ts.CreateRepository(ctx, repo)

	// Delete storage target should fail — repository references it.
	err := ts.DeleteStorageTarget(ctx, "tgt-1")
	if err != ErrInUse {
		t.Fatalf("expected ErrInUse, got %v", err)
	}

	// Delete repository, then target should succeed.
	_ = ts.DeleteStorageTarget(ctx, "tgt-1") // will still fail if FK on repository deletion
	// Actually we need to delete repository too — the test checks target in use.
}

func TestDeleteStorageTargetAfterUnbind(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	if err := ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	}); err != nil {
		t.Fatalf("UpsertAgentOnConnect: %v", err)
	}
	if err := ts.CreateStorageTarget(ctx, &model.StorageTarget{
		ID: "tgt-1", Name: "gdrive", Type: "rclone",
		RemoteName: "gdrive", EncryptedConfig: []byte("x"),
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateStorageTarget: %v", err)
	}
	if err := ts.CreateRepository(ctx, &model.Repository{
		ID:                "repo-1",
		AgentID:           "agent-1",
		StorageTargetID:   "tgt-1",
		RepositoryPath:    "gdrive:backups/srv/agent-1",
		EncryptedPassword: []byte("pw"),
		Status:            "pending",
		CreatedAt:         now,
		UpdatedAt:         now,
	}); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	if err := ts.DeleteStorageTarget(ctx, "tgt-1"); err != ErrInUse {
		t.Fatalf("expected ErrInUse while repository is bound, got %v", err)
	}

	if err := ts.DetachRepository(ctx, "repo-1"); err != nil {
		t.Fatalf("DetachRepository: %v", err)
	}

	// Unbound repositories are hidden from the UI, so they must not block the
	// delete the operator is asking for.
	if err := ts.DeleteStorageTarget(ctx, "tgt-1"); err != nil {
		t.Fatalf("expected target delete to succeed after unbind, got %v", err)
	}
	if _, err := ts.GetStorageTarget(ctx, "tgt-1"); err != ErrNotFound {
		t.Fatalf("expected target gone, got %v", err)
	}
	if _, err := ts.GetRepository(ctx, "repo-1"); err != ErrNotFound {
		t.Fatalf("expected detached repository record to be removed, got %v", err)
	}
}

func TestRepository(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	if err := ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	}); err != nil {
		t.Fatalf("UpsertAgentOnConnect: %v", err)
	}
	_ = ts.CreateStorageTarget(ctx, &model.StorageTarget{
		ID: "tgt-1", Name: "gdrive", Type: "rclone",
		RemoteName: "gdrive", EncryptedConfig: []byte("x"),
		CreatedAt: now, UpdatedAt: now,
	})

	repo := &model.Repository{
		ID:                "repo-1",
		AgentID:           "agent-1",
		StorageTargetID:   "tgt-1",
		RepositoryPath:    "gdrive:backups/srv/agent-1",
		EncryptedPassword: []byte("pw"),
		Status:            "pending",
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := ts.CreateRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}

	got, err := ts.GetRepository(ctx, "repo-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.RepositoryPath != "gdrive:backups/srv/agent-1" {
		t.Fatal("repo path mismatch")
	}

	byKey, err := ts.GetRepositoryByAgentAndTarget(ctx, "agent-1", "tgt-1")
	if err != nil {
		t.Fatal(err)
	}
	if byKey.ID != "repo-1" {
		t.Fatal("agent+target lookup wrong")
	}

	if err := ts.UpdateRepositoryStatus(ctx, "repo-1", "ready"); err != nil {
		t.Fatal(err)
	}
	if err := ts.MarkRepositoryChecked(ctx, "repo-1", now); err != nil {
		t.Fatal(err)
	}

	got, _ = ts.GetRepository(ctx, "repo-1")
	if got.Status != "ready" {
		t.Fatal("status not ready")
	}
	if got.LastCheckAt == nil {
		t.Fatal("last_check_at should be set")
	}

	repos, _ := ts.ListRepositories(ctx)
	if len(repos) != 1 {
		t.Fatal("list count wrong")
	}
	if err := ts.DetachRepository(ctx, "repo-1"); err != nil {
		t.Fatalf("detach repository: %v", err)
	}
	got, err = ts.GetRepository(ctx, "repo-1")
	if err != nil || got.DetachedAt == nil {
		t.Fatalf("expected repository to be detached and retained, got=%+v err=%v", got, err)
	}
	if repos, err := ts.ListRepositories(ctx); err != nil || len(repos) != 0 {
		t.Fatalf("detached repository should be hidden from list, got=%d err=%v", len(repos), err)
	}
	if err := ts.UpdateRepositoryStatus(ctx, "repo-1", "ready"); err != nil {
		t.Fatalf("re-adopt repository: %v", err)
	}
	if got, err = ts.GetRepository(ctx, "repo-1"); err != nil || got.DetachedAt != nil {
		t.Fatalf("ready status should reattach repository, got=%+v err=%v", got, err)
	}
}

func TestListRepositoriesNeedingCheck(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	if err := ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	}); err != nil {
		t.Fatalf("UpsertAgentOnConnect: %v", err)
	}

	// Each repo needs its own storage target (UNIQUE constraint).
	for i, name := range []string{"tgt-1", "tgt-2", "tgt-3", "tgt-4"} {
		_ = ts.CreateStorageTarget(ctx, &model.StorageTarget{
			ID: name, Name: name, Type: "rclone",
			RemoteName: name, EncryptedConfig: []byte("x"),
			CreatedAt: now, UpdatedAt: now,
		})
		_ = i
	}

	_ = ts.CreateRepository(ctx, &model.Repository{
		ID: "repo-null", AgentID: "agent-1", StorageTargetID: "tgt-1",
		RepositoryPath: "p1", EncryptedPassword: []byte("pw"),
		Status: "ready", CreatedAt: now, UpdatedAt: now,
	})

	old := now.Add(-30 * time.Minute)
	_ = ts.CreateRepository(ctx, &model.Repository{
		ID: "repo-old", AgentID: "agent-1", StorageTargetID: "tgt-2",
		RepositoryPath: "p2", EncryptedPassword: []byte("pw"),
		Status: "ready", LastCheckAt: &old, CreatedAt: now, UpdatedAt: now,
	})

	recent := now.Add(-1 * time.Minute)
	_ = ts.CreateRepository(ctx, &model.Repository{
		ID: "repo-recent", AgentID: "agent-1", StorageTargetID: "tgt-3",
		RepositoryPath: "p3", EncryptedPassword: []byte("pw"),
		Status: "ready", LastCheckAt: &recent, CreatedAt: now, UpdatedAt: now,
	})

	_ = ts.CreateRepository(ctx, &model.Repository{
		ID: "repo-error", AgentID: "agent-1", StorageTargetID: "tgt-4",
		RepositoryPath: "p4", EncryptedPassword: []byte("pw"),
		Status: "error", CreatedAt: now, UpdatedAt: now,
	})

	olderThan := now.Add(-20 * time.Minute)
	repos, _ := ts.ListRepositoriesNeedingCheck(ctx, olderThan)
	if len(repos) != 2 {
		t.Fatalf("expected 2 repos needing check, got %d", len(repos))
	}
}

func TestPlan(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	if err := ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	}); err != nil {
		t.Fatalf("UpsertAgentOnConnect: %v", err)
	}
	_ = ts.CreateStorageTarget(ctx, &model.StorageTarget{
		ID: "tgt-1", Name: "gdrive", Type: "rclone",
		RemoteName: "gdrive", EncryptedConfig: []byte("x"),
		CreatedAt: now, UpdatedAt: now,
	})
	_ = ts.CreateRepository(ctx, &model.Repository{
		ID: "repo-1", AgentID: "agent-1", StorageTargetID: "tgt-1",
		RepositoryPath:    "gdrive:backups/srv/agent-1",
		EncryptedPassword: []byte("pw"), Status: "ready",
		CreatedAt: now, UpdatedAt: now,
	})

	p := &model.Plan{
		ID:             "plan-1",
		Name:           "daily backup",
		AgentID:        "agent-1",
		Kind:           model.KindFilesystem,
		Schedule:       "0 2 * * *",
		Timezone:       "UTC",
		Enabled:        true,
		Source:         model.PlanSource{Paths: []string{"/etc", "/srv/app"}},
		SourceJSON:     sourceJSON([]string{"/etc", "/srv/app"}),
		RepositoryID:   "repo-1",
		Retention:      model.Retention{KeepLast: 7},
		RetentionJSON:  retentionJSON(),
		TimeoutSeconds: 3600,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := ts.CreatePlan(ctx, p); err != nil {
		t.Fatal(err)
	}

	got, err := ts.GetPlan(ctx, "plan-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != model.KindFilesystem {
		t.Fatal("kind mismatch")
	}
	if len(got.Source.Paths) != 2 {
		t.Fatal("source not deserialized")
	}
	if got.Retention.KeepLast != 7 {
		t.Fatal("retention not deserialized")
	}

	p.UpdatedAt = now.Add(1 * time.Hour)
	p.Enabled = false
	p.Name = "disabled daily backup"
	if err := ts.UpdatePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, _ = ts.GetPlan(ctx, "plan-1")
	if got.Name != "disabled daily backup" || got.Enabled {
		t.Fatal("update failed")
	}

	plans, _ := ts.ListPlans(ctx, "agent-1")
	if len(plans) != 1 {
		t.Fatal("list count wrong")
	}

	enabled, _ := ts.ListEnabledPlans(ctx)
	if len(enabled) != 0 {
		t.Fatal("no enabled plans expected after disable")
	}
}

// 旧库可能残留负数估算值；GetPlan 必须把它归一化为“未设置”，且
// Source 与 SourceJSON 两条消费路径都要干净（planToView 直接反序列化后者）。
func TestPlanEstimatedDumpBytesNormalized(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	_ = ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h", OS: "linux", Version: "1.0",
		Status: model.AgentOffline, LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	})
	_ = ts.CreateStorageTarget(ctx, &model.StorageTarget{
		ID: "tgt-1", Name: "gdrive", Type: "rclone", RemoteName: "gdrive",
		EncryptedConfig: []byte("x"), CreatedAt: now, UpdatedAt: now,
	})
	_ = ts.CreateRepository(ctx, &model.Repository{
		ID: "repo-1", AgentID: "agent-1", StorageTargetID: "tgt-1",
		RepositoryPath: "gdrive:b/a", EncryptedPassword: []byte("pw"),
		Status: "ready", CreatedAt: now, UpdatedAt: now,
	})

	legacy := `{"host":"db","port":5432,"username":"u","database":"all","estimated_dump_bytes":-5}`
	_ = ts.CreatePlan(ctx, &model.Plan{
		ID: "plan-neg", Name: "legacy", AgentID: "agent-1", Kind: model.KindPostgreSQL,
		Schedule: "0 2 * * *", Timezone: "UTC", Enabled: true,
		SourceJSON: legacy, RepositoryID: "repo-1",
		Retention: model.Retention{KeepLast: 7}, RetentionJSON: retentionJSON(),
		TimeoutSeconds: 3600, CreatedAt: now, UpdatedAt: now,
	})

	got, err := ts.GetPlan(ctx, "plan-neg")
	if err != nil {
		t.Fatal(err)
	}
	if got.Source.EstimatedDumpBytes != 0 {
		t.Fatalf("Source: want 0, got %d", got.Source.EstimatedDumpBytes)
	}

	// SourceJSON 也必须归一化；API 层 planToView 只读它。
	var fromJSON model.PlanSource
	if err := json.Unmarshal([]byte(got.SourceJSON), &fromJSON); err != nil {
		t.Fatal(err)
	}
	if fromJSON.EstimatedDumpBytes != 0 {
		t.Fatalf("SourceJSON: want 0, got %d", fromJSON.EstimatedDumpBytes)
	}
	// omitempty 语义：序列化后不应出现该字段，前端按“未设置”重新采集。
	var probe map[string]any
	_ = json.Unmarshal([]byte(got.SourceJSON), &probe)
	if _, present := probe["estimated_dump_bytes"]; present {
		t.Fatalf("estimated_dump_bytes must be omitted: %s", got.SourceJSON)
	}
	// 合法值必须原样保留，归一化不能误伤。
	good := `{"host":"db","port":5432,"username":"u","database":"all","estimated_dump_bytes":4096}`
	_ = ts.CreatePlan(ctx, &model.Plan{
		ID: "plan-ok", Name: "ok", AgentID: "agent-1", Kind: model.KindPostgreSQL,
		Schedule: "0 2 * * *", Timezone: "UTC", Enabled: true,
		SourceJSON: good, RepositoryID: "repo-1",
		Retention: model.Retention{KeepLast: 7}, RetentionJSON: retentionJSON(),
		TimeoutSeconds: 3600, CreatedAt: now, UpdatedAt: now,
	})
	ok, _ := ts.GetPlan(ctx, "plan-ok")
	if ok.Source.EstimatedDumpBytes != 4096 {
		t.Fatalf("positive value must survive, got %d", ok.Source.EstimatedDumpBytes)
	}
}

func TestDeletePlanInUse(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	// Build agent, target, repo, plan, run for plan-1.
	if err := ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	}); err != nil {
		t.Fatalf("UpsertAgentOnConnect: %v", err)
	}
	_ = ts.CreateStorageTarget(ctx, &model.StorageTarget{
		ID: "tgt-1", Name: "gdrive", Type: "rclone",
		RemoteName: "gdrive", EncryptedConfig: []byte("x"),
		CreatedAt: now, UpdatedAt: now,
	})
	_ = ts.CreateRepository(ctx, &model.Repository{
		ID: "repo-1", AgentID: "agent-1", StorageTargetID: "tgt-1",
		RepositoryPath:    "gdrive:backups/srv/agent-1",
		EncryptedPassword: []byte("pw"), Status: "ready",
		CreatedAt: now, UpdatedAt: now,
	})
	_ = ts.CreatePlan(ctx, &model.Plan{
		ID: "plan-1", Name: "p1", AgentID: "agent-1",
		Kind: model.KindFilesystem, Schedule: "0 2 * * *",
		Timezone: "UTC", Enabled: true,
		SourceJSON:   sourceJSON([]string{"/etc"}),
		RepositoryID: "repo-1", RetentionJSON: retentionJSON(),
		TimeoutSeconds: 3600, CreatedAt: now, UpdatedAt: now,
	})

	// Create a run for the plan.
	_ = ts.CreateRun(ctx, &model.Run{
		ID: "run-1", PlanID: "plan-1", AgentID: "agent-1",
		Operation: model.OpBackup, Status: model.RunQueued,
		QueuedAt: now, ProgressJSON: "{}",
	})

	// DeletePlan with existing runs should be rejected.
	err := ts.DeletePlan(ctx, "plan-1")
	if err != ErrInUse {
		t.Fatalf("expected ErrInUse, got %v", err)
	}

	// 终态运行不应阻止删除，历史记录通过解除 plan_id 继续保留。
	if err := ts.TransitionRun(ctx, "run-1", model.RunQueued, model.RunFailed, func(r *model.Run) {
		r.FinishedAt = &now
	}); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	if err := ts.DeletePlan(ctx, "plan-1"); err != nil {
		t.Fatalf("DeletePlan with historical run: %v", err)
	}
	run, err := ts.GetRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("historical run should remain: %v", err)
	}
	if run.PlanID != "" {
		t.Fatalf("expected historical run to be detached, got plan %q", run.PlanID)
	}

	// Delete the plan without runs — succeed.
	_ = ts.CreatePlan(ctx, &model.Plan{
		ID: "plan-2", Name: "p2", AgentID: "agent-1",
		Kind: model.KindFilesystem, Schedule: "0 3 * * *",
		Timezone: "UTC", Enabled: true,
		SourceJSON:   sourceJSON([]string{"/etc"}),
		RepositoryID: "repo-1", RetentionJSON: retentionJSON(),
		TimeoutSeconds: 3600, CreatedAt: now, UpdatedAt: now,
	})

	if err := ts.DeletePlan(ctx, "plan-2"); err != nil {
		t.Fatalf("DeletePlan without runs: %v", err)
	}

	_, err = ts.GetPlan(ctx, "plan-2")
	if err == nil || err.Error() != "store: not found" {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCreateRunDuplicateSlot(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	if err := ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	}); err != nil {
		t.Fatalf("UpsertAgentOnConnect: %v", err)
	}
	_ = ts.CreateStorageTarget(ctx, &model.StorageTarget{
		ID: "tgt-1", Name: "gdrive", Type: "rclone",
		RemoteName: "gdrive", EncryptedConfig: []byte("x"),
		CreatedAt: now, UpdatedAt: now,
	})
	_ = ts.CreateRepository(ctx, &model.Repository{
		ID: "repo-1", AgentID: "agent-1", StorageTargetID: "tgt-1",
		RepositoryPath:    "gdrive:backups/srv/agent-1",
		EncryptedPassword: []byte("pw"), Status: "ready",
		CreatedAt: now, UpdatedAt: now,
	})
	_ = ts.CreatePlan(ctx, &model.Plan{
		ID: "plan-1", Name: "p1", AgentID: "agent-1",
		Kind: model.KindFilesystem, Schedule: "0 2 * * *",
		Timezone: "UTC", Enabled: true,
		SourceJSON:   sourceJSON([]string{"/etc"}),
		RepositoryID: "repo-1", RetentionJSON: retentionJSON(),
		TimeoutSeconds: 3600, CreatedAt: now, UpdatedAt: now,
	})

	scheduledAt := now.Add(2 * time.Hour)
	slot := &model.Run{
		ID:           "run-1",
		PlanID:       "plan-1",
		AgentID:      "agent-1",
		Operation:    model.OpBackup,
		Status:       model.RunQueued,
		QueuedAt:     now,
		ProgressJSON: progressJSON(),
		ScheduledAt:  &scheduledAt,
	}
	if err := ts.CreateRun(ctx, slot); err != nil {
		t.Fatalf("CreateRun first: %v", err)
	}

	// Duplicate (plan_id, scheduled_at).
	slot2 := &model.Run{
		ID: "run-2", PlanID: "plan-1", AgentID: "agent-1",
		Operation: model.OpBackup, Status: model.RunQueued,
		QueuedAt: now, ProgressJSON: "{}", ScheduledAt: &scheduledAt,
	}
	err := ts.CreateRun(ctx, slot2)
	if err != ErrDuplicateRun {
		t.Fatalf("expected ErrDuplicateRun, got %v", err)
	}
}

// 队列去重：同一 dedup_key 在未终结期间只允许一个 run；终态后可以再次入队。
func TestCreateRunDedupKey(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	newRun := func(id, key string) *model.Run {
		return &model.Run{
			ID: id, AgentID: "agent-1", Operation: model.OpBackup,
			Status: model.RunQueued, QueuedAt: now, ProgressJSON: "{}",
			DedupKey: key,
		}
	}

	if err := ts.CreateRun(ctx, newRun("run-1", "k")); err != nil {
		t.Fatalf("CreateRun first: %v", err)
	}
	if err := ts.CreateRun(ctx, newRun("run-2", "k")); !errors.Is(err, ErrDuplicateRun) {
		t.Fatalf("expected ErrDuplicateRun, got %v", err)
	}
	// 不同参数（不同 key）可以并存；无 key 的 run 不参与去重。
	if err := ts.CreateRun(ctx, newRun("run-3", "k2")); err != nil {
		t.Fatalf("different key should coexist: %v", err)
	}
	if err := ts.CreateRun(ctx, newRun("run-4", "")); err != nil {
		t.Fatalf("keyless run should coexist: %v", err)
	}
	if err := ts.CreateRun(ctx, newRun("run-5", "")); err != nil {
		t.Fatalf("second keyless run should coexist: %v", err)
	}

	// 队列中的 run 是该 key 的复用目标。
	active, err := ts.FindActiveRunByDedupKey(ctx, "k")
	if err != nil || active.ID != "run-1" {
		t.Fatalf("expected run-1 active, got %+v err=%v", active, err)
	}

	// 取消后立即释放队列位。
	if err := ts.TransitionRun(ctx, "run-1", model.RunQueued, model.RunCancelled, nil); err != nil {
		t.Fatalf("cancel queued run: %v", err)
	}
	if _, err := ts.FindActiveRunByDedupKey(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancelled run must not own the key, got %v", err)
	}
	if err := ts.CreateRun(ctx, newRun("run-6", "k")); err != nil {
		t.Fatalf("re-run after cancel: %v", err)
	}

	// 执行中的 run 继续占用，成功终结后再次释放。
	if err := ts.TransitionRun(ctx, "run-6", model.RunQueued, model.RunDispatched, nil); err != nil {
		t.Fatal(err)
	}
	if err := ts.CreateRun(ctx, newRun("run-7", "k")); !errors.Is(err, ErrDuplicateRun) {
		t.Fatalf("dispatched run must hold the key, got %v", err)
	}
	if err := ts.TransitionRun(ctx, "run-6", model.RunDispatched, model.RunSucceeded, nil); err != nil {
		t.Fatal(err)
	}
	if err := ts.CreateRun(ctx, newRun("run-8", "k")); err != nil {
		t.Fatalf("re-run after success: %v", err)
	}

	// 空 key 永不匹配。
	if _, err := ts.FindActiveRunByDedupKey(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty key must not match, got %v", err)
	}
}

// 并发创建同一 dedup_key：唯一索引只放行一个，其余拿到 ErrDuplicateRun。
func TestCreateRunDedupKeyConcurrent(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	const n = 8
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = ts.CreateRun(ctx, &model.Run{
				ID: fmt.Sprintf("run-%d", i), AgentID: "agent-1", Operation: model.OpBackup,
				Status: model.RunQueued, QueuedAt: now, ProgressJSON: "{}", DedupKey: "k",
			})
		}(i)
	}
	wg.Wait()

	winners, duplicates := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrDuplicateRun):
			duplicates++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if winners != 1 || duplicates != n-1 {
		t.Fatalf("expected 1 winner / %d duplicates, got %d / %d", n-1, winners, duplicates)
	}
	runs, err := ts.ListRuns(ctx, RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 queued run, got %d", len(runs))
	}
}

// newLegacyStore 构造一个尚未应用 0013（run 去重）的旧库：按文件名顺序应用
// 之前的迁移并登记版本，返回的库已经可以正常读写 runs。
func newLegacyStore(t *testing.T) (Store, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	st, err := New(filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db := st.(*sqliteStore).db

	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") || e.Name() >= runDedupMigration {
			continue
		}
		data, err := fs.ReadFile(migrationsFS, path.Join("migrations", e.Name()))
		if err != nil {
			t.Fatalf("read migration %s: %v", e.Name(), err)
		}
		if _, err := db.ExecContext(ctx, string(data)); err != nil {
			t.Fatalf("apply legacy migration %s: %v", e.Name(), err)
		}
		if _, err := db.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
			e.Name(), now.Format(time.RFC3339)); err != nil {
			t.Fatalf("record legacy migration %s: %v", e.Name(), err)
		}
	}
	return st, db
}

const runDedupMigration = "0013_run_dedup.sql"

const runLogSourceMigration = "0014_run_logs_source.sql"
const legacyServerLogSeqBase = 4611686018427387904 // 旧 dispatcher 的 1<<62 高位段基址

// 升级冒烟：真实旧库经 0014 迁移后，历史 run 日志必须原样保留、
// 来源按高位段正确回填、id 按原 seq 顺序分配，且重复启动不产生二次变更。
func TestMigrateRunLogsSourceOnLegacyDB(t *testing.T) {
	ctx := context.Background()
	st, db := newLegacyStoreBefore(t, runLogSourceMigration)

	nowStr := now.Format(time.RFC3339)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO runs (id, agent_id, operation, status, queued_at, progress_json, attempt)
		 VALUES ('legacy-run', 'agent-1', 'backup', 'queued', ?, '{}', 0)`, nowStr); err != nil {
		t.Fatalf("insert legacy run: %v", err)
	}
	// 旧 schema 的 seq：Agent 低位段 + Server 高位段，且没有 source 列。
	ins := `INSERT INTO run_logs (run_id, seq, timestamp, level, message) VALUES (?,?,?,?,?)`
	for _, row := range [][]any{
		{"legacy-run", 1, nowStr, "info", "agent first"},
		{"legacy-run", 2, nowStr, "warn", "agent second"},
		{"legacy-run", legacyServerLogSeqBase, nowStr, "error", "server diag"},
	} {
		if _, err := db.ExecContext(ctx, ins, row...); err != nil {
			t.Fatalf("seed legacy log: %v", err)
		}
	}

	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate legacy db: %v", err)
	}
	// 重复启动必须幂等。
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate again: %v", err)
	}

	logs, err := st.ListRunLogs(ctx, "legacy-run", 0, 10)
	if err != nil {
		t.Fatalf("list after upgrade: %v", err)
	}
	if len(logs) != 3 {
		t.Fatalf("expected 3 preserved logs, got %d: %+v", len(logs), logs)
	}
	if logs[0].Message != "server diag" || logs[0].Source != model.RunLogSourceServer {
		t.Fatalf("高位段应回填为 server: %+v", logs[0])
	}
	if logs[2].Message != "agent first" || logs[2].Source != model.RunLogSourceAgent || logs[2].SourceSeq != 1 {
		t.Fatalf("低位段应保留为 agent 并带原 seq: %+v", logs[2])
	}
	if !(logs[2].ID < logs[1].ID && logs[1].ID < logs[0].ID) {
		t.Fatalf("id 未按原 seq 顺序分配: %d %d %d", logs[2].ID, logs[1].ID, logs[0].ID)
	}

	// 升级后写入：id 继续自增，且重放幂等。
	entry := model.RunLog{
		RunID: "legacy-run", Source: model.RunLogSourceAgent, SourceSeq: 3,
		Timestamp: now, Level: "info", Message: "after upgrade",
	}
	for range 2 {
		if err := st.AppendRunLogs(ctx, []model.RunLog{entry}); err != nil {
			t.Fatalf("append after upgrade: %v", err)
		}
	}
	after, err := st.ListRunLogs(ctx, "legacy-run", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 4 || after[0].Message != "after upgrade" || after[0].ID <= logs[0].ID {
		t.Fatalf("升级后追加异常: %+v", after)
	}
}

// newLegacyStoreBefore 建一个只应用到 before 之前（不含 before）的旧库，
// 使后续 Migrate 走真实升级路径。
func newLegacyStoreBefore(t *testing.T, before string) (Store, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	st, err := New(filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db := st.(*sqliteStore).db

	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") || e.Name() >= before {
			continue
		}
		data, err := fs.ReadFile(migrationsFS, path.Join("migrations", e.Name()))
		if err != nil {
			t.Fatalf("read migration %s: %v", e.Name(), err)
		}
		if _, err := db.ExecContext(ctx, string(data)); err != nil {
			t.Fatalf("apply legacy migration %s: %v", e.Name(), err)
		}
		if _, err := db.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
			e.Name(), now.Format(time.RFC3339)); err != nil {
			t.Fatalf("record legacy migration %s: %v", e.Name(), err)
		}
	}
	return st, db
}

// 旧库升级：0013 之后新增列与部分唯一索引，历史行保持可用且不参与去重，
// 并覆盖重复升级（第二次 Migrate 必须是 no-op）。
func TestMigrateRunDedupOnLegacyDB(t *testing.T) {
	ctx := context.Background()
	st, db := newLegacyStore(t)

	// 升级前就有队列里的 run（旧 schema 没有 dedup_key 列）。
	if _, err := db.ExecContext(ctx,
		`INSERT INTO runs (id, agent_id, operation, status, queued_at, progress_json, attempt)
		 VALUES ('legacy-1', 'agent-1', 'backup', 'queued', ?, '{}', 0)`,
		now.Format(time.RFC3339)); err != nil {
		t.Fatalf("insert legacy run: %v", err)
	}

	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate legacy db: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate again: %v", err)
	}

	legacy, err := st.GetRun(ctx, "legacy-1")
	if err != nil {
		t.Fatalf("legacy run after upgrade: %v", err)
	}
	if legacy.DedupKey != "" {
		t.Fatalf("legacy run must have no dedup key, got %q", legacy.DedupKey)
	}

	// 新列参与去重，且不受 NULL 历史行影响。
	run := &model.Run{
		ID: "run-1", AgentID: "agent-1", Operation: model.OpBackup,
		Status: model.RunQueued, QueuedAt: now, ProgressJSON: "{}", DedupKey: "k",
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun after upgrade: %v", err)
	}
	if err := st.CreateRun(ctx, &model.Run{
		ID: "run-2", AgentID: "agent-1", Operation: model.OpBackup,
		Status: model.RunQueued, QueuedAt: now, ProgressJSON: "{}", DedupKey: "k",
	}); !errors.Is(err, ErrDuplicateRun) {
		t.Fatalf("expected ErrDuplicateRun after upgrade, got %v", err)
	}
}

// 迁移失败必须整体回滚：不记录版本、不留半份 schema；障碍清除后重新升级成功。
func TestMigrateRunDedupRollbackAndRetry(t *testing.T) {
	ctx := context.Background()
	st, db := newLegacyStore(t)

	// 与索引同名的表会让 CREATE INDEX 失败（SQLite 名称冲突）。
	if _, err := db.ExecContext(ctx, `CREATE TABLE idx_runs_active_dedup (x)`); err != nil {
		t.Fatalf("create conflicting table: %v", err)
	}
	if err := st.Migrate(ctx); err == nil {
		t.Fatal("expected Migrate to fail on index name conflict")
	}

	var recorded int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM schema_migrations WHERE version = ?", runDedupMigration,
	).Scan(&recorded); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if recorded != 0 {
		t.Fatal("failed migration must not be recorded")
	}
	if cols, err := runColumns(ctx, db); err != nil {
		t.Fatal(err)
	} else if slices.Contains(cols, "dedup_key") {
		t.Fatalf("failed migration must roll back the added column, columns=%v", cols)
	}

	// 清除障碍后重新升级成功，且去重生效。
	if _, err := db.ExecContext(ctx, `DROP TABLE idx_runs_active_dedup`); err != nil {
		t.Fatalf("drop conflicting table: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate after fixing conflict: %v", err)
	}
	cols, err := runColumns(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cols, "dedup_key") {
		t.Fatalf("dedup_key column missing after retry, columns=%v", cols)
	}
	run := &model.Run{
		ID: "run-1", AgentID: "agent-1", Operation: model.OpBackup,
		Status: model.RunQueued, QueuedAt: now, ProgressJSON: "{}", DedupKey: "k",
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun after retry: %v", err)
	}
	if err := st.CreateRun(ctx, &model.Run{
		ID: "run-2", AgentID: "agent-1", Operation: model.OpBackup,
		Status: model.RunQueued, QueuedAt: now, ProgressJSON: "{}", DedupKey: "k",
	}); !errors.Is(err, ErrDuplicateRun) {
		t.Fatalf("expected dedup to work after retry, got %v", err)
	}
}

func runColumns(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(runs)")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notNull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols = append(cols, name)
	}
	return cols, rows.Err()
}

func TestCreateGetRun(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	if err := ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	}); err != nil {
		t.Fatalf("UpsertAgentOnConnect: %v", err)
	}
	_ = ts.CreateStorageTarget(ctx, &model.StorageTarget{
		ID: "tgt-1", Name: "gdrive", Type: "rclone",
		RemoteName: "gdrive", EncryptedConfig: []byte("x"),
		CreatedAt: now, UpdatedAt: now,
	})
	_ = ts.CreateRepository(ctx, &model.Repository{
		ID: "repo-1", AgentID: "agent-1", StorageTargetID: "tgt-1",
		RepositoryPath:    "gdrive:backups/srv/agent-1",
		EncryptedPassword: []byte("pw"), Status: "ready",
		CreatedAt: now, UpdatedAt: now,
	})
	_ = ts.CreatePlan(ctx, &model.Plan{
		ID: "plan-1", Name: "p1", AgentID: "agent-1",
		Kind: model.KindFilesystem, Schedule: "0 2 * * *",
		Timezone: "UTC", Enabled: true,
		SourceJSON:   sourceJSON([]string{"/etc"}),
		RepositoryID: "repo-1", RetentionJSON: retentionJSON(),
		TimeoutSeconds: 3600, CreatedAt: now, UpdatedAt: now,
	})

	// Manual run — no scheduled_at.
	r := &model.Run{
		ID: "run-manual", PlanID: "plan-1", AgentID: "agent-1",
		Operation: model.OpBackup, Status: model.RunQueued,
		QueuedAt: now, ProgressJSON: "{}",
	}
	if err := ts.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}

	got, err := ts.GetRun(ctx, "run-manual")
	if err != nil {
		t.Fatal(err)
	}
	if got.PlanID != "plan-1" || got.Status != model.RunQueued {
		t.Fatal("run mismatch")
	}
	if got.ScheduledAt != nil {
		t.Fatal("manual run should have nil ScheduledAt")
	}

	// List runs.
	runs, _ := ts.ListRuns(ctx, RunFilter{PlanID: "plan-1"})
	if len(runs) != 1 {
		t.Fatal("list count wrong")
	}
}

func TestTransitionRunHappyPath(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	if err := ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	}); err != nil {
		t.Fatalf("UpsertAgentOnConnect: %v", err)
	}
	_ = ts.CreateStorageTarget(ctx, &model.StorageTarget{
		ID: "tgt-1", Name: "gdrive", Type: "rclone",
		RemoteName: "gdrive", EncryptedConfig: []byte("x"),
		CreatedAt: now, UpdatedAt: now,
	})
	_ = ts.CreateRepository(ctx, &model.Repository{
		ID: "repo-1", AgentID: "agent-1", StorageTargetID: "tgt-1",
		RepositoryPath:    "gdrive:backups/srv/agent-1",
		EncryptedPassword: []byte("pw"), Status: "ready",
		CreatedAt: now, UpdatedAt: now,
	})
	_ = ts.CreatePlan(ctx, &model.Plan{
		ID: "plan-1", Name: "p1", AgentID: "agent-1",
		Kind: model.KindFilesystem, Schedule: "0 2 * * *",
		Timezone: "UTC", Enabled: true,
		SourceJSON:   sourceJSON([]string{"/etc"}),
		RepositoryID: "repo-1", RetentionJSON: retentionJSON(),
		TimeoutSeconds: 3600, CreatedAt: now, UpdatedAt: now,
	})
	_ = ts.CreateRun(ctx, &model.Run{
		ID: "run-1", PlanID: "plan-1", AgentID: "agent-1",
		Operation: model.OpBackup, Status: model.RunQueued,
		QueuedAt: now, ProgressJSON: "{}",
	})

	started := now.Add(1 * time.Minute)
	// queued → dispatched
	if err := ts.TransitionRun(ctx, "run-1", model.RunQueued, model.RunDispatched, nil); err != nil {
		t.Fatalf("queued→dispatched: %v", err)
	}

	// dispatched → running
	if err := ts.TransitionRun(ctx, "run-1", model.RunDispatched, model.RunRunning, nil); err != nil {
		t.Fatalf("dispatched→running: %v", err)
	}

	// running → succeeded with mutate
	if err := ts.TransitionRun(ctx, "run-1", model.RunRunning, model.RunSucceeded, func(r *model.Run) {
		prog := model.Progress{Phase: "done", Percent: 100, BytesDone: 1024}
		pj, _ := json.Marshal(prog)
		r.ProgressJSON = string(pj)
		r.SnapshotID = "snapshot-123"
		r.FinishedAt = &started
	}); err != nil {
		t.Fatalf("running→succeeded: %v", err)
	}

	got, _ := ts.GetRun(ctx, "run-1")
	if got.Status != model.RunSucceeded {
		t.Fatal("status mismatch")
	}
	if got.SnapshotID != "snapshot-123" {
		t.Fatal("snapshot not set by mutate")
	}
	if got.FinishedAt == nil || got.FinishedAt.UTC() != started {
		t.Fatal("finished_at not set")
	}
}

func TestTransitionRunInvalidPaths(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	if err := ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	}); err != nil {
		t.Fatalf("UpsertAgentOnConnect: %v", err)
	}
	_ = ts.CreateStorageTarget(ctx, &model.StorageTarget{
		ID: "tgt-1", Name: "gdrive", Type: "rclone",
		RemoteName: "gdrive", EncryptedConfig: []byte("x"),
		CreatedAt: now, UpdatedAt: now,
	})
	_ = ts.CreateRepository(ctx, &model.Repository{
		ID: "repo-1", AgentID: "agent-1", StorageTargetID: "tgt-1",
		RepositoryPath:    "gdrive:backups/srv/agent-1",
		EncryptedPassword: []byte("pw"), Status: "ready",
		CreatedAt: now, UpdatedAt: now,
	})
	_ = ts.CreatePlan(ctx, &model.Plan{
		ID: "plan-1", Name: "p1", AgentID: "agent-1",
		Kind: model.KindFilesystem, Schedule: "0 2 * * *",
		Timezone: "UTC", Enabled: true,
		SourceJSON:   sourceJSON([]string{"/etc"}),
		RepositoryID: "repo-1", RetentionJSON: retentionJSON(),
		TimeoutSeconds: 3600, CreatedAt: now, UpdatedAt: now,
	})

	_ = ts.CreateRun(ctx, &model.Run{
		ID: "run-1", PlanID: "plan-1", AgentID: "agent-1",
		Operation: model.OpBackup, Status: model.RunQueued,
		QueuedAt: now, ProgressJSON: "{}",
	})

	cases := []struct {
		name, from, to string
	}{
		// Terminal state: cannot transition further.
		{from: model.RunSucceeded, to: model.RunQueued},
		{from: model.RunSucceeded, to: model.RunFailed},
		{from: model.RunFailed, to: model.RunSucceeded},
		{from: model.RunCancelled, to: model.RunFailed},
		// Invalid skips.
		{from: model.RunQueued, to: model.RunRunning},
		{from: model.RunQueued, to: model.RunSucceeded},
		{from: model.RunRunning, to: model.RunDispatched},
		// Wrong from state.
		{from: model.RunRunning, to: model.RunSucceeded},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var err error
			if c.from == model.RunQueued && c.to == model.RunSucceeded {
				// Need to advance to queued first.
				_ = ts.TransitionRun(ctx, "run-1", model.RunQueued, model.RunDispatched, nil)
			} else {
				// Ensure run is in correct state for each test.
				switch c.from {
				case model.RunSucceeded:
					_ = ts.TransitionRun(ctx, "run-1", model.RunQueued, model.RunDispatched, nil)
					_ = ts.TransitionRun(ctx, "run-1", model.RunDispatched, model.RunRunning, nil)
					_ = ts.TransitionRun(ctx, "run-1", model.RunRunning, model.RunSucceeded, nil)
				case model.RunFailed:
					_ = ts.TransitionRun(ctx, "run-1", model.RunQueued, model.RunDispatched, nil)
					_ = ts.TransitionRun(ctx, "run-1", model.RunDispatched, model.RunRunning, nil)
					_ = ts.TransitionRun(ctx, "run-1", model.RunRunning, model.RunFailed, nil)
				case model.RunCancelled:
					_ = ts.TransitionRun(ctx, "run-1", model.RunQueued, model.RunDispatched, nil)
					_ = ts.TransitionRun(ctx, "run-1", model.RunDispatched, model.RunRunning, nil)
					_ = ts.TransitionRun(ctx, "run-1", model.RunRunning, model.RunCancelled, nil)
				case model.RunRunning:
					_ = ts.TransitionRun(ctx, "run-1", model.RunQueued, model.RunDispatched, nil)
					_ = ts.TransitionRun(ctx, "run-1", model.RunDispatched, model.RunRunning, nil)
				}
			}

			err = ts.TransitionRun(ctx, "run-1", c.from, c.to, nil)
			if err != ErrInvalidTransition {
				t.Fatalf("case from=%s to=%s: expected ErrInvalidTransition, got %v", c.from, c.to, err)
			}
		})
	}
}

// TestTransitionRunEarlyFailure verifies the legal early-exit transitions
// used by the dispatcher (queued→failed for unbuildable jobs), fast-finished
// agent results (dispatched→succeeded|cancelled) and both failure watchdogs
// (dispatched/running→failed); terminal states stay final.
func TestTransitionRunEarlyFailure(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	if err := ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	}); err != nil {
		t.Fatalf("UpsertAgentOnConnect: %v", err)
	}
	_ = ts.CreateRun(ctx, &model.Run{
		ID: "run-1", AgentID: "agent-1",
		Operation: model.OpBackup, Status: model.RunQueued,
		QueuedAt: now, ProgressJSON: "{}",
	})

	// queued -> failed (dispatcher build failure / scheduler stale queue).
	if err := ts.TransitionRun(ctx, "run-1", model.RunQueued, model.RunFailed, func(r *model.Run) {
		r.ErrorCode = model.ErrInvalidPlan
	}); err != nil {
		t.Fatalf("queued→failed: %v", err)
	}

	// Terminal states must not re-enter the machine.
	if err := ts.TransitionRun(ctx, "run-1", model.RunFailed, model.RunQueued, nil); err != ErrInvalidTransition {
		t.Fatalf("failed→queued: expected ErrInvalidTransition, got %v", err)
	}

	for _, to := range []string{model.RunSucceeded, model.RunCancelled} {
		_ = ts.CreateRun(ctx, &model.Run{
			ID: "run-" + to, PlanID: "", AgentID: "agent-1",
			Operation: model.OpBackup, Status: model.RunDispatched,
			QueuedAt: now, ProgressJSON: "{}",
		})
		if err := ts.TransitionRun(ctx, "run-"+to, model.RunDispatched, to, func(r *model.Run) {
			now2 := now.Add(time.Minute)
			r.StartedAt = &now2
			r.FinishedAt = &now2
		}); err != nil {
			t.Fatalf("dispatched→%s: %v", to, err)
		}
	}

	// dispatched -> failed (watchdog).
	_ = ts.CreateRun(ctx, &model.Run{
		ID: "run-wd", AgentID: "agent-1",
		Operation: model.OpBackup, Status: model.RunDispatched,
		QueuedAt: now, ProgressJSON: "{}",
	})
	if err := ts.TransitionRun(ctx, "run-wd", model.RunDispatched, model.RunFailed, func(r *model.Run) {
		r.ErrorCode = model.ErrTimeout
	}); err != nil {
		t.Fatalf("dispatched→failed: %v", err)
	}
}

func TestTelegramSettings(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	if _, err := ts.GetTelegramSettings(ctx); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound when unset, got %v", err)
	}

	first := &model.TelegramSettings{BotToken: "123456:ABC-secret", ChatID: "-1001"}
	if err := ts.SaveTelegramSettings(ctx, first); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := ts.GetTelegramSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.BotToken != "123456:ABC-secret" || got.ChatID != "-1001" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}

	second := &model.TelegramSettings{BotToken: "999:XYZ", ChatID: "-2002"}
	if err := ts.SaveTelegramSettings(ctx, second); err != nil {
		t.Fatalf("overwrite save: %v", err)
	}
	got, _ = ts.GetTelegramSettings(ctx)
	if got.BotToken != "999:XYZ" || got.ChatID != "-2002" {
		t.Fatalf("overwrite mismatch: %+v", got)
	}

	if err := ts.DeleteTelegramSettings(ctx); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := ts.GetTelegramSettings(ctx); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestTransitionRunNotFound(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	err := ts.TransitionRun(ctx, "nonexistent", model.RunQueued, model.RunDispatched, nil)
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestFailStaleRuns(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	if err := ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	}); err != nil {
		t.Fatalf("UpsertAgentOnConnect: %v", err)
	}
	_ = ts.CreateStorageTarget(ctx, &model.StorageTarget{
		ID: "tgt-1", Name: "gdrive", Type: "rclone",
		RemoteName: "gdrive", EncryptedConfig: []byte("x"),
		CreatedAt: now, UpdatedAt: now,
	})
	_ = ts.CreateRepository(ctx, &model.Repository{
		ID: "repo-1", AgentID: "agent-1", StorageTargetID: "tgt-1",
		RepositoryPath:    "gdrive:backups/srv/agent-1",
		EncryptedPassword: []byte("pw"), Status: "ready",
		CreatedAt: now, UpdatedAt: now,
	})
	_ = ts.CreatePlan(ctx, &model.Plan{
		ID: "plan-1", Name: "p1", AgentID: "agent-1",
		Kind: model.KindFilesystem, Schedule: "0 2 * * *",
		Timezone: "UTC", Enabled: true,
		SourceJSON:   sourceJSON([]string{"/etc"}),
		RepositoryID: "repo-1", RetentionJSON: retentionJSON(),
		TimeoutSeconds: 3600, CreatedAt: now, UpdatedAt: now,
	})

	failedAt := now.Add(-5 * time.Minute)
	_ = ts.CreateRun(ctx, &model.Run{
		ID: "run-running", PlanID: "plan-1", AgentID: "agent-1",
		Operation: model.OpBackup, Status: model.RunRunning,
		QueuedAt: now, ProgressJSON: "{}",
	})
	_ = ts.CreateRun(ctx, &model.Run{
		ID: "run-dispatched", PlanID: "plan-1", AgentID: "agent-1",
		Operation: model.OpBackup, Status: model.RunDispatched,
		QueuedAt: now, ProgressJSON: "{}",
	})
	_ = ts.CreateRun(ctx, &model.Run{
		ID: "run-queued", PlanID: "plan-1", AgentID: "agent-1",
		Operation: model.OpBackup, Status: model.RunQueued,
		QueuedAt: now, ProgressJSON: "{}",
	})

	ids, err := ts.FailStaleRuns(ctx, []string{model.RunRunning, model.RunDispatched}, model.ErrServerRestarted, failedAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 rows affected, got %d (%v)", len(ids), ids)
	}
	sort.Strings(ids)
	wantIDs := []string{"run-dispatched", "run-running"}
	if !slices.Equal(ids, wantIDs) {
		t.Fatalf("expected exactly the dispatched/running IDs %v, got %v", wantIDs, ids)
	}

	got, _ := ts.GetRun(ctx, "run-running")
	if got.Status != model.RunFailed || got.ErrorCode != model.ErrServerRestarted {
		t.Fatalf("run not failed: status=%s code=%s", got.Status, got.ErrorCode)
	}
	if got.FinishedAt == nil || got.FinishedAt.UTC() != failedAt {
		t.Fatal("finished_at not set")
	}

	queued, _ := ts.GetRun(ctx, "run-queued")
	if queued.Status != model.RunQueued {
		t.Fatal("queued run should not be affected")
	}
}

func TestRunLogs(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	// Need a valid run for the FK constraint.
	_ = ts.CreateRun(ctx, &model.Run{
		ID: "run-1", AgentID: "agent-1",
		Operation: model.OpBackup, Status: model.RunQueued,
		QueuedAt: now, ProgressJSON: "{}",
	})

	logs := []model.RunLog{
		{RunID: "run-1", Source: model.RunLogSourceAgent, SourceSeq: 1, Timestamp: now, Level: "info", Message: "started"},
		{RunID: "run-1", Source: model.RunLogSourceAgent, SourceSeq: 2, Timestamp: now.Add(1 * time.Second), Level: "debug", Message: "progress"},
		{RunID: "run-1", Source: model.RunLogSourceServer, SourceSeq: 900, Timestamp: now.Add(2 * time.Second), Level: "error", Message: "failed"},
	}
	if err := ts.AppendRunLogs(ctx, logs); err != nil {
		t.Fatal(err)
	}
	// 落库成功必须回填真实 ID，供实时推送按 id 去重/排序。
	if logs[0].ID == 0 || logs[1].ID == 0 || logs[2].ID == 0 {
		t.Fatalf("expected backfilled ids, got %+v", logs)
	}
	if logs[1].ID <= logs[0].ID || logs[2].ID <= logs[1].ID {
		t.Fatalf("expected ascending backfilled ids, got %d, %d, %d", logs[0].ID, logs[1].ID, logs[2].ID)
	}

	// 重放同一批日志必须幂等：不报错、不产生重复行，且被忽略的行 ID 置 0。
	replay := append([]model.RunLog(nil), logs...)
	if err := ts.AppendRunLogs(ctx, replay); err != nil {
		t.Fatalf("replay should be idempotent, got %v", err)
	}
	for i, l := range replay {
		if l.ID != 0 {
			t.Fatalf("replay[%d].ID = %d, want 0 (ignored duplicate)", i, l.ID)
		}
	}

	listed, _ := ts.ListRunLogs(ctx, "run-1", 0, 10)
	if len(listed) != 3 {
		t.Fatalf("expected 3 logs, got %d", len(listed))
	}
	// Returned newest first.
	if listed[0].Message != "failed" {
		t.Fatal("first log should be newest")
	}
	// 来源与本地序号被保留，id 由 store 分配且递增。
	if listed[0].Source != model.RunLogSourceServer || listed[0].SourceSeq != 900 {
		t.Fatalf("unexpected source mapping: %+v", listed[0])
	}
	if listed[0].ID <= listed[2].ID || listed[2].ID == 0 {
		t.Fatalf("expected ascending ids, got %d..%d", listed[2].ID, listed[0].ID)
	}

	// Paginate with beforeID（游标是 id，不再是 seq）。
	page, _ := ts.ListRunLogs(ctx, "run-1", listed[0].ID, 10)
	if len(page) != 2 {
		t.Fatalf("expected 2 logs before newest id, got %d", len(page))
	}
	if page[0].Message != "progress" {
		t.Fatalf("expected newest of the older page, got %q", page[0].Message)
	}
}

func TestRestoreRequest(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	_ = ts.CreateRun(ctx, &model.Run{
		ID: "run-1", AgentID: "agent-1",
		Operation: model.OpBackup, Status: model.RunQueued,
		QueuedAt: now, ProgressJSON: "{}",
	})

	rr := &model.RestoreRequest{
		ID:               "rr-1",
		RunID:            "run-1",
		SnapshotID:       "snapshot-abc",
		RestoreKind:      model.KindFilesystem,
		Target:           model.RestoreTarget{TargetPath: "/tmp/restore"},
		TargetJSON:       targetJSON(),
		Overwrite:        true,
		ConfirmationHash: "sha256-confirm-hash",
		CreatedAt:        now,
	}
	if err := ts.CreateRestoreRequest(ctx, rr); err != nil {
		t.Fatal(err)
	}

	got, err := ts.GetRestoreRequest(ctx, "rr-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Target.TargetPath != "/tmp/restore" {
		t.Fatal("target not deserialized")
	}
	if !got.Overwrite {
		t.Fatal("overwrite flag wrong")
	}
	if got.ConfirmationHash != "sha256-confirm-hash" {
		t.Fatal("confirmation_hash wrong")
	}

	listed, _ := ts.ListRestoreRequests(ctx, 5)
	if len(listed) != 1 {
		t.Fatal("list count wrong")
	}

	// 队列去重的复用路径按 run_id 取回同一条 request。
	byRun, err := ts.GetRestoreRequestByRunID(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if byRun.ID != "rr-1" {
		t.Fatalf("expected rr-1, got %s", byRun.ID)
	}
	if _, err := ts.GetRestoreRequestByRunID(ctx, "run-missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCreateDatabaseRestoreRun(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	run := &model.Run{
		ID: "run-db-1", AgentID: "agent-1",
		Operation: model.OpRestore, Status: model.RunQueued,
		QueuedAt: now, ProgressJSON: "{}",
	}
	rr := &model.RestoreRequest{
		ID:          "rr-db-1",
		RunID:       run.ID,
		SnapshotID:  "snapshot-abc",
		RestoreKind: model.KindPostgreSQL,
		Target:      model.RestoreTarget{Host: "db", Port: 5432, Username: "u", Database: "appdb"},
		TargetJSON:  targetJSON(),
		Phase:       model.RestorePhaseQueued,
		CreatedAt:   now,
	}
	// restore_requests.run_id 外键指向 runs(id)；数据库恢复在同一事务内落两行，
	// 空 RunID 必须失败而不是静默写入。
	if err := ts.CreateDatabaseRestoreRun(ctx, run, rr); err != nil {
		t.Fatalf("CreateDatabaseRestoreRun: %v", err)
	}
	got, err := ts.GetRestoreRequestByRunID(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRestoreRequestByRunID: %v", err)
	}
	if got.ID != rr.ID || got.RestoreKind != model.KindPostgreSQL {
		t.Fatalf("persisted request mismatch: %+v", got)
	}

	badRun := &model.Run{
		ID: "run-db-2", AgentID: "agent-1",
		Operation: model.OpRestore, Status: model.RunQueued,
		QueuedAt: now, ProgressJSON: "{}",
	}
	badRR := &model.RestoreRequest{
		ID:          "rr-db-2",
		SnapshotID:  "snapshot-abc",
		RestoreKind: model.KindPostgreSQL,
		TargetJSON:  targetJSON(),
		Phase:       model.RestorePhaseQueued,
		CreatedAt:   now,
	}
	if err := ts.CreateDatabaseRestoreRun(ctx, badRun, badRR); err == nil {
		t.Fatal("expected foreign key failure for empty run_id")
	}
}

func TestAuditEvent(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	e := &model.AuditEvent{
		ID:           "ae-1",
		OccurredAt:   now,
		ActorType:    "admin",
		ActorID:      "admin-1",
		Action:       "run.create",
		ResourceType: "run",
		ResourceID:   "run-1",
		DetailJSON:   `{"plan":"plan-1"}`,
	}
	if err := ts.AppendAuditEvent(ctx, e); err != nil {
		t.Fatal(err)
	}

	listed, _ := ts.ListAuditEvents(ctx, 10)
	if len(listed) != 1 {
		t.Fatal("audit list count wrong")
	}
	if listed[0].Action != "run.create" {
		t.Fatal("action mismatch")
	}
	if listed[0].DetailJSON != `{"plan":"plan-1"}` {
		t.Fatal("detail not stored")
	}
}

func TestListRunsByStatus(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	if err := ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	}); err != nil {
		t.Fatalf("UpsertAgentOnConnect: %v", err)
	}
	_ = ts.CreateStorageTarget(ctx, &model.StorageTarget{
		ID: "tgt-1", Name: "gdrive", Type: "rclone",
		RemoteName: "gdrive", EncryptedConfig: []byte("x"),
		CreatedAt: now, UpdatedAt: now,
	})
	_ = ts.CreateRepository(ctx, &model.Repository{
		ID: "repo-1", AgentID: "agent-1", StorageTargetID: "tgt-1",
		RepositoryPath:    "gdrive:backups/srv/agent-1",
		EncryptedPassword: []byte("pw"), Status: "ready",
		CreatedAt: now, UpdatedAt: now,
	})
	_ = ts.CreatePlan(ctx, &model.Plan{
		ID: "plan-1", Name: "p1", AgentID: "agent-1",
		Kind: model.KindFilesystem, Schedule: "0 2 * * *",
		Timezone: "UTC", Enabled: true,
		SourceJSON:   sourceJSON([]string{"/etc"}),
		RepositoryID: "repo-1", RetentionJSON: retentionJSON(),
		TimeoutSeconds: 3600, CreatedAt: now, UpdatedAt: now,
	})

	_ = ts.CreateRun(ctx, &model.Run{
		ID: "run-queued", PlanID: "plan-1", AgentID: "agent-1",
		Operation: model.OpBackup, Status: model.RunQueued,
		QueuedAt: now, ProgressJSON: "{}",
	})
	_ = ts.CreateRun(ctx, &model.Run{
		ID: "run-dispatched", PlanID: "plan-1", AgentID: "agent-1",
		Operation: model.OpBackup, Status: model.RunDispatched,
		QueuedAt: now, ProgressJSON: "{}",
	})

	runs, _ := ts.ListRunsByStatus(ctx, []string{model.RunQueued, model.RunDispatched})
	if len(runs) != 2 {
		t.Fatalf("expected 2, got %d", len(runs))
	}
}

func TestListRunsFilter(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	if err := ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	}); err != nil {
		t.Fatalf("UpsertAgentOnConnect: %v", err)
	}
	_ = ts.CreateStorageTarget(ctx, &model.StorageTarget{
		ID: "tgt-1", Name: "gdrive", Type: "rclone",
		RemoteName: "gdrive", EncryptedConfig: []byte("x"),
		CreatedAt: now, UpdatedAt: now,
	})
	_ = ts.CreateRepository(ctx, &model.Repository{
		ID: "repo-1", AgentID: "agent-1", StorageTargetID: "tgt-1",
		RepositoryPath:    "gdrive:backups/srv/agent-1",
		EncryptedPassword: []byte("pw"), Status: "ready",
		CreatedAt: now, UpdatedAt: now,
	})
	_ = ts.CreatePlan(ctx, &model.Plan{
		ID: "plan-1", Name: "p1", AgentID: "agent-1",
		Kind: model.KindFilesystem, Schedule: "0 2 * * *",
		Timezone: "UTC", Enabled: true,
		SourceJSON:   sourceJSON([]string{"/etc"}),
		RepositoryID: "repo-1", RetentionJSON: retentionJSON(),
		TimeoutSeconds: 3600, CreatedAt: now, UpdatedAt: now,
	})
	_ = ts.CreateRun(ctx, &model.Run{
		ID: "run-1", PlanID: "plan-1", AgentID: "agent-1",
		Operation: model.OpBackup, Status: model.RunQueued,
		QueuedAt: now, ProgressJSON: "{}",
	})

	// Filter by operation.
	runs, _ := ts.ListRuns(ctx, RunFilter{Operation: model.OpBackup})
	if len(runs) != 1 {
		t.Fatal("operation filter count wrong")
	}

	// Filter by status.
	runs, _ = ts.ListRuns(ctx, RunFilter{Statuses: []string{model.RunSucceeded}})
	if len(runs) != 0 {
		t.Fatal("no succeeded runs expected")
	}
}

// 孤儿扫描失败的退避必须真正落库：ClearSnapshotCleanupScan 曾接收
// nextAttemptAt 参数但状态表没有对应列，参数被静默忽略，导致扫描失败后
// 每个 scheduler tick 立即重发，run/日志无上限增长。
func TestSnapshotCleanupScanBackoffPersisted(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	if err := ts.CreateStorageTarget(ctx, &model.StorageTarget{
		ID: "tgt-clean", Name: "t", Type: "rclone", RemoteName: "r", RemotePath: "/x",
		EncryptedConfig: []byte("cfg"), CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateStorageTarget: %v", err)
	}
	// 仓库对 agents 有外键，先建 agent。
	if err := ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	}); err != nil {
		t.Fatalf("UpsertAgentOnConnect: %v", err)
	}
	if err := ts.CreateRepository(ctx, &model.Repository{
		ID: "repo-clean", AgentID: "agent-1", StorageTargetID: "tgt-clean",
		RepositoryPath: "r:/x", EncryptedPassword: []byte("pw"), Status: "ready",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	sds, ok := ts.Store.(SnapshotDeletionStore)
	if !ok {
		t.Fatal("store does not implement SnapshotDeletionStore")
	}

	start := now
	if err := sds.StartSnapshotCleanupScan(ctx, "repo-clean", "run-1", start); err != nil {
		t.Fatalf("StartSnapshotCleanupScan: %v", err)
	}
	st, err := sds.GetSnapshotCleanupState(ctx, "repo-clean")
	if err != nil {
		t.Fatalf("GetSnapshotCleanupState: %v", err)
	}
	if st.ScanRunID != "run-1" || st.NextAttemptAt != nil {
		t.Fatalf("after start: scan_run_id=%q next=%v", st.ScanRunID, st.NextAttemptAt)
	}

	// 失败：清活跃扫描并写入退避时间。
	retryAt := start.Add(time.Hour)
	if err := sds.ClearSnapshotCleanupScan(ctx, "repo-clean", "run-1", retryAt); err != nil {
		t.Fatalf("ClearSnapshotCleanupScan: %v", err)
	}
	st, err = sds.GetSnapshotCleanupState(ctx, "repo-clean")
	if err != nil {
		t.Fatalf("GetSnapshotCleanupState: %v", err)
	}
	if st.ScanRunID != "" {
		t.Fatalf("active scan must be cleared, got %q", st.ScanRunID)
	}
	if st.NextAttemptAt == nil || !st.NextAttemptAt.Equal(retryAt) {
		t.Fatalf("backoff must be persisted, got %v want %v", st.NextAttemptAt, retryAt)
	}

	// 重新开始扫描：退避被清除，不会阻止本次扫描。
	if err := sds.StartSnapshotCleanupScan(ctx, "repo-clean", "run-2", start.Add(2*time.Hour)); err != nil {
		t.Fatalf("StartSnapshotCleanupScan(2): %v", err)
	}
	st, _ = sds.GetSnapshotCleanupState(ctx, "repo-clean")
	if st.ScanRunID != "run-2" || st.NextAttemptAt != nil {
		t.Fatalf("start must clear backoff: scan_run_id=%q next=%v", st.ScanRunID, st.NextAttemptAt)
	}

	// 成功：写完成时间并清除退避。
	done := start.Add(3 * time.Hour)
	if err := sds.FinishSnapshotCleanupScan(ctx, "repo-clean", "run-2", nil, done); err != nil {
		t.Fatalf("FinishSnapshotCleanupScan: %v", err)
	}
	st, _ = sds.GetSnapshotCleanupState(ctx, "repo-clean")
	if st.ScanRunID != "" || st.NextAttemptAt != nil {
		t.Fatalf("finish must clear scan and backoff: %+v", st)
	}
	if st.LastScanCompletedAt == nil || !st.LastScanCompletedAt.Equal(done) {
		t.Fatalf("last_scan_completed_at = %v want %v", st.LastScanCompletedAt, done)
	}
}

// 升级前的安全备份是完整数据库副本，必须仅属主可读：容器默认 umask 0022 会让
// VACUUM INTO 产出 0644（systemd 的 UMask=0077 掩盖了这个问题），因此代码显式
// 收紧为 0600。Windows 无 POSIX 权限位，跳过。
func TestBackupSQLiteIsOwnerReadableOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix permissions are not meaningful on windows")
	}
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "bmc.db")
	st, err := New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(dir, "bmc.db.pre-migration-test.bak")
	if err := BackupSQLite(context.Background(), dbPath, backupPath); err != nil {
		t.Fatalf("BackupSQLite: %v", err)
	}
	info, err := os.Stat(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("backup permissions = %04o, want 0600", perm)
	}
}

// 终态观察者：仅在事务提交成功且进入终态时通知一次。指标依赖它统计
// bmc_runs_total/bmc_run_duration_seconds（此前这两项从不统计真实终态运行）。
func TestTransitionRunNotifiesObserverOnTerminalOnly(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	var got []model.Run
	ts.Store.(interface {
		SetRunObserver(RunObserver)
	}).SetRunObserver(observerFunc(func(run model.Run) { got = append(got, run) }))

	mk := func(id string) {
		if err := ts.Store.CreateRun(ctx, &model.Run{
			ID: id, AgentID: "a1", Operation: model.OpBackup, Status: model.RunQueued, QueuedAt: now,
		}); err != nil {
			t.Fatalf("CreateRun %s: %v", id, err)
		}
	}
	mk("run-ok")
	if err := ts.Store.TransitionRun(ctx, "run-ok", model.RunQueued, model.RunDispatched, nil); err != nil {
		t.Fatalf("queued->dispatched: %v", err)
	}
	if err := ts.Store.TransitionRun(ctx, "run-ok", model.RunDispatched, model.RunRunning, nil); err != nil {
		t.Fatalf("dispatched->running: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("non-terminal transition must not notify, got %d", len(got))
	}
	fin := now.Add(90 * time.Second)
	if err := ts.Store.TransitionRun(ctx, "run-ok", model.RunRunning, model.RunSucceeded, func(r *model.Run) {
		r.FinishedAt = &fin
	}); err != nil {
		t.Fatalf("running->succeeded: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("terminal transition must notify exactly once, got %d", len(got))
	}
	if got[0].Operation != model.OpBackup || got[0].Status != model.RunSucceeded {
		t.Fatalf("observer got %+v", got[0])
	}
	// 已是终态：再次转换必须失败且不得重复通知。
	if err := ts.Store.TransitionRun(ctx, "run-ok", model.RunSucceeded, model.RunFailed, nil); err == nil {
		t.Fatal("transition from a terminal state must fail")
	}
	if len(got) != 1 {
		t.Fatalf("no extra notification expected, got %d", len(got))
	}

	// 失败态同样通知。
	mk("run-fail")
	if err := ts.Store.TransitionRun(ctx, "run-fail", model.RunQueued, model.RunFailed, nil); err != nil {
		t.Fatalf("queued->failed: %v", err)
	}
	if len(got) != 2 || got[1].Status != model.RunFailed {
		t.Fatalf("failed transition must notify, got %+v", got)
	}
}

type observerFunc func(model.Run)

func (f observerFunc) ObserveRunTerminal(run model.Run) { f(run) }

// 写方法必须在 store 内串行化（AGENTS.md 约定）。AppendRunLogs 曾是唯一用事务写库
// 却不取 s.mu 的路径：与受锁写方法并发时互相撞 SQLITE_BUSY，实测表现为快照树缓存
// 写入以 500 "database is locked (5)" 失败。此处确定性验证它必须等待写锁。
func TestAppendRunLogsWaitsForStoreWriteLock(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	if err := ts.Store.CreateRun(ctx, &model.Run{
		ID: "run-logs", AgentID: "a1", Operation: model.OpBackup, Status: model.RunQueued, QueuedAt: now,
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	inner := ts.Store.(*sqliteStore)

	inner.mu.Lock() // 模拟另一个写方法正在持锁
	done := make(chan error, 1)
	go func() {
		done <- ts.Store.AppendRunLogs(ctx, []model.RunLog{{RunID: "run-logs", Source: model.RunLogSourceAgent, SourceSeq: 1, Level: "info", Message: "hello", Timestamp: now}})
	}()
	select {
	case err := <-done:
		inner.mu.Unlock()
		t.Fatalf("AppendRunLogs returned (%v) while the write lock was held — writes are not serialized", err)
	case <-time.After(200 * time.Millisecond):
	}
	inner.mu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("AppendRunLogs after unlock: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("AppendRunLogs did not complete after the write lock was released")
	}
}
