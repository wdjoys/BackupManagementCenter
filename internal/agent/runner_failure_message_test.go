package agent

import (
	"errors"
	"strings"
	"testing"

	"backupmanagementcenter/internal/agent/pipeline"
)

// 回滚失败这类需要人工判断的场景，调用方写在 Message 里的判定必须保留，不能被
// Cause 顶掉——否则 run 错误串里永远看不到"rollback also failed"。
func TestFailureCodeAndMessageKeepsPipelineMessage(t *testing.T) {
	cause := errors.New("target database is busy; close all target connections and retry: database is locked (5) (SQLITE_BUSY)")
	err := &pipeline.PipelineError{
		Code:    "rollback_failed",
		Message: "database import failed: " + cause.Error() + "; rollback also failed: re-import protection data",
		Cause:   cause,
	}
	code, msg := failureCodeAndMessage(err, "backup_failed", "")
	if code != "rollback_failed" {
		t.Fatalf("code = %q, want rollback_failed", code)
	}
	if !strings.Contains(msg, "rollback also failed") {
		t.Fatalf("Message 中的判定被丢弃: %q", msg)
	}
	if !strings.Contains(msg, "SQLITE_BUSY") {
		t.Fatalf("底层原因丢失: %q", msg)
	}
}

// Message 为空时仍回退到 Cause；Message 未覆盖 Cause 时补上细节。
func TestFailureCodeAndMessageFallsBackToCause(t *testing.T) {
	code, msg := failureCodeAndMessage(&pipeline.PipelineError{Code: "backup_failed", Cause: errors.New("boom")}, "x", "")
	if code != "backup_failed" || msg != "boom" {
		t.Fatalf("code=%q msg=%q", code, msg)
	}
	_, msg = failureCodeAndMessage(&pipeline.PipelineError{
		Code: "backup_failed", Message: "restic backup failed", Cause: errors.New("exit 10"),
	}, "x", "")
	if msg != "restic backup failed: exit 10" {
		t.Fatalf("msg=%q", msg)
	}
}
