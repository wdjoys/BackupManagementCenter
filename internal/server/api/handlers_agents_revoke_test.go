package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/server/agentreg"
	"backupmanagementcenter/internal/server/events"
)

// 撤销 Agent 会直接踢掉活跃 stream，此时 Service 自己的 defer UnregisterIf 不再生效。
// 若不显式按断连处理，在途 run 会一直停在 running（实测 300s+ 只能靠 plan 超时兜底），
// 并占住该仓库的队列槽位（同仓库后续 run 被 head-of-line 阻塞）。
func TestAgentRevokeReconcilesInFlightRuns(t *testing.T) {
	s, st, cleanup := newTestServerWithAdmin(t)
	defer cleanup()

	reg := agentreg.NewRegistry()
	s.Reg = reg
	s.AgentSvc = agentreg.NewService(st, reg, events.New(), agentreg.DefaultConfig(), nil, nil)

	ctx := context.Background()
	now := time.Now().UTC()
	agent := &model.Agent{
		ID: "agent-revoke-1", Name: "revoke-me", Hostname: "host-1",
		OS: "linux", Arch: "amd64", Version: "v0.1.0",
		Status: model.AgentOnline, EnrolledAt: now, LastSeenAt: &now, TokenHash: "hash-1",
	}
	if err := st.UpsertAgentOnConnect(ctx, agent); err != nil {
		t.Fatalf("UpsertAgentOnConnect: %v", err)
	}
	target := &model.StorageTarget{
		ID: "target-revoke-1", Name: "target-1", Type: "rclone",
		RemoteName: "remote", RemotePath: "/tmp/repo", EncryptedConfig: []byte("cfg"),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateStorageTarget(ctx, target); err != nil {
		t.Fatalf("CreateStorageTarget: %v", err)
	}
	repo := &model.Repository{
		ID: "repo-revoke-1", AgentID: agent.ID, StorageTargetID: target.ID,
		RepositoryPath: "/tmp/repo", EncryptedPassword: []byte("pass"), Status: "ready",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateRepository(ctx, repo); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	started := now.Add(-time.Minute)
	if err := st.CreateRun(ctx, &model.Run{
		ID: "run-inflight", AgentID: agent.ID, Operation: model.OpBackup,
		Status: model.RunRunning, QueuedAt: started, StartedAt: &started, RepositoryID: repo.ID,
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	handler := New(s)
	do := loginSession(t, handler)
	if rec := do(http.MethodDelete, "/api/v1/agents/agent-revoke-1"); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	run, err := st.GetRun(ctx, "run-inflight")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != model.RunQueued {
		t.Fatalf("撤销后在途备份运行应回退 queued（否则会卡 running 并阻塞仓库队列），实际 %s", run.Status)
	}
	if got, err := st.GetAgent(ctx, agent.ID); err != nil {
		t.Fatalf("GetAgent: %v", err)
	} else if got.Status != model.AgentOffline {
		t.Fatalf("撤销后 agent 应置 offline，实际 %s", got.Status)
	}
}
