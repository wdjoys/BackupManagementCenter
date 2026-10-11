package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"backupmanagementcenter/internal/model"
)

// 取消是尽力而为：运行被乐观写成 cancelled 后，Agent 仍可能上报 succeeded（备份在
// 取消送达前已提交快照）。ReconcileCancelledRunSuccess 必须把这种迟到的成功结果收敛
// 成 succeeded 并记录快照，否则留下"cancelled 但仓库里已有该次运行的快照、snapshot_id
// 为空"的终态不一致。
func TestReconcileCancelledRunSuccess(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	run := &model.Run{
		ID: "run-cancel-reconcile", AgentID: "a1", Operation: model.OpBackup,
		Status: model.RunCancelled, QueuedAt: now, FinishedAt: &now,
		ErrorCode: model.ErrCancelled, ProgressJSON: "{}",
	}
	if err := ts.Store.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if err := ts.Store.(*sqliteStore).ReconcileCancelledRunSuccess(ctx, run.ID, "snap-1", `{"phase":"done","percent":100}`); err != nil {
		t.Fatalf("ReconcileCancelledRunSuccess: %v", err)
	}
	got, err := ts.Store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != model.RunSucceeded {
		t.Fatalf("status = %q, want succeeded", got.Status)
	}
	if got.SnapshotID != "snap-1" {
		t.Fatalf("snapshot_id = %q, want snap-1", got.SnapshotID)
	}
	if got.ErrorCode != "" {
		t.Fatalf("error_code = %q, want empty (成功终态不应保留取消错误码)", got.ErrorCode)
	}
	if got.ProgressJSON != `{"phase":"done","percent":100}` {
		t.Fatalf("progress = %q, want the agent result payload", got.ProgressJSON)
	}

	// 再次修正必须是无操作：终态不可反复改写（也不重复计入终态指标）。
	if err := ts.Store.(*sqliteStore).ReconcileCancelledRunSuccess(ctx, run.ID, "snap-2", "{}"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("second reconcile = %v, want ErrInvalidTransition", err)
	}
	got, _ = ts.Store.GetRun(ctx, run.ID)
	if got.SnapshotID != "snap-1" {
		t.Fatalf("snapshot_id = %q, want snap-1 (第二次修正不得改写)", got.SnapshotID)
	}
}

// 非 cancelled 的运行绝不能被这条路径改写：运行失败（或被看门狗判定超时）时，
// 迟到的结果不能把 failed 悄悄变成 succeeded。
func TestReconcileCancelledRunSuccessOnlyTouchesCancelled(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()

	finished := time.Now().UTC()
	run := &model.Run{
		ID: "run-failed", AgentID: "a1", Operation: model.OpBackup,
		Status: model.RunFailed, QueuedAt: now, FinishedAt: &finished,
		ErrorCode: model.ErrTimeout, ProgressJSON: "{}",
	}
	if err := ts.Store.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := ts.Store.(*sqliteStore).ReconcileCancelledRunSuccess(ctx, run.ID, "snap-9", "{}"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("reconcile on failed run = %v, want ErrInvalidTransition", err)
	}
	got, err := ts.Store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != model.RunFailed || got.SnapshotID != "" {
		t.Fatalf("failed run was rewritten: status=%q snapshot=%q", got.Status, got.SnapshotID)
	}
}
