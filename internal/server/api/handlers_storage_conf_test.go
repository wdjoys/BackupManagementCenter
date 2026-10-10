package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"backupmanagementcenter/internal/server/auth"
)

// 空 rclone_conf 会被 Seal 成 NULL 并撞 storage_targets.encrypted_config 的 NOT NULL
// 约束，此前冒泡成 500 internal（实测）。这是客户端输入问题，必须回 400。
func TestCreateStorageTargetRejectsEmptyRcloneConf(t *testing.T) {
	s, _, cleanup := newTestServerWithAdmin(t)
	defer cleanup()

	handler := New(s)
	sessionCookie, csrfCookie, csrf := loginCookies(t, handler)

	body := `{"name":"t1","rclone_conf":"","remote_name":"r","remote_path":"/x"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/storage-targets", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookie)
	req.AddCookie(csrfCookie)
	req.Header.Set(auth.CSRFHeader, csrf)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空 rclone_conf 应返回 400，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "validation_failed") {
		t.Fatalf("应返回 validation_failed，实际: %s", rec.Body.String())
	}
}
