package backup

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"backupmanagementcenter/internal/model"
)

// argRecorder 记录每次命令调用的参数，并可按可执行文件名返回退出码。
type argRecorder struct {
	calls     []Cmd
	exitTools map[string]int
}

func (r *argRecorder) Run(_ context.Context, c Cmd, _, _ func(string)) (int, error) {
	r.calls = append(r.calls, c)
	for _, arg := range c.Args {
		if strings.HasPrefix(arg, "--defaults-extra-file=") && c.Args[0] != arg {
			// 复刻 mysql/mariadb 客户端行为：只有第一个参数位置的
			// --defaults-extra-file 被识别，其余报 unknown variable (exit 7)。
			return 7, nil
		}
	}
	if code, ok := r.exitTools[filepath.Base(c.Exe)]; ok {
		return code, nil
	}
	return 0, nil
}

func (r *argRecorder) find(exe string) *Cmd {
	for i := range r.calls {
		if filepath.Base(r.calls[i].Exe) == exe {
			return &r.calls[i]
		}
	}
	return nil
}

func mysqlBackupTask(database string) model.BackupTask {
	return model.BackupTask{
		PlanID: "plan-1",
		Kind:   KindMySQL,
		Source: model.PlanSource{
			Host: "127.0.0.1", Port: 3306, Username: "bmc",
			Database: database, EstimatedDumpBytes: 1 << 20,
		},
	}
}

// --defaults-extra-file 必须位于 argv[0]：靠后会被客户端当成未知变量并以
// exit 7 失败，备份根本不开始。这是曾经的线上故障。
func TestMySQLBackupPassesCnfFileFirst(t *testing.T) {
	rec := &argRecorder{}
	rc := &RunContext{
		Task:    mysqlBackupTask("appdb"),
		Secrets: SecretBundle{DBPassword: "pw"},
		TempDir: t.TempDir(),
		Exec:    rec,
		Logf:    func(string, string, ...any) {},
	}
	if _, err := (&MySQLAdapter{}).Backup(context.Background(), rc); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	dump := rec.find("mysqldump")
	if dump == nil {
		t.Fatal("mysqldump was never invoked")
	}
	if !strings.HasPrefix(dump.Args[0], "--defaults-extra-file=") {
		t.Fatalf("--defaults-extra-file must be argv[0], got %v", dump.Args)
	}
	if last := dump.Args[len(dump.Args)-1]; last != "appdb" {
		t.Fatalf("database must be the trailing positional arg, got %q", last)
	}
	// 探测查询与 dump 都必须拿到 cnf；探测失败会导致统计缺失而非备份失败。
	for _, name := range []string{"mysql", "mysqldump"} {
		c := rec.find(name)
		if c == nil || !strings.HasPrefix(c.Args[0], "--defaults-extra-file=") {
			t.Fatalf("%s must receive the cnf file first, got %+v", name, c)
		}
	}
}

// 恢复路径的 mysql 客户端同样要求 cnf 在前；--binary-mode 顺序无关。
func TestMySQLRestorePassesCnfFileFirst(t *testing.T) {
	rec := &argRecorder{}
	spec := &RestoreSpec{
		Kind:         KindMySQL,
		StagingDir:   t.TempDir(),
		ArtifactFile: filepath.Join(t.TempDir(), "dump.sql"),
		Database: &model.DatabaseRestore{
			TargetDatabase: "appdb", TargetHost: "127.0.0.1", TargetPort: 3306, TargetUsername: "bmc",
		},
		Secrets: SecretBundle{DBPassword: "pw"},
		Logf:    func(string, string, ...any) {},
		Exec:    rec,
	}
	if err := (&MySQLAdapter{}).Import(context.Background(), spec); err != nil {
		t.Fatalf("Import: %v", err)
	}
	for i, c := range rec.calls {
		if !strings.HasPrefix(c.Args[0], "--defaults-extra-file=") {
			t.Fatalf("call %d (%s) must pass the cnf file first: %v", i, filepath.Base(c.Exe), c.Args)
		}
	}
}

// 进程正常启动但非零退出时 Executor 返回 (code, nil)，错误消息不得渲染出
// "%!w(<nil>)"。
func TestExitErrorMessageNeverRendersNilWrap(t *testing.T) {
	rec := &argRecorder{exitTools: map[string]int{"mysqldump": 7}}
	rc := &RunContext{
		Task:    mysqlBackupTask("all"),
		Secrets: SecretBundle{DBPassword: "pw"},
		TempDir: t.TempDir(),
		Exec:    rec,
		Logf:    func(string, string, ...any) {},
	}
	_, err := (&MySQLAdapter{}).Backup(context.Background(), rc)
	if err == nil {
		t.Fatal("expected a failure for exit 7")
	}
	if strings.Contains(err.Error(), "%!w") {
		t.Fatalf("error must not contain a nil %%w directive: %v", err)
	}
	if !strings.Contains(err.Error(), "exit 7") {
		t.Fatalf("error must carry the exit code: %v", err)
	}
}

// err 非 nil 时必须被包装保留（进程无法启动/流读取失败）。
func TestExitErrorMessageWrapsCause(t *testing.T) {
	cause := errors.New("exec: not found")
	err := exitError("mysqldump failed", -1, cause)
	if !errors.Is(err, cause) {
		t.Fatalf("cause must stay wrapped, got %v", err)
	}
	if strings.Contains(err.Error(), "%!w") {
		t.Fatalf("unexpected directive rendering: %v", err)
	}
}

// 错误文案不得重复 "failed"：exitError 自身会补 "failed (exit N)"。
func TestExitErrorMessageHasSingleFailedWord(t *testing.T) {
	err := exitError("mysqldump", 2, nil)
	if got := err.Error(); got != "mysqldump failed (exit 2)" {
		t.Fatalf("unexpected message: %q", got)
	}
	if strings.Count(err.Error(), "failed") != 1 {
		t.Fatalf("must not repeat failed: %q", err.Error())
	}
}

// 官方 MySQL 8.0 客户端默认开启 --column-statistics，会先查
// information_schema.COLUMN_STATISTICS。该表是 MySQL 8.0 专有的，MariaDB 与
// MySQL 5.x 都没有，dump 会以
//   Unknown table 'COLUMN_STATISTICS' in information_schema (1109)
// 失败（exit 2）。必须在参数中显式关闭。
func TestMySQLBackupDisablesColumnStatistics(t *testing.T) {
	rec := &argRecorder{}
	rc := &RunContext{
		Task:    mysqlBackupTask("appdb"),
		Secrets: SecretBundle{DBPassword: "pw"},
		TempDir: t.TempDir(),
		Exec:    rec,
		Logf:    func(string, string, ...any) {},
	}
	if _, err := (&MySQLAdapter{}).Backup(context.Background(), rc); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	dump := rec.find("mysqldump")
	if dump == nil {
		t.Fatal("mysqldump was never invoked")
	}
	found := false
	for _, a := range dump.Args {
		if a == "--column-statistics=0" {
			found = true
		}
	}
	if !found {
		t.Fatalf("mysqldump must disable column statistics, got %v", dump.Args)
	}
}

// MySQL ≤5.7 默认 character_set_server=latin1，官方 8.0 客户端请求 utf8mb4 时因
// utf8mb4_0900_ai_ci 在旧服务端不存在而回退 latin1，非 ASCII 库名被错误解释，
// 服务端报 "Unknown database"（库其实存在）。必须给出可诊断提示，避免运维误判。
func TestMySQLDumpNameCharsetHint(t *testing.T) {
	unknown := []string{"mysqldump: Got error: 1049: Unknown database '测试库-1' when selecting the database"}

	if got := mysqlDumpNameCharsetHint("测试库-1", unknown); got == "" {
		t.Fatal("non-ASCII database with Unknown database must produce a hint")
	} else if !strings.Contains(got, "测试库-1") || !strings.Contains(got, "utf8mb4") {
		t.Fatalf("hint must name the database and the charset remedy, got %q", got)
	}

	// ASCII 库名不受该字符集回退影响，不应给出误导性提示。
	if got := mysqlDumpNameCharsetHint("legacy57", unknown); got != "" {
		t.Fatalf("ASCII database must not produce a charset hint, got %q", got)
	}
	// 非 ASCII 库名但错误不是 1049（例如权限/网络）时也不该归因到字符集。
	if got := mysqlDumpNameCharsetHint("测试库-1", []string{"Access denied for user 'root'@'%'"}); got != "" {
		t.Fatalf("unrelated failure must not produce a charset hint, got %q", got)
	}
	// all 范围不涉及单个库名。
	if got := mysqlDumpNameCharsetHint("all", unknown); got != "" {
		t.Fatalf("all scope must not produce a charset hint, got %q", got)
	}
}
