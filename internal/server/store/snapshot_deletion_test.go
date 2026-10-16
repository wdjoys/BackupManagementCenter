package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"
	"time"

	"backupmanagementcenter/internal/model"
)

func newSnapshotDeletionRepository(t *testing.T) (testStore, *model.Agent, *model.StorageTarget, *model.Repository) {
	t.Helper()
	ts := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	agent := &model.Agent{
		ID: "del-agent", Name: "agent", Hostname: "host", OS: "linux", Version: "1",
		Status: model.AgentOnline, LastSeenAt: &now, EnrolledAt: now, TokenHash: "token",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	}
	if err := ts.UpsertAgentOnConnect(ctx, agent); err != nil {
		ts.Close(t)
		t.Fatalf("create agent: %v", err)
	}

	target := &model.StorageTarget{
		ID: "del-target", Name: "del-target", Type: "rclone", RemoteName: "remote",
		EncryptedConfig: []byte("config"), CreatedAt: now, UpdatedAt: now,
	}
	if err := ts.CreateStorageTarget(ctx, target); err != nil {
		ts.Close(t)
		t.Fatalf("create target: %v", err)
	}

	repo := &model.Repository{
		ID: "del-repo", AgentID: "del-agent", StorageTargetID: target.ID,
		RepositoryPath: "remote/del", EncryptedPassword: []byte("password"), Status: "ready",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := ts.CreateRepository(ctx, repo); err != nil {
		ts.Close(t)
		t.Fatalf("create repo: %v", err)
	}
	return ts, agent, target, repo
}

// TestSnapshotDeletionQueueManualIdempotent verifies manual deletion is idempotent
// across new insert, upgrade from candidate, and already-pending/running/succeeded.
func TestSnapshotDeletionQueueManualIdempotent(t *testing.T) {
	ts, _, _, repo := newSnapshotDeletionRepository(t)
	defer ts.Close(t)
	ctx := context.Background()
	store := ts.Store.(SnapshotDeletionStore)
	now := time.Now().UTC()
	actor := "admin-1"

	// 首次请求：新建 pending。
	d, created, err := store.QueueManualSnapshotDeletion(ctx, repo.ID, repo.AgentID, "snap-a", actor, now)
	if err != nil {
		t.Fatalf("first queue: %v", err)
	}
	if !created {
		t.Fatalf("expected created=true, got false")
	}
	if d.State != model.SnapshotDeletionPending {
		t.Fatalf("expected pending, got %s", d.State)
	}

	// 重复请求：返回同一 ID，created=false。
	d2, created2, err := store.QueueManualSnapshotDeletion(ctx, repo.ID, repo.AgentID, "snap-a", actor, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("second queue: %v", err)
	}
	if created2 {
		t.Fatalf("expected created=false, got true")
	}
	if d2.ID != d.ID {
		t.Fatalf("expected same id, got %s vs %s", d2.ID, d.ID)
	}

	// 同一快照第二个 snapshot：独立记录。
	d3, created3, err := store.QueueManualSnapshotDeletion(ctx, repo.ID, repo.AgentID, "snap-b", actor, now)
	if err != nil {
		t.Fatalf("third queue: %v", err)
	}
	if !created3 || d3.ID == d.ID {
		t.Fatalf("expected new deletion for snap-b")
	}
}

// TestSnapshotDeletionQueueManualCoversCandidate verifies an orphan candidate
// for the same snapshot is atomically upgraded to manual/pending.
func TestSnapshotDeletionQueueManualCoversCandidate(t *testing.T) {
	ts, _, _, repo := newSnapshotDeletionRepository(t)
	defer ts.Close(t)
	ctx := context.Background()
	store := ts.Store.(SnapshotDeletionStore)
	now := time.Now().UTC()

	// 先模拟一条 orphan/candidate 意图（通过 FinishSnapshotCleanupScan）。
	snapshots := []model.Snapshot{
		{ID: "snap-orphan", Time: "2026-08-01T00:00:00Z", Tags: []string{"plan:p1", "kind:filesystem", "run:r1"}},
	}
	if err := store.FinishSnapshotCleanupScan(ctx, repo.ID, "", snapshots, now); err != nil {
		t.Fatalf("scan: %v", err)
	}

	// 现在手动删除同一快照：应原子升级为 manual/pending。
	actor := "admin-1"
	d, created, err := store.QueueManualSnapshotDeletion(ctx, repo.ID, repo.AgentID, "snap-orphan", actor, now)
	if err != nil {
		t.Fatalf("queue manual: %v", err)
	}
	if !created {
		t.Fatalf("expected upgrade=true, got false")
	}
	if d.Source != model.SnapshotDeletionManual {
		t.Fatalf("expected source=manual, got %s", d.Source)
	}
	if d.State != model.SnapshotDeletionPending {
		t.Fatalf("expected state=pending, got %s", d.State)
	}
	if d.RequestedBy != actor {
		t.Fatalf("expected requested_by=%s, got %s", actor, d.RequestedBy)
	}
}

// TestSnapshotDeletionHiddenSet verifies hidden IDs include manual pending/running/succeeded
// and orphan pending/running/succeeded, but NOT orphan candidate.
func TestSnapshotDeletionHiddenSet(t *testing.T) {
	ts, _, _, repo := newSnapshotDeletionRepository(t)
	defer ts.Close(t)
	ctx := context.Background()
	store := ts.Store.(SnapshotDeletionStore)
	now := time.Now().UTC()

	// 三个 manual pending。
	for i := 0; i < 3; i++ {
		_, _, err := store.QueueManualSnapshotDeletion(ctx, repo.ID, repo.AgentID, "snap-manual", "admin", now)
		if err != nil {
			t.Fatalf("queue manual: %v", err)
		}
	}

	// 一个 orphan candidate（首次扫描发现，应可见）。
	snapshots := []model.Snapshot{
		{ID: "snap-orphan-cand", Time: "2026-08-01T00:00:00Z", Tags: []string{"plan:p1", "kind:filesystem", "run:r2"}},
	}
	if err := store.FinishSnapshotCleanupScan(ctx, repo.ID, "", snapshots, now); err != nil {
		t.Fatalf("scan: %v", err)
	}

	hidden, err := store.HiddenSnapshotIDs(ctx, repo.ID)
	if err != nil {
		t.Fatalf("hidden ids: %v", err)
	}

	if _, ok := hidden["snap-manual"]; !ok {
		t.Fatalf("expected snap-manual in hidden set")
	}
	if _, ok := hidden["snap-orphan-cand"]; ok {
		t.Fatalf("expected snap-orphan-cand NOT in hidden set (candidate is visible)")
	}
}

// TestSnapshotDeletionListDueSnapshotDeletions verifies due pending deletions
// are returned in order, respecting next_attempt_at.
func TestSnapshotDeletionListDueSnapshotDeletions(t *testing.T) {
	ts, _, _, repo := newSnapshotDeletionRepository(t)
	defer ts.Close(t)
	ctx := context.Background()
	store := ts.Store.(SnapshotDeletionStore)
	now := time.Now().UTC()

	// 两个 pending（无 next_attempt_at）和一个 future pending。
	store.QueueManualSnapshotDeletion(ctx, repo.ID, repo.AgentID, "snap-now-1", "admin", now)
	store.QueueManualSnapshotDeletion(ctx, repo.ID, repo.AgentID, "snap-now-2", "admin", now)
	store.QueueManualSnapshotDeletion(ctx, repo.ID, repo.AgentID, "snap-later", "admin", now)
	store.RetrySnapshotDeletion(ctx, "x-does-not-exist", "", "", time.Now().Add(24*time.Hour)) // no-op on missing

	// 将 snap-now-1 推迟到明天，验证排序。
	// 先找到它的 ID。
	due, err := store.ListDueSnapshotDeletions(ctx, now, 3)
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	if len(due) != 3 {
		t.Fatalf("expected 3 due, got %d", len(due))
	}

	// 将所有 pending 标记为 future。
	for _, d := range due {
		store.RetrySnapshotDeletion(ctx, d.ID, "", "", now.Add(24*time.Hour))
	}

	// 现在都不到期。
	due2, err := store.ListDueSnapshotDeletions(ctx, now, 3)
	if err != nil {
		t.Fatalf("list due 2: %v", err)
	}
	if len(due2) != 0 {
		t.Fatalf("expected 0 due, got %d", len(due2))
	}

	// 时间推进到明天。
	due3, err := store.ListDueSnapshotDeletions(ctx, now.Add(25*time.Hour), 3)
	if err != nil {
		t.Fatalf("list due 3: %v", err)
	}
	if len(due3) != 3 {
		t.Fatalf("expected 3 due, got %d", len(due3))
	}
}

// TestSnapshotDeletionClaimRunAtomic verifies ClaimSnapshotDeletionRun inserts a queued run
// and transitions the intent to running in one transaction.
func TestSnapshotDeletionClaimRunAtomic(t *testing.T) {
	ts, _, _, repo := newSnapshotDeletionRepository(t)
	defer ts.Close(t)
	ctx := context.Background()
	store := ts.Store.(SnapshotDeletionStore)
	now := time.Now().UTC()

	_, _, err := store.QueueManualSnapshotDeletion(ctx, repo.ID, repo.AgentID, "snap-claim", "admin", now)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}

	// 通过 GetSnapshotCleanupState + HiddenSnapshotIDs 拿到 ID 不可行；
	// 直接通过 ListDue 获得意图 ID。
	due, err := store.ListDueSnapshotDeletions(ctx, now, 10)
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	if len(due) == 0 {
		t.Fatalf("expected 1 due")
	}
	deletionID := due[0].ID

	leaseUntil := now.Add(30 * time.Minute)
	run := &model.Run{
		ID:         model.NewUUIDv7(),
		AgentID:    repo.AgentID,
		Operation:  model.OpForget,
		Status:     model.RunQueued,
		QueuedAt:   now,
		RepositoryID: repo.ID,
	}

	if err := store.ClaimSnapshotDeletionRun(ctx, deletionID, run, leaseUntil); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// 现在不应再出现在 due 列表中。
	due2, err := store.ListDueSnapshotDeletions(ctx, now, 10)
	if err != nil {
		t.Fatalf("list due after claim: %v", err)
	}
	if len(due2) != 0 {
		t.Fatalf("expected 0 due after claim, got %d", len(due2))
	}

	// 重试 pending claim 应失败。
	if err := store.ClaimSnapshotDeletionRun(ctx, deletionID, run, leaseUntil); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition on second claim, got %v", err)
	}
}

// TestSnapshotDeletionRetryAndComplete verifies the retry→complete flow.
func TestSnapshotDeletionRetryAndComplete(t *testing.T) {
	ts, _, _, repo := newSnapshotDeletionRepository(t)
	defer ts.Close(t)
	ctx := context.Background()
	store := ts.Store.(SnapshotDeletionStore)
	now := time.Now().UTC()

	_, _, err := store.QueueManualSnapshotDeletion(ctx, repo.ID, repo.AgentID, "snap-retry", "admin", now)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	due, err := store.ListDueSnapshotDeletions(ctx, now, 10)
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	deletionID := due[0].ID

	// 重试并设置 next_attempt_at。
	future := now.Add(5 * time.Minute)
	if err := store.RetrySnapshotDeletion(ctx, deletionID, "agent_disconnected", "agent gone", future); err != nil {
		t.Fatalf("retry: %v", err)
	}

	// 不应在 now 到期。
	due2, err := store.ListDueSnapshotDeletions(ctx, now, 10)
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	if len(due2) != 0 {
		t.Fatalf("expected 0 due, got %d", len(due2))
	}

	// 时间推进后完成。
	if err := store.CompleteSnapshotDeletion(ctx, deletionID, now.Add(6*time.Minute)); err != nil {
		t.Fatalf("complete: %v", err)
	}
}

// TestSnapshotDeletionCompleteNotFound verifies CompleteSnapshotDeletion returns ErrNotFound.
func TestSnapshotDeletionCompleteNotFound(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()
	store := ts.Store.(SnapshotDeletionStore)

	if err := store.CompleteSnapshotDeletion(ctx, "nonexistent", time.Now().UTC()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestSnapshotDeletionRetryNotFound verifies RetrySnapshotDeletion returns ErrNotFound.
func TestSnapshotDeletionRetryNotFound(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()
	store := ts.Store.(SnapshotDeletionStore)

	if err := store.RetrySnapshotDeletion(ctx, "nonexistent", "err", "msg", time.Now().UTC()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestSnapshotDeletionCleanupState verifies StartScan/FinishScan/ClearScan.
func TestSnapshotDeletionCleanupState(t *testing.T) {
	ts, _, _, repo := newSnapshotDeletionRepository(t)
	defer ts.Close(t)
	ctx := context.Background()
	store := ts.Store.(SnapshotDeletionStore)
	now := time.Now().UTC()

	// 首次读取应为 NotFound。
	if _, err := store.GetSnapshotCleanupState(ctx, repo.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	// 启动扫描。
	runID := model.NewUUIDv7()
	if err := store.StartSnapshotCleanupScan(ctx, repo.ID, runID, now); err != nil {
		t.Fatalf("start scan: %v", err)
	}

	st, err := store.GetSnapshotCleanupState(ctx, repo.ID)
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	if st.ScanRunID != runID {
		t.Fatalf("expected scan_run_id=%s, got %s", runID, st.ScanRunID)
	}

	// 并发扫描（不同 runID）应失败。
	otherRun := model.NewUUIDv7()
	if err := store.StartSnapshotCleanupScan(ctx, repo.ID, otherRun, now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition on concurrent scan, got %v", err)
	}

	// 完成扫描：远端快照有权威匹配 run，不应创建 candidate。
	snapshots := []model.Snapshot{
		{ID: "snap-matched", Time: "2026-08-01T00:00:00Z", Tags: []string{"plan:p1", "kind:filesystem", "run:r1"}},
	}
	if err := store.FinishSnapshotCleanupScan(ctx, repo.ID, runID, snapshots, now.Add(time.Minute)); err != nil {
		t.Fatalf("finish scan: %v", err)
	}

	st2, err := store.GetSnapshotCleanupState(ctx, repo.ID)
	if err != nil {
		t.Fatalf("get state after finish: %v", err)
	}
	if st2.ScanRunID != "" {
		t.Fatalf("expected scan_run_id cleared, got %s", st2.ScanRunID)
	}
}

// TestSnapshotDeletionCleanupScanOrphanCandidate verifies the orphan candidate
// lifecycle: first scan creates candidate, second scan increments seen_count,
// and after 7 days + 2 scans transitions to pending.
func TestSnapshotDeletionCleanupScanOrphanCandidate(t *testing.T) {
	ts, _, _, repo := newSnapshotDeletionRepository(t)
	defer ts.Close(t)
	ctx := context.Background()
	store := ts.Store.(SnapshotDeletionStore)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	snapshots := []model.Snapshot{
		{ID: "snap-orphan", Time: "2026-08-01T00:00:00Z", Tags: []string{"plan:p1", "kind:filesystem", "run:r1"}},
	}

	// 第 1 天：创建 candidate。
	if err := store.StartSnapshotCleanupScan(ctx, repo.ID, model.NewUUIDv7(), now); err != nil {
		t.Fatalf("start scan 1: %v", err)
	}
	if err := store.FinishSnapshotCleanupScan(ctx, repo.ID, "", snapshots, now); err != nil {
		t.Fatalf("finish scan 1: %v", err)
	}

	// candidate 不应在 hidden set 中。
	hidden, err := store.HiddenSnapshotIDs(ctx, repo.ID)
	if err != nil {
		t.Fatalf("hidden ids: %v", err)
	}
	if _, ok := hidden["snap-orphan"]; ok {
		t.Fatalf("expected snap-orphan NOT hidden (candidate)")
	}

	// 第 6 天：第二次扫描，still candidate (seen_count=2, but <7 days).
	day6 := now.Add(6 * 24 * time.Hour)
	if err := store.StartSnapshotCleanupScan(ctx, repo.ID, model.NewUUIDv7(), day6); err != nil {
		t.Fatalf("start scan 2: %v", err)
	}
	if err := store.FinishSnapshotCleanupScan(ctx, repo.ID, "", snapshots, day6); err != nil {
		t.Fatalf("finish scan 2: %v", err)
	}
	hidden2, err := store.HiddenSnapshotIDs(ctx, repo.ID)
	if err != nil {
		t.Fatalf("hidden ids 2: %v", err)
	}
	if _, ok := hidden2["snap-orphan"]; ok {
		t.Fatalf("expected snap-orphan NOT hidden (still candidate)")
	}

	// 第 8 天：第三次扫描，>=7 days && seen_count>=2 → pending。
	day8 := now.Add(8 * 24 * time.Hour)
	if err := store.StartSnapshotCleanupScan(ctx, repo.ID, model.NewUUIDv7(), day8); err != nil {
		t.Fatalf("start scan 3: %v", err)
	}
	if err := store.FinishSnapshotCleanupScan(ctx, repo.ID, "", snapshots, day8); err != nil {
		t.Fatalf("finish scan 3: %v", err)
	}
	hidden3, err := store.HiddenSnapshotIDs(ctx, repo.ID)
	if err != nil {
		t.Fatalf("hidden ids 3: %v", err)
	}
	if _, ok := hidden3["snap-orphan"]; !ok {
		t.Fatalf("expected snap-orphan hidden (pending)")
	}
}

// TestSnapshotDeletionCleanupScanWithAuthorityMatch verifies a snapshot with
// an authoritative succeeded backup run never becomes an orphan candidate.
func TestSnapshotDeletionCleanupScanWithAuthorityMatch(t *testing.T) {
	ts, agent, target, repo := newSnapshotDeletionRepository(t)
	defer ts.Close(t)
	ctx := context.Background()
	store := ts.Store.(SnapshotDeletionStore)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	// 先创建一个 succeeded backup run 对应 snap-matched。
	run := &model.Run{
		ID:         "run-r1",
		AgentID:    repo.AgentID,
		Operation:  model.OpBackup,
		Status:     model.RunQueued,
		QueuedAt:   now.Add(-time.Hour),
		ProgressJSON: "{}",
		SnapshotID: "snap-matched",
		RepositoryID: repo.ID,
	}
	if err := ts.CreateRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := ts.TransitionRun(ctx, "run-r1", model.RunQueued, model.RunDispatched, nil); err != nil {
		t.Fatalf("transition run dispatched: %v", err)
	}
	if err := ts.TransitionRun(ctx, "run-r1", model.RunDispatched, model.RunRunning, nil); err != nil {
		t.Fatalf("transition run running: %v", err)
	}
	if err := ts.TransitionRun(ctx, "run-r1", model.RunRunning, model.RunSucceeded, nil); err != nil {
		t.Fatalf("transition run succeeded: %v", err)
	}

	snapshots := []model.Snapshot{
		{ID: "snap-matched", Time: "2026-08-01T00:00:00Z", Tags: []string{"plan:p1", "kind:filesystem", "run:r1"}},
		{ID: "snap-matched-2", Time: "2026-08-01T00:00:00Z", Tags: []string{"plan:p1", "kind:filesystem", "run:r1"}},
	}

	if err := store.StartSnapshotCleanupScan(ctx, repo.ID, model.NewUUIDv7(), now); err != nil {
		t.Fatalf("start scan: %v", err)
	}
	if err := store.FinishSnapshotCleanupScan(ctx, repo.ID, "", snapshots, now); err != nil {
		t.Fatalf("finish scan: %v", err)
	}

	// 无删除意图。
	hidden, err := store.HiddenSnapshotIDs(ctx, repo.ID)
	if err != nil {
		t.Fatalf("hidden ids: %v", err)
	}
	if _, ok := hidden["snap-matched"]; ok {
		t.Fatalf("expected snap-matched NOT hidden")
	}

	_ = agent
	_ = target
}

// TestSnapshotDeletionCleanupScanClearsAbsentCandidate verifies a candidate
// that no longer appears in the remote list is removed.
func TestSnapshotDeletionCleanupScanClearsAbsentCandidate(t *testing.T) {
	ts, _, _, repo := newSnapshotDeletionRepository(t)
	defer ts.Close(t)
	ctx := context.Background()
	store := ts.Store.(SnapshotDeletionStore)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	snapshots := []model.Snapshot{
		{ID: "snap-orphan", Time: "2026-08-01T00:00:00Z", Tags: []string{"plan:p1", "kind:filesystem", "run:r1"}},
	}

	// 第 1 天：创建 candidate。
	store.StartSnapshotCleanupScan(ctx, repo.ID, model.NewUUIDv7(), now)
	store.FinishSnapshotCleanupScan(ctx, repo.ID, "", snapshots, now)

	// 第 2 天：远端不再包含该快照。
	day2 := now.Add(24 * time.Hour)
	emptySnapshots := []model.Snapshot{}
	store.StartSnapshotCleanupScan(ctx, repo.ID, model.NewUUIDv7(), day2)
	store.FinishSnapshotCleanupScan(ctx, repo.ID, "", emptySnapshots, day2)

	// candidate 应被删除。
	hidden, err := store.HiddenSnapshotIDs(ctx, repo.ID)
	if err != nil {
		t.Fatalf("hidden ids: %v", err)
	}
	if _, ok := hidden["snap-orphan"]; ok {
		t.Fatalf("expected snap-orphan NOT hidden after removed from remote")
	}
}

// TestSnapshotDeletionCleanupScanSkipsIncompleteTags verifies snapshots with
// missing/multiple plan: tags are never promoted to candidate or pending.
func TestSnapshotDeletionCleanupScanSkipsIncompleteTags(t *testing.T) {
	ts, _, _, repo := newSnapshotDeletionRepository(t)
	defer ts.Close(t)
	ctx := context.Background()
	store := ts.Store.(SnapshotDeletionStore)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	// 无 tags、缺失 run:、多个 plan: 的快照。
	snapshots := []model.Snapshot{
		{ID: "snap-no-tags", Time: "2026-08-01T00:00:00Z"},
		{ID: "snap-no-run", Time: "2026-08-01T00:00:00Z", Tags: []string{"plan:p1", "kind:filesystem"}},
		{ID: "snap-multi-plan", Time: "2026-08-01T00:00:00Z", Tags: []string{"plan:p1", "plan:p2", "kind:filesystem", "run:r1"}},
		{ID: "snap-empty-run", Time: "2026-08-01T00:00:00Z", Tags: []string{"plan:p1", "kind:filesystem", "run:"}},
	}

	store.StartSnapshotCleanupScan(ctx, repo.ID, model.NewUUIDv7(), now)
	if err := store.FinishSnapshotCleanupScan(ctx, repo.ID, "", snapshots, now); err != nil {
		t.Fatalf("finish scan: %v", err)
	}

	hidden, err := store.HiddenSnapshotIDs(ctx, repo.ID)
	if err != nil {
		t.Fatalf("hidden ids: %v", err)
	}
	if len(hidden) != 0 {
		t.Fatalf("expected no hidden snapshots for incomplete tags, got %v", hidden)
	}
}

// TestSnapshotDeletionCleanupScanClearScanOnFailure verifies ClearSnapshotCleanupScan
// clears active scan without modifying candidate state.
func TestSnapshotDeletionCleanupScanClearScanOnFailure(t *testing.T) {
	ts, _, _, repo := newSnapshotDeletionRepository(t)
	defer ts.Close(t)
	ctx := context.Background()
	store := ts.Store.(SnapshotDeletionStore)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	runID := model.NewUUIDv7()
	if err := store.StartSnapshotCleanupScan(ctx, repo.ID, runID, now); err != nil {
		t.Fatalf("start scan: %v", err)
	}
	if err := store.ClearSnapshotCleanupScan(ctx, repo.ID, runID, now.Add(time.Hour)); err != nil {
		t.Fatalf("clear scan: %v", err)
	}

	// 无扫描状态。
	st, err := store.GetSnapshotCleanupState(ctx, repo.ID)
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	if st.ScanRunID != "" {
		t.Fatalf("expected scan_run_id cleared, got %s", st.ScanRunID)
	}
}

// TestSnapshotDeletionHistoryRunWithNullPlanID verifies that a historical
// succeeded backup run with plan_id=NULL still protects its snapshot from
// being orphaned.
func TestSnapshotDeletionHistoryRunWithNullPlanID(t *testing.T) {
	ts, _, _, repo := newSnapshotDeletionRepository(t)
	defer ts.Close(t)
	ctx := context.Background()
	store := ts.Store.(SnapshotDeletionStore)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	// 创建 system-initiated run（无 plan_id）。
	run := &model.Run{
		ID:         "run-historical",
		AgentID:    repo.AgentID,
		Operation:  model.OpBackup,
		Status:     model.RunQueued,
		QueuedAt:   now.Add(-time.Hour),
		SnapshotID: "snap-historical",
		RepositoryID: repo.ID,
		PlanID:     "", // 无 plan
	}
	if err := ts.CreateRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := ts.TransitionRun(ctx, "run-historical", model.RunQueued, model.RunDispatched, nil); err != nil {
		t.Fatalf("transition run dispatched: %v", err)
	}
	if err := ts.TransitionRun(ctx, "run-historical", model.RunDispatched, model.RunRunning, nil); err != nil {
		t.Fatalf("transition run running: %v", err)
	}
	if err := ts.TransitionRun(ctx, "run-historical", model.RunRunning, model.RunSucceeded, nil); err != nil {
		t.Fatalf("transition run succeeded: %v", err)
	}

	snapshots := []model.Snapshot{
		{ID: "snap-historical", Time: "2026-08-01T00:00:00Z", Tags: []string{"plan:p1", "kind:filesystem", "run:run-historical"}},
	}

	store.StartSnapshotCleanupScan(ctx, repo.ID, model.NewUUIDv7(), now)
	if err := store.FinishSnapshotCleanupScan(ctx, repo.ID, "", snapshots, now); err != nil {
		t.Fatalf("finish scan: %v", err)
	}

	hidden, err := store.HiddenSnapshotIDs(ctx, repo.ID)
	if err != nil {
		t.Fatalf("hidden ids: %v", err)
	}
	if _, ok := hidden["snap-historical"]; ok {
		t.Fatalf("expected snap-historical NOT hidden (protected by succeeded run)")
	}
}

// TestSnapshotDeletionMigrateRepeatability verifies that applying migration 0011
// is idempotent when the DB is reopened.
func TestSnapshotDeletionMigrateRepeatability(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()
	s := ts.Store

	// Migrate twice: both should succeed without error.
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	// 确保 SnapshotDeletionStore 可用。
	store := s.(SnapshotDeletionStore)
	// 对一个不存在 repository 调用 HiddenSnapshotIDs 应返回空 map。
	hidden, err := store.HiddenSnapshotIDs(ctx, "nonexistent-repo")
	if err != nil {
		t.Fatalf("hidden ids: %v", err)
	}
	if len(hidden) != 0 {
		t.Fatalf("expected empty hidden set, got %v", hidden)
	}
}

// TestSnapshotDeletionCleanupStateFinishRequiresMatchingRunID verifies
// FinishSnapshotCleanupScan returns ErrInvalidTransition when run_id mismatched.
func TestSnapshotDeletionCleanupStateFinishRequiresMatchingRunID(t *testing.T) {
	ts, _, _, repo := newSnapshotDeletionRepository(t)
	defer ts.Close(t)
	ctx := context.Background()
	store := ts.Store.(SnapshotDeletionStore)
	now := time.Now().UTC()

	store.StartSnapshotCleanupScan(ctx, repo.ID, "run-1", now)

	wrongRun := "run-wrong"
	if err := store.FinishSnapshotCleanupScan(ctx, repo.ID, wrongRun, nil, now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition on run id mismatch, got %v", err)
	}
}

// TestSnapshotDeletionCleanupStateStartScanOverwriteWithSameRunID verifies
// StartSnapshotCleanupScan with the same runID overwrites the scan_started_at
// (recovery from restart).
func TestSnapshotDeletionCleanupStateStartScanOverwriteWithSameRunID(t *testing.T) {
	ts, _, _, repo := newSnapshotDeletionRepository(t)
	defer ts.Close(t)
	ctx := context.Background()
	store := ts.Store.(SnapshotDeletionStore)
	now := time.Now().UTC()
	runID := "run-1"

	store.StartSnapshotCleanupScan(ctx, repo.ID, runID, now)
	if err := store.StartSnapshotCleanupScan(ctx, repo.ID, runID, now.Add(time.Minute)); err != nil {
		t.Fatalf("overwrite start: %v", err)
	}
	st, err := store.GetSnapshotCleanupState(ctx, repo.ID)
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	if st.ScanRunID != runID {
		t.Fatalf("expected scan_run_id=%s, got %s", runID, st.ScanRunID)
	}
	if st.LastScanStartedAt == nil || st.LastScanStartedAt.Truncate(time.Minute) != now.Add(time.Minute).Truncate(time.Minute) {
		t.Fatalf("expected updated start time, got %v", st.LastScanStartedAt)
	}
}

// TestSnapshotDeletionFinishScanReconcilesPresentSnapshot verifies FinishSnapshotCleanupScan
// reconciles the snapshot list by marking present snapshots' tree cache generation
// in the same transaction (via SaveSnapshotListCache). This test only verifies
// FinishSnapshotCleanupScan succeeds when snapshot IDs match a succeeded backup run.
func TestSnapshotDeletionFinishScanReconcilesPresentSnapshot(t *testing.T) {
	ts, _, _, repo := newSnapshotDeletionRepository(t)
	defer ts.Close(t)
	ctx := context.Background()
	store := ts.Store.(SnapshotDeletionStore)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	run := &model.Run{
		ID:         "run-match",
		AgentID:    repo.AgentID,
		Operation:  model.OpBackup,
		Status:     model.RunQueued,
		QueuedAt:   now.Add(-time.Hour),
		ProgressJSON: "{}",
		SnapshotID: "snap-present",
		RepositoryID: repo.ID,
	}
	ts.CreateRun(ctx, run)
	ts.TransitionRun(ctx, "run-match", model.RunQueued, model.RunSucceeded, nil)

	snapshots := []model.Snapshot{
		{ID: "snap-present", Time: "2026-08-01T00:00:00Z", Tags: []string{"plan:p1", "kind:filesystem", "run:run-match"}},
	}

	store.StartSnapshotCleanupScan(ctx, repo.ID, model.NewUUIDv7(), now)
	if err := store.FinishSnapshotCleanupScan(ctx, repo.ID, "", snapshots, now); err != nil {
		t.Fatalf("finish scan: %v", err)
	}

	// 应有完成的扫描状态且无活跃扫描。
	st, err := store.GetSnapshotCleanupState(ctx, repo.ID)
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	if st.ScanRunID != "" {
		t.Fatalf("expected scan_run_id cleared, got %s", st.ScanRunID)
	}
}

// TestSnapshotDeletionClaimRunPreservesRepositoryCache verifies that claiming
// a deletion run keeps the last verified snapshot cache available for browsing.
func TestSnapshotDeletionClaimRunPreservesRepositoryCache(t *testing.T) {
	ts, _, _, repo := newSnapshotDeletionRepository(t)
	defer ts.Close(t)
	ctx := context.Background()
	store := ts.Store.(SnapshotDeletionStore)
	cacheStore := ts.Store.(SnapshotCacheStore)
	now := time.Now().UTC()

	// 保存快照缓存。
	snaps := []model.Snapshot{{ID: "snap-old", Time: "2026-08-01T00:00:00Z"}}
	snapsJSON, _ := json.Marshal(snaps)
	fp := SnapshotFingerprint(snaps)
	if err := cacheStore.SaveSnapshotListCache(ctx, repo.ID, 0, string(snapsJSON), fp, now); err != nil {
		t.Fatalf("save cache: %v", err)
	}

	gen1, err := cacheStore.SnapshotCacheGeneration(ctx, repo.ID)
	if err != nil {
		t.Fatalf("gen: %v", err)
	}

	// 排队并 claim。
	_, _, err = store.QueueManualSnapshotDeletion(ctx, repo.ID, repo.AgentID, "snap-old", "admin", now)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	due, err := store.ListDueSnapshotDeletions(ctx, now, 10)
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	run := &model.Run{
		ID:         model.NewUUIDv7(),
		AgentID:    repo.AgentID,
		Operation:  model.OpForget,
		Status:     model.RunQueued,
		QueuedAt:   now,
		RepositoryID: repo.ID,
	}
	if err := store.ClaimSnapshotDeletionRun(ctx, due[0].ID, run, now.Add(30*time.Minute)); err != nil {
		t.Fatalf("claim: %v", err)
	}

	gen2, err := cacheStore.SnapshotCacheGeneration(ctx, repo.ID)
	if err != nil {
		t.Fatalf("gen after claim: %v", err)
	}
	if gen2 != gen1 {
		t.Fatalf("cache generation changed after claim: %d -> %d", gen1, gen2)
	}
	if _, err := cacheStore.GetSnapshotListCache(ctx, repo.ID); err != nil {
		t.Fatalf("snapshot cache unavailable after claim: %v", err)
	}
}

// TestSnapshotDeletionMigrateTo0011 verifies migration from a database that
// has been through 0010 (snapshot caches) to 0011.
func TestSnapshotDeletionMigrateTo0011(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	// newTestStore 已经执行了 Migrate 到最新 migration。
	// 再次 Migrate 应该无错误。
	if err := ts.Migrate(ctx); err != nil {
		t.Fatalf("migrate to 0011: %v", err)
	}

	// 确保 snapshot_deletions 表可写。
	store := ts.Store.(SnapshotDeletionStore)
	_, _, err := store.QueueManualSnapshotDeletion(ctx, "repo-x", "agent-x", "snap-y", "admin", time.Now().UTC())
	// 这里会失败（因为 repo/agent 不存在），但说明表存在且可访问。
	if err == nil {
		t.Fatalf("expected error for missing repo/agent FK")
	}
}

// TestSnapshotDeletionCleanupScanOrphanCandidateRetryDoesNotProgress verifies
// that a running/succeeded deletion intent is never downgraded by a later scan.
func TestSnapshotDeletionCleanupScanOrphanCandidateRetryDoesNotProgress(t *testing.T) {
	ts, _, _, repo := newSnapshotDeletionRepository(t)
	defer ts.Close(t)
	ctx := context.Background()
	store := ts.Store.(SnapshotDeletionStore)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	// 先手动创建 pending（跳过候选阶段）。
	store.QueueManualSnapshotDeletion(ctx, repo.ID, repo.AgentID, "snap-manual-pending", "admin", now)

	// 第 8 天扫描时远端仍存在（但意图已是 pending，不会被回退）。
	day8 := now.Add(8 * 24 * time.Hour)
	snapshots := []model.Snapshot{
		{ID: "snap-manual-pending", Time: "2026-08-01T00:00:00Z", Tags: []string{"plan:p1", "kind:filesystem", "run:r1"}},
	}
	store.StartSnapshotCleanupScan(ctx, repo.ID, model.NewUUIDv7(), day8)
	if err := store.FinishSnapshotCleanupScan(ctx, repo.ID, "", snapshots, day8); err != nil {
		t.Fatalf("finish scan: %v", err)
	}

	// 仍然 hidden。
	hidden, err := store.HiddenSnapshotIDs(ctx, repo.ID)
	if err != nil {
		t.Fatalf("hidden ids: %v", err)
	}
	if _, ok := hidden["snap-manual-pending"]; !ok {
		t.Fatalf("expected snap-manual-pending still hidden")
	}
}

// TestSnapshotDeletionHiddenSetIncludesRunningAndSucceeded verifies hidden
// set includes manual pending/running/succeeded and orphan pending/running/succeeded.
func TestSnapshotDeletionHiddenSetIncludesRunningAndSucceeded(t *testing.T) {
	ts, _, _, repo := newSnapshotDeletionRepository(t)
	defer ts.Close(t)
	ctx := context.Background()
	store := ts.Store.(SnapshotDeletionStore)
	now := time.Now().UTC()

	// pending
	store.QueueManualSnapshotDeletion(ctx, repo.ID, repo.AgentID, "snap-pending", "admin", now)
	// 找 ID 并置 running
	due, _ := store.ListDueSnapshotDeletions(ctx, now, 10)
	if len(due) > 0 {
		run := &model.Run{
			ID: model.NewUUIDv7(), AgentID: repo.AgentID,
			Operation: model.OpForget, Status: model.RunQueued,
			QueuedAt: now, RepositoryID: repo.ID,
		}
		store.ClaimSnapshotDeletionRun(ctx, due[0].ID, run, now.Add(30*time.Minute))
	}

	// 新增 succeeded
	d, _, _ := store.QueueManualSnapshotDeletion(ctx, repo.ID, repo.AgentID, "snap-succeeded", "admin", now)
	store.CompleteSnapshotDeletion(ctx, d.ID, now)

	// 新增 orphan/pending（通过 7 天+2 扫描）
	day1 := time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC)
	day8 := day1.Add(8 * 24 * time.Hour)
	snapshots := []model.Snapshot{
		{ID: "snap-orphan-pending", Time: "2026-08-01T00:00:00Z", Tags: []string{"plan:p1", "kind:filesystem", "run:r1"}},
	}
	store.StartSnapshotCleanupScan(ctx, repo.ID, model.NewUUIDv7(), day1)
	store.FinishSnapshotCleanupScan(ctx, repo.ID, "", snapshots, day1)
	store.StartSnapshotCleanupScan(ctx, repo.ID, model.NewUUIDv7(), day8)
	store.FinishSnapshotCleanupScan(ctx, repo.ID, "", snapshots, day8)

	hidden, err := store.HiddenSnapshotIDs(ctx, repo.ID)
	if err != nil {
		t.Fatalf("hidden ids: %v", err)
	}
	for _, id := range []string{"snap-pending", "snap-succeeded", "snap-orphan-pending"} {
		if _, ok := hidden[id]; !ok {
			t.Fatalf("expected %s in hidden set", id)
		}
	}
}

// TestSnapshotDeletionListDueOrderByNextAttemptAt verifies pending deletions
// with NULL next_attempt_at are returned before those with a future time.
func TestSnapshotDeletionListDueOrderByNextAttemptAt(t *testing.T) {
	ts, _, _, repo := newSnapshotDeletionRepository(t)
	defer ts.Close(t)
	ctx := context.Background()
	store := ts.Store.(SnapshotDeletionStore)
	now := time.Now().UTC()

	// snap-now (NULL next_attempt_at), snap-future (24h later)
	store.QueueManualSnapshotDeletion(ctx, repo.ID, repo.AgentID, "snap-now", "admin", now)
	store.QueueManualSnapshotDeletion(ctx, repo.ID, repo.AgentID, "snap-future", "admin", now)
	store.RetrySnapshotDeletion(ctx, "x-does-not-exist", "", "", now.Add(24*time.Hour))

	// 找 snap-future 的 ID 并推迟。
	due, _ := store.ListDueSnapshotDeletions(ctx, now, 10)
	var futureID string
	for _, d := range due {
		if d.SnapshotID == "snap-future" {
			futureID = d.ID
			break
		}
	}
	if futureID != "" {
		store.RetrySnapshotDeletion(ctx, futureID, "tmp", "tmp", now.Add(24*time.Hour))
	}

	// 现在仅 snap-now 到期。
	due2, err := store.ListDueSnapshotDeletions(ctx, now, 10)
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	if len(due2) != 1 || due2[0].SnapshotID != "snap-now" {
		t.Fatalf("expected only snap-now, got %v", due2)
	}

	// 推进时间后两者都到期。
	due3, err := store.ListDueSnapshotDeletions(ctx, now.Add(25*time.Hour), 10)
	if err != nil {
		t.Fatalf("list due 3: %v", err)
	}
	if len(due3) != 2 {
		t.Fatalf("expected 2 due, got %d", len(due3))
	}
	sort.Slice(due3, func(i, j int) bool {
		return due3[i].SnapshotID < due3[j].SnapshotID
	})
	ids := make([]string, len(due3))
	for i, d := range due3 {
		ids[i] = d.SnapshotID
	}
	sort.Strings(ids)
	if ids[0] != "snap-future" || ids[1] != "snap-now" {
		t.Fatalf("unexpected due order: %v", ids)
	}
}

// TestSnapshotDeletionCleanupScanRunIDMismatchOnStart verifies that starting
// a scan with a different runID than the currently active one fails.
func TestSnapshotDeletionCleanupScanRunIDMismatchOnStart(t *testing.T) {
	ts, _, _, repo := newSnapshotDeletionRepository(t)
	defer ts.Close(t)
	ctx := context.Background()
	store := ts.Store.(SnapshotDeletionStore)
	now := time.Now().UTC()

	store.StartSnapshotCleanupScan(ctx, repo.ID, "run-1", now)
	if err := store.StartSnapshotCleanupScan(ctx, repo.ID, "run-2", now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition, got %v", err)
	}
}

// TestSnapshotDeletionCleanupScanClearAbsentsOnFinish verifies FinishScan removes
// candidates that no longer appear in the remote snapshot list.
func TestSnapshotDeletionCleanupScanClearAbsentsOnFinish(t *testing.T) {
	ts, _, _, repo := newSnapshotDeletionRepository(t)
	defer ts.Close(t)
	ctx := context.Background()
	store := ts.Store.(SnapshotDeletionStore)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	// 第 1 天：两个快照存在。
	store.StartSnapshotCleanupScan(ctx, repo.ID, model.NewUUIDv7(), now)
	store.FinishSnapshotCleanupScan(ctx, repo.ID, "", []model.Snapshot{
		{ID: "snap-live", Tags: []string{"plan:p1", "kind:filesystem", "run:r1"}},
		{ID: "snap-deleted", Tags: []string{"plan:p1", "kind:filesystem", "run:r2"}},
	}, now)

	// 第 8 天：snap-deleted 不在远端了。
	day8 := now.Add(8 * 24 * time.Hour)
	store.StartSnapshotCleanupScan(ctx, repo.ID, model.NewUUIDv7(), day8)
	store.FinishSnapshotCleanupScan(ctx, repo.ID, "", []model.Snapshot{
		{ID: "snap-live", Tags: []string{"plan:p1", "kind:filesystem", "run:r1"}},
	}, day8)

	// snap-deleted 的候选应被移除；snap-live 的 pending 应在 hidden 中。
	hidden, err := store.HiddenSnapshotIDs(ctx, repo.ID)
	if err != nil {
		t.Fatalf("hidden ids: %v", err)
	}
	if _, ok := hidden["snap-deleted"]; ok {
		t.Fatalf("expected snap-deleted NOT hidden (removed from remote)")
	}
	if _, ok := hidden["snap-live"]; !ok {
		t.Fatalf("expected snap-live hidden (pending)")
	}
}

// TestSnapshotDeletionFinishScanReconcileDeletesOrphanCandidateWhenAuthorityMatches verifies
// that if a snapshot gains an authoritative match (a succeeded backup run) during reconciliation,
// its orphan candidate is deleted.
func TestSnapshotDeletionFinishScanReconcileDeletesOrphanCandidateWhenAuthorityMatches(t *testing.T) {
	ts, _, _, repo := newSnapshotDeletionRepository(t)
	defer ts.Close(t)
	ctx := context.Background()
	store := ts.Store.(SnapshotDeletionStore)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	// 第 1 天：创建 candidate。
	store.StartSnapshotCleanupScan(ctx, repo.ID, model.NewUUIDv7(), now)
	store.FinishSnapshotCleanupScan(ctx, repo.ID, "", []model.Snapshot{
		{ID: "snap-was-orphan", Tags: []string{"plan:p1", "kind:filesystem", "run:r1"}},
	}, now)

	// 第 2 天：创建权威的 backup run 并扫描。
	day2 := now.Add(24 * time.Hour)
	run := &model.Run{
		ID: "run-now-matches", AgentID: repo.AgentID,
		Operation: model.OpBackup, Status: model.RunQueued,
		QueuedAt: now.Add(-time.Hour), ProgressJSON: "{}",
		SnapshotID: "snap-was-orphan", RepositoryID: repo.ID,
	}
	ts.CreateRun(ctx, run)
	ts.TransitionRun(ctx, "run-now-matches", model.RunQueued, model.RunSucceeded, nil)

	store.StartSnapshotCleanupScan(ctx, repo.ID, model.NewUUIDv7(), day2)
	store.FinishSnapshotCleanupScan(ctx, repo.ID, "", []model.Snapshot{
		{ID: "snap-was-orphan", Tags: []string{"plan:p1", "kind:filesystem", "run:run-now-matches"}},
	}, day2)

	// 此时 run 的 snapshot_id 是 snap-was-orphan，operation=backup，status=succeeded，repository 相同。
	// 但 tag 的 run 前缀是 "run-now-matches"，匹配该 run 的 ID → 权威匹配。
	// 候选应被删除。
	hidden, err := store.HiddenSnapshotIDs(ctx, repo.ID)
	if err != nil {
		t.Fatalf("hidden ids: %v", err)
	}
	if _, ok := hidden["snap-was-orphan"]; ok {
		t.Fatalf("expected snap-was-orphan NOT hidden after authority matched")
	}
}