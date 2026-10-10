package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 写锁冲突必须重放整个事务：WAL 下的 SQLITE_BUSY_SNAPSHOT(517) 对 busy_timeout
// 无效，不重放就会丢掉终态转换——备份成功、快照已入库，run 却被 watchdog 判成
// run_timeout（实测并发下发备份时发生过）。
func TestRetryOnBusyReplaysTransactionUntilSuccess(t *testing.T) {
	busy := errors.New("transition run update: database is locked (517) (SQLITE_BUSY_SNAPSHOT)")
	calls := 0
	err := retryOnBusy(context.Background(), 5, time.Millisecond, func() error {
		calls++
		if calls < 3 {
			return busy
		}
		return nil
	})
	if err != nil {
		t.Fatalf("重放后应成功: %v", err)
	}
	if calls != 3 {
		t.Fatalf("应重放 3 次，实际 %d", calls)
	}
}

// 非冲突错误必须立即返回，不能白白重放（否则会把真实的逻辑错误藏起来）。
func TestRetryOnBusyStopsOnNonBusyError(t *testing.T) {
	hard := errors.New("transition run update: no such table: runs")
	calls := 0
	err := retryOnBusy(context.Background(), 5, time.Millisecond, func() error {
		calls++
		return hard
	})
	if !errors.Is(err, hard) {
		t.Fatalf("应原样返回非冲突错误，实际 %v", err)
	}
	if calls != 1 {
		t.Fatalf("非冲突错误不应重放，实际调用 %d 次", calls)
	}
}

// 持续冲突时应在尝试次数耗尽后返回最后一次错误，而不是无限等待。
func TestRetryOnBusyGivesUpAfterAttempts(t *testing.T) {
	busy := errors.New("database is locked (5) (SQLITE_BUSY)")
	calls := 0
	err := retryOnBusy(context.Background(), 3, time.Millisecond, func() error {
		calls++
		return busy
	})
	if err == nil {
		t.Fatal("持续冲突应返回错误")
	}
	if calls != 3 {
		t.Fatalf("应尝试 3 次，实际 %d", calls)
	}
}

// ctx 取消后不再重放。
func TestRetryOnBusyStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	busy := errors.New("database is locked (5) (SQLITE_BUSY)")
	calls := 0
	err := retryOnBusy(ctx, 100, time.Second, func() error {
		calls++
		cancel()
		return busy
	})
	if err == nil {
		t.Fatal("ctx 取消后应返回错误")
	}
	if calls > 2 {
		t.Fatalf("ctx 取消后不应继续重放，实际调用 %d 次", calls)
	}
}
