package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/server/agentreg"
	"backupmanagementcenter/internal/server/events"
	"backupmanagementcenter/internal/server/jobs"
)

// 缺省响应仍返回 run: Run 结构，Web 用 run.id 跳转；数据库 kind 未启用时 503。
func TestStartRestoreResponseShapeAndKindGate(t *testing.T) {
	s, st, cleanup := newTestServerWithAdmin(t)
	defer cleanup()
	s.DatabaseRestoreKinds = map[string]bool{model.KindSQLite: true}
	handler := New(s)
	cookie, csrf := loginTestAdminSession(t, handler)
	repo, _ := setupTestRepoAndSnapshots(t, s, st)
	seedVerifiedRestoreCache(t, st, repo.ID, "snap-1", model.KindPostgreSQL)

	rec := postRestore(t, handler, cookie, csrf, map[string]any{
		"repository_id": repo.ID,
		"snapshot_id":   "snap-1",
		"restore_kind":  model.KindPostgreSQL,
		"target":        map[string]any{"host": "db", "port": 5432, "username": "pg", "database": "app"},
	})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for a disabled kind, got %d (body %s)", rec.Code, rec.Body.String())
	}

	// 启用后的 PostgreSQL：目标 Agent 尚未上报能力 → 409 agent_capabilities_pending。
	// 能力未就绪的处置发生在连通性检查之后：这里把注册表同时接到
	// dispatcher（连通性判定）与 orchestrator（能力判定）。
	s.DatabaseRestoreKinds = map[string]bool{model.KindPostgreSQL: true}
	reg := agentreg.NewRegistry()
	s.Reg = reg
	jobsWithRegistry := jobs.New(st, nil, registryDispatcher{reg}, events.New(), "inst-1")
	jobsWithRegistry.AgentCaps = reg
	s.Jobs = jobsWithRegistry
	onlineAgent := &model.Agent{
		ID: repo.AgentID, Name: repo.AgentID, Hostname: repo.AgentID, OS: "linux", Arch: "amd64",
		Version: "v1", Status: model.AgentOnline, EnrolledAt: time.Now().UTC(), TokenHash: "h-1",
	}
	if err := st.UpsertAgentOnConnect(context.Background(), onlineAgent); err != nil {
		t.Fatal(err)
	}
	// 目标 Agent 已上报恢复所需工具，使能力（而非工具）成为唯一阻塞点。
	if err := st.SaveAgentCapabilities(context.Background(), repo.AgentID,
		[]model.ToolInfo{
			{Name: "restic", Path: "/usr/bin/restic"},
			{Name: "rclone", Path: "/usr/bin/rclone"},
			{Name: "pg_dump", Path: "/usr/bin/pg_dump"},
			{Name: "pg_restore", Path: "/usr/bin/pg_restore"},
			{Name: "psql", Path: "/usr/bin/psql"},
		}, nil, nil, true, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	reg.Register(context.Background(), repo.AgentID)
	rec = postRestore(t, handler, cookie, csrf, map[string]any{
		"repository_id": repo.ID,
		"snapshot_id":   "snap-1",
		"restore_kind":  model.KindPostgreSQL,
		"target":        map[string]any{"host": "db", "port": 5432, "username": "pg", "database": "app"},
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 without a safe-restore capability, got %d (body %s)", rec.Code, rec.Body.String())
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != model.ErrAgentCapabilitiesPending {
		t.Fatalf("expected %s, got %s (body %s)", model.ErrAgentCapabilitiesPending, payload.Error.Code, rec.Body.String())
	}
}

// 目标 Agent 必须在线且未撤销：离线/撤销分别映射到稳定错误码。
func TestStartRestoreTargetAgentAvailability(t *testing.T) {
	s, st, cleanup := newTestServerWithAdmin(t)
	defer cleanup()
	reg := agentreg.NewRegistry()
	s.Reg = reg
	handler := New(s)
	cookie, csrf := loginTestAdminSession(t, handler)
	repo, _ := setupTestRepoAndSnapshots(t, s, st)
	seedVerifiedRestoreCache(t, st, repo.ID, "snap-1", model.KindFilesystem)

	body := func() map[string]any {
		return map[string]any{
			"repository_id": repo.ID,
			"snapshot_id":   "snap-1",
			"restore_kind":  model.KindFilesystem,
			"target":        map[string]any{"target_path": "/tmp/out", "overwrite_mode": "always"},
			"target_agent_id": "agent-ghost",
		}
	}

	rec := postRestore(t, handler, cookie, csrf, body())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown target agent, got %d (body %s)", rec.Code, rec.Body.String())
	}

	// 已撤销的目标 Agent
	revoked := &model.Agent{
		ID: "agent-revoked", Name: "revoked", Hostname: "revoked", OS: "linux", Arch: "amd64",
		Version: "v1", Status: model.AgentOnline, EnrolledAt: time.Now().UTC(), Revoked: true, TokenHash: "h-revoked",
	}
	if err := st.UpsertAgentOnConnect(context.Background(), revoked); err != nil {
		t.Fatal(err)
	}
	in := body()
	in["target_agent_id"] = "agent-revoked"
	rec = postRestore(t, handler, cookie, csrf, in)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for a revoked target agent, got %d (body %s)", rec.Code, rec.Body.String())
	}

	// 在线但无 stream → agent_unavailable
	online := &model.Agent{
		ID: "agent-online", Name: "online", Hostname: "online", OS: "linux", Arch: "amd64",
		Version: "v1", Status: model.AgentOnline, EnrolledAt: time.Now().UTC(), TokenHash: "h-online",
	}
	if err := st.UpsertAgentOnConnect(context.Background(), online); err != nil {
		t.Fatal(err)
	}
	in = body()
	in["target_agent_id"] = "agent-online"
	rec = postRestore(t, handler, cookie, csrf, in)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for an offline stream, got %d (body %s)", rec.Code, rec.Body.String())
	}
	var unavailable struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &unavailable); err != nil {
		t.Fatal(err)
	}
	if unavailable.Error.Code != model.ErrAgentUnavailable {
		t.Fatalf("expected %s, got %s", model.ErrAgentUnavailable, unavailable.Error.Code)
	}
}

// registryDispatcher 让 orchestrator 的连通性判定使用真实注册表。
type registryDispatcher struct{ reg *agentreg.Registry }

func (registryDispatcher) Enqueue(context.Context, string, string, string) {}
func (d registryDispatcher) Cancel(context.Context, string) error       { return nil }
func (d registryDispatcher) ConnectedAgents() []string                  { return d.reg.List() }
func (d registryDispatcher) IsConnected(agentID string) bool            { return d.reg.Connected(agentID) }
