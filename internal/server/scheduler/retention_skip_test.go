package scheduler

import (
	"testing"
	"time"
)

// 保留被跳过必须可见，且同一仓库同一原因每小时最多记一条（tick 每 15s，不限频会刷屏）。
// 此前因 active/recent 跳过时完全静默，实测 keep_last=1 的每分钟计划在 24h 节流窗口内
// 快照持续累积，而界面与日志都没有"保留被跳过"的提示。
func TestLogRetentionSkipIsRateLimited(t *testing.T) {
	s := &Scheduler{}
	const msg = "scheduler: skip retention (already ran within 24h)"
	if !s.logRetentionSkip("repo-1", "recent", msg) {
		t.Fatal("首次应记录")
	}
	if s.logRetentionSkip("repo-1", "recent", msg) {
		t.Fatal("同一仓库同一原因一小时内的第二次不应再记录")
	}
	if !s.logRetentionSkip("repo-1", "active", "scheduler: skip retention while a backup is active") {
		t.Fatal("不同原因应独立限频")
	}
	if !s.logRetentionSkip("repo-2", "recent", msg) {
		t.Fatal("不同仓库应独立限频")
	}
	// 窗口过期后应重新记录。
	s.retentionSkipMu.Lock()
	s.retentionSkipAt["repo-1|recent"] = time.Now().Add(-2 * time.Hour)
	s.retentionSkipMu.Unlock()
	if !s.logRetentionSkip("repo-1", "recent", msg) {
		t.Fatal("超过窗口后应重新记录")
	}
}
