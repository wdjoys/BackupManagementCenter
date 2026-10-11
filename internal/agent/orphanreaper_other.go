//go:build !linux

package agent

import "os/exec"

// 非 Linux 平台没有「PID 1 回收孤儿」的语义（Windows 的子进程由 taskkill /T
// 按进程树处理），此处只需保持 API 一致。

// StartTracked 启动 c，release 为空操作。
func StartTracked(c *exec.Cmd) (release func(), err error) {
	if err := c.Start(); err != nil {
		return func() {}, err
	}
	return func() {}, nil
}

// RunTracked 启动并等待 c。
func RunTracked(c *exec.Cmd) error { return c.Run() }

// StartOrphanReaper 在非 Linux 平台为空操作。
func StartOrphanReaper() {}
