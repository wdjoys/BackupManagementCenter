package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"backupmanagementcenter/internal/model"
)

func newSnapshotCacheRepository(t *testing.T) (testStore, *model.StorageTarget, *model.Repository) {
	t.Helper()
	ts := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := ts.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "cache-agent", Name: "agent", Hostname: "host", OS: "linux", Version: "1",
		Status: model.AgentOffline, LastSeenAt: &now, EnrolledAt: now, TokenHash: "token",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	}); err != nil {
		ts.Close(t)
		t.Fatalf("create agent: %v", err)
	}
	target := &model.StorageTarget{
		ID: "cache-target", Name: "cache-target", Type: "rclone", RemoteName: "remote",
		EncryptedConfig: []byte("config"), CreatedAt: now, UpdatedAt: now,
	}
	if err := ts.CreateStorageTarget(ctx, target); err != nil {
		ts.Close(t)
		t.Fatalf("create target: %v", err)
	}
	repo := &model.Repository{
		ID: "cache-repo", AgentID: "cache-agent", StorageTargetID: target.ID,
		RepositoryPath: "remote/cache", EncryptedPassword: []byte("password"), Status: "ready",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := ts.CreateRepository(ctx, repo); err != nil {
		ts.Close(t)
		t.Fatalf("create repository: %v", err)
	}
	return ts, target, repo
}

func snapshotJSON(t *testing.T, snapshots []model.Snapshot) string {
	t.Helper()
	data, err := json.Marshal(snapshots)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSnapshotCachePathAndFingerprint(t *testing.T) {
	if got := NormalizeSnapshotPath(""); got != "/" {
		t.Fatalf("empty path = %q", got)
	}
	if got := NormalizeSnapshotPath("foo/../bar/"); got != "/bar" {
		t.Fatalf("normalized path = %q", got)
	}
	left := []model.Snapshot{{ID: "b", Tags: []string{"z", "a"}, Paths: []string{"/two", "/one"}}, {ID: "a"}}
	right := []model.Snapshot{{ID: "a"}, {ID: "b", Tags: []string{"a", "z"}, Paths: []string{"/one", "/two"}}}
	if SnapshotFingerprint(left) != SnapshotFingerprint(right) {
		t.Fatal("fingerprint depends on remote ordering")
	}
}

func TestSnapshotCacheGenerationCASAndReconcile(t *testing.T) {
	ts, _, repo := newSnapshotCacheRepository(t)
	defer ts.Close(t)
	ctx := context.Background()
	cs := ts.Store.(SnapshotCacheStore)
	generation, err := cs.SnapshotCacheGeneration(ctx, repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	list := []model.Snapshot{{ID: "snap-1"}, {ID: "snap-2"}}
	listJSON := snapshotJSON(t, list)
	if err := cs.SaveSnapshotListCache(ctx, repo.ID, generation, listJSON, SnapshotFingerprint(list), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := cs.SaveSnapshotTreeCache(ctx, repo.ID, "snap-1", "", generation, `{"entries":[]}`, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := cs.SaveSnapshotTreeCache(ctx, repo.ID, "snap-2", "/dir/../", generation, `{"entries":[]}`, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.GetSnapshotTreeCache(ctx, repo.ID, "snap-2", "/"); err != nil {
		t.Fatalf("tree cache lookup: %v", err)
	}

	if err := cs.InvalidateSnapshotCache(ctx, repo.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.GetSnapshotListCache(ctx, repo.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("invalidated list should not be fresh, got %v", err)
	}
	if err := cs.SaveSnapshotListCache(ctx, repo.ID, generation, listJSON, SnapshotFingerprint(list), time.Now()); !errors.Is(err, ErrCacheGenerationChanged) {
		t.Fatalf("old generation write = %v", err)
	}

	generation, err = cs.SnapshotCacheGeneration(ctx, repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	remaining := []model.Snapshot{{ID: "snap-1"}}
	if err := cs.SaveSnapshotListCache(ctx, repo.ID, generation, snapshotJSON(t, remaining), SnapshotFingerprint(remaining), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.GetSnapshotTreeCache(ctx, repo.ID, "snap-2", "/"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed snapshot tree should be reconciled, got %v", err)
	}
}

func TestMutatingRunAndTargetUpdateInvalidateCache(t *testing.T) {
	ts, target, repo := newSnapshotCacheRepository(t)
	defer ts.Close(t)
	ctx := context.Background()
	cs := ts.Store.(SnapshotCacheStore)
	generation, err := cs.SnapshotCacheGeneration(ctx, repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	list := []model.Snapshot{{ID: "snap-1"}}
	if err := cs.SaveSnapshotListCache(ctx, repo.ID, generation, snapshotJSON(t, list), SnapshotFingerprint(list), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := cs.SaveSnapshotTreeCache(ctx, repo.ID, "snap-1", "/", generation, `{"entries":[]}`, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := ts.CreateRun(ctx, &model.Run{
		ID: "cache-backup", AgentID: repo.AgentID, Operation: model.OpBackup, Status: model.RunQueued,
		QueuedAt: time.Now().UTC(), RepositoryID: repo.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.GetSnapshotListCache(ctx, repo.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("backup-created cache should be stale, got %v", err)
	}
	if _, err := cs.GetSnapshotTreeCache(ctx, repo.ID, "snap-1", "/"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("backup should hide old tree until list revalidation, got %v", err)
	}

	generation, err = cs.SnapshotCacheGeneration(ctx, repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.SaveSnapshotListCache(ctx, repo.ID, generation, snapshotJSON(t, list), SnapshotFingerprint(list), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.GetSnapshotTreeCache(ctx, repo.ID, "snap-1", "/"); err != nil {
		t.Fatalf("existing snapshot tree should be promoted after list revalidation: %v", err)
	}
	target.UpdatedAt = time.Now().UTC()
	target.RemotePath = "changed"
	if err := ts.UpdateStorageTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.GetSnapshotListCache(ctx, repo.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("target update should stale cache, got %v", err)
	}
}

func TestForgetClearsTreesAndDuplicateRunKeepsCache(t *testing.T) {
	ts, _, repo := newSnapshotCacheRepository(t)
	defer ts.Close(t)
	ctx := context.Background()
	cs := ts.Store.(SnapshotCacheStore)
	list := []model.Snapshot{{ID: "snap-1"}}

	generation, err := cs.SnapshotCacheGeneration(ctx, repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.SaveSnapshotListCache(ctx, repo.ID, generation, snapshotJSON(t, list), SnapshotFingerprint(list), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := cs.SaveSnapshotTreeCache(ctx, repo.ID, "snap-1", "/", generation, `{"entries":[]}`, time.Now()); err != nil {
		t.Fatal(err)
	}

	forgetRun := &model.Run{
		ID: "cache-forget", AgentID: repo.AgentID, Operation: model.OpForget, Status: model.RunQueued,
		QueuedAt: time.Now().UTC(), RepositoryID: repo.ID,
	}
	if err := ts.CreateRun(ctx, forgetRun); err != nil {
		t.Fatal(err)
	}
	generation, err = cs.SnapshotCacheGeneration(ctx, repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.SaveSnapshotListCache(ctx, repo.ID, generation, snapshotJSON(t, list), SnapshotFingerprint(list), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.GetSnapshotTreeCache(ctx, repo.ID, "snap-1", "/"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("forget should clear tree instead of promoting it: %v", err)
	}
	if err := cs.SaveSnapshotTreeCache(ctx, repo.ID, "snap-1", "/", generation, `{"entries":[]}`, time.Now()); err != nil {
		t.Fatal(err)
	}

	if err := ts.CreateRun(ctx, forgetRun); !errors.Is(err, ErrDuplicateRun) {
		t.Fatalf("duplicate run = %v", err)
	}
	if got, err := cs.SnapshotCacheGeneration(ctx, repo.ID); err != nil || got != generation {
		t.Fatalf("duplicate run changed generation to %d, err %v", got, err)
	}
	if _, err := cs.GetSnapshotListCache(ctx, repo.ID); err != nil {
		t.Fatalf("duplicate run invalidated list cache: %v", err)
	}
	if _, err := cs.GetSnapshotTreeCache(ctx, repo.ID, "snap-1", "/"); err != nil {
		t.Fatalf("duplicate run invalidated tree cache: %v", err)
	}
}

func TestSnapshotCachePersistsAcrossReopen(t *testing.T) {
	ts, _, repo := newSnapshotCacheRepository(t)
	ctx := context.Background()
	cs := ts.Store.(SnapshotCacheStore)
	generation, err := cs.SnapshotCacheGeneration(ctx, repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	list := []model.Snapshot{{ID: "snap-1"}}
	if err := cs.SaveSnapshotListCache(ctx, repo.ID, generation, snapshotJSON(t, list), SnapshotFingerprint(list), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := cs.SaveSnapshotTreeCache(ctx, repo.ID, "snap-1", "/", generation, `{"path":"/","entries":[]}`, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := ts.Store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := New(ts.tmpDir + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	reopenedCache := reopened.(SnapshotCacheStore)
	if cached, err := reopenedCache.GetSnapshotListCache(ctx, repo.ID); err != nil || cached.Fingerprint != SnapshotFingerprint(list) {
		t.Fatalf("reopened list cache = %#v, err %v", cached, err)
	}
	if cached, err := reopenedCache.GetSnapshotTreeCache(ctx, repo.ID, "snap-1", "/"); err != nil || cached.TreeJSON == "" {
		t.Fatalf("reopened tree cache = %#v, err %v", cached, err)
	}
}
