package main

import (
	"context"
	"testing"
	"time"

	"backupmanagementcenter/internal/model"
)

type fakeAgentStatusStore struct {
	agents []model.Agent
	set    []string
	status model.AgentStatus
}

func (f *fakeAgentStatusStore) ListAgents(context.Context) ([]model.Agent, error) {
	return f.agents, nil
}

func (f *fakeAgentStatusStore) SetAgentStatus(_ context.Context, agentID string, st model.AgentStatus, _ time.Time) error {
	f.set = append(f.set, agentID)
	f.status = st
	return nil
}

// 重启后必须把持久化为 online 的 Agent 置为 offline：否则 GET /agents 与实际可调度性
// 短暂不一致（实测重启后约 1.2s 内 POST /restores 先报 agent_unavailable /
// agent_capabilities_pending，而列表仍显示 online）。已是 offline 的不必重复写。
func TestMarkAgentsOfflineOnStartup(t *testing.T) {
	st := &fakeAgentStatusStore{agents: []model.Agent{
		{ID: "a-1", Status: model.AgentOnline},
		{ID: "a-2", Status: model.AgentOffline},
		{ID: "a-3", Status: model.AgentOnline},
	}}
	markAgentsOfflineOnStartup(context.Background(), st)

	if len(st.set) != 2 {
		t.Fatalf("只应处理 online 的 agent，实际 %v", st.set)
	}
	if st.set[0] != "a-1" || st.set[1] != "a-3" {
		t.Fatalf("处理对象错误: %v", st.set)
	}
	if st.status != model.AgentOffline {
		t.Fatalf("应写入 offline，实际 %q", st.status)
	}
}
