package agent

import (
	"context"
	"testing"
)

// 优雅关闭用 CancelAll 触发在途 run 的取消路径（回滚 + 结果上报），而不是直接断开
// 流：断流会让 client.Run 立刻返回、进程退出，回滚被杀（实测阶段二因此是死代码）。
func TestCancelAllCancelsInFlightRuns(t *testing.T) {
	cancelled := 0
	r := &Runner{running: map[string]context.CancelFunc{}}
	r.running["run-1"] = func() { cancelled++ }
	r.running["run-2"] = func() { cancelled++ }

	if n := r.CancelAll(); n != 2 || cancelled != 2 {
		t.Fatalf("CancelAll = %d, cancelled = %d, want 2/2", n, cancelled)
	}
	// 取消不会立刻把条目从 running 移除（由 run 的 goroutine 结束时移除），
	// 所以再次调用仍应逐个取消——重复取消是幂等的，但不能漏掉任何一个。
	if n := r.CancelAll(); n != 2 || cancelled != 4 {
		t.Fatalf("重复调用应再次逐个取消，得到 n=%d cancelled=%d", n, cancelled)
	}
}
