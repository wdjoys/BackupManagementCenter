package agent

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	bmcv1 "backupmanagementcenter/api/proto/v1"
	"backupmanagementcenter/internal/agent/backup"
	"backupmanagementcenter/internal/agent/pipeline"
	"backupmanagementcenter/internal/model"
	"google.golang.org/grpc/metadata"
)

// fakePipelineExecute implements the executeFn signature for testing.
type fakePipelineExecute struct {
	mu        sync.Mutex
	calls     int
	blockWait chan struct{}
	errorRet  error
	resultRet *pipeline.Result
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
		blockWait: make(chan struct{}),
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
		blockWait: make(chan struct{}),
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

func (s *fakeStream) Context() context.Context     { return context.Background() }
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

// BMC_AGENT_MAX_CONCURRENCY 的全局槽位此前**没有任何测试覆盖**。上限语义：同 Agent 上
// 同时执行的运行数不超过 MaxConcurrency，多余的等待（而不是失败或无限并行）。
// 这里用空 repository 参数避免仓库锁参与（此处只测全局槽位）。
func TestRunner_MaxConcurrencyCapsParallelExecutions(t *testing.T) {
	const cap = 2
	ident := &Identity{
		AgentID:   "test-agent",
		SecretHex: "aabbccddee0011223344556677889900aabbccddee0011223344556677889900",
	}
	deps := pipeline.Deps{
		Tools:          make(map[string]backup.ToolInfo),
		Exec:           &OSExecutor{},
		MaxConcurrency: cap,
	}
	runner := NewRunner(deps, t.TempDir(), ident)

	total := cap * 3
	finished := make(chan struct{}, total)
	var mu sync.Mutex
	cur, peak := 0, 0
	runner.executeFn = func(context.Context, pipeline.Deps, string, bmcv1.ExecuteCommand_Operation, []byte, backup.SecretBundle) (*pipeline.Result, error) {
		mu.Lock()
		cur++
		if cur > peak {
			peak = cur
		}
		mu.Unlock()
		time.Sleep(40 * time.Millisecond)
		mu.Lock()
		cur--
		mu.Unlock()
		finished <- struct{}{}
		return &pipeline.Result{}, nil
	}

	stream := &fakeStream{}
	ctx := context.Background()
	// Execute 立即返回、真正的执行在内部 goroutine 中，因此用完成信号同步。
	for i := 0; i < total; i++ {
		runner.Execute(ctx, stream, &bmcv1.ExecuteCommand{
			CommandId:  fmt.Sprintf("cmd-%d", i),
			RunId:      fmt.Sprintf("run-%d", i),
			Operation:  bmcv1.ExecuteCommand_BACKUP,
			ParamsJson: []byte("{}"),
		})
	}
	deadline := time.After(10 * time.Second)
	done := 0
	for done < total {
		select {
		case <-finished:
			done++
		case <-deadline:
			t.Fatalf("only %d/%d executions completed", done, total)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if peak > cap {
		t.Fatalf("concurrent executions peaked at %d, must not exceed MaxConcurrency=%d", peak, cap)
	}
	if peak != cap {
		t.Fatalf("expected the cap to be reached (%d), peaked at %d — parallel capacity may be broken", cap, peak)
	}
}

// MaxConcurrency=0（或负）按实现表示"不限制并发"（slots 为 nil）。该语义此前未文档化，
// 用测试锚定，避免将来被误改成"一个都不执行"。与上限用例同构，仅输入不同。
func TestRunner_MaxConcurrencyZeroMeansUnlimited(t *testing.T) {
	const total = 6
	ident := &Identity{
		AgentID:   "test-agent",
		SecretHex: "aabbccddee0011223344556677889900aabbccddee0011223344556677889900",
	}
	deps := pipeline.Deps{Tools: make(map[string]backup.ToolInfo), Exec: &OSExecutor{}} // MaxConcurrency 缺省为 0
	runner := NewRunner(deps, t.TempDir(), ident)

	finished := make(chan struct{}, total)
	var mu sync.Mutex
	cur, peak := 0, 0
	runner.executeFn = func(context.Context, pipeline.Deps, string, bmcv1.ExecuteCommand_Operation, []byte, backup.SecretBundle) (*pipeline.Result, error) {
		mu.Lock()
		cur++
		if cur > peak {
			peak = cur
		}
		mu.Unlock()
		time.Sleep(40 * time.Millisecond)
		mu.Lock()
		cur--
		mu.Unlock()
		finished <- struct{}{}
		return &pipeline.Result{}, nil
	}

	stream := &fakeStream{}
	for i := 0; i < total; i++ {
		runner.Execute(context.Background(), stream, &bmcv1.ExecuteCommand{
			CommandId:  fmt.Sprintf("ul-cmd-%d", i),
			RunId:      fmt.Sprintf("ul-run-%d", i),
			Operation:  bmcv1.ExecuteCommand_BACKUP,
			ParamsJson: []byte("{}"),
		})
	}
	deadline := time.After(10 * time.Second)
	done := 0
	for done < total {
		select {
		case <-finished:
			done++
		case <-deadline:
			t.Fatalf("only %d/%d executions completed", done, total)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if peak != total {
		t.Fatalf("MaxConcurrency=0 must not serialize: peak=%d, want %d", peak, total)
	}
}

type streamProbeExecute struct{ onRun func(pipeline.Deps) }

func (p *streamProbeExecute) Execute(_ context.Context, d pipeline.Deps, _ string, _ bmcv1.ExecuteCommand_Operation, _ []byte, _ backup.SecretBundle) (*pipeline.Result, error) {
	if p.onRun != nil {
		p.onRun(d)
	}
	return &pipeline.Result{SnapshotIDs: []string{"snap-1"}}, nil
}

func sentKinds(s *fakeStream) map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for _, m := range s.sent {
		switch {
		case m.GetRunLogBatch() != nil:
			out["log"]++
		case m.GetRunProgress() != nil:
			out["progress"]++
		case m.GetRunResult() != nil:
			out["result"]++
		default:
			out["other"]++
		}
	}
	return out
}

// run 可能跨越重连（服务端重启/网络抖动）：日志、进度与结果必须发到**当前**连接。
// 闭包捕获派发时的流会让重连后的发送全部 EOF（实测进度与日志丢失，run 只能等下一次
// 重派 + 结果重放才收敛）。
func TestRunnerSinksFollowCurrentStreamAfterReconnect(t *testing.T) {
	ident := &Identity{
		AgentID:   "test-agent",
		SecretHex: "aabbccddee0011223344556677889900aabbccddee0011223344556677889900",
	}
	runner := NewRunner(pipeline.Deps{Tools: make(map[string]backup.ToolInfo)}, t.TempDir(), ident)

	streamA := &fakeStream{}
	streamB := &fakeStream{}
	runner.SetStream(streamA)

	runner.executeFn = (&streamProbeExecute{onRun: func(d pipeline.Deps) {
		runner.SetStream(streamB) // 模拟重连成功
		d.Logf("info", "after-reconnect-log")
		d.Progress(model.Progress{Phase: "dumping", Percent: 50})
	}}).Execute

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runner.Execute(ctx, streamA, &bmcv1.ExecuteCommand{
		CommandId: "cmd-1", RunId: "run-1",
		Operation: bmcv1.ExecuteCommand_BACKUP, ParamsJson: []byte(`{}`),
	})
	time.Sleep(300 * time.Millisecond)

	gotB := sentKinds(streamB)
	if gotB["log"] == 0 || gotB["progress"] == 0 || gotB["result"] == 0 {
		t.Fatalf("重连后的日志/进度/结果必须发到新连接，实际 %v", gotB)
	}
	gotA := sentKinds(streamA)
	if gotA["log"] != 0 || gotA["progress"] != 0 || gotA["result"] != 0 {
		t.Fatalf("旧连接不应再收到日志/进度/结果，实际 %v", gotA)
	}
}
