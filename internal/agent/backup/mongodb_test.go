package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"backupmanagementcenter/internal/model"
)

// mongoExecutor 记录调用，并把 mongosh 脚本内容回放给回调，用于断言生成的 JS。
type mongoExecutor struct {
	calls  []Cmd
	stdout []string // 每次调用要回放的 stdout 行
	stderr []string // 每次调用要回放的 stderr 行
	exits  []int    // 每次调用的退出码（缺省 0）
	n      int
}

func (e *mongoExecutor) Run(_ context.Context, c Cmd, onStdout, onStderr func(string)) (int, error) {
	idx := e.n
	e.n++
	e.calls = append(e.calls, c)
	code := 0
	if idx < len(e.exits) {
		code = e.exits[idx]
	}
	if idx < len(e.stdout) && onStdout != nil {
		onStdout(e.stdout[idx])
	}
	if idx < len(e.stderr) && onStderr != nil {
		onStderr(e.stderr[idx])
	}
	return code, nil
}

// mongorestore 把 "found collection ..." / "archive prelude ..." 全写到 stderr，
// 只收集 stdout 会让每次恢复都判成 "could not read any namespace"。
func TestMongoVerifyReadsNamespacesFromStderr(t *testing.T) {
	dir := t.TempDir()
	exec := &mongoExecutor{
		stderr: []string{
			"2026-09-30T13:21:48.738+0000\tarchive prelude appdb.orders\n" +
				"2026-09-30T13:21:48.738+0000\tfound collection appdb.orders bson to restore to appdb.orders\n" +
				"2026-09-30T13:21:48.738+0000\tfound collection metadata from appdb.orders to restore to appdb.orders\n" +
				"2026-09-30T13:21:48.738+0000\tdry run completed\n",
			"orders\n",
		},
	}
	spec := &RestoreSpec{
		Kind:         KindMongoDB,
		StagingDir:   dir,
		ArtifactFile: dir + "/a.archive",
		Database: &model.DatabaseRestore{
			TargetDatabase: "appdb", TargetHost: "127.0.0.1", TargetPort: 27017, TargetUsername: "bmc",
		},
		Secrets: SecretBundle{DBPassword: "pw"},
		Logf:    func(string, string, ...any) {},
		Exec:    exec,
	}
	err := (&MongoDBAdapter{}).VerifyRestored(context.Background(), spec)
	if err != nil && strings.Contains(err.Error(), "could not read any namespace") {
		t.Fatalf("stderr output must be parsed, got: %v", err)
	}
}

// mongodump 的 --config 必须是 Database Tools 认可的 uri+password 结构。
// 扁平 host/port/username/authSource 会被拒绝并 exit 1，备份永远不可能成功。
func TestMongoConfigUsesUIDocumentSchema(t *testing.T) {
	content := buildMongoConfig("db.internal", 27017, "bmc", "p@ss:word/1", "")
	for _, bad := range []string{"host:", "port:", "username:", "authSource:"} {
		if strings.Contains(content, bad) {
			t.Fatalf("config must not contain %q (unmarshal error with Database Tools):\n%s", bad, content)
		}
	}
	if !strings.Contains(content, "uri: ") {
		t.Fatalf("config must carry a uri document:\n%s", content)
	}
	// 凭据必须转义，否则特殊字符会破坏 URI 结构。
	if !strings.Contains(content, "p%40ss%3Aword%2F1") {
		t.Fatalf("credentials must be URL-escaped:\n%s", content)
	}
	if !strings.Contains(content, "authSource=admin") {
		t.Fatalf("authSource must default to admin:\n%s", content)
	}
}

// 无用户名时不得产出 "mongodb://:pass@host" 这种空用户信息段。
func TestMongoURIOmitsEmptyCredentials(t *testing.T) {
	uri := mongoURI("db.internal", 27017, "", "pw", "admin")
	if strings.Contains(uri, "@") {
		t.Fatalf("uri must not contain credentials when username is empty: %s", uri)
	}
	if want := "mongodb://db.internal:27017/?authSource=admin"; uri != want {
		t.Fatalf("got %s want %s", uri, want)
	}
}

// 空格必须编码成 %20。url.QueryEscape 按 form 语义会给出 "+"，而 URI 的 userinfo
// 与查询值里 "+" 是字面加号，服务端不会还原成空格——含空格的密码因此永远认证失败
// （实测：密码 "p a s s" 的计划备份 100% 失败，去掉 config 的 password 字段后仍失败）。
func TestMongoURIEncodesSpaceAsPercent20(t *testing.T) {
	uri := mongoURI("db.internal", 27017, "bmc", "p a s s", "my db")
	if strings.Contains(uri, "+") {
		t.Fatalf("uri must not contain '+': %s", uri)
	}
	if !strings.Contains(uri, "bmc:p%20a%20s%20s@") {
		t.Fatalf("space in password must be %%20: %s", uri)
	}
	if !strings.Contains(uri, "authSource=my%20db") {
		t.Fatalf("space in authSource must be %%20: %s", uri)
	}
}

// TargetExists 的 listDatabases 必须挂在 Database 对象上：Mongo(uri) 返回的
// 连接对象没有 adminCommand，调用会抛 TypeError。
func TestMongoTargetExistsCallsAdminCommandOnDatabase(t *testing.T) {
	script := mongoTargetExistsScript("appdb")
	if strings.Contains(script, "conn.adminCommand") {
		t.Fatalf("adminCommand must not be called on the connection object:\n%s", script)
	}
	if !strings.Contains(script, "db.adminCommand(") {
		t.Fatalf("adminCommand must be called on the database object:\n%s", script)
	}
}

// 解析当前 Database Tools 的 archive 模式输出。
func TestMongoDumpNamespacesParsesArchiveFormat(t *testing.T) {
	out := "2026-09-30T07:47:28.092+0000\tusing write concern: &{0x2564321ac468 0s}\n" +
		"2026-09-30T07:47:28.110+0000\tarchive prelude `appdb.users`\n" +
		"2026-09-30T07:47:28.110+0000\tpreparing collections to restore from\n" +
		"2026-09-30T07:47:28.110+0000\tfound collection `appdb.users` bson to restore to `appdb.users`\n" +
		"2026-09-30T07:47:28.110+0000\tfound collection metadata from `appdb.users` to restore to `appdb.users`\n" +
		"2026-09-30T07:47:28.110+0000\tdry run completed\n"
	got := mongoDumpNamespaces(out)
	if len(got) != 1 || got[0] != "appdb.users" {
		t.Fatalf("expected [appdb.users], got %v", got)
	}
}

// 目录模式的旧格式仍要认，且取 from 一侧（to 可能是 --nsTo 重命名后的目标）。
func TestMongoDumpNamespacesParsesLegacyFormat(t *testing.T) {
	out := "2026-01-01T00:00:00.000+0000\treading metadata for appdb.users from dump/appdb/users.metadata.json\n" +
		"2026-01-01T00:00:00.000+0000\treading metadata for appdb.orders from dump/appdb/orders.metadata.json\n"
	got := mongoDumpNamespaces(out)
	if len(got) != 2 || got[0] != "appdb.users" || got[1] != "appdb.orders" {
		t.Fatalf("expected [appdb.users appdb.orders], got %v", got)
	}
}

// 认不出的输出必须返回空，交给 VerifyRestored 判定为失败（fail-closed）。
func TestMongoDumpNamespacesUnknownOutputIsEmpty(t *testing.T) {
	if got := mongoDumpNamespaces("some unrelated tool chatter\n"); len(got) != 0 {
		t.Fatalf("expected no namespaces, got %v", got)
	}
}

// 解析不到任何命名空间时必须中止，绝不能当成“归档为空”而放过未验证的导入。
func TestMongoVerifyFailsClosedWhenOutputUnparsable(t *testing.T) {
	dir := t.TempDir()
	exec := &mongoExecutor{stdout: []string{"nothing useful here"}}
	spec := &RestoreSpec{
		Kind:         KindMongoDB,
		StagingDir:   dir,
		ArtifactFile: dir + "/a.archive",
		Database: &model.DatabaseRestore{
			TargetDatabase: "appdb", TargetHost: "127.0.0.1", TargetPort: 27017, TargetUsername: "bmc",
		},
		Secrets: SecretBundle{DBPassword: "pw"},
		Logf:    func(string, string, ...any) {},
		Exec:    exec,
	}
	err := (&MongoDBAdapter{}).VerifyRestored(context.Background(), spec)
	if err == nil {
		t.Fatal("unparsable dry-run output must fail the verification")
	}
	if !strings.Contains(err.Error(), "could not read any namespace") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// 覆盖恢复必须“完整替换目标内容”。mongorestore --drop 只重建归档里存在的集合，
// 归档之外的集合会残留（实测目标 pre-existing 集合在覆盖后仍在），因此必须先整库
// 删除，与 MySQL/PostgreSQL 适配器的 DROP DATABASE 等价。
func TestMongoImportDropsTargetBeforeOverwrite(t *testing.T) {
	dir := t.TempDir()
	exec := &mongoExecutor{}
	spec := &RestoreSpec{
		Kind:         KindMongoDB,
		StagingDir:   dir,
		ArtifactFile: dir + "/a.archive",
		Database: &model.DatabaseRestore{
			TargetDatabase: "appdb", TargetHost: "127.0.0.1", TargetPort: 27017, TargetUsername: "bmc",
		},
		Secrets: SecretBundle{DBPassword: "pw"},
		Logf:    func(string, string, ...any) {},
		Exec:    exec,
		TargetIsNew: false, // 覆盖已有目标
	}
	if err := (&MongoDBAdapter{}).Import(context.Background(), spec); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(exec.calls) != 2 {
		t.Fatalf("calls = %d, want 2 (drop then restore)", len(exec.calls))
	}
	if !strings.Contains(exec.calls[0].Exe, "mongosh") {
		t.Fatalf("first call must drop the target via mongosh, got %q", exec.calls[0].Exe)
	}
	if !strings.Contains(exec.calls[1].Exe, "mongorestore") {
		t.Fatalf("second call must be mongorestore, got %q", exec.calls[1].Exe)
	}
	// drop 脚本必须真的删库
	js, err := os.ReadFile(filepath.Join(dir, "mongo-check.js"))
	if err != nil {
		t.Fatalf("read drop script: %v", err)
	}
	if !strings.Contains(string(js), "dropDatabase()") {
		t.Fatalf("drop script must call dropDatabase, got %q", string(js))
	}
}

// 目标为新建时不得先删库（该库由本次运行创建，且可能并不存在）。
func TestMongoImportDoesNotDropNewTarget(t *testing.T) {
	dir := t.TempDir()
	exec := &mongoExecutor{}
	spec := &RestoreSpec{
		Kind:         KindMongoDB,
		StagingDir:   dir,
		ArtifactFile: dir + "/a.archive",
		Database: &model.DatabaseRestore{
			TargetDatabase: "appdb", TargetHost: "127.0.0.1", TargetPort: 27017, TargetUsername: "bmc",
		},
		Secrets: SecretBundle{DBPassword: "pw"},
		Logf:    func(string, string, ...any) {},
		Exec:    exec,
		TargetIsNew: true,
	}
	if err := (&MongoDBAdapter{}).Import(context.Background(), spec); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(exec.calls) != 1 || !strings.Contains(exec.calls[0].Exe, "mongorestore") {
		t.Fatalf("new target must only run mongorestore, got %d calls: %+v", len(exec.calls), exec.calls)
	}
}

// mongodump 限制：--oplog 只能用于整实例 dump，配单库时以
// "bad option: --oplog mode only supported on full dumps" 失败。
// 必须在 Validate 阶段拒绝，否则该计划每次备份都失败。
func TestMongoValidateRejectsOplogWithSingleDatabase(t *testing.T) {
	a := &MongoDBAdapter{}
	base := PlanSpec{Kind: KindMongoDB, Source: model.PlanSource{
		Host: "h", Port: 27017, Username: "bmc", EstimatedDumpBytes: 1 << 20,
	}}

	bad := base
	bad.Source.Database = "appdb"
	bad.Source.CaptureOplog = true
	if err := a.Validate(context.Background(), bad); err == nil {
		t.Fatal("capture_oplog with a single database must be rejected")
	}

	ok := base
	ok.Source.Database = "all"
	ok.Source.CaptureOplog = true
	if err := a.Validate(context.Background(), ok); err != nil {
		t.Fatalf("capture_oplog with all scope must be accepted: %v", err)
	}
}

// mongodbExecutor 按命令行内容回放输出：备份流程会先跑授权检查（mongosh），
// 再跑 mongodump。
type mongodbExecutor struct {
	roles     []string // connectionStatus 的角色行（role\tdb）
	rolesExit int
	dumpExit  int
	dumpErr   []string
	dumped    bool
	calls     []Cmd
}

func (e *mongodbExecutor) Run(_ context.Context, c Cmd, onStdout, onStderr func(string)) (int, error) {
	e.calls = append(e.calls, c)
	switch {
	case strings.Contains(c.Exe, "mongosh"):
		for _, r := range e.roles {
			if onStdout != nil {
				onStdout(r)
			}
		}
		return e.rolesExit, nil
	case strings.Contains(c.Exe, "mongodump"):
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

func mongodbBackupRC(t *testing.T, exec Executor, database string) *RunContext {
	t.Helper()
	return &RunContext{
		RunID: "run-1",
		Task: model.BackupTask{
			PlanID: "plan-1",
			Kind:   KindMongoDB,
			Source: model.PlanSource{
				Host: "127.0.0.1", Port: 27017, Username: "bmc",
				Database: database, EstimatedDumpBytes: 1024,
			},
		},
		Secrets: SecretBundle{DBPassword: "pw"},
		TempDir: t.TempDir(),
		Exec:    exec,
		Logf:    func(string, string, ...any) {},
	}
}

// 只有库级角色的账号在 all 模式下必须被拒绝：listDatabases 对这类账号只返回已授权
// 库（实测 read@p_f2_m1_src 只看到 1 个库），mongodump 不报错也不告警，快照会静默
// 缺失其它库。
func TestMongoBackupAllRefusesDatabaseScopedRoles(t *testing.T) {
	exec := &mongodbExecutor{roles: []string{"read\tp_f2_m1_src"}}
	_, err := (&MongoDBAdapter{}).Backup(context.Background(), mongodbBackupRC(t, exec, "all"))
	if err == nil {
		t.Fatal("库级角色 + all 模式必须拒绝")
	}
	for _, want := range []string{"backup", "read@p_f2_m1_src", "单库备份"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误必须可执行（含 %q）: %v", want, err)
		}
	}
	if exec.dumped {
		t.Fatal("拒绝时不得再启动 mongodump")
	}
}

// 集群级角色（readAnyDatabase/clusterMonitor）能枚举全部库，必须照常导出。
func TestMongoBackupAllAllowsClusterWideRoles(t *testing.T) {
	for _, role := range []string{"readAnyDatabase\tadmin", "clusterMonitor\tadmin", "root\tadmin"} {
		exec := &mongodbExecutor{roles: []string{role}}
		if _, err := (&MongoDBAdapter{}).Backup(context.Background(), mongodbBackupRC(t, exec, "all")); err != nil {
			t.Fatalf("%s: 集群级角色不得被拒绝: %v", role, err)
		}
		if !exec.dumped {
			t.Fatalf("%s: 应正常执行 mongodump", role)
		}
	}
}

// 单库模式不做该判定：部分集合授权是响亮失败（mongodump 用 listCollections 枚举
// 全部集合），行为不得改变。
func TestMongoBackupSingleDatabaseSkipsScopeCheck(t *testing.T) {
	exec := &mongodbExecutor{roles: []string{"read\tappdb"}}
	if _, err := (&MongoDBAdapter{}).Backup(context.Background(), mongodbBackupRC(t, exec, "appdb")); err != nil {
		t.Fatalf("单库模式不得被授权检查拦住: %v", err)
	}
	if !exec.dumped {
		t.Fatal("应正常执行 mongodump")
	}
	for _, c := range exec.calls {
		if strings.Contains(c.Exe, "mongosh") {
			t.Fatal("单库模式不应运行授权检查")
		}
	}
}

// mongodump 失败时 error_message 必须带上 stderr 原因（与 mysql/pg 一致）。
func TestMongoDumpFailureCarriesStderrReason(t *testing.T) {
	exec := &mongodbExecutor{
		dumpExit: 1,
		dumpErr: []string{
			"2026-10-11T00:00:00.000+0000\tFailed: error creating intents to dump: error counting p_f2.c1: (Unauthorized) not authorized on p_f2 to execute command",
		},
	}
	_, err := (&MongoDBAdapter{}).Backup(context.Background(), mongodbBackupRC(t, exec, "p_f2"))
	if err == nil {
		t.Fatal("mongodump 非零退出必须失败")
	}
	if !strings.Contains(err.Error(), "error creating intents to dump") {
		t.Fatalf("error_message 必须并入 stderr 原因: %v", err)
	}
}

// mongorestore 失败时最后一行是统计（"0 document(s) ... failed to restore"），
// 真正的原因行在它上面：error_message 必须带上原因行，而不是统计行。
func TestStderrReasonSkipsMongorestoreSummary(t *testing.T) {
	tail := []string{
		"2026-10-11T05:50:15.778+0000\tFailed: p_f2_m2_ro.c1: error creating collection p_f2_m2_ro.c1: error running create command: (Unauthorized) not authorized on p_f2_m2_ro to execute command { create: \"c1\" }",
		"2026-10-11T05:50:15.778+0000\t0 document(s) restored successfully. 0 document(s) failed to restore.",
	}
	got := stderrReason(tail)
	if !strings.Contains(got, "not authorized on p_f2_m2_ro") {
		t.Fatalf("必须挑出原因行，得到 %q", got)
	}
}
