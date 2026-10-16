//go:build linux

package agent

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// 回收器必须收掉「不是 os/exec 正在等待」的僵尸子进程：容器内 agent 是 PID 1，
// restic 退出后留下的 rclone 就是这种僵尸，不收就会累积到 PID 表耗尽。
func TestReapOrphansReapsUntrackedZombie(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	// 故意不 Wait：子进程退出后成为未登记的僵尸，正是回收器的目标。
	pid := cmd.Process.Pid
	deadline := time.Now().Add(5 * time.Second)
	for !isZombieChildOf(pid, os.Getpid()) {
		if time.Now().After(deadline) {
			t.Fatalf("child %d never became a zombie", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
	reapOrphans()
	if isZombieChildOf(pid, os.Getpid()) {
		t.Fatalf("zombie %d not reaped", pid)
	}
}

// 回收器绝不能抢走 os/exec 正在等待的子进程：抢走后 cmd.Wait() 会以 ECHILD 失败，
// 一次正常的备份会被判成失败运行。
func TestReaperDoesNotStealTrackedChild(t *testing.T) {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				reapOrphans()
			}
		}
	}()
	defer func() { close(stop); <-done }()

	for range 30 {
		if err := RunTracked(exec.Command("sh", "-c", "exit 0")); err != nil {
			t.Fatalf("RunTracked failed (child stolen by reaper?): %v", err)
		}
	}
	// 登记表不得随运行次数增长。
	childMu.Lock()
	n := len(ownedPids)
	childMu.Unlock()
	if n != 0 {
		t.Fatalf("ownedPids leaked %d entries", n)
	}
}
