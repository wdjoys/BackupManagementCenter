package backup

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// sqliteRowCount 读取 items 表行数；表不存在（例如只复制了主库、丢掉了 WAL）返回 0。
func sqliteRowCount(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM items").Scan(&n); err != nil {
		return 0
	}
	return n
}

// WAL 未 checkpoint 时主库文件本身不含最新提交。回退路径必须连同 -wal 一起复制，
// 副本才能读到完整数据——这是只读挂载下无法直接 VACUUM INTO 时的兜底。
func TestCopySQLiteForBackupKeepsUncheckpointedWAL(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "wal-source.sqlite")

	// wal_autocheckpoint=0 且连接保持打开：提交的行停留在 -wal，主库仍是空库。
	db, err := sql.Open("sqlite", src)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=0"} {
		if _, err := db.Exec(pragma); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("CREATE TABLE items (id INTEGER PRIMARY KEY, payload TEXT)"); err != nil {
		t.Fatal(err)
	}
	const rows = 200
	const insert = "INSERT INTO items(payload) SELECT hex(randomblob(32)) FROM (WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < ?) SELECT x FROM c)"
	if _, err := db.Exec(insert, rows); err != nil {
		t.Fatal(err)
	}
	// 模拟崩溃后的状态：-shm 缺失（只读挂载下 SQLite 无法重建它）。
	// Windows 上文件被打开时删除会失败，忽略即可：无论 -shm 在不在，
	// 副本都必须包含 WAL 中的数据。
	_ = os.Remove(src + "-shm")

	workDir := filepath.Join(dir, "work")
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	copied, err := copySQLiteForBackup(src, workDir)
	if err != nil {
		t.Fatal(err)
	}
	exported := filepath.Join(workDir, "export.sqlite")
	if err := sqliteVacuumInto(ctx, copied, exported); err != nil {
		t.Fatalf("对副本执行 VACUUM INTO 失败: %v", err)
	}
	if err := sqliteIntegrityCheck(ctx, exported); err != nil {
		t.Fatalf("副本导出后 integrity_check 失败: %v", err)
	}
	if got := sqliteRowCount(t, exported); got != rows {
		t.Fatalf("副本导出应包含 WAL 中提交的 %d 行，实际 %d 行", rows, got)
	}

	// 对照组：只复制主库会丢掉尚未 checkpoint 的提交。
	naive := filepath.Join(workDir, "naive.sqlite")
	if err := copySQLiteFile(src, naive); err != nil {
		t.Fatal(err)
	}
	if got := sqliteRowCount(t, naive); got >= rows {
		t.Fatalf("主库单文件不应包含全部 WAL 行，实际 %d 行（对照组失效）", got)
	}
}
