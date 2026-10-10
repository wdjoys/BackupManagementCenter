package agent

import (
	"errors"
	"strings"
	"testing"

	"backupmanagementcenter/internal/agent/pipeline"
	"backupmanagementcenter/internal/agent/restic"
	"backupmanagementcenter/internal/model"
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

// 恢复前保护备份失败的稳定码必须活下来：它在 pipeline 里写明分类（目标未被修改），
// 但底层 restic 会给出兜底码 restic_failed。此前后者无条件覆盖前者，导致
// run.error_code 只显示 restic_failed、pre_restore_backup_failed 全仓无消费方
// （实测 run 01a1265b-4fb1-7bb2-5828-2875b781309a）。
func TestFailureCodeKeepsSpecificPipelineCodeOverGenericResticCode(t *testing.T) {
	cause := &restic.ResticError{Code: "restic_failed", ExitCode: 1, Err: errors.New("permission denied")}
	err := &pipeline.PipelineError{
		Code:    model.ErrPreRestoreBackupFailed,
		Message: "pre-restore protection backup failed; target was not modified",
		Cause:   cause,
	}
	code, msg := failureCodeAndMessage(err, "pipeline_error", "x")
	if code != model.ErrPreRestoreBackupFailed {
		t.Fatalf("code = %q, want %q", code, model.ErrPreRestoreBackupFailed)
	}
	if !strings.Contains(msg, "target was not modified") {
		t.Fatalf("message 丢失: %q", msg)
	}

	// restic 的"具体分类"仍必须优先于 pipeline 的通用码。
	specific := &pipeline.PipelineError{
		Code:  "backup_failed",
		Cause: &restic.ResticError{Code: model.ErrRepositoryMissing, ExitCode: 1, Err: errors.New("no repo")},
	}
	if code, _ := failureCodeAndMessage(specific, "pipeline_error", "x"); code != model.ErrRepositoryMissing {
		t.Fatalf("具体 restic 分类必须优先，得到 %q", code)
	}

	// 两侧都是兜底码时维持既有行为（restic 码胜出），避免无关行为漂移。
	generic := &pipeline.PipelineError{
		Code:  "backup_failed",
		Cause: &restic.ResticError{Code: "restic_failed", ExitCode: 1, Err: errors.New("exit 1")},
	}
	if code, _ := failureCodeAndMessage(generic, "pipeline_error", "x"); code != "restic_failed" {
		t.Fatalf("兜底码行为不应漂移，得到 %q", code)
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
