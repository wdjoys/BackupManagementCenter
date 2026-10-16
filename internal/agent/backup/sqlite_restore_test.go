package backup

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"backupmanagementcenter/internal/model"
)

func newSQLiteFixture(t *testing.T, dir, name string, tables ...string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range tables {
		if _, err := db.Exec("CREATE TABLE " + table + " (id INTEGER PRIMARY KEY)"); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func sqliteTableNames(t *testing.T, path string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out = append(out, name)
	}
	return out
}

func sqliteSpec(targetPath, artifact string, replace, isNew bool) *RestoreSpec {
	return &RestoreSpec{
		Kind:         KindSQLite,
		ArtifactFile: artifact,
		TargetIsNew:  isNew,
		Database:     &model.DatabaseRestore{TargetDatabase: targetPath, ReplaceExisting: replace},
	}
}

// 新建导入后目标应携带快照内容，且验证能发现空 schema 的伪导入。
func TestSQLiteRestoreCreatesAndVerifiesTarget(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := newSQLiteFixture(t, dir, "source.sqlite", "users", "orders")
	target := filepath.Join(dir, "target.sqlite")
	adapter := &SQLiteAdapter{}

	spec := sqliteSpec(target, source, false, true)
	exists, err := adapter.TargetExists(ctx, spec)
	if err != nil || exists {
		t.Fatalf("expected a new target, got exists=%v err=%v", exists, err)
	}
	if err := adapter.Import(ctx, spec); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := adapter.VerifyRestored(ctx, spec); err != nil {
		t.Fatalf("VerifyRestored: %v", err)
	}
	if got := sqliteTableNames(t, target); len(got) != 2 {
		t.Fatalf("expected 2 tables after import, got %v", got)
	}
}

// 覆盖导入必须完整替换旧库：旧库独有对象不得留在新库中。
func TestSQLiteRestoreReplaceRemovesOldObjects(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := newSQLiteFixture(t, dir, "source.sqlite", "users")
	target := newSQLiteFixture(t, dir, "target.sqlite", "users", "legacy_only")
	adapter := &SQLiteAdapter{}

	spec := sqliteSpec(target, source, true, false)
	exists, err := adapter.TargetExists(ctx, spec)
	if err != nil || !exists {
		t.Fatalf("expected an existing target, got exists=%v err=%v", exists, err)
	}
	if err := adapter.Import(ctx, spec); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := adapter.VerifyRestored(ctx, spec); err != nil {
		t.Fatalf("VerifyRestored: %v", err)
	}
	got := sqliteTableNames(t, target)
	if len(got) != 1 || got[0] != "users" {
		t.Fatalf("expected only the snapshot table, got %v", got)
	}
}

// 目标已存在时不得保留“新建”语义（并发执行者创建的库不能被覆盖）。
func TestSQLiteRestoreNewTargetRefusesExistingFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := newSQLiteFixture(t, dir, "source.sqlite", "users")
	target := newSQLiteFixture(t, dir, "target.sqlite", "other")
	adapter := &SQLiteAdapter{}

	if err := adapter.Import(ctx, sqliteSpec(target, source, false, true)); err == nil {
		t.Fatal("expected the import to refuse an existing target with new-target semantics")
	}
	if got := sqliteTableNames(t, target); len(got) != 1 || got[0] != "other" {
		t.Fatalf("target must keep another actor's content, got %v", got)
	}
}

// 新建清理只删除本 run 创建的文件及其侧车，不触碰其他文件。
func TestSQLiteRemoveTargetOnlyRemovesOwnFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	target := filepath.Join(dir, "created.sqlite")
	artifact := newSQLiteFixture(t, dir, "artifact.sqlite", "users")
	sibling := newSQLiteFixture(t, dir, "sibling.sqlite", "other")
	adapter := &SQLiteAdapter{}

	spec := sqliteSpec(target, artifact, false, true)
	if err := adapter.Import(ctx, spec); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := adapter.RemoveTarget(ctx, spec); err != nil {
		t.Fatalf("RemoveTarget: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected the created target to be removed, got err=%v", err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Fatalf("expected unrelated files to survive cleanup, got err=%v", err)
	}
	// 非新建目标拒绝清理。
	if err := adapter.RemoveTarget(ctx, sqliteSpec(sibling, artifact, true, false)); err == nil {
		t.Fatal("expected RemoveTarget to refuse a target this run did not create")
	}
}

// 目标被并发独占事务占用时，预检必须报"目标被占用"，而不是"integrity check failed"
// ——后者会被运维读成库损坏（实测：持 BEGIN EXCLUSIVE 时 error_message 显示
// integrity check failed，真实原因 database is locked (5) 只在句尾）。
func TestSQLiteErrBusyDistinguishesLockFromCorruption(t *testing.T) {
	busy := []string{
		"database is locked (5) (SQLITE_BUSY)",
		"SQLITE_BUSY: database is locked",
		"database table is locked: users",
	}
	for _, msg := range busy {
		if !sqliteErrBusy(errors.New(msg)) {
			t.Fatalf("%q 应判为被占用", msg)
		}
	}
	notBusy := []string{
		"database disk image is malformed",
		"file is not a database",
		"unable to open database file: no such file or directory",
	}
	for _, msg := range notBusy {
		if sqliteErrBusy(errors.New(msg)) {
			t.Fatalf("%q 不应判为被占用", msg)
		}
	}
	if sqliteErrBusy(nil) {
		t.Fatal("nil 不应判为被占用")
	}
}

// 行为级验证：目标被同进程的独占事务占用时，预检返回的错误必须说"目标被占用"，
// 而不是把 SQLITE_BUSY 误报成 integrity check failed。（同进程的不同连接之间
// SQLite 仍会检测锁冲突，故该测试可稳定复现。）
func TestSQLitePreflightReportsBusyNotCorruption(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	target := newSQLiteFixture(t, dir, "target.sqlite", "users")

	writer, err := sql.Open("sqlite", target)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		t.Fatalf("begin exclusive: %v", err)
	}
	defer writer.ExecContext(ctx, "ROLLBACK")

	err = sqliteCheckMaintenanceWindow(ctx, target)
	if err == nil {
		t.Fatal("目标被独占事务占用时预检必须失败")
	}
	if !strings.Contains(err.Error(), "target database is busy") {
		t.Fatalf("预检应报\"目标被占用\"而不是损坏: %v", err)
	}
}

// 导入的产物与快照内容不一致时，验证必须发现（这里用旧 schema 覆盖新 schema 后
// 与快照对比，模拟导入没有真正落盘的情况）。
func TestSQLiteVerifyDetectsStaleContent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := newSQLiteFixture(t, dir, "source.sqlite", "users", "orders")
	target := newSQLiteFixture(t, dir, "target.sqlite", "legacy_only")
	adapter := &SQLiteAdapter{}

	// 不导入、直接验证：目标缺少快照中的表，必须失败。
	spec := sqliteSpec(target, source, true, false)
	if err := adapter.VerifyRestored(ctx, spec); err == nil {
		t.Fatal("expected verification to fail when the target is missing the snapshot tables")
	}
	// 用真实快照重新导入后通过验证。
	if err := adapter.Import(ctx, spec); err != nil {
		t.Fatalf("Import replace: %v", err)
	}
	if err := adapter.VerifyRestored(ctx, spec); err != nil {
		t.Fatalf("VerifyRestored after a real import: %v", err)
	}
}
