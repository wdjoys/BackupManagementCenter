package backup

import (
	"context"
	"strings"
	"testing"

	"backupmanagementcenter/internal/model"
)

// mongoExecutor 记录调用，并把 mongosh 脚本内容回放给回调，用于断言生成的 JS。
type mongoExecutor struct {
	calls  []Cmd
	stdout []string // 每次调用要回放的 stdout 行
	exits  []int    // 每次调用的退出码（缺省 0）
	n      int
}

func (e *mongoExecutor) Run(_ context.Context, c Cmd, onStdout, _ func(string)) (int, error) {
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
	return code, nil
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
