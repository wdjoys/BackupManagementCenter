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

type recordingRunNotifier struct{ ids []string }

func (r *recordingRunNotifier) NotifyPlanFailure(_ context.Context, runID string) error {
	r.ids = append(r.ids, runID)
	return nil
}

// 服务端重启时在跑的恢复，run 与 restore_requests.phase 必须一起推进到安全终态：
// 只置 run failed 会让请求永久停在中间态（restoring），resolve 以 restore_conflict
// 拒绝，而中间态不是安全相位 → 全局数据库恢复占用被一直占住且重启不自愈。
func TestRecoverStaleRunsAdvancesRestoreRequestPhase(t *testing.T) {
	tempDir := t.TempDir()
	seal := secrets.NewNoopSealer()
	st, err := store.NewWithSealer(filepath.Join(tempDir, "bmc.db"), seal)
	if err != nil {
		t.Fatalf("NewWithSealer: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	started := time.Now().UTC().Add(-time.Minute)
	run := &model.Run{
		ID: "run-restore", AgentID: "agent-1", Operation: model.OpRestore,
		Status: model.RunRunning, QueuedAt: started, StartedAt: &started, RepositoryID: "repo-1",
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	req := &model.RestoreRequest{
		ID: "rr-1", RunID: "run-restore", SnapshotID: "snap-1",
		RestoreKind: model.KindMySQL, Phase: model.RestorePhaseRestoring, CreatedAt: started,
	}
	if err := st.CreateRestoreRequest(ctx, req); err != nil {
		t.Fatalf("CreateRestoreRequest: %v", err)
	}

	notifier := &recordingRunNotifier{}
	recoverStaleRuns(ctx, st, notifier)

	got, err := st.GetRun(ctx, "run-restore")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != model.RunFailed || got.ErrorCode != model.ErrAgentDisconnected {
		t.Fatalf("run 应被置为 agent_disconnected 失败，实际 %s/%s", got.Status, got.ErrorCode)
	}
	rr, err := st.GetRestoreRequest(ctx, "rr-1")
	if err != nil {
		t.Fatalf("GetRestoreRequest: %v", err)
	}
	if rr.Phase != model.RestorePhaseManualRecoveryNeeded {
		t.Fatalf("恢复请求相位应推进为 %s（否则 resolve 会 409、全局占用锁死），实际 %s",
			model.RestorePhaseManualRecoveryNeeded, rr.Phase)
	}
	if len(notifier.ids) != 1 || notifier.ids[0] != "run-restore" {
		t.Fatalf("应通知一次计划失败，实际 %v", notifier.ids)
	}
}
