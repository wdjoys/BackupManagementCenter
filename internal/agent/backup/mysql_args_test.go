package backup

import (
	"context"
	"errors"
	"os"
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

// 客户端选择必须按服务端版本区分：MariaDB 与 MySQL <8.0 需要 5.7 客户端，
// 否则官方 8.0 客户端会把连接字符集回退到 latin1，非 ASCII 标识符无法解析。
func TestMySQLNeedsLegacyClient(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		{"5.5.62", true},
		{"5.6.51-log", true},
		{"5.7.44", true},
		{"8.0.40", false},
		{"8.4.0", false},
		{"11.8.9-MariaDB-ubu2404", true},
		{"5.5.5-10.6.16-MariaDB", true},
		{"", true}, // 无法解析时按旧服务端处理（更安全）
	}
	for _, tc := range cases {
		if got := mysqlNeedsLegacyClient(tc.version); got != tc.want {
			t.Errorf("mysqlNeedsLegacyClient(%q) = %v, want %v", tc.version, got, tc.want)
		}
	}
}

func TestMySQLMajorVersion(t *testing.T) {
	cases := map[string]int{
		"5.7.44": 5, "8.0.40": 8, "11.8.9-MariaDB": 11,
		"5.5.5-10.6.16-MariaDB": 5, "": 0, "abc": 0,
	}
	for in, want := range cases {
		if got := mysqlMajorVersion(in); got != want {
			t.Errorf("mysqlMajorVersion(%q) = %d, want %d", in, got, want)
		}
	}
}

// 连接字符集必须显式写入 cnf：客户端默认字符集由镜像 locale 推导，agent 镜像
// 没有 UTF-8 locale，会退到 latin1，非 ASCII 标识符（库名/表名）在恢复的建库与
// 校验语句里会被错误解释。
func TestMySQLCnfSetsUTF8MB4(t *testing.T) {
	dir := t.TempDir()
	rec := &argRecorder{}
	rc := &RunContext{
		Task:    mysqlBackupTask("appdb"),
		Secrets: SecretBundle{DBPassword: "pw"},
		TempDir: dir,
		Exec:    rec,
		Logf:    func(string, string, ...any) {},
	}
	if _, err := (&MySQLAdapter{}).Backup(context.Background(), rc); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	backupCnf, err := os.ReadFile(filepath.Join(dir, "my.cnf"))
	if err != nil {
		t.Fatalf("read backup my.cnf: %v", err)
	}
	if !strings.Contains(string(backupCnf), "default-character-set=utf8mb4") {
		t.Fatalf("backup my.cnf must set utf8mb4, got %q", string(backupCnf))
	}

	rspec := pgRestoreSpec(t)
	rspec.Kind = KindMySQL
	rspec.Database = &model.DatabaseRestore{
		TargetHost: "db", TargetPort: 3306, TargetUsername: "bmc", TargetDatabase: "appdb",
	}
	rspec.Exec = rec
	if _, err := mysqlPrepare(rspec); err != nil {
		t.Fatalf("mysqlPrepare: %v", err)
	}
	restoreCnf, err := os.ReadFile(filepath.Join(rspec.StagingDir, "my.cnf"))
	if err != nil {
		t.Fatalf("read restore my.cnf: %v", err)
	}
	if !strings.Contains(string(restoreCnf), "default-character-set=utf8mb4") {
		t.Fatalf("restore my.cnf must set utf8mb4, got %q", string(restoreCnf))
	}
}

// 合法表名可以含反引号（转义为 ``）与单引号。此前按"第一个反引号前的内容"截断，
// 会把 `back``tick` 解析成 back，导致恢复校验误报 missing tables 并触发回滚。
func TestMySQLDumpTableNamesHandlesEscapedBackticks(t *testing.T) {
	dump := "CREATE TABLE `it's` (\n  `id` int NOT NULL\n);\n" +
		"CREATE TABLE `back``tick` (\n  `id` int NOT NULL\n);\n" +
		"CREATE TABLE IF NOT EXISTS `plain` (\n  `id` int NOT NULL\n);\n"
	dir := t.TempDir()
	p := filepath.Join(dir, "dump.sql")
	if err := os.WriteFile(p, []byte(dump), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := mysqlDumpTableNames(p)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct{}{"it's": {}, "back`tick": {}, "plain": {}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			t.Fatalf("missing %q in %v", name, got)
		}
	}
}

// 合法库名可以以 '-' 开头（引号标识符）。库名是位置参数，必须用 -- 结束选项，
// 否则客户端把它当选项簇解析：实测 mysqldump 报 "unknown option '-s'"，
// 该库永远无法备份。同时锚定 extra_args 仍排在 -- 之前（它们是选项）。
func TestMySQLDumpArgsEndOptionsBeforeDatabase(t *testing.T) {
	rec := &argRecorder{}
	task := mysqlBackupTask("-dashdb")
	task.Source.ExtraArgs = []string{"--skip-comments"}
	rc := &RunContext{
		Task: task, Secrets: SecretBundle{DBPassword: "pw"}, TempDir: t.TempDir(),
		Exec: rec, Logf: func(string, string, ...any) {},
	}
	if _, err := (&MySQLAdapter{}).Backup(context.Background(), rc); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	dump := rec.find("mysqldump")
	if dump == nil {
		t.Fatal("mysqldump was never invoked")
	}
	dash := indexOf(dump.Args, "--")
	if dash < 0 {
		t.Fatalf("dump args must contain -- separator, got %v", dump.Args)
	}
	if dash != len(dump.Args)-2 || dump.Args[len(dump.Args)-1] != "-dashdb" {
		t.Fatalf("-- must immediately precede the database name, got %v", dump.Args)
	}
	if ea := indexOf(dump.Args, "--skip-comments"); ea < 0 || ea > dash {
		t.Fatalf("extra_args must stay before -- (they are options), got %v", dump.Args)
	}
}

// 恢复导入时目标库名同样是位置参数：以 '-' 开头的合法库名会被 mysql 客户端
// 当成选项（实测 unknown option '-d'）。
func TestMySQLImportArgsEndOptionsBeforeTargetDatabase(t *testing.T) {
	rec := &argRecorder{}
	spec := &RestoreSpec{
		Kind:         KindMySQL,
		StagingDir:   t.TempDir(),
		ArtifactFile: filepath.Join(t.TempDir(), "dump.sql"),
		Database: &model.DatabaseRestore{
			TargetDatabase: "-dashout", TargetHost: "127.0.0.1", TargetPort: 3306, TargetUsername: "bmc",
		},
		Secrets: SecretBundle{DBPassword: "pw"},
		Logf:    func(string, string, ...any) {},
		Exec:    rec,
	}
	if err := (&MySQLAdapter{}).Import(context.Background(), spec); err != nil {
		t.Fatalf("Import: %v", err)
	}
	// 准备步骤也用 mysql 客户端（-e 内嵌 SQL，库名以反引号引用，天然安全）；
	// 导入步骤以 --binary-mode 区分。
	var imp *Cmd
	for i := range rec.calls {
		c := &rec.calls[i]
		// 导入：走 stdin 且不带 -e（准备步骤用 -e 执行建库语句）。
		if filepath.Base(c.Exe) == "mysql" && indexOf(c.Args, "--binary-mode") >= 0 && indexOf(c.Args, "-e") < 0 {
			imp = c
			break
		}
	}
	if imp == nil {
		t.Fatal("mysql import (with --binary-mode) was never invoked")
	}
	dash := indexOf(imp.Args, "--")
	if dash < 0 || dash != len(imp.Args)-2 || imp.Args[len(imp.Args)-1] != "-dashout" {
		t.Fatalf("-- must immediately precede the target database, got %v", imp.Args)
	}
}

func indexOf(xs []string, want string) int {
	for i, x := range xs {
		if x == want {
			return i
		}
	}
	return -1
}
