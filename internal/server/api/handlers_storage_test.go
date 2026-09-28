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

// seedBoundTarget writes an agent, storage target and repository directly to the
// store so the delete path can be exercised without an online agent.
func seedBoundTarget(t *testing.T, st interface {
	UpsertAgentOnConnect(context.Context, *model.Agent) error
	CreateStorageTarget(context.Context, *model.StorageTarget) error
	CreateRepository(context.Context, *model.Repository) error
}) {
	t.Helper()
	ctx := context.Background()
	at := time.Now().UTC()
	if err := st.UpsertAgentOnConnect(ctx, &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h",
		OS: "linux", Version: "1.0", Status: model.AgentOffline,
		LastSeenAt: &at, EnrolledAt: at, TokenHash: "sh",
		Capabilities: []model.ToolInfo{}, CapabilitiesJSON: "[]",
	}); err != nil {
		t.Fatalf("UpsertAgentOnConnect: %v", err)
	}
	if err := st.CreateStorageTarget(ctx, &model.StorageTarget{
		ID: "tgt-1", Name: "gdrive", Type: "rclone", RemoteName: "gdrive",
		EncryptedConfig: []byte("x"), CreatedAt: at, UpdatedAt: at,
	}); err != nil {
		t.Fatalf("CreateStorageTarget: %v", err)
	}
	if err := st.CreateRepository(ctx, &model.Repository{
		ID: "repo-1", AgentID: "agent-1", StorageTargetID: "tgt-1",
		RepositoryPath: "gdrive:backups/srv/agent-1", EncryptedPassword: []byte("pw"),
		Status: "pending", CreatedAt: at, UpdatedAt: at,
	}); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
}

// TestDeleteStorageTargetConflictCode pins the wire error the web UI switches
// on: 409 with code "conflict" while a repository is still bound, and a clean
// 204 once the binding is released.
func TestDeleteStorageTargetConflictCode(t *testing.T) {
	s, st, cleanup := newTestServerWithAdmin(t)
	defer cleanup()
	seedBoundTarget(t, st)

	handler := New(s)

	loginBody, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": "AdminPassword123",
	})
	loginReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	handler.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", loginRec.Code, loginRec.Body.String())
	}
	var csrf string
	var csrfCookie *http.Cookie
	var sessionCookie *http.Cookie
	for _, c := range loginRec.Result().Cookies() {
		switch c.Name {
		case auth.SessionCookie:
			sessionCookie = c
		case auth.CSRFCookie:
			csrfCookie = c
			csrf = c.Value
		}
	}
	if sessionCookie == nil || csrfCookie == nil {
		t.Fatalf("login did not set session/csrf cookies: %v", loginRec.Result().Cookies())
	}

	del := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/storage-targets/tgt-1", nil)
		req.AddCookie(sessionCookie)
		req.AddCookie(csrfCookie)
		req.Header.Set(auth.CSRFHeader, csrf)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	rec := del()
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 while repository is bound, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	if body.Error.Code != "conflict" {
		t.Fatalf("expected error code \"conflict\", got %q", body.Error.Code)
	}

	// Unbinding hides the repository from the UI; deleting the target must then
	// succeed instead of reporting an invisible reference.
	if err := st.DetachRepository(context.Background(), "repo-1"); err != nil {
		t.Fatalf("DetachRepository: %v", err)
	}
	if rec := del(); rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 after unbind, got %d: %s", rec.Code, rec.Body.String())
	}
}
