package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"backupmanagementcenter/internal/model"
)

func newRestoreStore(t *testing.T) Store {
	t.Helper()
	st, err := New(t.TempDir() + "/restore.db")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	now := time.Now().UTC()
	if err := st.CreateStorageTarget(context.Background(), &model.StorageTarget{
		ID: "tgt-1", Name: "t1", Type: "rclone", RemoteName: "r", RemotePath: "/p",
		EncryptedConfig: []byte("c"), CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"agent-1", "agent-2"} {
		if err := st.UpsertAgentOnConnect(context.Background(), &model.Agent{
			ID: id, Name: id, Hostname: id, OS: "linux", Arch: "amd64", Version: "v1",
			Status: model.AgentOnline, EnrolledAt: now, TokenHash: "hash-" + id,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func restoreRunAndRequest(id, repoID, kind, phase, dedup string) (*model.Run, *model.RestoreRequest) {
	run := &model.Run{
		ID: id, AgentID: "agent-2", Operation: model.OpRestore, Status: model.RunQueued,
		QueuedAt: time.Now().UTC(), RepositoryID: repoID, ProgressJSON: "{}", DedupKey: dedup,
	}
	rr := &model.RestoreRequest{
		ID: "rr-" + id, RunID: id, SnapshotID: "snap-1", RestoreKind: kind,
		Target: model.RestoreTarget{Database: "app"}, Phase: phase, CreatedAt: time.Now().UTC(),
	}
	return run, rr
}

func createRepo(t *testing.T, st Store, id, agentID string) {
	t.Helper()
	now := time.Now().UTC()
	if err := st.CreateRepository(context.Background(), &model.Repository{
		ID: id, AgentID: agentID, StorageTargetID: "tgt-1", RepositoryPath: "r:/" + id,
		EncryptedPassword: []byte("p"), Status: "ready", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

// 占用检查与 run/request 创建在同一写事务内：第二个数据库恢复必须拿到 409 语义错误，
// 而相同 dedup 键复用原任务。
func TestCreateDatabaseRestoreRunEnforcesOccupancy(t *testing.T) {
	ctx := context.Background()
	st := newRestoreStore(t)
	createRepo(t, st, "repo-1", "agent-1")

	run, rr := restoreRunAndRequest("run-1", "repo-1", model.KindPostgreSQL, model.RestorePhaseQueued, "dedup-1")
	if err := st.CreateDatabaseRestoreRun(ctx, run, rr); err != nil {
		t.Fatalf("first CreateDatabaseRestoreRun: %v", err)
	}

	other, otherRR := restoreRunAndRequest("run-2", "repo-1", model.KindMySQL, model.RestorePhaseQueued, "dedup-2")
	if err := st.CreateDatabaseRestoreRun(ctx, other, otherRR); !errors.Is(err, ErrDatabaseRestoreBusy) {
		t.Fatalf("expected ErrDatabaseRestoreBusy, got %v", err)
	}

	dup, dupRR := restoreRunAndRequest("run-3", "repo-1", model.KindPostgreSQL, model.RestorePhaseQueued, "dedup-1")
	if err := st.CreateDatabaseRestoreRun(ctx, dup, dupRR); !errors.Is(err, ErrDuplicateRun) {
		t.Fatalf("expected ErrDuplicateRun for an equivalent task, got %v", err)
	}

	active, err := st.ActiveDatabaseRestoreRunID(ctx)
	if err != nil || active != "run-1" {
		t.Fatalf("expected run-1 to hold the occupancy, got %q err=%v", active, err)
	}
	blocked, err := st.RepositoryRestoreBlocked(ctx, "repo-1", "")
	if err != nil || !blocked {
		t.Fatalf("expected the repository to be blocked, got %v err=%v", blocked, err)
	}
	// 该 run 自身不受阻塞。
	if blocked, err := st.RepositoryRestoreBlocked(ctx, "repo-1", "run-1"); err != nil || blocked {
		t.Fatalf("a run must not block itself, got %v err=%v", blocked, err)
	}

	// 安全终态释放占用与阻塞。
	if err := st.FinishRestoreRun(ctx, FinishRestoreRunInput{
		RunID: "run-1", ToStatus: model.RunSucceeded, Phase: model.RestorePhaseSucceeded,
		FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("FinishRestoreRun: %v", err)
	}
	if active, err := st.ActiveDatabaseRestoreRunID(ctx); err != nil || active != "" {
		t.Fatalf("expected the occupancy to be released, got %q err=%v", active, err)
	}
	if blocked, err := st.RepositoryRestoreBlocked(ctx, "repo-1", ""); err != nil || blocked {
		t.Fatalf("expected the repository to be unblocked, got %v err=%v", blocked, err)
	}
}

// 不安全的终态（回滚失败/人工恢复）必须继续阻塞，直到人工解除。
func TestUnsafeRestorePhaseKeepsOccupancyUntilResolved(t *testing.T) {
	ctx := context.Background()
	st := newRestoreStore(t)
	createRepo(t, st, "repo-1", "agent-1")

	run, rr := restoreRunAndRequest("run-1", "repo-1", model.KindPostgreSQL, model.RestorePhaseQueued, "dedup-1")
	if err := st.CreateDatabaseRestoreRun(ctx, run, rr); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishRestoreRun(ctx, FinishRestoreRunInput{
		RunID: "run-1", ToStatus: model.RunFailed, Phase: model.RestorePhaseRollbackFailed,
		ErrorCode: model.ErrRollbackFailed, FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if active, _ := st.ActiveDatabaseRestoreRunID(ctx); active != "run-1" {
		t.Fatalf("rollback_failed must keep the occupancy, got %q", active)
	}
	got, err := st.GetRestoreRequestByRunID(ctx, "run-1")
	if err != nil || got.Phase != model.RestorePhaseRollbackFailed {
		t.Fatalf("unexpected request state: %+v err=%v", got, err)
	}

	// 迟到的进度/结果不得把不安全终态改写回中间态。
	phases, ok := st.(interface {
		UpdateRestorePhase(context.Context, string, string) error
	})
	if !ok {
		t.Fatal("store does not implement UpdateRestorePhase")
	}
	if err := phases.UpdateRestorePhase(ctx, "run-1", model.RestorePhaseRestoring); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetRestoreRequestByRunID(ctx, "run-1")
	if got.Phase != model.RestorePhaseRollbackFailed {
		t.Fatalf("terminal phase must not be overwritten, got %q", got.Phase)
	}

	// 人工解除后释放占用并留下审计。
	if err := st.ResolveRestoreRequest(ctx, "rr-run-1", "run-1", "admin-1", "operator verified target", time.Now().UTC()); err != nil {
		t.Fatalf("ResolveRestoreRequest: %v", err)
	}
	if active, _ := st.ActiveDatabaseRestoreRunID(ctx); active != "" {
		t.Fatalf("manual resolution must release the occupancy, got %q", active)
	}
	events, err := st.ListAuditEvents(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Action == "restore.resolve" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected an audit event for the manual resolution")
	}
	// 重复解除必须冲突。
	if err := st.ResolveRestoreRequest(ctx, "rr-run-1", "run-1", "admin-1", "again", time.Now().UTC()); !errors.Is(err, ErrRestoreConflict) {
		t.Fatalf("expected ErrRestoreConflict on a second resolve, got %v", err)
	}
}

// 受保护快照（引用或标签）在删除入队时被拒绝；安全终态后引用消失。
func TestProtectedRestoreSnapshotIDsLifecycle(t *testing.T) {
	ctx := context.Background()
	st := newRestoreStore(t)
	createRepo(t, st, "repo-1", "agent-1")

	run, rr := restoreRunAndRequest("run-1", "repo-1", model.KindPostgreSQL, model.RestorePhaseRestoring, "dedup-1")
	rr.RollbackSnapshotID = "prot-snap"
	if err := st.CreateDatabaseRestoreRun(ctx, run, rr); err != nil {
		t.Fatal(err)
	}
	ids, err := st.ProtectedRestoreSnapshotIDs(ctx, "repo-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ids["prot-snap"]; !ok {
		t.Fatalf("expected prot-snap to be protected, got %v", ids)
	}
	// 进度补全不覆盖已有的非空 ID。
	if err := st.UpdateRestoreRollbackSnapshot(ctx, "run-1", "other-snap"); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetRestoreRequestByRunID(ctx, "run-1")
	if got.RollbackSnapshotID != "prot-snap" {
		t.Fatalf("existing rollback snapshot must win, got %q", got.RollbackSnapshotID)
	}

	if err := st.FinishRestoreRun(ctx, FinishRestoreRunInput{
		RunID: "run-1", ToStatus: model.RunSucceeded, Phase: model.RestorePhaseSucceeded,
		FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	ids, _ = st.ProtectedRestoreSnapshotIDs(ctx, "repo-1")
	if len(ids) != 0 {
		t.Fatalf("a safe terminal phase must release snapshot protection, got %v", ids)
	}
}
