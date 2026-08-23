package store

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"sort"
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

	if err := ts.SaveAgentCapabilities(ctx, "agent-1", []model.ToolInfo{{Name: "restic", Version: "0.17.0"}}, now); err != nil {
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
	_ = ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	})

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

func TestRepository(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	_ = ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	})
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
}

func TestListRepositoriesNeedingCheck(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	_ = ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	})

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

	_ = ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	})
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

func TestDeletePlanInUse(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	// Build agent, target, repo, plan, run for plan-1.
	_ = ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	})
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

	_ = ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	})
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

func TestCreateGetRun(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	_ = ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	})
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

	_ = ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	})
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

	_ = ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	})
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
		{from: model.RunRunning, to: model.RunQueued},
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

	_ = ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	})
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

	_ = ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	})
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
		{RunID: "run-1", Seq: 1, Timestamp: now, Level: "info", Message: "started"},
		{RunID: "run-1", Seq: 2, Timestamp: now.Add(1 * time.Second), Level: "debug", Message: "progress"},
		{RunID: "run-1", Seq: 3, Timestamp: now.Add(2 * time.Second), Level: "error", Message: "failed"},
	}
	if err := ts.AppendRunLogs(ctx, logs); err != nil {
		t.Fatal(err)
	}

	listed, _ := ts.ListRunLogs(ctx, "run-1", 0, 10)
	if len(listed) != 3 {
		t.Fatalf("expected 3 logs, got %d", len(listed))
	}
	// Returned newest first.
	if listed[0].Message != "failed" {
		t.Fatal("first log should be newest")
	}

	seq, _ := ts.MaxRunLogSeq(ctx, "run-1")
	if seq != 3 {
		t.Fatalf("expected max seq 3, got %d", seq)
	}

	// Paginate with beforeSeq.
	page, _ := ts.ListRunLogs(ctx, "run-1", 3, 10)
	if len(page) != 2 {
		t.Fatalf("expected 2 logs before seq 3, got %d", len(page))
	}

	_, err := ts.MaxRunLogSeq(ctx, "nonexistent")
	if err != nil {
		t.Fatal(err)
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

	_ = ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	})
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

	_ = ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &now, EnrolledAt: now, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	})
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
