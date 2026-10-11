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
	// "只被授予部分表"而拒绝备份。这里模拟全局 SELECT 的账号。
	if indexOf(c.Args, "SHOW GRANTS FOR CURRENT_USER()") >= 0 && onStdout != nil {
		onStdout("GRANT SELECT ON *.* TO `bmc`@`%`")
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
	if got := mysqlDumpFailureHint("p_中文库", unknown, true); got != "" {
		t.Fatalf("legacy client must not get the charset hint, got %q", got)
	}
	if got := mysqlDumpFailureHint("p_中文库", unknown, false); got == "" {
		t.Fatal("modern client with a non-ASCII name must get the charset hint")
	}
	// 含单引号的库名在任何客户端下都优先给更具体的原因（该缺陷与客户端版本无关）。
	quoted := []string{"mysqldump: Got error: 1049: Unknown database 'a\\'b' when selecting the database"}
	if got := mysqlDumpFailureHint("a'b", quoted, true); !strings.Contains(got, "--skip-routines") {
		t.Fatalf("quote hint must fire on the legacy client too, got %q", got)
	}
	// 与库名无关的错误（如连不上）不给任何提示。
	if got := mysqlDumpFailureHint("p_中文库", []string{"mysqldump: Got error: 2003: Can't connect"}, false); got != "" {
		t.Fatalf("unrelated error must not produce a hint, got %q", got)
	}
	// 提示不得再断言库一定存在（曾因此把"库不存在"误诊为字符集问题）。
	for _, db := range []string{"p_中文库", "a'b"} {
		hint := mysqlDumpFailureHint(db, unknown, false)
		if hint == "" {
			hint = mysqlDumpFailureHint(db, quoted, false)
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
func TestMySQLBackupAllowsFullGrants(t *testing.T) {
	cases := map[string][]string{
		"全局 SELECT": {"GRANT SELECT ON *.* TO `u`@`%`"},
		"库级 SELECT": {"GRANT SELECT, EVENT ON `appdb`.* TO `u`@`%`"},
		"库级 ALL PRIVILEGES": {
			"GRANT ALL PRIVILEGES ON `appdb`.* TO `u`@`%`",
		},
		"角色展开后的库级 SELECT": {
			"GRANT USAGE ON *.* TO `u`@`%`",
			"GRANT SELECT ON `appdb`.* TO `u`@`%`",
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

