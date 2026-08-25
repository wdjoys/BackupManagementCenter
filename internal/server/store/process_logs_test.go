package store

import (
	"context"
	"testing"
	"time"

	"backupmanagementcenter/internal/model"
)

func TestProcessLogs(t *testing.T) {
	ts := newTestStore(t)
	defer ts.Close(t)
	ctx := context.Background()
	logStore, ok := ts.Store.(LogStore)
	if !ok {
		t.Fatal("test store does not implement LogStore")
	}

	if err := ts.Store.UpsertAgentOnConnect(ctx, &model.Agent{
		ID:         "agent-logs",
		Name:       "agent-logs",
		TokenHash:  "agent-logs-secret",
		EnrolledAt: now,
		Status:     model.AgentOffline,
	}); err != nil {
		t.Fatal(err)
	}

	if err := logStore.AppendServerLogs(ctx, []model.SystemLog{
		{Timestamp: now, Level: "info", Message: "server started"},
		{Timestamp: now.Add(time.Second), Level: "error", Message: "server warning"},
	}); err != nil {
		t.Fatal(err)
	}
	serverLogs, err := logStore.ListServerLogs(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(serverLogs) != 2 || serverLogs[0].Message != "server warning" {
		t.Fatalf("expected newest server log first, got %+v", serverLogs)
	}

	if err := logStore.AppendAgentLogs(ctx, "agent-logs", []model.SystemLog{
		{SourceSeq: 7, Timestamp: now, Level: "debug", Message: "agent connected"},
		{SourceSeq: 8, Timestamp: now.Add(time.Second), Level: "warn", Message: "agent retry"},
	}); err != nil {
		t.Fatal(err)
	}
	agentLogs, err := logStore.ListAgentLogs(ctx, "agent-logs", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(agentLogs) != 2 || agentLogs[0].SourceSeq != 8 || agentLogs[0].AgentID != "agent-logs" {
		t.Fatalf("unexpected agent logs: %+v", agentLogs)
	}
	older, err := logStore.ListAgentLogs(ctx, "agent-logs", agentLogs[0].ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(older) != 1 || older[0].SourceSeq != 7 {
		t.Fatalf("expected cursor pagination to return older log, got %+v", older)
	}
}
