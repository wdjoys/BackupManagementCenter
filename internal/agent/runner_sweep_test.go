package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// 进程被 SIGTERM/SIGKILL 中断时 defer 不会执行，半个 dump 会永久留在 DataDir 下
// （实测每次中断泄漏 160MB–1.8GB）。启动清扫必须回收 bmc-run-* 目录，且不得误删
// 其它内容。
func TestSweepStaleRunDirsRemovesOnlyRunDirs(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, file bool) {
		p := filepath.Join(dir, name)
		if file {
			if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			return
		}
		if err := os.MkdirAll(filepath.Join(p, "staging"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "staging", "half.sql"), []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mk("bmc-run-111", false)
	mk("bmc-run-222", false)
	mk("bmc-agent-fixed", true) // 身份/二进制等其它文件必须保留
	mk("restic-repo", false)    // 仓库目录必须保留

	if removed := SweepStaleRunDirs(dir); removed != 2 {
		t.Fatalf("应回收 2 个 run 目录，实际 %d", removed)
	}
	for _, gone := range []string{"bmc-run-111", "bmc-run-222"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s 未被回收", gone)
		}
	}
	for _, keep := range []string{"bmc-agent-fixed", "restic-repo"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Fatalf("%s 不应被删除: %v", keep, err)
		}
	}
	// 幂等：再次清扫不应报错也不应删除任何东西。
	if removed := SweepStaleRunDirs(dir); removed != 0 {
		t.Fatalf("重复清扫应无残留，实际 %d", removed)
	}
}
