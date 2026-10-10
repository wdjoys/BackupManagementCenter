package restic

import (
	"strings"
	"testing"
)

// 保留策略裁剪摘要是我们自己生成的正常结果，必须走 info 出口（InfoLogf），不能借用
// restic stderr 的出口——否则会以 error 级 + "restic stderr: " 前缀落日志，按 error
// 告警/检索会把正常保留误判为失败（实测）。
func TestLogRetentionResultUsesInfoSink(t *testing.T) {
	stdout := `[{"keep":[{"id":"a"},{"id":"b"}],"remove":[{"id":"c"}]}]`
	var stderrLines, infoLines []string
	opts := Options{
		Logf:     func(line string) { stderrLines = append(stderrLines, line) },
		InfoLogf: func(line string) { infoLines = append(infoLines, line) },
	}
	logRetentionResult(opts, []string{"plan:p1"}, stdout)

	if len(infoLines) != 1 || !strings.Contains(infoLines[0], "删除 1 个快照") || !strings.Contains(infoLines[0], "保留 2 个") {
		t.Fatalf("摘要必须走 info 出口，实际 info=%v stderr=%v", infoLines, stderrLines)
	}
	if len(stderrLines) != 0 {
		t.Fatalf("摘要不得走 stderr 出口: %v", stderrLines)
	}

	// 只提供 stderr 出口时仍要输出（老调用方/测试兼容）。
	var fallback []string
	logRetentionResult(Options{Logf: func(line string) { fallback = append(fallback, line) }}, nil, stdout)
	if len(fallback) != 1 {
		t.Fatalf("仅设置 Logf 时应回退到 Logf，实际 %v", fallback)
	}
}
