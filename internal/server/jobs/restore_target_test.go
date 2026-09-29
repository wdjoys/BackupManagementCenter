package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/secrets"
	"backupmanagementcenter/internal/server/events"
	"backupmanagementcenter/internal/server/store"
)

func newRestoreEnv(t *testing.T) (*fakeStore, *fakeDispatcher, *Orchestrator, *model.Repository) {
	t.Helper()
	st := newFakeStore()
	disp := newFakeDispatcher()
	seal, err := secrets.NewSealer(fakeKey())
	if err != nil {
		t.Fatal(err)
	}
	o := New(st, seal, disp, events.New(), "inst-1")
	agent := testAgent()
	st.agents[agent.ID] = agent
	target := testTarget(seal)
	if err := st.CreateStorageTarget(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	repo := testRepo(seal, st, target)
	return st, disp, o, repo
}

func dbRestoreInput(repoID, dbName string) RestoreInput {
	return RestoreInput{
		RepositoryID: repoID,
		SnapshotID:   "snap-1",
		RestoreKind:  model.KindPostgreSQL,
		Target:       model.RestoreTarget{Host: "db", Port: 5432, Username: "pg", Database: dbName},
		Overwrite:    true,
		Confirmation: secrets.HashToken(dbName),
	}
}

// 来源离线但存在已验证缓存时，跨 Agent 恢复必须以目标 Agent 入队，
// 且命令里的仓库路径与凭据仍来自来源 repository。
func TestCrossAgentRestoreBindsTargetAgentAndKeepsSourceRepo(t *testing.T) {
	st, disp, o, repo := newRestoreEnv(t)
	ctx := context.Background()

	targetAgent := testAgent()
	targetAgent.ID = "agent-2"
	targetAgent.Name = "target"
	st.agents[targetAgent.ID] = targetAgent
	allowRestore(st, disp, o, repo, restoreSnapshot("snap-1", model.KindPostgreSQL))
	disp.mu.Lock()
	disp.connected["agent-2"] = true
	disp.mu.Unlock()

	in := dbRestoreInput(repo.ID, "app")
	in.TargetAgentID = "agent-2"
	rr, run, err := o.StartRestore(ctx, "admin-1", in)
	if err != nil {
		t.Fatalf("StartRestore: %v", err)
	}
	if run.AgentID != "agent-2" {
		t.Fatalf("expected run bound to target agent, got %q", run.AgentID)
	}
	if enq := disp.Enqueued(); len(enq) != 1 || enq[0] != run.ID {
		t.Fatalf("expected the run to be enqueued once, got %v", enq)
	}

	_, cmd, err := o.BuildCommand(ctx, run.ID)
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	if cmd.Secrets.ResticPassword != "repo-secret-123" || cmd.Secrets.RcloneConf != "dummy-conf" {
		t.Fatalf("expected source repository credentials, got %+v", cmd.Secrets)
	}
	if rr.Phase != model.RestorePhaseQueued {
		t.Fatalf("expected queued phase, got %q", rr.Phase)
	}
	_ = targetAgent
}

// 目标 Agent 离线、被撤销或来源仓库未就绪都必须拒绝，且不得入队。
func TestStartRestoreRejectsUnusableTargets(t *testing.T) {
	st, disp, o, repo := newRestoreEnv(t)
	ctx := context.Background()
	allowRestore(st, disp, o, repo, restoreSnapshot("snap-1", model.KindPostgreSQL))
	disp.mu.Lock()
	disp.connected["agent-2"] = true
	disp.mu.Unlock()
	capable := testAgent()
	capable.ID = "agent-2"
	st.agents["agent-2"] = capable

	in := dbRestoreInput(repo.ID, "app")
	in.TargetAgentID = "agent-3"
	if _, _, err := o.StartRestore(ctx, "admin-1", in); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found for an unknown target agent, got %v", err)
	}

	revoked := testAgent()
	revoked.ID = "agent-4"
	revoked.Revoked = true
	st.agents["agent-4"] = revoked
	in.TargetAgentID = "agent-4"
	if _, _, err := o.StartRestore(ctx, "admin-1", in); !errors.Is(err, ErrAgentRevoked) {
		t.Fatalf("expected ErrAgentRevoked, got %v", err)
	}

	in.TargetAgentID = "agent-5"
	offline := testAgent()
	offline.ID = "agent-5"
	st.agents["agent-5"] = offline
	if _, _, err := o.StartRestore(ctx, "admin-1", in); !errors.Is(err, ErrAgentOffline) {
		t.Fatalf("expected ErrAgentOffline without a live stream, got %v", err)
	}

	// 来源仓库未 ready 时，跨 Agent 恢复必须在破坏性操作前拒绝。
	deprecatedRepo := *repo
	deprecatedRepo.Status = "pending"
	if err := st.UpdateRepositoryStatus(ctx, repo.ID, "pending"); err != nil {
		t.Fatal(err)
	}
	in.TargetAgentID = "agent-2"
	if _, _, err := o.StartRestore(ctx, "admin-1", in); !errors.Is(err, ErrRepositoryNotReady) {
		t.Fatalf("expected ErrRepositoryNotReady, got %v", err)
	}
	if len(disp.Enqueued()) != 0 {
		t.Fatalf("no run may be enqueued for a rejected target, got %v", disp.Enqueued())
	}
}

// 缺少已验证列表、kind 不符或快照被隐藏时拒绝授权（fail-closed）。
func TestStartRestoreSnapshotAuthorization(t *testing.T) {
	st, disp, o, repo := newRestoreEnv(t)
	ctx := context.Background()
	allowRestore(st, disp, o, repo, restoreSnapshot("snap-1", model.KindPostgreSQL))

	in := dbRestoreInput(repo.ID, "app")

	// 列表缓存缺失
	other := in
	other.SnapshotID = "snap-missing"
	other.Confirmation = secrets.HashToken("app")
	if _, _, err := o.StartRestore(ctx, "admin-1", other); !errors.Is(err, ErrSnapshotRefreshRequired) {
		t.Fatalf("expected ErrSnapshotRefreshRequired for an unknown snapshot, got %v", err)
	}

	// kind 标签不符
	mismatch := in
	mismatch.RestoreKind = model.KindMySQL
	st.seedVerifiedSnapshotCache(repo.ID, []model.Snapshot{restoreSnapshot("snap-1", model.KindPostgreSQL)})
	if _, _, err := o.StartRestore(ctx, "admin-1", mismatch); !errors.Is(err, ErrSnapshotKindMismatch) {
		t.Fatalf("expected ErrSnapshotKindMismatch, got %v", err)
	}

	// 摘要被篡改（缓存与 fingerprint 不一致）
	st.snapshotCache[repo.ID].Fingerprint = "deadbeef"
	if _, _, err := o.StartRestore(ctx, "admin-1", in); !errors.Is(err, ErrSnapshotRefreshRequired) {
		t.Fatalf("expected ErrSnapshotRefreshRequired for a tampered cache, got %v", err)
	}

	// 快照被隐藏（待删除）
	st.seedVerifiedSnapshotCache(repo.ID, restoreSnapshotList("snap-1", model.KindPostgreSQL))
	st.hidden[repo.ID] = map[string]struct{}{"snap-1": {}}
	if _, _, err := o.StartRestore(ctx, "admin-1", in); !errors.Is(err, ErrSnapshotRefreshRequired) {
		t.Fatalf("expected ErrSnapshotRefreshRequired for a hidden snapshot, got %v", err)
	}
}

// 目标 Agent 未上报安全能力时，数据库恢复返回 409/422 语义错误且不入队。
func TestStartRestoreRequiresSafeDatabaseRestoreCapability(t *testing.T) {
	st, disp, o, repo := newRestoreEnv(t)
	ctx := context.Background()
	allowRestore(st, disp, o, repo, restoreSnapshot("snap-1", model.KindPostgreSQL))
	in := dbRestoreInput(repo.ID, "app")

	o.AgentCaps = staticCaps{ready: false, safe: true, connected: true}
	if _, _, err := o.StartRestore(ctx, "admin-1", in); !errors.Is(err, ErrCapabilitiesPending) {
		t.Fatalf("expected ErrCapabilitiesPending, got %v", err)
	}

	o.AgentCaps = staticCaps{ready: true, safe: false, connected: true}
	if _, _, err := o.StartRestore(ctx, "admin-1", in); !errors.Is(err, ErrUnsafeDatabaseRestore) {
		t.Fatalf("expected ErrUnsafeDatabaseRestore, got %v", err)
	}

	// 无连接：di dispatcher 与能力来源都报告未连接 → 离线（连接层先于能力层判定）。
	o.AgentCaps = staticCaps{ready: true, safe: true, connected: false}
	if _, _, err := o.StartRestore(ctx, "admin-1", in); !errors.Is(err, ErrAgentOffline) {
		t.Fatalf("expected ErrAgentOffline without a live stream, got %v", err)
	}
	if len(disp.Enqueued()) != 0 {
		t.Fatalf("no run may be enqueued without the safe capability, got %v", disp.Enqueued())
	}
}

// 文件恢复必须用已验证目录缓存校验包含路径层级。
func TestStartRestoreValidatesIncludePathsAgainstVerifiedTree(t *testing.T) {
	st, disp, o, repo := newRestoreEnv(t)
	ctx := context.Background()
	allowRestore(st, disp, o, repo, restoreSnapshot("snap-1", model.KindFilesystem))

	in := RestoreInput{
		RepositoryID: repo.ID,
		SnapshotID:   "snap-1",
		RestoreKind:  model.KindFilesystem,
		Target: model.RestoreTarget{
			TargetPath:    restoreTestPath(),
			OverwriteMode: "always",
			IncludePaths:  []string{"/data/app"},
		},
	}

	// 无目录缓存 → 拒绝
	if _, _, err := o.StartRestore(ctx, "admin-1", in); !errors.Is(err, ErrSnapshotRefreshRequired) {
		t.Fatalf("expected ErrSnapshotRefreshRequired without a verified tree, got %v", err)
	}

	// 有缓存但条目不存在 → 拒绝
	st.seedVerifiedTreeCache(repo.ID, "snap-1", "/data", []TreeEntry{{Name: "other.txt", Type: "file"}})
	if _, _, err := o.StartRestore(ctx, "admin-1", in); !errors.Is(err, ErrSnapshotRefreshRequired) {
		t.Fatalf("expected ErrSnapshotRefreshRequired for an unverified entry, got %v", err)
	}

	// 条目存在 → 允许
	st.seedVerifiedTreeCache(repo.ID, "snap-1", "/data", []TreeEntry{{Name: "app", Type: "dir"}})
	if _, _, err := o.StartRestore(ctx, "admin-1", in); err != nil {
		t.Fatalf("expected the verified entry to authorize the restore, got %v", err)
	}
}

// 受保护快照（引用或标签）不得进入删除队列。
func TestQueueSnapshotDeletionRejectsRestoreProtectedSnapshot(t *testing.T) {
	st, disp, o, repo := newRestoreEnv(t)
	ctx := context.Background()
	allowRestore(st, disp, o, repo, restoreSnapshot("snap-1", model.KindPostgreSQL))

	// 引用保护：未终结的恢复请求记录了回滚快照 ID
	run := &model.Run{ID: "run-prot", AgentID: repo.AgentID, Operation: model.OpRestore, Status: model.RunRunning,
		QueuedAt: time.Now().UTC(), RepositoryID: repo.ID}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	rr := &model.RestoreRequest{ID: "rr-prot", RunID: run.ID, SnapshotID: "snap-src", RestoreKind: model.KindPostgreSQL,
		Phase: model.RestorePhaseManualRecoveryNeeded, RollbackSnapshotID: "prot-snap", CreatedAt: time.Now().UTC()}
	if err := st.CreateRestoreRequest(ctx, rr); err != nil {
		t.Fatal(err)
	}
	st.seedVerifiedSnapshotCache(repo.ID, []model.Snapshot{{ID: "prot-snap", Tags: []string{"kind:postgresql", "restore-protection:run-prot"}}})

	if _, _, err := o.QueueSnapshotDeletion(ctx, "admin-1", repo.ID, "prot-snap"); !errors.Is(err, ErrSnapshotRestoreProtected) {
		t.Fatalf("expected ErrSnapshotRestoreProtected for a referenced snapshot, got %v", err)
	}

	// 标签保护：仅带 restore-protection 标签、尚无引用
	st.restoreRequests = map[string]*model.RestoreRequest{}
	st.seedVerifiedSnapshotCache(repo.ID, []model.Snapshot{{ID: "tag-snap", Tags: []string{"kind:postgresql", "restore-protection:run-2"}}})
	if _, _, err := o.QueueSnapshotDeletion(ctx, "admin-1", repo.ID, "tag-snap"); !errors.Is(err, ErrSnapshotRestoreProtected) {
		t.Fatalf("expected ErrSnapshotRestoreProtected for a tagged snapshot, got %v", err)
	}

	// 元数据不可用（存储不支持缓存/隐藏状态）时 fail-closed
	bare := New(&limitedStore{Store: st}, o.Seal, disp, events.New(), "inst-1")
	bare.AgentCaps = staticCaps{ready: true, safe: true, connected: true}
	if _, _, err := bare.QueueSnapshotDeletion(ctx, "admin-1", repo.ID, "snap-1"); err == nil {
		t.Fatal("expected deletion to be refused without reliable metadata")
	}
}

// limitedStore 只暴露 Store 接口，用于验证缓存/隐藏状态不可用时的 fail-closed 行为。
type limitedStore struct{ store.Store }
