package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/server/auth"
	"backupmanagementcenter/internal/server/store"
)

func loginTestAdmin(t *testing.T, handler http.Handler) *http.Cookie {
	t.Helper()
	loginBody, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": "AdminPassword123",
	})
	loginReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	handler.ServeHTTP(loginRec, loginReq)

	for _, c := range loginRec.Result().Cookies() {
		if c.Name == auth.SessionCookie {
			return c
		}
	}
	t.Fatalf("session cookie not found after login")
	return nil
}

func TestListAgentLogsViaQueryAndPathParam(t *testing.T) {
	s, st, cleanup := newTestServerWithAdmin(t)
	defer cleanup()

	ctx := context.Background()
	agentID := "agent-test-1"
	now := time.Now().UTC()

	// 1. Create test agent
	agent := &model.Agent{
		ID:         agentID,
		Name:       "test-agent",
		Hostname:   "test-host",
		OS:         "linux",
		Arch:       "amd64",
		Version:    "v0.1.0",
		Status:     model.AgentOnline,
		EnrolledAt: now,
		LastSeenAt: &now,
	}
	if err := st.UpsertAgentOnConnect(ctx, agent); err != nil {
		t.Fatalf("UpsertAgentOnConnect: %v", err)
	}

	// 2. Append test agent logs
	logStore, ok := st.(store.LogStore)
	if !ok {
		t.Fatalf("st does not implement store.LogStore")
	}
	sampleLogs := []model.SystemLog{
		{
			Timestamp: now,
			Level:     "info",
			Type:      "agent",
			Message:   "agent started",
		},
		{
			Timestamp: now.Add(time.Second),
			Level:     "warn",
			Type:      "system",
			Message:   "agent warning",
		},
	}
	if err := logStore.AppendAgentLogs(ctx, agentID, sampleLogs); err != nil {
		t.Fatalf("AppendAgentLogs: %v", err)
	}

	handler := New(s)
	sessionCookie := loginTestAdmin(t, handler)

	// Case 1: GET /api/v1/logs/agent?agent_id=... (query param)
	{
		req := httptest.NewRequest(http.MethodGet, "/api/v1/logs/agent?limit=500&agent_id="+agentID, nil)
		req.AddCookie(sessionCookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK for /logs/agent?agent_id=..., got %d: %s", rec.Code, rec.Body.String())
		}
		var logs []model.SystemLog
		if err := json.Unmarshal(rec.Body.Bytes(), &logs); err != nil {
			t.Fatalf("json.Unmarshal: %v", err)
		}
		if len(logs) != 2 {
			t.Fatalf("expected 2 logs, got %d", len(logs))
		}
	}

	// Case 2: GET /api/v1/agents/{id}/logs (path param)
	{
		req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/"+agentID+"/logs?limit=500", nil)
		req.AddCookie(sessionCookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK for /agents/{id}/logs, got %d: %s", rec.Code, rec.Body.String())
		}
		var logs []model.SystemLog
		if err := json.Unmarshal(rec.Body.Bytes(), &logs); err != nil {
			t.Fatalf("json.Unmarshal: %v", err)
		}
		if len(logs) != 2 {
			t.Fatalf("expected 2 logs, got %d", len(logs))
		}
	}

	// Case 3: GET /api/v1/logs/agent without agent_id -> 400 validation_failed
	{
		req := httptest.NewRequest(http.MethodGet, "/api/v1/logs/agent?limit=500", nil)
		req.AddCookie(sessionCookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request without agent_id, got %d: %s", rec.Code, rec.Body.String())
		}
	}

	// Case 4: GET /api/v1/logs/agent with non-existent agent_id -> 404 not_found
	{
		req := httptest.NewRequest(http.MethodGet, "/api/v1/logs/agent?agent_id=non-existent", nil)
		req.AddCookie(sessionCookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404 Not Found for non-existent agent, got %d: %s", rec.Code, rec.Body.String())
		}
	}
}

func TestListServerLogs(t *testing.T) {
	s, st, cleanup := newTestServerWithAdmin(t)
	defer cleanup()

	ctx := context.Background()
	logStore, ok := st.(store.LogStore)
	if !ok {
		t.Fatalf("st does not implement store.LogStore")
	}
	now := time.Now().UTC()
	serverLogs := []model.SystemLog{
		{
			Timestamp: now,
			Level:     "info",
			Type:      "system",
			Message:   "server started",
		},
	}
	if err := logStore.AppendServerLogs(ctx, serverLogs); err != nil {
		t.Fatalf("AppendServerLogs: %v", err)
	}

	handler := New(s)
	sessionCookie := loginTestAdmin(t, handler)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/logs/server?limit=200", nil)
	req.AddCookie(sessionCookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for /logs/server, got %d: %s", rec.Code, rec.Body.String())
	}
	var logs []model.SystemLog
	if err := json.Unmarshal(rec.Body.Bytes(), &logs); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
}
