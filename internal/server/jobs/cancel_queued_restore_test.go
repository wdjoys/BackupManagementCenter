package jobs

import (
	"context"
	"testing"
	"time"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/server/events"
	"backupmanagementcenter/internal/secrets"
)

// 排队中的恢复被取消时，run 状态必须由 dispatcher 的恢复取消路径处理：那条路径会
// 同时终结 restore_requests（phase=failed）。只改 run 会留下 phase=queued 的孤儿
// request，RepositoryRestoreBlocked 于是永久阻塞该仓库的后续任务，且 resolve 接口
// 不受理 queued 阶段（只能手工改库）。
func TestCancelRunQueuedRestoreGoesToDispatcher(t *testing.T) {
	st := newFakeStore()
	disp := newFakeDispatcher()
	seal, _ := secrets.NewSealer(fakeKey())
	o := New(st, seal, disp, events.New(), "inst-1")

	run := &model.Run{
		ID: "run-restore-queued", AgentID: "agent-1", Operation: model.OpRestore,
		Status: model.RunQueued, QueuedAt: time.Now().UTC(), ProgressJSON: "{}",
	}
	st.runs[run.ID] = run

	if err := o.CancelRun(context.Background(), run.ID); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if len(disp.cancelled) != 1 || disp.cancelled[0] != run.ID {
		t.Fatalf("排队中的恢复必须交给 dispatcher 的恢复取消路径，cancelled=%v", disp.cancelled)
	}
	got, err := st.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != model.RunQueued {
		t.Fatalf("run 状态必须由 dispatcher 的恢复路径终结，这里不得改写，got %s", got.Status)
	}
}

// 非恢复的排队 run 保持原有行为：直接置 cancelled（不必等 dispatcher）。
func TestCancelRunQueuedNonRestoreStaysLocal(t *testing.T) {
	st := newFakeStore()
	disp := newFakeDispatcher()
	seal, _ := secrets.NewSealer(fakeKey())
	o := New(st, seal, disp, events.New(), "inst-1")

	run := &model.Run{
		ID: "run-backup-queued", AgentID: "agent-1", Operation: model.OpBackup,
		Status: model.RunQueued, QueuedAt: time.Now().UTC(), ProgressJSON: "{}",
	}
	st.runs[run.ID] = run

	if err := o.CancelRun(context.Background(), run.ID); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if len(disp.cancelled) != 0 {
		t.Fatalf("非恢复 run 不应走 dispatcher，cancelled=%v", disp.cancelled)
	}
	got, _ := st.GetRun(context.Background(), run.ID)
	if got.Status != model.RunCancelled || got.ErrorCode != model.ErrCancelled {
		t.Fatalf("expected cancelled/%s, got %s/%s", model.ErrCancelled, got.Status, got.ErrorCode)
	}
}
