package main

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// 关闭排空的等待语义：空闲立即返回（不白等宽限）、超时返回、第二次信号立即返回、
// 任务收尾后立即返回。任何一条坏了都会让重启要么白等、要么打断在途任务。
func TestWaitForIdleOrSignal(t *testing.T) {
	// 空闲：立即返回，不必等满宽限（实测空闲重启曾白等 20s）。
	start := time.Now()
	if !waitForIdleOrSignal(func() int { return 0 }, make(chan os.Signal, 1), 5*time.Second) {
		t.Fatal("空闲时应返回 true")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("空闲时不应等待，实际 %v", elapsed)
	}

	// 一直有在途任务：等到 deadline 后返回（由调用方决定下一步）。
	start = time.Now()
	if !waitForIdleOrSignal(func() int { return 1 }, make(chan os.Signal, 1), 300*time.Millisecond) {
		t.Fatal("超时应返回 true")
	}
	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Fatalf("应等到 deadline，实际 %v", elapsed)
	}

	// 第二次信号：立即返回 false（操作者要立即退出）。
	sigCh := make(chan os.Signal, 1)
	sigCh <- syscall.SIGTERM
	start = time.Now()
	if waitForIdleOrSignal(func() int { return 1 }, sigCh, 5*time.Second) {
		t.Fatal("第二次信号应立即返回 false")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("第二次信号不应等待，实际 %v", elapsed)
	}

	// 任务收尾后立即返回，不等满 deadline。
	done := make(chan struct{})
	go func() {
		time.Sleep(200 * time.Millisecond)
		close(done)
	}()
	inFlight := func() int {
		select {
		case <-done:
			return 0
		default:
			return 1
		}
	}
	start = time.Now()
	if !waitForIdleOrSignal(inFlight, make(chan os.Signal, 1), 5*time.Second) {
		t.Fatal("任务收尾后应返回 true")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("任务收尾后不应继续等待，实际 %v", elapsed)
	}
}
