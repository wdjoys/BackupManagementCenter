//go:build linux

package agent

import (
	"bytes"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
)

// 容器内 Agent 以 PID 1 运行，因此它同时是容器的孤儿回收者。restic 走 rclone
// 后端（repository_path 形如 `remote:/path`）时会 fork 出 `rclone serve restic`；
// restic 退出后该子进程被 reparent 到 PID 1，若无人 wait 就永久滞留为僵尸。
// 实测每次运行泄漏 3 个 rclone 僵尸（30 次运行后 137 个），PID 表耗尽后 fork
// 失败、所有备份都起不来。
//
// 回收孤儿只能针对「os/exec 没有在等」的子进程：wait4(-1) 会把 exec 的子进程一起
// 收走，之后 cmd.Wait() 以 ECHILD 失败，一次正常的备份会被判成失败运行。所以所有
// exec 启动都经 StartTracked/RunTracked 登记 pid，回收器与启动共用 childMu 互斥：
// 启动期间不可能有未登记的活子进程，回收期间也不可能新起子进程。
var (
	childMu   sync.Mutex
	ownedPids = map[int]struct{}{}
)

// StartTracked 启动 c 并登记其 pid。返回的 release 必须在 Wait 返回后调用。
func StartTracked(c *exec.Cmd) (release func(), err error) {
	childMu.Lock()
	defer childMu.Unlock()
	if err := c.Start(); err != nil {
		return func() {}, err
	}
	pid := c.Process.Pid
	ownedPids[pid] = struct{}{}
	return func() {
		childMu.Lock()
		delete(ownedPids, pid)
		childMu.Unlock()
	}, nil
}

// RunTracked 启动并等待 c（等价于 c.Run()，但 pid 对孤儿回收器可见）。
func RunTracked(c *exec.Cmd) error {
	release, err := StartTracked(c)
	if err != nil {
		return err
	}
	defer release()
	return c.Wait()
}

// StartOrphanReaper 启动后台孤儿回收，并先清扫一次历史遗留。
func StartOrphanReaper() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGCHLD)
	go func() {
		for range ch {
			reapOrphans()
		}
	}()
	reapOrphans()
}

func reapOrphans() {
	childMu.Lock()
	defer childMu.Unlock()
	self := os.Getpid()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		if _, owned := ownedPids[pid]; owned {
			continue
		}
		if !isZombieChildOf(pid, self) {
			continue
		}
		var ws syscall.WaitStatus
		_, _ = syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
	}
}

// isZombieChildOf 判断 pid 是否为当前进程的僵尸子进程。/proc/<pid>/stat 的 comm
// 可能含空格和括号，所以从最后一个 ')' 之后再切分字段（state, ppid, ...）。
func isZombieChildOf(pid, self int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return false
	}
	f := bytes.Fields(b[i+1:])
	if len(f) < 2 || len(f[0]) == 0 || f[0][0] != 'Z' {
		return false
	}
	ppid, err := strconv.Atoi(string(f[1]))
	return err == nil && ppid == self
}
