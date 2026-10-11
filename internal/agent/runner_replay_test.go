package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc/metadata"

	bmcv1 "backupmanagementcenter/api/proto/v1"
	"backupmanagementcenter/internal/agent/backup"
	"backupmanagementcenter/internal/agent/pipeline"
)

// ccReplayStream 是可控失败的客户端流：Send 失败用于模拟"结果发出时流已被替换"。
type ccReplayStream struct {
	mu   sync.Mutex
	sent []*bmcv1.AgentMessage
	fail bool
}

func (s *ccReplayStream) Send(msg *bmcv1.AgentMessage) error {
	if s.fail {
		return fmt.Errorf("EOF")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, msg)
	return nil
}

func (s *ccReplayStream) Recv() (*bmcv1.ServerMessage, error) { return nil, fmt.Errorf("no messages") }
func (s *ccReplayStream) CloseSend() error                    { return nil }
func (s *ccReplayStream) Context() context.Context            { return context.Background() }
func (s *ccReplayStream) Header() (metadata.MD, error)        { return metadata.MD{}, nil }
func (s *ccReplayStream) Trailer() metadata.MD                { return metadata.MD{} }
func (s *ccReplayStream) SendMsg(any) error                   { return nil }
func (s *ccReplayStream) RecvMsg(any) error                   { return nil }

func (s *ccReplayStream) results() []*bmcv1.RunResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*bmcv1.RunResult, 0, len(s.sent))
	for _, m := range s.sent {
		if r := m.GetRunResult(); r != nil {
			out = append(out, r)
		}
	}
	return out
}

func ccReplayRunner(t *testing.T) *Runner {
	t.Helper()
	ident := &Identity{AgentID: "test-agent", SecretHex: strings.Repeat("ab", 32)}
	return NewRunner(pipeline.Deps{Tools: make(map[string]backup.ToolInfo)}, t.TempDir(), ident)
}

// 结果只发一次会在流被替换/断开的瞬间永久丢失：服务端把 run 留在 running 直到看门狗
// 超时，期间该仓库的 FIFO 队列被阻塞。发送失败的结果必须暂存，并在下次连接建立时补发。
func TestRunnerReplaysResultLostOnStreamReplacement(t *testing.T) {
	runner := ccReplayRunner(t)
	dead := &ccReplayStream{fail: true}
	live := &ccReplayStream{}
	runner.SetStream(dead)

	runner.sendRunResult(&bmcv1.RunResult{
		RunId: "run-1", Status: bmcv1.RunResult_SUCCEEDED, SnapshotIds: []string{"snap-1"},
	})

	if n := len(runner.pendingResults); n != 1 {
		t.Fatalf("发送失败的结果必须暂存待补发，pending=%d", n)
	}
	if got := len(dead.results()); got != 0 {
		t.Fatalf("失败的流不应记录结果，got %d", got)
	}

	runner.SetStream(live) // 模拟重连成功

	if n := len(runner.pendingResults); n != 0 {
		t.Fatalf("重连后必须清空待补发队列，pending=%d", n)
	}
	got := live.results()
	if len(got) != 1 || got[0].GetRunId() != "run-1" || len(got[0].GetSnapshotIds()) != 1 {
		t.Fatalf("重连后必须补发原结果，got %+v", got)
	}
}

// 补发本身再次失败时结果必须留在队列里，等下一次连接——否则等于换了个地方丢结果。
func TestRunnerKeepsResultWhenReplayAlsoFails(t *testing.T) {
	runner := ccReplayRunner(t)
	runner.SetStream(&ccReplayStream{fail: true})
	runner.sendRunResult(&bmcv1.RunResult{RunId: "run-2", Status: bmcv1.RunResult_SUCCEEDED})

	runner.SetStream(&ccReplayStream{fail: true}) // 重连后仍然发不出去
	if n := len(runner.pendingResults); n != 1 {
		t.Fatalf("补发失败必须保留结果，pending=%d", n)
	}
	live := &ccReplayStream{}
	runner.SetStream(live)
	if n := len(runner.pendingResults); n != 0 || len(live.results()) != 1 {
		t.Fatalf("下一次连接必须补发，pending=%d sent=%d", n, len(live.results()))
	}
}

// Send 返回 nil 不等于送达：网络断开时 gRPC 只把消息放进本地写队列就返回 nil，结果
// 被静默丢弃（实测 `docker network disconnect`），run 卡在 running 直到看门狗租约
// 到期。发出后没收到任何服务端消息的结果必须在换流时重新入队补发。
func TestRunnerRequeuesResultSentButUnackedOnStreamSwitch(t *testing.T) {
	runner := ccReplayRunner(t)
	dead := &ccReplayStream{} // Send 返回 nil，但服务端收不到（连接已断）
	runner.SetStream(dead)

	runner.sendRunResult(&bmcv1.RunResult{
		RunId: "run-lost", Status: bmcv1.RunResult_SUCCEEDED, SnapshotIds: []string{"snap-1"},
	})
	if n := len(dead.results()); n != 1 {
		t.Fatalf("Send 成功时旧流会记下结果，got %d", n)
	}

	live := &ccReplayStream{}
	runner.SetStream(live) // 模拟断流后重连

	got := live.results()
	if len(got) != 1 || got[0].GetRunId() != "run-lost" || len(got[0].GetSnapshotIds()) != 1 {
		t.Fatalf("未确认的结果必须在重连后补发，got %+v", got)
	}
	if n := len(runner.pendingResults); n != 0 {
		t.Fatalf("补发后队列必须清空，pending=%d", n)
	}
}

// 收到服务端消息即证明连接双向可达，此时结果视为已送达：换流不得重复补发。
func TestRunnerDoesNotReplayConfirmedResult(t *testing.T) {
	runner := ccReplayRunner(t)
	runner.SetStream(&ccReplayStream{})
	runner.sendRunResult(&bmcv1.RunResult{RunId: "run-ok", Status: bmcv1.RunResult_SUCCEEDED})

	runner.confirmSentResults() // 服务端回消息了

	live := &ccReplayStream{}
	runner.SetStream(live)
	if got := live.results(); len(got) != 0 {
		t.Fatalf("已确认送达的结果不应重复补发，got %+v", got)
	}
}
