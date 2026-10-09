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
	"backupmanagementcenter/internal/server/jobs"
	"backupmanagementcenter/internal/server/store"
)

// seedVerifiedRestoreCache 写入一份已验证的快照列表缓存（含 kind 标签），
// 让恢复请求能通过服务端快照授权。
func seedVerifiedRestoreCache(t *testing.T, st store.Store, repoID, snapshotID, kind string) {
	t.Helper()
	cs, ok := st.(store.SnapshotCacheStore)
	if !ok {
		t.Fatal("store does not implement SnapshotCacheStore")
	}
	ctx := context.Background()
	gen, err := cs.SnapshotCacheGeneration(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	snaps := []model.Snapshot{
		{ID: snapshotID, Host: "host-1", Paths: []string{"/data"}, Tags: []string{"kind:" + kind, "plan:plan-1"}},
	}
	raw, _ := json.Marshal(snaps)
	if err := cs.SaveSnapshotListCache(ctx, repoID, gen, string(raw), store.SnapshotFingerprint(snaps), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

// postJSON 以已认证会话发送 JSON POST；浏览器同源请求需要 CSRF 头。
func postJSON(t *testing.T, handler http.Handler, cookie *http.Cookie, csrf *http.Cookie, path string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf.Value)
	req.AddCookie(cookie)
	req.AddCookie(csrf)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func postRestore(t *testing.T, handler http.Handler, cookie *http.Cookie, csrf *http.Cookie, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return postJSON(t, handler, cookie, csrf, "/api/v1/restores", body)
}

// database=all 必须在 HTTP 层同步拒绝，不能返回 202 后再由 run 失败。
func TestStartRestoreRejectsDatabaseAll(t *testing.T) {
	s, st, cleanup := newTestServerWithAdmin(t)
	defer cleanup()
	handler := New(s)
	cookie, csrf := loginTestAdminSession(t, handler)
	repo, _ := setupTestRepoAndSnapshots(t, s, st)

	rec := postRestore(t, handler, cookie, csrf, map[string]any{
		"repository_id": repo.ID,
		"snapshot_id":   "snap-1",
		"restore_kind":  model.KindPostgreSQL,
		"target": map[string]any{
			"host": "db", "port": 5432, "username": "pg", "database": "all",
		},
		"overwrite": false,
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for database=all, got %d (body %s)", rec.Code, rec.Body.String())
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != model.ErrUnsupportedRestoreManifest {
		t.Fatalf("expected %s, got %s", model.ErrUnsupportedRestoreManifest, payload.Error.Code)
	}
}

// 每个数据库 kind 在真实实例上完成验证前保持 503 禁用。
func TestStartRestoreRequiresVerifiedKind(t *testing.T) {
	s, st, cleanup := newTestServerWithAdmin(t)
	defer cleanup()
	handler := New(s)
	cookie, csrf := loginTestAdminSession(t, handler)
	repo, _ := setupTestRepoAndSnapshots(t, s, st)
	seedVerifiedRestoreCache(t, st, repo.ID, "snap-1", model.KindSQLite)

	rec := postRestore(t, handler, cookie, csrf, map[string]any{
		"repository_id": repo.ID,
		"snapshot_id":   "snap-1",
		"restore_kind":  model.KindSQLite,
		"target":        map[string]any{"database": "/tmp/restore/target.sqlite"},
		"overwrite":     false,
	})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 while the kind is not enabled, got %d (body %s)", rec.Code, rec.Body.String())
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != model.ErrDatabaseRestoreDisabled {
		t.Fatalf("expected %s, got %s", model.ErrDatabaseRestoreDisabled, payload.Error.Code)
	}
}

// 缺少已验证快照缓存时，文件恢复返回 409 snapshot_list_refresh_required。
func TestStartRestoreRequiresVerifiedSnapshotList(t *testing.T) {
	s, st, cleanup := newTestServerWithAdmin(t)
	defer cleanup()
	handler := New(s)
	cookie, csrf := loginTestAdminSession(t, handler)
	repo, _ := setupTestRepoAndSnapshots(t, s, st)

	rec := postRestore(t, handler, cookie, csrf, map[string]any{
		"repository_id": repo.ID,
		"snapshot_id":   "snap-1",
		"restore_kind":  model.KindFilesystem,
		"target":        map[string]any{"target_path": "/tmp/restore/out", "overwrite_mode": "always"},
		"overwrite":     false,
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 without a verified snapshot list, got %d (body %s)", rec.Code, rec.Body.String())
	}
}

// 人工解除只接受被阻塞的 phase，并要求 run ID、处理说明与两项确认。
func TestResolveRestoreRequiresBlockedPhaseAndConfirmations(t *testing.T) {
	s, st, cleanup := newTestServerWithAdmin(t)
	defer cleanup()
	handler := New(s)
	cookie, csrf := loginTestAdminSession(t, handler)
	repo, _ := setupTestRepoAndSnapshots(t, s, st)
	ctx := context.Background()

	run := &model.Run{
		ID: "run-1", AgentID: repo.AgentID, Operation: model.OpRestore, Status: model.RunRunning,
		QueuedAt: time.Now().UTC(), RepositoryID: repo.ID, ProgressJSON: "{}",
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	rr := &model.RestoreRequest{
		ID: "rr-1", RunID: "run-1", SnapshotID: "snap-1", RestoreKind: model.KindPostgreSQL,
		Target: model.RestoreTarget{Host: "db", Port: 5432, Username: "pg", Database: "app"},
		Phase:  model.RestorePhaseQueued, CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateRestoreRequest(ctx, rr); err != nil {
		t.Fatal(err)
	}

	post := func(body map[string]any) *httptest.ResponseRecorder {
		return postJSON(t, handler, cookie, csrf, "/api/v1/restores/rr-1/resolve", body)
	}

	// 未确认执行已停止/目标已核验 → 400
	rec := post(map[string]any{"run_id": "run-1", "note": "checked", "execution_stopped": true, "target_verified": false})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without both confirmations, got %d", rec.Code)
	}
	// 非阻塞 phase → 409
	rec = post(map[string]any{"run_id": "run-1", "note": "checked", "execution_stopped": true, "target_verified": true})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for a non-blocked phase, got %d (body %s)", rec.Code, rec.Body.String())
	}
	// run ID 不匹配 → 409
	if ph, ok := st.(interface {
		UpdateRestorePhase(context.Context, string, string) error
	}); ok {
		if err := ph.UpdateRestorePhase(ctx, "run-1", model.RestorePhaseManualRecoveryNeeded); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Fatal("store does not support restore phase updates")
	}
	rec = post(map[string]any{"run_id": "other-run", "note": "checked", "execution_stopped": true, "target_verified": true})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for a mismatched run id, got %d", rec.Code)
	}
	// 正确解除 → 200 且 phase 变为 manual_recovery_resolved
	rec = post(map[string]any{"run_id": "run-1", "note": "operator verified target", "execution_stopped": true, "target_verified": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 resolving a blocked restore, got %d (body %s)", rec.Code, rec.Body.String())
	}
	got, err := st.GetRestoreRequest(ctx, "rr-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != model.RestorePhaseManualRecoveryDone {
		t.Fatalf("expected %s, got %s", model.RestorePhaseManualRecoveryDone, got.Phase)
	}
	_ = repo
}

// 覆盖恢复的确认值不匹配属于客户端问题：必须是 403 + 稳定码，而不是 500 internal
// （实测：错误 confirmation 曾被兜底成 500 internal/forbidden，运维会误判为服务端故障）。
func TestJobsErrMapsForbiddenTo403(t *testing.T) {
	rec := httptest.NewRecorder()
	(&Server{}).jobsErr(rec, jobs.ErrForbidden)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Error.Code != model.ErrForbidden {
		t.Fatalf("expected code %q, got %q", model.ErrForbidden, body.Error.Code)
	}
}
