package backup

import (
	"context"
	"errors"
	"fmt"
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

func (r *argRecorder) Run(_ context.Context, c Cmd, onStdout, _ func(string)) (int, error) {
	r.calls = append(r.calls, c)
	for _, arg := range c.Args {
		if strings.HasPrefix(arg, "--defaults-extra-file=") && c.Args[0] != arg {
			// 复刻 mysql/mariadb 客户端行为：只有第一个参数位置的
			// --defaults-extra-file 被识别，其余报 unknown variable (exit 7)。
			return 7, nil
		}
	}
	// 真实服务端总能回答 SHOW GRANTS；导出前的授权范围检查依赖它，缺了会被判成
	// "只被授予部分表"而拒绝备份。这里模拟 SELECT+EVENT+TRIGGER 全实例账号
	// （BMC 恒定带 --events --routines --triggers，缺 EVENT/TRIGGER 会被明确拒绝）。
	if indexOf(c.Args, "SHOW GRANTS FOR CURRENT_USER()") >= 0 && onStdout != nil {
		onStdout("GRANT SELECT, EVENT, TRIGGER ON *.* TO `bmc`@`%`")
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
//
//	Unknown table 'COLUMN_STATISTICS' in information_schema (1109)
//
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

// 合法表名可以含反引号（转义为 “）与单引号。此前按"第一个反引号前的内容"截断，
// 会把 `back“tick` 解析成 back，导致恢复校验误报 missing tables 并触发回滚。
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
	want := []string{"it's", "back`tick", "plain"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, name := range want {
		if got[i] != name {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// 表名折叠必须跟随服务端大小写敏感性。折叠时（lower_case_table_names != 0）服务端
// 自身会折叠名字，不折叠会误报缺表；不折叠时（Linux 默认 0）折叠会让"只恢复了同名
// 异大小写表之一"永远通过校验（实测 p_hint_Foo/p_hint_foo 被折叠成 1，日志显示
// "1 tables present, 1 expected"，漏检）。
func TestVerifyTableSetsFoldsOnlyWhenServerIsCaseInsensitive(t *testing.T) {
	// 大小写敏感服务端：dump 有两张仅大小写不同的表，目标只剩一张 -> 必须报缺失。
	missing := verifyTableSets([]string{"p_Foo", "p_foo"}, []string{"p_foo"}, false)
	if len(missing) != 1 || missing[0] != "p_Foo" {
		t.Fatalf("case-sensitive compare must report p_Foo missing, got %v", missing)
	}
	// 大小写不敏感服务端：服务端把 dump 的 p_Foo 存成 p_foo -> 不得误报。
	if missing := verifyTableSets([]string{"p_Foo", "p_foo"}, []string{"p_foo"}, true); len(missing) != 0 {
		t.Fatalf("case-insensitive compare must not report missing, got %v", missing)
	}
	// 敏感服务端：两边完全一致 -> 无缺失，且缺失列表升序。
	if missing := verifyTableSets([]string{"b", "a"}, []string{"a", "b"}, false); len(missing) != 0 {
		t.Fatalf("identical sets must not report missing, got %v", missing)
	}
	if missing := verifyTableSets([]string{"z", "a"}, []string{}, false); len(missing) != 2 || missing[0] != "a" || missing[1] != "z" {
		t.Fatalf("missing list must be sorted, got %v", missing)
	}
}

// 字符集提示只在官方 8.0 客户端路径下成立：legacy（5.7 客户端）下不可能发生
// utf8mb4 排序规则回退，同样的 "Unknown database" 就是库确实不存在——实测
// bmc-mysql56 上不存在的非 ASCII 库名被误诊为"库实际存在"的字符集问题。
func TestMysqlDumpFailureHintSkipsCharsetHintOnLegacyClient(t *testing.T) {
	unknown := []string{"mysqldump: Got error: 1049: Unknown database 'p_中文库' when selecting the database"}
	if got := mysqlDumpFailureHint("p_中文库", "u", unknown, true); got != "" {
		t.Fatalf("legacy client must not get the charset hint, got %q", got)
	}
	if got := mysqlDumpFailureHint("p_中文库", "u", unknown, false); got == "" {
		t.Fatal("modern client with a non-ASCII name must get the charset hint")
	}
	// 含单引号的库名在任何客户端下都优先给更具体的原因（该缺陷与客户端版本无关）。
	quoted := []string{"mysqldump: Got error: 1049: Unknown database 'a\\'b' when selecting the database"}
	if got := mysqlDumpFailureHint("a'b", "u", quoted, true); !strings.Contains(got, "--skip-routines") {
		t.Fatalf("quote hint must fire on the legacy client too, got %q", got)
	}
	// 与库名无关的错误（如连不上）不给任何提示。
	if got := mysqlDumpFailureHint("p_中文库", "u", []string{"mysqldump: Got error: 2003: Can't connect"}, false); got != "" {
		t.Fatalf("unrelated error must not produce a hint, got %q", got)
	}
	// 提示不得再断言库一定存在（曾因此把"库不存在"误诊为字符集问题）。
	for _, db := range []string{"p_中文库", "a'b"} {
		hint := mysqlDumpFailureHint(db, "u", unknown, false)
		if hint == "" {
			hint = mysqlDumpFailureHint(db, "u", quoted, false)
		}
		if strings.Contains(hint, "（库实际存在）") {
			t.Fatalf("hint must not assert the database exists: %q", hint)
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

// 库名含单引号时 mysqldump 在 --routines 下会错误转义该名字，备份必然失败
// （上游缺陷）。必须给出指向 --skip-routines 的可诊断提示，而不是只剩一行 stderr。
func TestMySQLDumpQuoteNameHint(t *testing.T) {
	stderr := []string{"mysqldump: Got error: 1049: Unknown database 'a\\'b' when selecting the database"}
	hint := mysqlDumpQuoteNameHint("a'b", stderr)
	if hint == "" || !strings.Contains(hint, "--skip-routines") {
		t.Fatalf("quote name + Unknown database must produce a hint, got %q", hint)
	}
	if hint := mysqlDumpQuoteNameHint("appdb", stderr); hint != "" {
		t.Fatalf("plain name must not produce the hint, got %q", hint)
	}
	if hint := mysqlDumpQuoteNameHint("all", stderr); hint != "" {
		t.Fatalf("all scope must not produce the hint, got %q", hint)
	}
	if hint := mysqlDumpQuoteNameHint("a'b", []string{"Access denied for user 'root'"}); hint != "" {
		t.Fatalf("unrelated stderr must not produce the hint, got %q", hint)
	}
}

// 库级字符集/排序规则来自快照 manifest（存在仓库里），拼接进 CREATE DATABASE 前
// 必须过白名单，否则恶意 manifest 可注入 SQL。
func TestValidCharsetName(t *testing.T) {
	for _, ok := range []string{"utf8mb4", "utf8mb4_unicode_ci", "latin1_swedish_ci", "utf8mb4_0900_ai_ci", "a1_B2"} {
		if !validCharsetName(ok) {
			t.Fatalf("%q must be accepted", ok)
		}
	}
	for _, bad := range []string{"", "utf8mb4; DROP DATABASE x", "utf8mb4'", "utf8mb4`", "utf 8", "utf8mb4-collation", strings.Repeat("a", 65)} {
		if validCharsetName(bad) {
			t.Fatalf("%q must be rejected", bad)
		}
	}
}

// rowsExec 分别回放 processlist 与 metadata_locks 两个查询的输出；failWith 非空时
// 对含 DROP DATABASE 的调用向 stderr 输出该文本并返回 exit 1（模拟锁等待超时等失败）。
type rowsExec struct {
	sessions []string
	mdl      []string
	mdlErr   bool
	failWith string
	cmds     [][]string
}

func (f *rowsExec) Run(_ context.Context, c Cmd, onStdout, onStderr func(string)) (int, error) {
	args := strings.Join(c.Args, " ")
	f.cmds = append(f.cmds, c.Args)
	if f.failWith != "" && strings.Contains(args, "DROP DATABASE") {
		if onStderr != nil {
			onStderr(f.failWith)
		}
		return 1, nil
	}
	switch {
	case strings.Contains(args, "information_schema.processlist"):
		for _, r := range f.sessions {
			onStdout(r)
		}
	case strings.Contains(args, "metadata_locks"):
		if f.mdlErr {
			return 1, nil
		}
		for _, r := range f.mdl {
			onStdout(r)
		}
	}
	return 0, nil
}

func (f *rowsExec) queryWith(sub string) (string, bool) {
	for _, args := range f.cmds {
		if joined := strings.Join(args, " "); strings.Contains(joined, sub) {
			return joined, true
		}
	}
	return "", false
}

func mysqlRestoreSpecForPreflight(exec Executor, target string) *RestoreSpec {
	return &RestoreSpec{
		Kind:       KindMySQL,
		StagingDir: "",
		Database: &model.DatabaseRestore{
			TargetDatabase: target, TargetHost: "127.0.0.1", TargetPort: 3306, TargetUsername: "bmc",
		},
		Secrets: SecretBundle{DBPassword: "pw"},
		Logf:    func(string, string, ...any) {},
		Exec:    exec,
	}
}

// 目标库被其它会话占用时必须拒绝：DROP DATABASE 要拿 schema metadata lock，被占用
// 时无界等待，且这一步失败后回滚会用同一个 DROP 再次失败，阶段机落到 rollback_failed
// 阻塞所有数据库恢复。
func TestMySQLPreflightRejectsBusyTarget(t *testing.T) {
	busy := &rowsExec{sessions: []string{"session 7861 user=bmc host=10.0.0.9 cmd=Sleep time=12 state=NULL"}}
	err := (&MySQLAdapter{}).PreflightRestore(context.Background(), mysqlRestoreSpecForPreflight(busy, "appdb"))
	if err == nil {
		t.Fatal("目标库仍被占用时必须拒绝恢复")
	}
	if !strings.Contains(err.Error(), "active lock(s)/session(s)") {
		t.Fatalf("报错应说明目标库仍有占用: %v", err)
	}
	// 查询本身必须按目标库过滤，并排除检查自身的连接——否则要么拦不住占用，
	// 要么把自己的连接当占用、永远拒绝恢复。
	query, ok := busy.queryWith("information_schema.processlist")
	if !ok {
		t.Fatal("必须查询 information_schema.processlist")
	}
	if !strings.Contains(query, "db = 'appdb'") {
		t.Fatalf("必须按目标库过滤: %s", query)
	}
	if !strings.Contains(query, "CONNECTION_ID()") {
		t.Fatalf("必须排除自身连接: %s", query)
	}

	// 没有其它会话时必须放行。
	idle := &rowsExec{}
	if err := (&MySQLAdapter{}).PreflightRestore(context.Background(), mysqlRestoreSpecForPreflight(idle, "appdb")); err != nil {
		t.Fatalf("无占用时不得拒绝: %v", err)
	}
	// 目标库名含单引号时必须安全转义，不能让查询结构被破坏。
	quoted := &rowsExec{}
	if err := (&MySQLAdapter{}).PreflightRestore(context.Background(), mysqlRestoreSpecForPreflight(quoted, "a'b")); err != nil {
		t.Fatalf("含单引号的目标库名应能正常检查: %v", err)
	}
	q, _ := quoted.queryWith("information_schema.processlist")
	if !strings.Contains(q, "db = 'a''b'") {
		t.Fatalf("目标库名必须做单引号转义: %s", q)
	}
}

// 占用方不设默认库、只用限定名锁住目标库表时 processlist.db 为 NULL，只看 db 会漏检
// 并继续阻塞在 DROP DATABASE（实测阻塞 23.6s）。必须按 metadata lock 兜住。
func TestMySQLPreflightDetectsMetadataLockWithoutDefaultDB(t *testing.T) {
	exec := &rowsExec{mdl: []string{"metadata lock SHARED_READ on appdb.items"}}
	err := (&MySQLAdapter{}).PreflightRestore(context.Background(), mysqlRestoreSpecForPreflight(exec, "appdb"))
	if err == nil {
		t.Fatal("目标库上仍有 GRANTED metadata lock 时必须拒绝恢复")
	}
	if !strings.Contains(err.Error(), "metadata lock SHARED_READ on appdb.items") {
		t.Fatalf("报错应带上具体的锁对象便于排查: %v", err)
	}
	q, ok := exec.queryWith("metadata_locks")
	if !ok {
		t.Fatal("必须查询 performance_schema.metadata_locks")
	}
	if !strings.Contains(q, "OBJECT_SCHEMA = 'appdb'") || !strings.Contains(q, "LOCK_STATUS = 'GRANTED'") {
		t.Fatalf("MDL 查询必须按目标库与 GRANTED 过滤: %s", q)
	}
}

// metadata_locks 在 MySQL 5.7 之前不存在、也可能未启用：该查询失败**不能**阻断恢复，
// 必须退化为只按连接默认库判断（否则会变成"旧服务端永远无法恢复"的新缺陷）。
func TestMySQLPreflightToleratesMissingMetadataLocksTable(t *testing.T) {
	exec := &rowsExec{mdlErr: true}
	if err := (&MySQLAdapter{}).PreflightRestore(context.Background(), mysqlRestoreSpecForPreflight(exec, "appdb")); err != nil {
		t.Fatalf("metadata_locks 查询失败时不得阻断恢复: %v", err)
	}
	if _, ok := exec.queryWith("information_schema.processlist"); !ok {
		t.Fatal("仍应照常检查连接默认库")
	}
}

// 竞态下的锁等待超时必须给出可操作的处置提示，而不是把原始 1205 直接抛给运维；
// 非锁等待的失败（如权限不足）不得被误报成"目标被占用"。
func TestMySQLOverwriteLockTimeoutMessageIsActionable(t *testing.T) {
	exec := &rowsExec{failWith: "ERROR 1205 (HY000) at line 1: Lock wait timeout exceeded; try restarting transaction"}
	spec := mysqlRestoreSpecForPreflight(exec, "appdb")
	spec.ArtifactFile = filepath.Join(t.TempDir(), "dump.sql")
	spec.TargetIsNew = false
	err := (&MySQLAdapter{}).Import(context.Background(), spec)
	if err == nil {
		t.Fatal("锁等待超时必须让 Import 失败")
	}
	if !strings.Contains(err.Error(), "close all target connections and retry") {
		t.Fatalf("应给出可操作的处置提示: %v", err)
	}

	other := &rowsExec{failWith: "ERROR 1044 (42000): Access denied for user 'bmc'@'%' to database 'appdb'"}
	spec.Exec = other
	err = (&MySQLAdapter{}).Import(context.Background(), spec)
	if err == nil {
		t.Fatal("权限错误必须失败")
	}
	if strings.Contains(err.Error(), "locked by another session") {
		t.Fatalf("非锁等待失败不得被误报为占用: %v", err)
	}
	if !strings.Contains(err.Error(), "Access denied") {
		t.Fatalf("原始原因必须保留: %v", err)
	}
}

// 覆盖恢复的 DROP DATABASE 必须带有限的 lock_wait_timeout：MySQL 客户端默认值极大
// （配合 12h 运行期限），预检之后才出现的锁（竞态）会让恢复长期占住全局恢复互斥。
func TestMySQLOverwriteImportBoundsDropLockWait(t *testing.T) {
	if mysqlDropLockWaitSeconds <= 0 || mysqlDropLockWaitSeconds > 300 {
		t.Fatalf("DROP 锁等待上限必须是有限的合理秒数，得到 %d", mysqlDropLockWaitSeconds)
	}
	rec := &argRecorder{}
	spec := &RestoreSpec{
		Kind:         KindMySQL,
		StagingDir:   t.TempDir(),
		ArtifactFile: filepath.Join(t.TempDir(), "dump.sql"),
		TargetIsNew:  false, // 走覆盖分支：DROP + CREATE
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
	setup := ""
	for _, c := range rec.calls {
		for _, a := range c.Args {
			if strings.Contains(a, "DROP DATABASE IF EXISTS") {
				setup = a
			}
		}
	}
	if setup == "" {
		t.Fatalf("未找到覆盖恢复的 DROP 语句，calls=%v", rec.calls)
	}
	if !strings.Contains(setup, "SET SESSION lock_wait_timeout =") {
		t.Fatalf("DROP 前必须设置有限锁等待: %s", setup)
	}
	// 新建目标（TargetIsNew）没有 DROP，不应被强加该设置。
	newRec := &argRecorder{}
	spec.TargetIsNew = true
	spec.Exec = newRec
	if err := (&MySQLAdapter{}).Import(context.Background(), spec); err != nil {
		t.Fatalf("Import(new): %v", err)
	}
	for _, c := range newRec.calls {
		for _, a := range c.Args {
			if strings.Contains(a, "lock_wait_timeout") {
				t.Fatalf("新建目标不应设置 DROP 锁等待: %s", a)
			}
		}
	}
}

// grantsExec 回放 SHOW GRANTS 输出，并按需让 mysqldump 失败（带 stderr）。
type grantsExec struct {
	grants   []string
	dumpExit int
	dumpErr  []string
	dumped   bool
}

func (e *grantsExec) Run(_ context.Context, c Cmd, onStdout, onStderr func(string)) (int, error) {
	args := strings.Join(c.Args, " ")
	switch {
	case strings.Contains(args, "SHOW GRANTS FOR CURRENT_USER()"):
		for _, g := range e.grants {
			if onStdout != nil {
				onStdout(g)
			}
		}
		return 0, nil
	case strings.Contains(args, "information_schema.VIEWS"):
		// 这些用例针对表/触发器/例程权限；库中无视图即无视图可丢，计数回答 0。
		if onStdout != nil {
			onStdout("0")
		}
		return 0, nil
	case filepath.Base(c.Exe) == "mysqldump":
		e.dumped = true
		for _, l := range e.dumpErr {
			if onStderr != nil {
				onStderr(l)
			}
		}
		return e.dumpExit, nil
	}
	return 0, nil
}

func grantsRC(exec Executor, database string) *RunContext {
	return &RunContext{
		Task:    mysqlBackupTask(database),
		Secrets: SecretBundle{DBPassword: "pw"},
		TempDir: "",
		Exec:    exec,
		Logf:    func(string, string, ...any) {},
	}
}

// SHOW GRANTS 的三种对象形式（*.* / `db`.* / `db`.`tbl`）与权限清单里的
// ALL PRIVILEGES 都必须被正确归类，否则要么漏判部分授权（静默丢表），
// 要么把全库账号误判成部分授权（正常备份被拒）。
func TestMySQLSelectScopeParsesGrantForms(t *testing.T) {
	global := mysqlSelectScope([]string{"GRANT SELECT, PROCESS ON *.* TO `u`@`%`"})
	if !global.global {
		t.Fatal("SELECT ON *.* 必须判为全局授权")
	}
	if all := mysqlSelectScope([]string{"GRANT ALL PRIVILEGES ON *.* TO `u`@`%` WITH GRANT OPTION"}); !all.global {
		t.Fatal("ALL PRIVILEGES ON *.* 含 SELECT，必须判为全局授权")
	}
	usage := mysqlSelectScope([]string{"GRANT USAGE ON *.* TO `u`@`%`"})
	if usage.global {
		t.Fatal("USAGE 不含 SELECT")
	}
	schema := mysqlSelectScope([]string{"GRANT SELECT ON `appdb`.* TO `u`@`%`"})
	if !schema.schemas["appdb"] || schema.global {
		t.Fatalf("库级 SELECT 归类错误: %+v", schema)
	}
	tbl := mysqlSelectScope([]string{"GRANT SELECT ON `appdb`.`t1` TO `u`@`%`"})
	if got := tbl.tables["appdb"]; len(got) != 1 || got[0] != "t1" {
		t.Fatalf("表级 SELECT 归类错误: %+v", tbl)
	}
	// 角色授予行没有 ON 对象，不能当成授权；默认角色在真实服务端会被展开成具体的
	// GRANT 行（实测 MySQL 8.0.46），因此展开行照常生效。
	role := mysqlSelectScope([]string{"GRANT `r`@`%` TO `u`@`%`"})
	if role.global || len(role.schemas) != 0 || len(role.tables) != 0 {
		t.Fatalf("角色授予行不得被当作对象授权: %+v", role)
	}
	// 合法库名可以含 " TO "：必须按最后一个 " TO " 切分，否则对象名被截断。
	weird := mysqlSelectScope([]string{"GRANT SELECT ON `a TO b`.* TO `u`@`%`"})
	if !weird.schemas["a TO b"] {
		t.Fatalf("含 \" TO \" 的库名解析错误: %+v", weird)
	}
	// 反引号转义（``）必须还原。
	esc := mysqlSelectScope([]string{"GRANT SELECT ON `a``b`.`t``1` TO `u`@`%`"})
	if got := esc.tables["a`b"]; len(got) != 1 || got[0] != "t`1" {
		t.Fatalf("含反引号的标识符解析错误: %+v", esc)
	}
}

// 只被授予部分表的 SELECT 时 mysqldump 会 rc=0 却静默漏表（实测 t2 的 2500 行
// 无声消失），必须明确拒绝备份，且不得启动 mysqldump 产出不完整 dump。
func TestMySQLBackupRefusesPartialTableGrants(t *testing.T) {
	cases := map[string][]string{
		"表级 SELECT 子集": {
			"GRANT USAGE ON *.* TO `u`@`%`",
			"GRANT EVENT ON `appdb`.* TO `u`@`%`",
			"GRANT SELECT ON `appdb`.`t1` TO `u`@`%`",
			"GRANT SELECT ON `appdb`.`t3` TO `u`@`%`",
		},
		"该库上完全没有 SELECT": {
			"GRANT USAGE ON *.* TO `u`@`%`",
			"GRANT EVENT ON `appdb`.* TO `u`@`%`",
		},
		"权限只在未激活的角色里": {
			"GRANT USAGE ON *.* TO `u`@`%`",
			"GRANT `r`@`%` TO `u`@`%`",
		},
		"其他库的库级 SELECT 不算": {
			"GRANT SELECT ON `otherdb`.* TO `u`@`%`",
		},
	}
	for name, grants := range cases {
		exec := &grantsExec{grants: grants}
		rc := grantsRC(exec, "appdb")
		rc.TempDir = t.TempDir()
		_, err := (&MySQLAdapter{}).Backup(context.Background(), rc)
		if err == nil {
			t.Fatalf("%s: 必须拒绝备份", name)
		}
		if !strings.Contains(err.Error(), "appdb") || !strings.Contains(err.Error(), "SELECT") {
			t.Fatalf("%s: 错误必须点名库与 SELECT 权限: %v", name, err)
		}
		if exec.dumped {
			t.Fatalf("%s: 拒绝时不得再启动 mysqldump（否则仍会产出不完整 dump）", name)
		}
	}
	// 错误必须给出可操作的处置（授予库级 SELECT）。
	exec := &grantsExec{grants: []string{"GRANT SELECT ON `appdb`.`t1` TO `u`@`%`"}}
	rc := grantsRC(exec, "appdb")
	rc.TempDir = t.TempDir()
	_, err := (&MySQLAdapter{}).Backup(context.Background(), rc)
	if err == nil || !strings.Contains(err.Error(), "GRANT SELECT ON `appdb`.*") {
		t.Fatalf("错误必须给出库级 SELECT 的处置建议: %v", err)
	}
	if !strings.Contains(err.Error(), "t1") {
		t.Fatalf("错误应列出已授权的表以便定位: %v", err)
	}
}

// 库级/全局 SELECT 必须照常成功，不能因新检查误伤正常备份。
// （BMC 恒定带 --events --routines --triggers，因此账号还需 EVENT 与 TRIGGER：
// 缺 EVENT 会在 show events 处响亮失败，缺 TRIGGER 会静默丢触发器。）
func TestMySQLBackupAllowsFullGrants(t *testing.T) {
	cases := map[string][]string{
		"全局 SELECT+EVENT+TRIGGER": {"GRANT SELECT, EVENT, TRIGGER ON *.* TO `u`@`%`"},
		"库级 SELECT+EVENT+TRIGGER": {"GRANT SELECT, EVENT, TRIGGER ON `appdb`.* TO `u`@`%`"},
		"库级 ALL PRIVILEGES": {
			"GRANT ALL PRIVILEGES ON `appdb`.* TO `u`@`%`",
		},
		"角色展开后的库级 SELECT+EVENT+TRIGGER": {
			"GRANT USAGE ON *.* TO `u`@`%`",
			"GRANT SELECT, EVENT, TRIGGER ON `appdb`.* TO `u`@`%`",
			"GRANT `r`@`%` TO `u`@`%`",
		},
	}
	for name, grants := range cases {
		exec := &grantsExec{grants: grants}
		rc := grantsRC(exec, "appdb")
		rc.TempDir = t.TempDir()
		if _, err := (&MySQLAdapter{}).Backup(context.Background(), rc); err != nil {
			t.Fatalf("%s: 正常授权不得被拒绝: %v", name, err)
		}
		if !exec.dumped {
			t.Fatalf("%s: 应正常执行 mysqldump", name)
		}
	}
}

// --tables=表名,表名 必须被翻译成 mysqldump 需要的位置参数：mysqldump 的 --tables
// 是布尔开关，原样透传会以 exit 4 失败（实测 "option '--tables' cannot take an
// argument"）。
func TestMySQLDumpArgsTranslateTableSelection(t *testing.T) {
	rec := &argRecorder{}
	task := mysqlBackupTask("appdb")
	task.Source.ExtraArgs = []string{"--tables=t1, t3", "--single-transaction"}
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
	for _, a := range dump.Args {
		if strings.HasPrefix(a, "--tables=") {
			t.Fatalf("--tables= 不得原样透传: %v", dump.Args)
		}
	}
	dash := indexOf(dump.Args, "--")
	if dash < 0 || len(dump.Args) < dash+4 ||
		dump.Args[dash+1] != "appdb" || dump.Args[dash+2] != "t1" || dump.Args[dash+3] != "t3" {
		t.Fatalf("表名必须作为位置参数紧跟库名之后: %v", dump.Args)
	}
}

// 整实例备份与 --tables= 互斥：否则用户以为只备份了部分表，实际拿到整实例 dump。
func TestMySQLValidateRejectsTablesForAllDatabases(t *testing.T) {
	spec := PlanSpec{Kind: KindMySQL, Source: model.PlanSource{
		Database: "all", EstimatedDumpBytes: 1 << 20, ExtraArgs: []string{"--tables=t1"},
	}}
	if err := (&MySQLAdapter{}).Validate(context.Background(), spec); err == nil {
		t.Fatal("database=all 与 --tables= 互斥，必须拒绝")
	}
}

// 缺 TRIGGER 权限时 mysqldump --triggers 会静默丢弃触发器（rc=0、无 warn），
// 必须在导出前明确拒绝，并点名缺的权限与确切 GRANT（实测 MySQL 8.0.46）。
func TestMySQLBackupRefusesMissingTriggerPrivilege(t *testing.T) {
	exec := &grantsExec{grants: []string{
		"GRANT USAGE ON *.* TO `u`@`%`",
		"GRANT SELECT, EVENT ON `appdb`.* TO `u`@`%`",
	}}
	rc := grantsRC(exec, "appdb")
	rc.TempDir = t.TempDir()
	_, err := (&MySQLAdapter{}).Backup(context.Background(), rc)
	if err == nil {
		t.Fatal("缺 TRIGGER 权限必须拒绝备份")
	}
	for _, want := range []string{"TRIGGER", "appdb", "GRANT TRIGGER ON `appdb`.*", "--skip-triggers"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误必须包含 %q: %v", want, err)
		}
	}
	if exec.dumped {
		t.Fatal("拒绝时不得启动 mysqldump（否则仍会静默丢触发器）")
	}
}

// 缺 EVENT 权限时同样拒绝，且不得把 "to database" 读成库级授权缺失。
func TestMySQLBackupRefusesMissingEventPrivilege(t *testing.T) {
	exec := &grantsExec{grants: []string{
		"GRANT USAGE ON *.* TO `u`@`%`",
		"GRANT SELECT, TRIGGER ON `appdb`.* TO `u`@`%`",
	}}
	rc := grantsRC(exec, "appdb")
	rc.TempDir = t.TempDir()
	_, err := (&MySQLAdapter{}).Backup(context.Background(), rc)
	if err == nil {
		t.Fatal("缺 EVENT 权限必须拒绝备份")
	}
	for _, want := range []string{"EVENT", "GRANT EVENT ON `appdb`.*", "--skip-events", "与库级 SELECT 是否缺失无关"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误必须包含 %q: %v", want, err)
		}
	}
	if exec.dumped {
		t.Fatal("拒绝时不得启动 mysqldump")
	}
}

// 显式跳过（--skip-triggers）是授权不足时的正当降级通道：不再拒绝。
func TestMySQLBackupExplicitSkipBypassesObjectCheck(t *testing.T) {
	exec := &grantsExec{grants: []string{
		"GRANT USAGE ON *.* TO `u`@`%`",
		"GRANT SELECT, EVENT ON `appdb`.* TO `u`@`%`",
	}}
	task := mysqlBackupTask("appdb")
	task.Source.ExtraArgs = []string{"--skip-triggers"}
	rc := grantsRC(exec, "appdb")
	rc.Task = task
	rc.TempDir = t.TempDir()
	var logs []string
	rc.Logf = func(lvl, format string, args ...any) {
		logs = append(logs, lvl+" "+fmt.Sprintf(format, args...))
	}
	if _, err := (&MySQLAdapter{}).Backup(context.Background(), rc); err != nil {
		t.Fatalf("显式 --skip-triggers 不得被拒绝: %v", err)
	}
	if !exec.dumped {
		t.Fatal("显式跳过后应正常执行 mysqldump")
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "--skip-triggers") || !strings.Contains(joined, "不含触发器") {
		t.Fatalf("显式跳过必须在日志里说明本次 dump 不含触发器:\n%s", joined)
	}
}

// 显式选表（--tables=...）是正当的子集备份：表级账号不得再被「部分授权」拒绝。
func TestMySQLBackupExplicitTablesBypassesPartialGrantCheck(t *testing.T) {
	exec := &grantsExec{grants: []string{
		"GRANT USAGE ON *.* TO `u`@`%`",
		"GRANT EVENT, TRIGGER ON `appdb`.* TO `u`@`%`",
		"GRANT SELECT ON `appdb`.`t1` TO `u`@`%`",
	}}
	task := mysqlBackupTask("appdb")
	task.Source.ExtraArgs = []string{"--tables=t1"}
	rc := grantsRC(exec, "appdb")
	rc.Task = task
	rc.TempDir = t.TempDir()
	if _, err := (&MySQLAdapter{}).Backup(context.Background(), rc); err != nil {
		t.Fatalf("显式选表不得被拒绝: %v", err)
	}
	if !exec.dumped {
		t.Fatal("显式选表后应正常执行 mysqldump")
	}
}

// 例程可见性无法证明（无权限时无法得知库中是否有例程），因此只告警不拒绝：
// 硬拒绝会误伤不含例程的库，以及没有 SHOW_ROUTINE 权限的 MariaDB。
func TestMySQLBackupWarnsWhenRoutinesInvisible(t *testing.T) {
	collect := func(exec *grantsExec) []string {
		var logs []string
		rc := grantsRC(exec, "appdb")
		rc.TempDir = t.TempDir()
		rc.Logf = func(lvl, format string, args ...any) {
			if lvl == "warn" {
				logs = append(logs, fmt.Sprintf(format, args...))
			}
		}
		if _, err := (&MySQLAdapter{}).Backup(context.Background(), rc); err != nil {
			t.Fatalf("例程不可见不得拒绝备份: %v", err)
		}
		return logs
	}
	logs := collect(&grantsExec{grants: []string{
		"GRANT USAGE ON *.* TO `u`@`%`",
		"GRANT SELECT, EVENT, TRIGGER ON `appdb`.* TO `u`@`%`",
	}})
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "SHOW_ROUTINE") || !strings.Contains(joined, "--skip-routines") {
		t.Fatalf("必须给出可执行的例程告警: %v", logs)
	}
	// 有全局 SHOW_ROUTINE 时不再告警。
	if logs := collect(&grantsExec{grants: []string{"GRANT SELECT, EVENT, TRIGGER, SHOW_ROUTINE ON *.* TO `u`@`%`"}}); len(logs) != 0 {
		t.Fatalf("全局 SHOW_ROUTINE 不应产生例程告警: %v", logs)
	}
}

// 权限类失败必须点名真正缺的权限，不得把 "Access denied ... to database" 读成
// 库级授权缺失，也不得再建议「改用全局 SELECT」（实测全局 SELECT 无法执行
// SHOW EVENTS）。
func TestMySQLDumpPrivilegeHintNamesMissingPrivilege(t *testing.T) {
	events := []string{"mysqldump: Couldn't execute 'show events': Access denied for user 'u'@'%' to database 'appdb' (1044)"}
	hint := mysqlDumpFailureHint("appdb", "u", events, false)
	for _, want := range []string{"EVENT", "GRANT EVENT ON `appdb`.*", "--skip-events", "不代表"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("事件权限提示必须包含 %q: %q", want, hint)
		}
	}
	if strings.Contains(hint, "改用拥有全局 SELECT") {
		t.Fatalf("提示不得再建议全局 SELECT: %q", hint)
	}
	triggers := []string{"mysqldump: Couldn't execute 'show triggers': Access denied for user 'u'@'%' to database 'appdb' (1044)"}
	if hint := mysqlDumpFailureHint("appdb", "u", triggers, true); !strings.Contains(hint, "GRANT TRIGGER ON `appdb`.*") {
		t.Fatalf("触发器权限提示缺失: %q", hint)
	}
	routines := []string{"mysqldump: u has insufficient privileges to SHOW CREATE FUNCTION `f`!"}
	if hint := mysqlDumpFailureHint("appdb", "u", routines, false); !strings.Contains(hint, "SHOW_ROUTINE") {
		t.Fatalf("例程权限提示缺失: %q", hint)
	}
	// 整实例备份只能给全局授权（报错里的库名可能是任意库）。
	allHint := mysqlDumpFailureHint("all", "u", events, false)
	if !strings.Contains(allHint, "GRANT EVENT ON *.*") {
		t.Fatalf("整实例提示必须给全局授权: %q", allHint)
	}
}

// 读取授权失败时无法证明不会漏表，必须拒绝而不是放行。
func TestMySQLBackupRefusesWhenGrantsUnreadable(t *testing.T) {
	exec := &grantsExec{grants: []string{"GRANT SELECT ON *.* TO `u`@`%`"}}
	rc := grantsRC(&failingGrantsExec{inner: exec}, "appdb")
	rc.TempDir = t.TempDir()
	_, err := (&MySQLAdapter{}).Backup(context.Background(), rc)
	if err == nil {
		t.Fatal("SHOW GRANTS 失败必须拒绝备份")
	}
	if exec.dumped {
		t.Fatal("拒绝时不得启动 mysqldump")
	}
}

// failingGrantsExec 让 SHOW GRANTS 以非零退出并在 stderr 给出原因。
type failingGrantsExec struct{ inner *grantsExec }

func (f *failingGrantsExec) Run(_ context.Context, c Cmd, onStdout, onStderr func(string)) (int, error) {
	if strings.Contains(strings.Join(c.Args, " "), "SHOW GRANTS FOR CURRENT_USER()") {
		if onStderr != nil {
			onStderr("mysql: Got error: 2003: Can't connect to MySQL server on '127.0.0.1'")
		}
		return 1, nil
	}
	return f.inner.Run(context.Background(), c, onStdout, onStderr)
}

// 备份失败时 error_message 必须直接带上 DB 工具 stderr 的原因（此前只出现在 run
// 日志流里，列表/详情页只剩 "mysqldump failed (exit 2)"，权限错与网络错不可区分）。
func TestMySQLBackupErrorMessageCarriesStderrReason(t *testing.T) {
	exec := &grantsExec{
		grants:   []string{"GRANT SELECT, EVENT, TRIGGER ON *.* TO `u`@`%`"},
		dumpExit: 2,
		dumpErr: []string{
			"mysqldump: [Warning] Using a password on the command line interface can be insecure.",
			"mysqldump: Couldn't execute 'show events': Access denied for user 'u'@'%' to database 'appdb' (1044)",
		},
	}
	rc := grantsRC(exec, "appdb")
	rc.TempDir = t.TempDir()
	_, err := (&MySQLAdapter{}).Backup(context.Background(), rc)
	if err == nil {
		t.Fatal("mysqldump 非零退出必须失败")
	}
	if !strings.Contains(err.Error(), "exit 2") {
		t.Fatalf("必须保留退出码: %v", err)
	}
	if !strings.Contains(err.Error(), "Access denied for user 'u'@'%' to database 'appdb' (1044)") {
		t.Fatalf("必须并入 stderr 的可操作原因: %v", err)
	}
}

// PG 的 stderr 里原因行在前、detail 行在后：必须挑出 error 行而不是最后一行。
func TestStderrReasonPrefersErrorLine(t *testing.T) {
	tail := []string{
		"pg_dump: error: query failed: ERROR:  permission denied for table p2",
		"pg_dump: detail: Query was: LOCK TABLE public.p1, public.p2 IN ACCESS SHARE MODE",
	}
	if got := stderrReason(tail); !strings.Contains(got, "permission denied for table p2") {
		t.Fatalf("必须挑出原因行，得到 %q", got)
	}
	// 没有 error/denied/failed 字样时退回最后一行。
	if got := stderrReason([]string{"first", "last"}); got != "last" {
		t.Fatalf("无关键字时必须退回最后一行，得到 %q", got)
	}
	if got := stderrReason(nil); got != "" {
		t.Fatalf("空 stderr 不得产生内容，得到 %q", got)
	}
	err := exitErrorWithStderr("pg_dump", 1, nil, tail)
	if !strings.Contains(err.Error(), "pg_dump failed (exit 1)") || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("错误必须同时含工具与原因: %v", err)
	}
}

// 各代服务端 root 的 SHOW GRANTS 原文（实测 bmc-mysql55/56/57/84-test、
// bmc-mariadb106-test、bmc-mysql-test）。这些账号被用于现网整库/整实例计划，
// 解析必须判为全局 SELECT——否则新检查会把所有既有备份计划拒掉。
func TestMySQLSelectScopeRealServerOutputs(t *testing.T) {
	cases := map[string][]string{
		"mysql55/56": {
			"GRANT ALL PRIVILEGES ON *.* TO 'root'@'localhost' IDENTIFIED BY PASSWORD '*AFA7207C4C299D26E88C6F4197958E4D49BDC65C' WITH GRANT OPTION",
			"GRANT PROXY ON ''@'' TO 'root'@'localhost' WITH GRANT OPTION",
		},
		"mysql57": {
			"GRANT ALL PRIVILEGES ON *.* TO 'root'@'localhost' WITH GRANT OPTION",
			"GRANT PROXY ON ''@'' TO 'root'@'localhost' WITH GRANT OPTION",
		},
		"mysql8.4": {
			"GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, RELOAD, SHUTDOWN, PROCESS, FILE, REFERENCES, INDEX, ALTER, SHOW DATABASES, SUPER, CREATE TEMPORARY TABLES, LOCK TABLES, EXECUTE, REPLICATION SLAVE, REPLICATION CLIENT, CREATE VIEW, SHOW VIEW, CREATE ROUTINE, ALTER ROUTINE, CREATE USER, EVENT, TRIGGER, CREATE TABLESPACE, CREATE ROLE, DROP ROLE ON *.* TO `root`@`localhost` WITH GRANT OPTION",
			"GRANT ALLOW_NONEXISTENT_DEFINER,APPLICATION_PASSWORD_ADMIN,SYSTEM_USER,SYSTEM_VARIABLES_ADMIN ON *.* TO `root`@`localhost` WITH GRANT OPTION",
			"GRANT PROXY ON ``@`` TO `root`@`localhost` WITH GRANT OPTION",
		},
		"mariadb10.6": {
			"GRANT ALL PRIVILEGES ON *.* TO `root`@`localhost` IDENTIFIED BY PASSWORD '*ED084723F7D9E67F477711729C6641018F12B972' WITH GRANT OPTION",
			"GRANT PROXY ON ``@`%` TO `root`@`localhost` WITH GRANT OPTION",
		},
		"mysql8.0": {
			"GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, RELOAD, SHUTDOWN, PROCESS, FILE, REFERENCES, INDEX, ALTER, SHOW DATABASES, SUPER, CREATE TEMPORARY TABLES, LOCK TABLES, EXECUTE, REPLICATION SLAVE, REPLICATION CLIENT, CREATE VIEW, SHOW VIEW, CREATE ROUTINE, ALTER ROUTINE, CREATE USER, EVENT, TRIGGER, CREATE TABLESPACE, CREATE ROLE, DROP ROLE ON *.* TO `root`@`localhost` WITH GRANT OPTION",
			"GRANT PROXY ON ``@`` TO `root`@`localhost` WITH GRANT OPTION",
		},
		// 库级授权（真实 SHOW GRANTS 形态）也必须判为完整。
		"库级 SELECT+EVENT": {
			"GRANT USAGE ON *.* TO `u`@`%`",
			"GRANT SELECT, EVENT ON `appdb`.* TO `u`@`%`",
		},
		// 部分授权（本次修复针对的形态）。
		"表级子集": {
			"GRANT USAGE ON *.* TO `u`@`%`",
			"GRANT EVENT ON `appdb`.* TO `u`@`%`",
			"GRANT SELECT ON `appdb`.`t1` TO `u`@`%`",
			"GRANT SELECT ON `appdb`.`t3` TO `u`@`%`",
		},
	}
	wantGlobal := map[string]bool{
		"mysql55/56": true, "mysql57": true, "mysql8.4": true, "mariadb10.6": true,
		"mysql8.0": true, "库级 SELECT+EVENT": false, "表级子集": false,
	}
	for name, lines := range cases {
		sc := mysqlSelectScope(lines)
		if sc.global != wantGlobal[name] {
			t.Fatalf("%s: global=%v want %v (%+v)", name, sc.global, wantGlobal[name], sc)
		}
		if name == "库级 SELECT+EVENT" && !sc.schemas["appdb"] {
			t.Fatalf("%s: 必须识别库级授权: %+v", name, sc)
		}
		if name == "表级子集" && len(sc.schemas) != 0 {
			t.Fatalf("%s: 不得识别出库级授权: %+v", name, sc)
		}
	}
}

// viewScopeExec 回放 SHOW GRANTS 与 information_schema.VIEWS 的计数，并记录是否真的
// 启动了 mysqldump。
type viewScopeExec struct {
	grants []string
	views  string // VIEWS 计数查询的 stdout；空串表示查询没有输出（无法证明）
	dumped bool
}

func (e *viewScopeExec) Run(_ context.Context, c Cmd, onStdout, onStderr func(string)) (int, error) {
	args := strings.Join(c.Args, " ")
	switch {
	case strings.Contains(args, "SHOW GRANTS FOR CURRENT_USER()"):
		for _, g := range e.grants {
			if onStdout != nil {
				onStdout(g)
			}
		}
		return 0, nil
	case strings.Contains(args, "information_schema.VIEWS"):
		if e.views != "" && onStdout != nil {
			onStdout(e.views)
		}
		return 0, nil
	case filepath.Base(c.Exe) == "mysqldump":
		e.dumped = true
		return 0, nil
	}
	return 0, nil
}

func viewScopeRC(t *testing.T, exec Executor, database string, logs *[]string) *RunContext {
	t.Helper()
	rc := grantsRC(exec, database)
	rc.TempDir = t.TempDir()
	rc.Logf = func(level, format string, args ...any) {
		*logs = append(*logs, level+" "+fmt.Sprintf(format, args...))
	}
	return rc
}

// 库中确有视图而账号缺 SHOW VIEW 时必须拒绝：mysqldump 此时无法导出视图
// （实测 MySQL 5.7/8.0、MariaDB 10.6 都在 SHOW CREATE VIEW 处 rc=2），报错只写作
// "Couldn't execute 'show create table'"，运维看不出缺哪个权限。
func TestMySQLBackupRefusesViewLossWhenViewsExist(t *testing.T) {
	exec := &viewScopeExec{
		grants: []string{"GRANT SELECT, EVENT, TRIGGER ON `appdb`.* TO `u`@`%`"},
		views:  "2",
	}
	var logs []string
	rc := viewScopeRC(t, exec, "appdb", &logs)
	_, err := (&MySQLAdapter{}).Backup(context.Background(), rc)
	if err == nil {
		t.Fatal("库中有视图且缺 SHOW VIEW 时必须拒绝备份")
	}
	for _, want := range []string{"SHOW VIEW", "GRANT SHOW VIEW ON `appdb`.*", "--ignore-table=appdb."} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误必须包含 %q: %v", want, err)
		}
	}
	if exec.dumped {
		t.Fatal("拒绝时不得再启动 mysqldump")
	}
}

// 无视图的库不得因缺 SHOW VIEW 被误伤（视图数为 0 时没有可丢的对象）。
func TestMySQLBackupAllowsViewLessDatabaseWithoutShowView(t *testing.T) {
	exec := &viewScopeExec{
		grants: []string{"GRANT SELECT, EVENT, TRIGGER ON `appdb`.* TO `u`@`%`"},
		views:  "0",
	}
	var logs []string
	rc := viewScopeRC(t, exec, "appdb", &logs)
	if _, err := (&MySQLAdapter{}).Backup(context.Background(), rc); err != nil {
		t.Fatalf("无视图的库不得被拒绝: %v", err)
	}
	if !exec.dumped {
		t.Fatal("应正常执行 mysqldump")
	}
}

// 无法枚举视图数量（查询无输出）时只告警不拒绝：与例程可见性同一取舍，避免把
// "证明不了"当成"有视图"而误伤。
func TestMySQLBackupWarnsWhenViewCountUnprovable(t *testing.T) {
	exec := &viewScopeExec{
		grants: []string{"GRANT SELECT, EVENT, TRIGGER ON `appdb`.* TO `u`@`%`"},
		views:  "",
	}
	var logs []string
	rc := viewScopeRC(t, exec, "appdb", &logs)
	if _, err := (&MySQLAdapter{}).Backup(context.Background(), rc); err != nil {
		t.Fatalf("无法证明时必须放行（只告警）: %v", err)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "无法确认库") {
		t.Fatalf("应留下无法确认视图的告警，实际日志:\n%s", strings.Join(logs, "\n"))
	}
}

// --ignore-table 是缺 SHOW VIEW 时唯一的降级通道（白名单已允许该前缀）：显式排除
// 视图后必须放行，并在日志里说明本次 dump 不含该视图。
func TestMySQLBackupViewRejectSkippedForExplicitIgnoreTable(t *testing.T) {
	exec := &viewScopeExec{
		grants: []string{"GRANT SELECT, EVENT, TRIGGER ON `appdb`.* TO `u`@`%`"},
		views:  "1",
	}
	var logs []string
	rc := viewScopeRC(t, exec, "appdb", &logs)
	rc.Task.Source.ExtraArgs = []string{"--ignore-table=appdb.v1"}
	if _, err := (&MySQLAdapter{}).Backup(context.Background(), rc); err != nil {
		t.Fatalf("显式排除视图后必须放行: %v", err)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "--ignore-table=appdb.v1") {
		t.Fatalf("日志必须说明本次排除了哪个视图，实际日志:\n%s", joined)
	}
}

// 整实例备份无法枚举各库授权：缺全局 SHOW VIEW 只告警不拒绝（与 TRIGGER 处理一致）。
func TestMySQLBackupAllModeWarnsWithoutGlobalShowView(t *testing.T) {
	exec := &viewScopeExec{
		grants: []string{"GRANT SELECT, EVENT, TRIGGER ON *.* TO `u`@`%`"},
	}
	var logs []string
	rc := viewScopeRC(t, exec, "all", &logs)
	if _, err := (&MySQLAdapter{}).Backup(context.Background(), rc); err != nil {
		t.Fatalf("整实例备份不得被拒绝: %v", err)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "SHOW VIEW") || !strings.Contains(joined, "GRANT SHOW VIEW ON *.*") {
		t.Fatalf("应给出全局 SHOW VIEW 的告警，实际日志:\n%s", joined)
	}
}
