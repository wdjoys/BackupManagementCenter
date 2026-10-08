package api

import (
	"errors"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/server/auth"
	"backupmanagementcenter/internal/server/jobs"
	"backupmanagementcenter/internal/server/store"
)

func newTestServerWithAdmin(t *testing.T) (*Server, store.Store, func()) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.New(dir + "/test.db")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("st.Migrate: %v", err)
	}
	hash, err := auth.HashPassword("AdminPassword123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	admin := &model.Admin{
		ID:           "admin-1",
		Username:     "admin",
		PasswordHash: hash,
		CreatedAt:    time.Now().UTC(),
	}
	if err := st.CreateAdmin(context.Background(), admin); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	s := &Server{
		ST:   st,
		Jobs: jobs.New(st, nil, nil, nil, "inst-1"),
	}
	return s, st, func() { _ = st.Close() }
}

func TestLogoutWhenNotAuthenticated(t *testing.T) {
	s, _, cleanup := newTestServerWithAdmin(t)
	defer cleanup()

	handler := New(s)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	var res map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || !res["ok"] {
		t.Fatalf("expected {ok: true}, got %s", rec.Body.String())
	}
}


func TestLogoutWithoutCSRF(t *testing.T) {
	s, _, cleanup := newTestServerWithAdmin(t)
	defer cleanup()

	handler := New(s)

	// 1. Login
	loginBody, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": "AdminPassword123",
	})
	loginReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	handler.ServeHTTP(loginRec, loginReq)

	var sessionCookie *http.Cookie
	for _, c := range loginRec.Result().Cookies() {
		if c.Name == auth.SessionCookie {
			sessionCookie = c
		}
	}

	// 2. Logout with session cookie but no CSRF
	logoutReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", strings.NewReader("{}"))
	logoutReq.Header.Set("Content-Type", "application/json")
	logoutReq.AddCookie(sessionCookie)
	logoutRec := httptest.NewRecorder()
	handler.ServeHTTP(logoutRec, logoutReq)

	if logoutRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", logoutRec.Code, logoutRec.Body.String())
	}

	// Ensure session and csrf cookies are cleared (MaxAge < 0 or empty)
	for _, c := range logoutRec.Result().Cookies() {
		if c.Name == auth.SessionCookie && c.MaxAge >= 0 && c.Value != "" {
			t.Errorf("session cookie not cleared: %v", c)
		}
		if c.Name == auth.CSRFCookie && c.MaxAge >= 0 && c.Value != "" {
			t.Errorf("csrf cookie not cleared: %v", c)
		}
	}
}

// failingAdminStore 让管理员查询失败，用于验证登录把基础设施错误与凭据错误区分开。
type failingAdminStore struct {
	store.Store
}

func (failingAdminStore) GetAdminByUsername(context.Context, string) (*model.Admin, error) {
	return nil, errors.New("simulated database outage")
}

// 数据库/会话故障必须返回 500，不能伪装成 401「用户名或密码错误」，
// 否则运维会把基础设施故障误判为密码输错。
func TestLoginStoreFailureReturnsInternalError(t *testing.T) {
	s, st, cleanup := newTestServerWithAdmin(t)
	defer cleanup()
	s.ST = failingAdminStore{Store: st}

	handler := New(s)
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "AdminPassword123"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for store failure, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "invalid_credentials") {
		t.Fatalf("store failure must not be reported as invalid credentials: %s", rec.Body.String())
	}
}
