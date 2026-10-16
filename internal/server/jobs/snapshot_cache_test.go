package jobs

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/secrets"
	"backupmanagementcenter/internal/server/events"
	"backupmanagementcenter/internal/server/store"
)

type cacheTestStore struct {
	*fakeStore
	mu         sync.Mutex
	generation int64
	list       *store.SnapshotListCache
	listFresh  bool
	trees      map[string]*store.SnapshotTreeCache
}

func newCacheTestStore() *cacheTestStore {
	return &cacheTestStore{fakeStore: newFakeStore(), trees: make(map[string]*store.SnapshotTreeCache)}
}

func (s *cacheTestStore) GetRun(ctx context.Context, id string) (*model.Run, error) {
	s.fakeStore.mu.Lock()
	defer s.fakeStore.mu.Unlock()
	r, ok := s.fakeStore.runs[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	copy := *r
	if r.StartedAt != nil {
		at := *r.StartedAt
		copy.StartedAt = &at
	}
	if r.FinishedAt != nil {
		at := *r.FinishedAt
		copy.FinishedAt = &at
	}
	if r.LeaseExpiresAt != nil {
		at := *r.LeaseExpiresAt
		copy.LeaseExpiresAt = &at
	}
	if r.ScheduledAt != nil {
		at := *r.ScheduledAt
		copy.ScheduledAt = &at
	}
	copy.Progress.Sample = append([]string(nil), r.Progress.Sample...)
	return &copy, nil
}

func (s *cacheTestStore) GetSnapshotListCache(ctx context.Context, repositoryID string) (*store.SnapshotListCache, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.list == nil || !s.listFresh || s.list.RepositoryID != repositoryID || s.list.Generation != s.generation {
		return nil, store.ErrNotFound
	}
	copy := *s.list
	return &copy, nil
}

func (s *cacheTestStore) GetSnapshotTreeCache(ctx context.Context, repositoryID, snapshotID, path string) (*store.SnapshotTreeCache, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path = store.NormalizeSnapshotPath(path)
	if !s.listFresh {
		return nil, store.ErrNotFound
	}
	cached := s.trees[repositoryID+"\x00"+snapshotID+"\x00"+path]
	if cached == nil || cached.Generation != s.generation {
		return nil, store.ErrNotFound
	}
	copy := *cached
	return &copy, nil
}

func (s *cacheTestStore) GetSnapshotListBrowseCache(ctx context.Context, repositoryID string) (*store.SnapshotListCache, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.list == nil || s.list.RepositoryID != repositoryID {
		return nil, false, store.ErrNotFound
	}
	copy := *s.list
	stale := !s.listFresh || s.list.Generation != s.generation
	return &copy, stale, nil
}

func (s *cacheTestStore) GetSnapshotTreeBrowseCache(ctx context.Context, repositoryID, snapshotID, path string) (*store.SnapshotTreeCache, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path = store.NormalizeSnapshotPath(path)
	cached := s.trees[repositoryID+"\x00"+snapshotID+"\x00"+path]
	if cached == nil {
		return nil, false, store.ErrNotFound
	}
	copy := *cached
	stale := !s.listFresh || cached.Generation != s.generation
	return &copy, stale, nil
}

func (s *cacheTestStore) SnapshotCacheGeneration(ctx context.Context, repositoryID string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.repos[repositoryID]; !ok {
		return 0, store.ErrNotFound
	}
	return s.generation, nil
}

func (s *cacheTestStore) SaveSnapshotListCache(ctx context.Context, repositoryID string, generation int64, snapshotsJSON, fingerprint string, verifiedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation != s.generation {
		return store.ErrCacheGenerationChanged
	}
	var snapshots []model.Snapshot
	if err := json.Unmarshal([]byte(snapshotsJSON), &snapshots); err != nil {
		return err
	}
	s.list = &store.SnapshotListCache{RepositoryID: repositoryID, Generation: generation, SnapshotsJSON: snapshotsJSON, Fingerprint: fingerprint, VerifiedAt: verifiedAt}
	s.listFresh = true
	ids := make(map[string]bool, len(snapshots))
	for _, snapshot := range snapshots {
		ids[snapshot.ID] = true
	}
	for key, tree := range s.trees {
		if tree.RepositoryID == repositoryID {
			if !ids[tree.SnapshotID] {
				delete(s.trees, key)
			} else {
				tree.Generation = generation
			}
		}
	}
	return nil
}

func (s *cacheTestStore) SaveSnapshotTreeCache(ctx context.Context, repositoryID, snapshotID, path string, generation int64, treeJSON string, verifiedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation != s.generation || !s.listFresh {
		return store.ErrCacheGenerationChanged
	}
	path = store.NormalizeSnapshotPath(path)
	key := repositoryID + "\x00" + snapshotID + "\x00" + path
	s.trees[key] = &store.SnapshotTreeCache{RepositoryID: repositoryID, SnapshotID: snapshotID, Path: path, Generation: generation, TreeJSON: treeJSON, VerifiedAt: verifiedAt}
	return nil
}

func (s *cacheTestStore) InvalidateSnapshotCache(ctx context.Context, repositoryID string, clearTrees bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generation++
	s.listFresh = false
	if clearTrees {
		for key, tree := range s.trees {
			if tree.RepositoryID == repositoryID {
				delete(s.trees, key)
			}
		}
	}
	return nil
}

type browseDispatcher struct {
	*fakeDispatcher
	st      *cacheTestStore
	delay   <-chan struct{}
	started chan struct{}
	fail    bool
}

func newBrowseDispatcher(st *cacheTestStore) *browseDispatcher {
	return &browseDispatcher{fakeDispatcher: newFakeDispatcher(), st: st}
}

func (d *browseDispatcher) Enqueue(ctx context.Context, runID, agentID, repositoryID string) {
	d.fakeDispatcher.Enqueue(ctx, runID, agentID, repositoryID)
	if d.started != nil {
		select {
		case d.started <- struct{}{}:
		default:
		}
	}
	go func() {
		if d.delay != nil {
			<-d.delay
		}
		run, err := d.st.GetRun(ctx, runID)
		if err != nil {
			return
		}
		_ = d.st.TransitionRun(ctx, runID, model.RunQueued, model.RunDispatched, nil)
		_ = d.st.TransitionRun(ctx, runID, model.RunDispatched, model.RunRunning, nil)
		_ = d.st.TransitionRun(ctx, runID, model.RunRunning, model.RunSucceeded, func(r *model.Run) {
			if d.fail {
				r.Status = model.RunFailed
				r.ErrorMessage = "remote browse failed"
				return
			}
			if run.Operation == model.OpSnapshots {
				payload, _ := json.Marshal([]model.Snapshot{{ID: "snap-1", Host: "host"}})
				r.ProgressJSON = string(payload)
			} else {
				payload, _ := json.Marshal(&TreeResult{Path: "/", Entries: []TreeEntry{{Name: "file", Type: "file"}}})
				r.ProgressJSON = string(payload)
			}
		})
	}()
}

func setupBrowse(t *testing.T) (*Orchestrator, *cacheTestStore, *browseDispatcher) {
	t.Helper()
	st := newCacheTestStore()
	d := newBrowseDispatcher(st)
	seal, _ := secrets.NewSealer(fakeKey())
	o := New(st, seal, d, events.New(), "inst-1")
	agent := testAgent()
	st.agents[agent.ID] = agent
	target := testTarget(o.Seal)
	if err := st.CreateStorageTarget(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	testRepo(o.Seal, st.fakeStore, target)
	return o, st, d
}

func TestSnapshotsCacheHitAndRefresh(t *testing.T) {
	o, _, d := setupBrowse(t)
	ctx := context.Background()
	if _, _, info, err := o.SnapshotsWithOptions(ctx, "repo-1", "agent-1", false); err != nil || info.Hit {
		t.Fatalf("first snapshots = hit %v, err %v", info.Hit, err)
	}
	if _, _, info, err := o.SnapshotsWithOptions(ctx, "repo-1", "agent-1", false); err != nil || !info.Hit {
		t.Fatalf("second snapshots = hit %v, err %v", info.Hit, err)
	}
	if got := len(d.Enqueued()); got != 1 {
		t.Fatalf("cache hit enqueued %d runs", got)
	}
	if _, _, info, err := o.SnapshotsWithOptions(ctx, "repo-1", "agent-1", true); err != nil || info.Hit {
		t.Fatalf("refresh snapshots = hit %v, err %v", info.Hit, err)
	}
	if got := len(d.Enqueued()); got != 2 {
		t.Fatalf("refresh enqueued %d runs", got)
	}
}

func TestSnapshotTreeCacheHit(t *testing.T) {
	o, _, d := setupBrowse(t)
	ctx := context.Background()
	if _, _, _, err := o.SnapshotTreeWithOptions(ctx, "repo-1", "agent-1", "snap-1", "", false); err != nil {
		t.Fatal(err)
	}
	if _, _, info, err := o.SnapshotTreeWithOptions(ctx, "repo-1", "agent-1", "snap-1", "/", false); err != nil || !info.Hit {
		t.Fatalf("tree cache hit = %v, err %v", info.Hit, err)
	}
	if got := len(d.Enqueued()); got != 2 {
		t.Fatalf("tree cache hit enqueued %d runs", got)
	}
}

func TestSnapshotBrowseFailureDoesNotReplaceCache(t *testing.T) {
	o, _, d := setupBrowse(t)
	ctx := context.Background()
	if _, _, _, err := o.SnapshotsWithOptions(ctx, "repo-1", "agent-1", false); err != nil {
		t.Fatal(err)
	}
	d.fail = true
	if _, _, _, err := o.SnapshotsWithOptions(ctx, "repo-1", "agent-1", true); err == nil {
		t.Fatal("failed refresh returned success")
	}
	d.fail = false
	if _, _, info, err := o.SnapshotsWithOptions(ctx, "repo-1", "agent-1", false); err != nil || !info.Hit {
		t.Fatalf("old cache after failed refresh = hit %v, err %v", info.Hit, err)
	}
}

func TestSnapshotBrowseFlightMergesMisses(t *testing.T) {
	o, _, d := setupBrowse(t)
	delay := make(chan struct{})
	d.delay = delay
	d.started = make(chan struct{}, 1)
	ctx := context.Background()
	results := make(chan error, 2)
	go func() { _, _, _, err := o.SnapshotsWithOptions(ctx, "repo-1", "agent-1", false); results <- err }()
	<-d.started
	go func() { _, _, _, err := o.SnapshotsWithOptions(ctx, "repo-1", "agent-1", false); results <- err }()
	close(delay)
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	if got := len(d.Enqueued()); got != 1 {
		t.Fatalf("merged misses enqueued %d runs", got)
	}
}

func TestWarmSnapshotCache(t *testing.T) {
	o, st, d := setupBrowse(t)
	ctx := context.Background()

	// 1. 成功预热：强制刷新列表并拉取根目录
	if err := o.WarmSnapshotCache(ctx, "repo-1", "agent-1", "snap-1"); err != nil {
		t.Fatalf("WarmSnapshotCache failed: %v", err)
	}
	if got := len(d.Enqueued()); got != 2 {
		t.Fatalf("expected 2 enqueued runs (snapshots + tree), got %d", got)
	}

	// 再次读取列表和根目录均为缓存命中，无额外排队运行
	if _, _, info, err := o.SnapshotsWithOptions(ctx, "repo-1", "agent-1", false); err != nil || !info.Hit {
		t.Fatalf("snapshots hit after warm: hit=%v, err=%v", info.Hit, err)
	}
	if _, _, info, err := o.SnapshotTreeWithOptions(ctx, "repo-1", "agent-1", "snap-1", "/", false); err != nil || !info.Hit {
		t.Fatalf("tree hit after warm: hit=%v, err=%v", info.Hit, err)
	}
	if got := len(d.Enqueued()); got != 2 {
		t.Fatalf("cache read after warm should not enqueue extra runs, got %d", got)
	}

	// 2. 空快照 ID：只刷新列表，不拉取树
	enqueuedBefore := len(d.Enqueued())
	if err := o.WarmSnapshotCache(ctx, "repo-1", "agent-1", ""); err != nil {
		t.Fatalf("empty snapshotID WarmSnapshotCache failed: %v", err)
	}
	if got := len(d.Enqueued()) - enqueuedBefore; got != 1 {
		t.Fatalf("empty snapshotID should only enqueue 1 list run, got %d", got)
	}

	// 3. 快照 ID 不在列表中：返回 ErrNotFound，不拉取树
	enqueuedBefore = len(d.Enqueued())
	if err := o.WarmSnapshotCache(ctx, "repo-1", "agent-1", "missing-snap"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for missing snapshot, got %v", err)
	}
	if got := len(d.Enqueued()) - enqueuedBefore; got != 1 {
		t.Fatalf("missing snapshot should only enqueue list run, got %d", got)
	}
	if _, err := st.GetSnapshotTreeCache(ctx, "repo-1", "missing-snap", "/"); err == nil {
		t.Fatal("tree cache must not be written for missing snapshot")
	}

	// 4. 远端失败：返回错误，根树不被写入
	d.fail = true
	_ = st.InvalidateSnapshotCache(ctx, "repo-1", true)
	if err := o.WarmSnapshotCache(ctx, "repo-1", "agent-1", "snap-1"); err == nil {
		t.Fatal("expected error on remote failure, got nil")
	}
	if _, err := st.GetSnapshotTreeCache(ctx, "repo-1", "snap-1", "/"); err == nil {
		t.Fatal("tree cache must not be written when remote check fails")
	}
}
