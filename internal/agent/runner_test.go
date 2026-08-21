package agent

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"


	"google.golang.org/grpc/metadata"
	bmcv1 "backupmanagementcenter/api/proto/v1"
	"backupmanagementcenter/internal/agent/backup"
	"backupmanagementcenter/internal/agent/pipeline"
)

// fakePipelineExecute implements the executeFn signature for testing.
type fakePipelineExecute struct {
	mu         sync.Mutex
	calls      int
	blockWait  chan struct{}
	errorRet   error
	resultRet  *pipeline.Result
}

func (f *fakePipelineExecute) Execute(ctx context.Context, d pipeline.Deps, tempDir string, op bmcv1.ExecuteCommand_Operation, params []byte, secrets backup.SecretBundle) (*pipeline.Result, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()

	// Wait for context cancellation or unblock
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.blockWait:
		// Continue
	}

	return f.resultRet, f.errorRet
}

func TestRunner_IdempotentReplay(t *testing.T) {
	dataDir := t.TempDir()
	ident := &Identity{
		AgentID:   "test-agent",
		SecretHex: "aabbccddee0011223344556677889900aabbccddee0011223344556677889900",
	}

	fake := &fakePipelineExecute{
		resultRet: &pipeline.Result{
			SnapshotIDs: []string{"snap-123"},
			ResultJSON:  []byte(`{"ok": true}`),
		},
	}

	deps := pipeline.Deps{
		Tools: make(map[string]backup.ToolInfo),
		Exec:  &OSExecutor{},
	}
	runner := NewRunner(deps, dataDir, ident)
	runner.executeFn = fake.Execute

	_, received := newFakeStream()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := &bmcv1.ExecuteCommand{
		CommandId:  "cmd-1",
		RunId:      "run-1",
		Operation:  bmcv1.ExecuteCommand_BACKUP,
		ParamsJson: []byte(`{}`),
	}

	runner.Execute(ctx, received, cmd)
	time.Sleep(200 * time.Millisecond)

	fake.mu.Lock()
	calls := fake.calls
	fake.mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected 1 pipeline call, got %d", calls)
	}

	// Re-send same run_id — should be replayed from cache
	cmd2 := &bmcv1.ExecuteCommand{
		CommandId:  "cmd-1",
		RunId:      "run-1",
		Operation:  bmcv1.ExecuteCommand_BACKUP,
		ParamsJson: []byte(`{}`),
	}
	runner.Execute(ctx, received, cmd2)
	time.Sleep(100 * time.Millisecond)

	fake.mu.Lock()
	calls = fake.calls
	fake.mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected 1 pipeline call after idempotent replay, got %d", calls)
	}
}

func TestRunner_DuplicateCommandIdIgnored(t *testing.T) {
	dataDir := t.TempDir()
	ident := &Identity{
		AgentID:   "test-agent",
		SecretHex: "aabbccddee0011223344556677889900aabbccddee0011223344556677889900",
	}

	fake := &fakePipelineExecute{
		blockWait:   make(chan struct{}),
		resultRet: &pipeline.Result{
			SnapshotIDs: []string{"snap-123"},
		},
	}

	deps := pipeline.Deps{
		Tools: make(map[string]backup.ToolInfo),
		Exec:  &OSExecutor{},
	}
	runner := NewRunner(deps, dataDir, ident)
	runner.executeFn = fake.Execute

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, received := newFakeStream()

	cmd := &bmcv1.ExecuteCommand{
		CommandId:  "cmd-1",
		RunId:      "run-1",
		Operation:  bmcv1.ExecuteCommand_BACKUP,
		ParamsJson: []byte(`{}`),
	}

	// First call starts execution (blocked)
	runner.Execute(ctx, received, cmd)
	time.Sleep(100 * time.Millisecond)

	// Second call with same command_id should be ignored
	runner.Execute(ctx, received, cmd)
	time.Sleep(100 * time.Millisecond)

	fake.mu.Lock()
	calls := fake.calls
	fake.mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected 1 pipeline call, got %d (duplicate should be ignored)", calls)
	}

	close(fake.blockWait)
}

func TestRunner_CancelCommand(t *testing.T) {
	dataDir := t.TempDir()
	ident := &Identity{
		AgentID:   "test-agent",
		SecretHex: "aabbccddee0011223344556677889900aabbccddee0011223344556677889900",
	}

	fake := &fakePipelineExecute{
		blockWait:   make(chan struct{}),
		resultRet: &pipeline.Result{
			SnapshotIDs: []string{"snap-123"},
		},
	}

	deps := pipeline.Deps{
		Tools: make(map[string]backup.ToolInfo),
		Exec:  &OSExecutor{},
	}
	runner := NewRunner(deps, dataDir, ident)
	runner.executeFn = fake.Execute

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, received := newFakeStream()

	cmd := &bmcv1.ExecuteCommand{
		CommandId:  "cmd-1",
		RunId:      "run-1",
		Operation:  bmcv1.ExecuteCommand_BACKUP,
		ParamsJson: []byte(`{}`),
	}

	runner.Execute(ctx, received, cmd)
	time.Sleep(100 * time.Millisecond)

	// Cancel
	runner.Cancel("run-1")
	time.Sleep(300 * time.Millisecond)

	fake.mu.Lock()
	calls := fake.calls
	fake.mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected 1 pipeline call, got %d", calls)
	}
}

func TestRunner_UnknownCancel(t *testing.T) {
	dataDir := t.TempDir()
	ident := &Identity{
		AgentID:   "test-agent",
		SecretHex: "aabbccddee0011223344556677889900aabbccddee0011223344556677889900",
	}

	deps := pipeline.Deps{
		Tools: make(map[string]backup.ToolInfo),
		Exec:  &OSExecutor{},
	}
	runner := NewRunner(deps, dataDir, ident)

	// Should not panic
	runner.Cancel("unknown-run-id")
}

func TestRunner_FinishedCacheIdempotency(t *testing.T) {
	dataDir := t.TempDir()
	ident := &Identity{
		AgentID:   "test-agent",
		SecretHex: "aabbccddee0011223344556677889900aabbccddee0011223344556677889900",
	}

	fake := &fakePipelineExecute{
		resultRet: &pipeline.Result{
			SnapshotIDs: []string{"snap-123"},
		},
	}

	deps := pipeline.Deps{
		Tools: make(map[string]backup.ToolInfo),
		Exec:  &OSExecutor{},
	}
	runner := NewRunner(deps, dataDir, ident)
	runner.executeFn = fake.Execute

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, received := newFakeStream()

	cmd := &bmcv1.ExecuteCommand{
		CommandId:  "cmd-1",
		RunId:      "run-1",
		Operation:  bmcv1.ExecuteCommand_BACKUP,
		ParamsJson: []byte(`{}`),
	}
	runner.Execute(ctx, received, cmd)

	time.Sleep(200 * time.Millisecond)

	// Different command_id, same run_id
	cmd2 := &bmcv1.ExecuteCommand{
		CommandId:  "cmd-2",
		RunId:      "run-1",
		Operation:  bmcv1.ExecuteCommand_RESTORE,
		ParamsJson: []byte(`{}`),
	}
	runner.Execute(ctx, received, cmd2)

	time.Sleep(100 * time.Millisecond)

	fake.mu.Lock()
	calls := fake.calls
	fake.mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected 1 pipeline call (idempotent replay), got %d", calls)
	}
}

// --- LRU Cache tests ---

func TestLRUCache_Basic(t *testing.T) {
	c := newLRUCache(3)
	c.put("a", &bmcv1.RunResult{RunId: "a"})
	c.put("b", &bmcv1.RunResult{RunId: "b"})
	c.put("c", &bmcv1.RunResult{RunId: "c"})

	if c.get("a") == nil {
		t.Fatal("expected 'a' to exist")
	}
	if c.get("b") == nil {
		t.Fatal("expected 'b' to exist")
	}
	if c.get("c") == nil {
		t.Fatal("expected 'c' to exist")
	}

	c.put("d", &bmcv1.RunResult{RunId: "d"})
	if c.get("a") != nil {
		t.Fatal("expected 'a' to be evicted")
	}
	if c.get("b") == nil {
		t.Fatal("expected 'b' to still exist")
	}
	if c.get("c") == nil {
		t.Fatal("expected 'c' to still exist")
	}
}

func TestLRUCache_MoveToFront(t *testing.T) {
	c := newLRUCache(3)
	c.put("a", &bmcv1.RunResult{RunId: "a"})
	c.put("b", &bmcv1.RunResult{RunId: "b"})
	c.put("c", &bmcv1.RunResult{RunId: "c"})

	_ = c.get("a")

	c.put("d", &bmcv1.RunResult{RunId: "d"})

	if c.get("a") == nil {
		t.Fatal("expected 'a' to still exist (moved to front)")
	}
	if c.get("b") != nil {
		t.Fatal("expected 'b' to be evicted (was LRU)")
	}
	if c.get("c") == nil {
		t.Fatal("expected 'c' to still exist")
	}
}

func TestLRUCache_GetNonExistent(t *testing.T) {
	c := newLRUCache(5)
	if c.get("nonexistent") != nil {
		t.Fatal("expected nil for non-existent key")
	}
}

func TestLRUCache_Update(t *testing.T) {
	c := newLRUCache(3)
	c.put("a", &bmcv1.RunResult{RunId: "a"})
	c.put("b", &bmcv1.RunResult{RunId: "b"})
	c.put("c", &bmcv1.RunResult{RunId: "c"})

	// Update existing key: value replaced, key becomes most recent.
	newResult := &bmcv1.RunResult{RunId: "a"}
	c.put("a", newResult)
	if c.get("a") != newResult {
		t.Fatal("expected updated value for 'a'")
	}
	// Recency now: a (newest), c, b (oldest).
	if c.get("b") == nil {
		t.Fatal("expected 'b' to still exist")
	}
	if c.get("c") == nil {
		t.Fatal("expected 'c' to still exist")
	}

	// get() refreshes recency: after touching b then c, b is oldest.
	_ = c.get("a") // a newest
	_ = c.get("b")
	_ = c.get("c") // c newest; order: c, b, a

	c.put("d", &bmcv1.RunResult{RunId: "d"})
	if c.get("d") == nil {
		t.Fatal("expected 'd' to exist")
	}
	if c.get("a") != nil {
		t.Fatal("expected 'a' to be evicted (least recently used)")
	}
	if c.get("b") == nil || c.get("c") == nil {
		t.Fatal("expected 'b' and 'c' to still exist")
	}
}
// --- Fake stream ---

type fakeStream struct {
	mu   sync.Mutex
	sent []*bmcv1.AgentMessage
}

func (s *fakeStream) CloseSend() error { return nil }

func (s *fakeStream) Context() context.Context { return context.Background() }
func (s *fakeStream) Header() (metadata.MD, error) { return metadata.MD{}, nil }

func (s *fakeStream) Trailer() metadata.MD { return metadata.MD{} }

func (s *fakeStream) SendMsg(any) error { return nil }

func (s *fakeStream) RecvMsg(any) error { return nil }

func (s *fakeStream) Send(msg *bmcv1.AgentMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, msg)
	return nil
}

func (s *fakeStream) Recv() (*bmcv1.ServerMessage, error) {
	select {
	case <-time.After(time.Second):
		return nil, fmt.Errorf("timeout")
	default:
		return nil, fmt.Errorf("no messages")
	}
}

func newFakeStream() (server, client *fakeStream) {
	return &fakeStream{}, &fakeStream{}
}

var _ = runtime.GOOS