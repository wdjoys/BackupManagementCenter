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
)

// loginSession 登录测试管理员并返回可携带 CSRF 的请求构造器。
// loginCookies 登录管理员并返回会话/CSRF cookie 与 CSRF token，供需要自定义请求体
// 或方法的测试直接构造请求（loginSession 只支持无 body 的调用）。
func loginCookies(t *testing.T, handler http.Handler) (*http.Cookie, *http.Cookie, string) {
	t.Helper()
	loginBody, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": "AdminPassword123",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
	}
	var sessionCookie, csrfCookie *http.Cookie
	var csrf string
	for _, c := range rec.Result().Cookies() {
		switch c.Name {
		case auth.SessionCookie:
			sessionCookie = c
		case auth.CSRFCookie:
			csrfCookie = c
			csrf = c.Value
		}
	}
	if sessionCookie == nil || csrfCookie == nil {
		t.Fatalf("login did not set session/csrf cookies: %v", rec.Result().Cookies())
	}
	return sessionCookie, csrfCookie, csrf
}

func loginSession(t *testing.T, handler http.Handler) func(method, path string) *httptest.ResponseRecorder {
	t.Helper()
	sessionCookie, csrfCookie, csrf := loginCookies(t, handler)
	return func(method, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.AddCookie(sessionCookie)
		r.AddCookie(csrfCookie)
		r.Header.Set(auth.CSRFHeader, csrf)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		return rec
	}
}

func TestAgentRevokeAndRestore(t *testing.T) {
	s, st, cleanup := newTestServerWithAdmin(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()
	agent := &model.Agent{
		ID: "agent-restore-1", Name: "restore-me", Hostname: "host-1",
		OS: "linux", Arch: "amd64", Version: "v0.1.0",
		Status: model.AgentOnline, EnrolledAt: now, LastSeenAt: &now,
		TokenHash: "hash-1",
	}
	if err := st.UpsertAgentOnConnect(ctx, agent); err != nil {
		t.Fatalf("UpsertAgentOnConnect: %v", err)
	}

	handler := New(s)
	do := loginSession(t, handler)

	if rec := do(http.MethodDelete, "/api/v1/agents/agent-restore-1"); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := st.GetAgent(ctx, "agent-restore-1")
	if err != nil {
		t.Fatalf("GetAgent after revoke: %v", err)
	}
	if !got.Revoked {
		t.Fatalf("agent should be revoked after DELETE")
	}

	if rec := do(http.MethodPost, "/api/v1/agents/agent-restore-1/restore"); rec.Code != http.StatusNoContent {
		t.Fatalf("restore: expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err = st.GetAgent(ctx, "agent-restore-1")
	if err != nil {
		t.Fatalf("GetAgent after restore: %v", err)
	}
	if got.Revoked {
		t.Fatalf("agent should not be revoked after restore")
	}
	if got.Status != model.AgentOffline {
		t.Fatalf("restored agent status = %q, want %q", got.Status, model.AgentOffline)
	}
	// 恢复必须保留身份哈希，否则 Agent 无法用原 secret 重连。
	if got.TokenHash != "hash-1" {
		t.Fatalf("restore changed token hash: %q", got.TokenHash)
	}

	if rec := do(http.MethodPost, "/api/v1/agents/nope/restore"); rec.Code != http.StatusNotFound {
		t.Fatalf("restore unknown agent: expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodDelete, "/api/v1/agents/nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("revoke unknown agent: expected 404, got %d: %s", rec.Code, rec.Body.String())
	}

	// 未登录/无 CSRF 时不允许恢复。
	anon := httptest.NewRequest(http.MethodPost, "/api/v1/agents/agent-restore-1/restore", nil)
	anonRec := httptest.NewRecorder()
	handler.ServeHTTP(anonRec, anon)
	if anonRec.Code != http.StatusUnauthorized && anonRec.Code != http.StatusForbidden {
		t.Fatalf("restore without session: expected 401/403, got %d", anonRec.Code)
	}
}

// 审计归属必须取自会话里的 admin：客户端不能用 X-Actor-ID 伪造操作者，
// 也不能让 actor_id 默认为空（否则审计日志无法归属到真实操作者）。
func TestAuditActorComesFromSessionNotRequestHeader(t *testing.T) {
	s, st, cleanup := newTestServerWithAdmin(t)
	defer cleanup()

	handler := New(s)
	sessionCookie, csrfCookie, csrf := loginCookies(t, handler)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/enrollment-tokens", bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://example.com") // httptest 默认 Host
	req.Header.Set(auth.CSRFHeader, csrf)
	req.Header.Set("X-Actor-ID", "spoofed-actor")
	req.AddCookie(sessionCookie)
	req.AddCookie(csrfCookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create enrollment token: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	events, err := st.ListAuditEvents(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	for _, e := range events {
		if e.Action != "enrollment.create_token" {
			continue
		}
		if e.ActorID != "admin-1" {
			t.Fatalf("audit actor_id = %q, want the session admin id %q", e.ActorID, "admin-1")
		}
		return
	}
	t.Fatal("no enrollment.create_token audit event found")
}
