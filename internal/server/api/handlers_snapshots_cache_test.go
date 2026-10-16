package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/server/events"
	"backupmanagementcenter/internal/server/jobs"
	"backupmanagementcenter/internal/server/store"
)

type testDispatcher struct{}

func (d *testDispatcher) Enqueue(ctx context.Context, runID, agentID, repositoryID string) {}
func (d *testDispatcher) Cancel(ctx context.Context, runID string) error { return nil }
func (d *testDispatcher) ConnectedAgents() []string                     { return nil }
func (d *testDispatcher) IsConnected(agentID string) bool               { return false }

func setupTestRepoAndSnapshots(t *testing.T, s *Server, st store.Store) (*model.Repository, store.SnapshotCacheStore) {
	s.Jobs = jobs.New(st, nil, &testDispatcher{}, events.New(), "inst-1")
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	agent := &model.Agent{
		ID:         "agent-snap-1",
		Name:       "agent-snap-1",
		Hostname:   "host-snap-1",
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

	target := &model.StorageTarget{
		ID:              "target-snap-1",
		Name:            "target-1",
		Type:            "rclone",
		RemoteName:      "remote",
		RemotePath:      "/tmp/repo",
		EncryptedConfig: []byte("cfg"),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := st.CreateStorageTarget(ctx, target); err != nil {
		t.Fatalf("CreateStorageTarget: %v", err)
	}

	repo := &model.Repository{
		ID:                "repo-snap-1",
		AgentID:           agent.ID,
		StorageTargetID:   target.ID,
		RepositoryPath:    "/tmp/repo",
		EncryptedPassword: []byte("pass"),
		Status:            "ready",
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := st.CreateRepository(ctx, repo); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	cs, ok := st.(store.SnapshotCacheStore)
	if !ok {
		t.Fatalf("store does not implement SnapshotCacheStore")
	}
	return repo, cs
}

func TestSnapshotListAndTreeCachedMode(t *testing.T) {
	s, st, cleanup := newTestServerWithAdmin(t)
	defer cleanup()

	handler := New(s)
	cookie := loginTestAdmin(t, handler)
	repo, cs := setupTestRepoAndSnapshots(t, s, st)
	ctx := context.Background()

	// 1. 冷缓存（无任何缓存记录）：GET ?cached=1 返回 204 No Content
	req := httptest.NewRequest(http.MethodGet, "/api/v1/repositories/"+repo.ID+"/snapshots?cached=1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for cold list cache, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	// 树冷缓存同样返回 204
	req = httptest.NewRequest(http.MethodGet, "/api/v1/snapshots/snap-1/tree?repo="+repo.ID+"&path=/&cached=1", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for cold tree cache, got %d", rec.Code)
	}

	// 2. 存入有效缓存：cached=1 返回 200, X-BMC-Cache: HIT, 带 X-BMC-Verified-At
	gen, err := cs.SnapshotCacheGeneration(ctx, repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	snaps := []model.Snapshot{
		{ID: "snap-1", Host: "host-1", Paths: []string{"/data"}},
		{ID: "snap-2", Host: "host-1", Paths: []string{"/var"}},
	}
	snapsJSON, _ := json.Marshal(snaps)
	now := time.Now().UTC().Truncate(time.Second)
	if err := cs.SaveSnapshotListCache(ctx, repo.ID, gen, string(snapsJSON), store.SnapshotFingerprint(snaps), now); err != nil {
		t.Fatal(err)
	}
	treeJSON := `{"path":"/","entries":[{"name":"test.txt","type":"file","size":123}]}`
	if err := cs.SaveSnapshotTreeCache(ctx, repo.ID, "snap-1", "/", gen, treeJSON, now); err != nil {
		t.Fatal(err)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/repositories/"+repo.ID+"/snapshots?cached=1", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for hit list cache, got %d", rec.Code)
	}
	if rec.Header().Get("X-BMC-Cache") != "HIT" {
		t.Fatalf("expected X-BMC-Cache: HIT, got %q", rec.Header().Get("X-BMC-Cache"))
	}
	if rec.Header().Get("X-BMC-Verified-At") == "" {
		t.Fatalf("expected X-BMC-Verified-At header")
	}
	var returnedSnaps []model.Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &returnedSnaps); err != nil || len(returnedSnaps) != 2 {
		t.Fatalf("failed to decode list response: %v, len=%d", err, len(returnedSnaps))
	}

	// 树有效缓存命中
	req = httptest.NewRequest(http.MethodGet, "/api/v1/snapshots/snap-1/tree?repo="+repo.ID+"&path=/&cached=1", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for hit tree cache, got %d", rec.Code)
	}
	if rec.Header().Get("X-BMC-Cache") != "HIT" {
		t.Fatalf("expected X-BMC-Cache: HIT for tree, got %q", rec.Header().Get("X-BMC-Cache"))
	}

	// 3. 备份任务入队导致缓存失效（stale）：cached=1 返回 200, X-BMC-Cache: STALE
	if err := st.CreateRun(ctx, &model.Run{
		ID: "backup-run-stale", AgentID: repo.AgentID, Operation: model.OpBackup, Status: model.RunQueued,
		QueuedAt: time.Now().UTC(), RepositoryID: repo.ID,
	}); err != nil {
		t.Fatal(err)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/repositories/"+repo.ID+"/snapshots?cached=1", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for stale list cache, got %d", rec.Code)
	}
	if rec.Header().Get("X-BMC-Cache") != "STALE" {
		t.Fatalf("expected X-BMC-Cache: STALE, got %q", rec.Header().Get("X-BMC-Cache"))
	}

	// 树继承 stale 状态
	req = httptest.NewRequest(http.MethodGet, "/api/v1/snapshots/snap-1/tree?repo="+repo.ID+"&path=/&cached=1", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for stale tree cache, got %d", rec.Code)
	}
	if rec.Header().Get("X-BMC-Cache") != "STALE" {
		t.Fatalf("expected X-BMC-Cache: STALE for tree, got %q", rec.Header().Get("X-BMC-Cache"))
	}

	// 4. 不存在的快照树在列表校验中被判定为 404
	req = httptest.NewRequest(http.MethodGet, "/api/v1/snapshots/snap-not-exist/tree?repo="+repo.ID+"&path=/&cached=1", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for nonexistent snapshot tree, got %d", rec.Code)
	}

	// 5. 隐藏的快照（QueueSnapshotDeletion）树返回 404，且在列表中被过滤。
	// 删除前置检查依赖已验证的列表与 kind 标签（保护快照是 fail-closed 的）。
	snapsForDeletion := []model.Snapshot{
		{ID: "snap-1", Host: "host-1", Paths: []string{"/data"}, Tags: []string{"kind:filesystem"}},
		{ID: "snap-2", Host: "host-1", Paths: []string{"/var"}, Tags: []string{"kind:filesystem"}},
	}
	// 重新读取当前 generation：前面的校验流程可能已使其推进。
	curGen, genErr := cs.SnapshotCacheGeneration(ctx, repo.ID)
	if genErr != nil {
		t.Fatal(genErr)
	}
	fpJSON, _ := json.Marshal(snapsForDeletion)
	if err := cs.SaveSnapshotListCache(ctx, repo.ID, curGen, string(fpJSON), store.SnapshotFingerprint(snapsForDeletion), now); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.Jobs.QueueSnapshotDeletion(ctx, "admin-1", repo.ID, "snap-2")
	if err != nil {
		t.Fatal(err)
	}

	// 列表过滤 snap-2
	req = httptest.NewRequest(http.MethodGet, "/api/v1/repositories/"+repo.ID+"/snapshots?cached=1", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var filteredSnaps []model.Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &filteredSnaps); err != nil {
		t.Fatal(err)
	}
	if len(filteredSnaps) != 1 || filteredSnaps[0].ID != "snap-1" {
		t.Fatalf("expected only snap-1, got %v", filteredSnaps)
	}
	// snap-2 树返回 404
	req = httptest.NewRequest(http.MethodGet, "/api/v1/snapshots/snap-2/tree?repo="+repo.ID+"&path=/&cached=1", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for hidden snapshot tree, got %d", rec.Code)
	}

	// 6. refresh=1 优先级高于 cached=1，不会直接返回 STALE
	ctxTimeout, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/repositories/"+repo.ID+"/snapshots?cached=1&refresh=1", nil).WithContext(ctxTimeout)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Header().Get("X-BMC-Cache") == "STALE" {
		t.Fatalf("refresh=1 should never return STALE cache header")
	}
}

func TestSnapshotListEmptyCacheAndCorruptCache(t *testing.T) {
	s, st, cleanup := newTestServerWithAdmin(t)
	defer cleanup()

	handler := New(s)
	cookie := loginTestAdmin(t, handler)
	repo, cs := setupTestRepoAndSnapshots(t, s, st)
	ctx := context.Background()

	// 1. 空列表 [] 缓存是有效结果，应返回 200 而非 204
	gen, err := cs.SnapshotCacheGeneration(ctx, repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	emptySnaps := []model.Snapshot{}
	emptyJSON, _ := json.Marshal(emptySnaps)
	now := time.Now().UTC()
	if err := cs.SaveSnapshotListCache(ctx, repo.ID, gen, string(emptyJSON), store.SnapshotFingerprint(emptySnaps), now); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/repositories/"+repo.ID+"/snapshots?cached=1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty list cache should return 200, got %d", rec.Code)
	}
	var out []model.Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out) != 0 {
		t.Fatalf("expected empty array, got %v", out)
	}

	// 2. 损坏的 JSON 或指纹不符：应视为冷缓存返回 204
	if err := cs.SaveSnapshotListCache(ctx, repo.ID, gen, "{invalid-json}", "bad-fingerprint", now); err != nil {
		// SaveSnapshotListCache parses JSON so it would reject, test corrupting directly or fingerpint mismatch
	}
	// Save valid json with mismatched fingerprint directly
	newGen, err := cs.SnapshotCacheGeneration(ctx, repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	validSnaps := []model.Snapshot{{ID: "snap-corrupt"}}
	validJSON, _ := json.Marshal(validSnaps)
	if err := cs.SaveSnapshotListCache(ctx, repo.ID, newGen, string(validJSON), "wrong-fingerprint", now); err != nil {
		t.Fatal(err)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/repositories/"+repo.ID+"/snapshots?cached=1", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("fingerprint mismatch should return 204, got %d", rec.Code)
	}
}

type smokeBrowseDispatcher struct {
	st store.Store
}

func (d *smokeBrowseDispatcher) Enqueue(ctx context.Context, runID, agentID, repositoryID string) {
	go func() {
		run, err := d.st.GetRun(ctx, runID)
		if err != nil {
			return
		}
		_ = d.st.TransitionRun(ctx, runID, model.RunQueued, model.RunDispatched, nil)
		_ = d.st.TransitionRun(ctx, runID, model.RunDispatched, model.RunRunning, nil)
		_ = d.st.TransitionRun(ctx, runID, model.RunRunning, model.RunSucceeded, func(r *model.Run) {
			if run.Operation == model.OpSnapshots {
				payload, _ := json.Marshal([]model.Snapshot{{ID: "snap-warm-1", Host: "host-1", Paths: []string{"/data"}}})
				r.ProgressJSON = string(payload)
			} else {
				var lsTask model.SnapshotLsTask
				_ = json.Unmarshal([]byte(run.ProgressJSON), &lsTask)
				path := lsTask.Path
				if path == "" || path == "/" {
					payload, _ := json.Marshal(&jobs.TreeResult{Path: "/", Entries: []jobs.TreeEntry{{Name: "subdir", Type: "dir"}}})
					r.ProgressJSON = string(payload)
				} else {
					payload, _ := json.Marshal(&jobs.TreeResult{Path: path, Entries: []jobs.TreeEntry{{Name: "file.txt", Type: "file"}}})
					r.ProgressJSON = string(payload)
				}
			}
		})
	}()
}
func (d *smokeBrowseDispatcher) Cancel(ctx context.Context, runID string) error { return nil }
func (d *smokeBrowseDispatcher) ConnectedAgents() []string                     { return nil }
func (d *smokeBrowseDispatcher) IsConnected(agentID string) bool               { return true }

func TestSnapshotWarmCacheAndAPISmoke(t *testing.T) {
	s, st, cleanup := newTestServerWithAdmin(t)
	defer cleanup()

	disp := &smokeBrowseDispatcher{st: st}
	orch := jobs.New(st, nil, disp, events.New(), "inst-smoke")
	s.Jobs = orch

	handler := New(s)
	cookie := loginTestAdmin(t, handler)
	repo, _ := setupTestRepoAndSnapshots(t, s, st)
	s.Jobs = orch
	ctx := context.Background()

	// 1. 执行 WarmSnapshotCache 预热列表和根目录
	if err := orch.WarmSnapshotCache(ctx, repo.ID, repo.AgentID, "snap-warm-1"); err != nil {
		t.Fatalf("WarmSnapshotCache failed: %v", err)
	}

	// 2. 模拟前端打开 /snapshots: ?cached=1 查列表 -> 应返回 200, X-BMC-Cache: HIT, 带 X-BMC-Verified-At
	req := httptest.NewRequest(http.MethodGet, "/api/v1/repositories/"+repo.ID+"/snapshots?cached=1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshots?cached=1 expected 200, got %d", rec.Code)
	}
	if rec.Header().Get("X-BMC-Cache") != "HIT" {
		t.Fatalf("snapshots?cached=1 expected X-BMC-Cache: HIT, got %q", rec.Header().Get("X-BMC-Cache"))
	}
	if rec.Header().Get("X-BMC-Verified-At") == "" {
		t.Fatal("snapshots?cached=1 expected X-BMC-Verified-At header")
	}
	var snaps []model.Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snaps); err != nil || len(snaps) != 1 || snaps[0].ID != "snap-warm-1" {
		t.Fatalf("unexpected snapshot list: %v", snaps)
	}

	// 3. 模拟前端打开详情查看根目录: ?cached=1 查 path=/ -> 应返回 200, X-BMC-Cache: HIT, 带 X-BMC-Verified-At
	req = httptest.NewRequest(http.MethodGet, "/api/v1/snapshots/snap-warm-1/tree?repo="+repo.ID+"&path=/&cached=1", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tree?path=/&cached=1 expected 200, got %d", rec.Code)
	}
	if rec.Header().Get("X-BMC-Cache") != "HIT" {
		t.Fatalf("tree?path=/&cached=1 expected X-BMC-Cache: HIT, got %q", rec.Header().Get("X-BMC-Cache"))
	}
	if rec.Header().Get("X-BMC-Verified-At") == "" {
		t.Fatal("tree?path=/&cached=1 expected X-BMC-Verified-At header")
	}
	var rootTree struct {
		Path    string           `json:"path"`
		Entries []jobs.TreeEntry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rootTree); err != nil || len(rootTree.Entries) != 1 || rootTree.Entries[0].Name != "subdir" {
		t.Fatalf("unexpected root tree: %+v", rootTree)
	}

	// 4. 模拟前端点击未预热的子目录: ?cached=1 查 path=/subdir -> 应返回 204 No Content
	req = httptest.NewRequest(http.MethodGet, "/api/v1/snapshots/snap-warm-1/tree?repo="+repo.ID+"&path=/subdir&cached=1", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("tree?path=/subdir&cached=1 expected 204, got %d", rec.Code)
	}

	// 5. 模拟前端 204 后回退执行普通 GET /subdir -> 应返回 200, X-BMC-Cache: MISS, 带 X-BMC-Verified-At
	req = httptest.NewRequest(http.MethodGet, "/api/v1/snapshots/snap-warm-1/tree?repo="+repo.ID+"&path=/subdir", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tree?path=/subdir normal GET expected 200, got %d", rec.Code)
	}
	if rec.Header().Get("X-BMC-Cache") != "MISS" {
		t.Fatalf("tree?path=/subdir normal GET expected X-BMC-Cache: MISS, got %q", rec.Header().Get("X-BMC-Cache"))
	}
	if rec.Header().Get("X-BMC-Verified-At") == "" {
		t.Fatal("tree?path=/subdir normal GET expected X-BMC-Verified-At header")
	}
	var subTree struct {
		Path    string           `json:"path"`
		Entries []jobs.TreeEntry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &subTree); err != nil || len(subTree.Entries) != 1 || subTree.Entries[0].Name != "file.txt" {
		t.Fatalf("unexpected subdir tree: %+v", subTree)
	}
}
