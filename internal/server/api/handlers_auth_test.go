package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/secrets"
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

// 登出是非 GET 的受保护路由，必须与其余路由一致地强制 CSRF：缺少
// CSRF/origin 的请求一律 403，且绝不能删掉会话（否则任意站点都能强制登出）。
func TestLogoutWithoutCSRFIsRejected(t *testing.T) {
	s, _, cleanup := newTestServerWithAdmin(t)
	defer cleanup()

	handler := New(s)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden, got %d: %s", rec.Code, rec.Body.String())
	}
}

// 带会话 cookie、缺 CSRF token 的登出必须被拒，且会话必须存活。
func TestLogoutWithSessionButNoCSRFKeepsSession(t *testing.T) {
	s, st, cleanup := newTestServerWithAdmin(t)
	defer cleanup()

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
		t.Fatalf("login: expected 200, got %d: %s", loginRec.Code, loginRec.Body.String())
	}

	var sessionCookie *http.Cookie
	for _, c := range loginRec.Result().Cookies() {
		if c.Name == auth.SessionCookie {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("login did not set a session cookie")
	}

	logoutReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", strings.NewReader("{}"))
	logoutReq.Header.Set("Content-Type", "application/json")
	logoutReq.AddCookie(sessionCookie)
	logoutRec := httptest.NewRecorder()
	handler.ServeHTTP(logoutRec, logoutReq)

	if logoutRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden, got %d: %s", logoutRec.Code, logoutRec.Body.String())
	}
	if _, err := st.GetSession(context.Background(), secrets.HashToken(sessionCookie.Value)); err != nil {
		t.Fatalf("session must survive a CSRF-less logout, got %v", err)
	}
}

// 带 CSRF token + 同源 Origin 的登出必须成功，清 cookie 并删除服务端会话。
func TestLogoutWithCSRFDeletesSession(t *testing.T) {
	s, st, cleanup := newTestServerWithAdmin(t)
	defer cleanup()

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
		t.Fatalf("login: expected 200, got %d: %s", loginRec.Code, loginRec.Body.String())
	}

	var sessionCookie, csrfCookie *http.Cookie
	for _, c := range loginRec.Result().Cookies() {
		switch c.Name {
		case auth.SessionCookie:
			sessionCookie = c
		case auth.CSRFCookie:
			csrfCookie = c
		}
	}
	if sessionCookie == nil || csrfCookie == nil {
		t.Fatalf("login did not set both cookies: session=%v csrf=%v", sessionCookie, csrfCookie)
	}

	logoutReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", strings.NewReader("{}"))
	logoutReq.Header.Set("Content-Type", "application/json")
	logoutReq.Header.Set("Origin", "http://example.com") // httptest 默认 Host 为 example.com
	logoutReq.Header.Set(auth.CSRFHeader, csrfCookie.Value)
	logoutReq.AddCookie(sessionCookie)
	logoutReq.AddCookie(csrfCookie)
	logoutRec := httptest.NewRecorder()
	handler.ServeHTTP(logoutRec, logoutReq)

	if logoutRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", logoutRec.Code, logoutRec.Body.String())
	}
	var res map[string]bool
	if err := json.Unmarshal(logoutRec.Body.Bytes(), &res); err != nil || !res["ok"] {
		t.Fatalf("expected {ok: true}, got %s", logoutRec.Body.String())
	}
	if _, err := st.GetSession(context.Background(), secrets.HashToken(sessionCookie.Value)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("session must be deleted after logout, got err=%v", err)
	}
	// cookie 必须被清除（MaxAge < 0 或空值）
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

// 限流拒绝必须带 Retry-After（秒）：此前 429 没有任何重试提示，客户端只能盲猜。
func TestLoginRateLimitSetsRetryAfter(t *testing.T) {
	s, _, cleanup := newTestServerWithAdmin(t)
	defer cleanup()

	handler := New(s)
	body := []byte(`{"username":"admin","password":"wrong"}`)
	var rec *httptest.ResponseRecorder
	for i := 0; i < 11; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("11th login attempt: got %d, want 429", rec.Code)
	}
	got := rec.Header().Get("Retry-After")
	secs, err := strconv.Atoi(got)
	if err != nil || secs < 1 || secs > 900 {
		t.Fatalf("Retry-After = %q, want 1..900 seconds", got)
	}
}

// 修改密码：校验当前密码、成功后新旧密码互换可用性，并只保留当前会话。
func TestChangePassword(t *testing.T) {
	s, st, cleanup := newTestServerWithAdmin(t)
	defer cleanup()
	handler := New(s)

	login := func(password string) (*http.Cookie, *http.Cookie) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"username": "admin", "password": password})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			return nil, nil
		}
		var sessionCookie, csrfCookie *http.Cookie
		for _, c := range rec.Result().Cookies() {
			switch c.Name {
			case auth.SessionCookie:
				sessionCookie = c
			case auth.CSRFCookie:
				csrfCookie = c
			}
		}
		return sessionCookie, csrfCookie
	}
	change := func(session, csrf *http.Cookie, current, next string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"current_password": current, "new_password": next})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://example.com")
		req.Header.Set(auth.CSRFHeader, csrf.Value)
		req.AddCookie(session)
		req.AddCookie(csrf)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// 两台设备各有一个会话：改密后旧设备的会话必须失效，当前设备保留。
	oldSession, _ := login("AdminPassword123")
	curSession, curCSRF := login("AdminPassword123")
	if oldSession == nil || curSession == nil {
		t.Fatal("login failed")
	}

	if rec := change(curSession, curCSRF, "WrongPassword123", "NewPassword456"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong current password: got %d, want 401: %s", rec.Code, rec.Body.String())
	}
	if _, err := st.GetAdminByID(context.Background(), "admin-1"); err != nil {
		t.Fatalf("GetAdminByID: %v", err)
	}
	admin, _ := st.GetAdminByID(context.Background(), "admin-1")
	if !auth.VerifyPassword("AdminPassword123", admin.PasswordHash) {
		t.Fatal("password must not change after a failed attempt")
	}

	if rec := change(curSession, curCSRF, "AdminPassword123", "NewPassword456"); rec.Code != http.StatusNoContent {
		t.Fatalf("change password: got %d, want 204: %s", rec.Code, rec.Body.String())
	}

	admin, _ = st.GetAdminByID(context.Background(), "admin-1")
	if !auth.VerifyPassword("NewPassword456", admin.PasswordHash) {
		t.Fatal("new password not persisted")
	}
	if _, err := st.GetSession(context.Background(), secrets.HashToken(curSession.Value)); err != nil {
		t.Fatalf("current session must survive password change: %v", err)
	}
	if _, err := st.GetSession(context.Background(), secrets.HashToken(oldSession.Value)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other sessions must be revoked, got err=%v", err)
	}

	if oldSession, _ := login("AdminPassword123"); oldSession != nil {
		t.Fatal("old password must stop working")
	}
	if newSession, _ := login("NewPassword456"); newSession == nil {
		t.Fatal("new password must work")
	}
}
