package backup

import (
	"context"
	"strings"
	"testing"

	"backupmanagementcenter/internal/model"
)

// fakeListExec 模拟 Executor 契约：非零退出返回 (exitCode, nil)。
// 列库命令（args 含 pg_database）的退出码与输出可分别控制。
type fakeListExec struct {
	listExit int
	listOut  []string
	cmds     [][]string
}

func (f *fakeListExec) Run(_ context.Context, c Cmd, onStdout, onStderr func(line string)) (int, error) {
	f.cmds = append(f.cmds, c.Args)
	if strings.Contains(strings.Join(c.Args, " "), "pg_database") {
		if onStdout != nil {
			for _, line := range f.listOut {
				onStdout(line)
			}
		}
		return f.listExit, nil
	}
	return 0, nil
}

func pgInstanceRunContext(t *testing.T, exec Executor) *RunContext {
	t.Helper()
	return &RunContext{
		RunID: "run-1",
		Task: model.BackupTask{
			PlanID: "plan-1",
			Kind:   KindPostgreSQL,
			Source: model.PlanSource{Host: "db", Port: 5432, Username: "bmc", Database: "all"},
		},
		Secrets: SecretBundle{DBPassword: "pw"},
		TempDir: t.TempDir(),
		Exec:    exec,
		Logf:    func(string, string, ...any) {},
	}
}

// 列库 psql 非零退出时，绝不能产出"只剩 globals、没有任何业务库"却报成功的整实例备份。
func TestPostgreSQLInstanceBackupFailsWhenDatabaseListingExitsNonZero(t *testing.T) {
	// 即使 psql 把库名打到了 stdout，非零退出也必须让整次备份失败。
	exec := &fakeListExec{listExit: 1, listOut: []string{"postgres"}}
	artifact, err := (&PostgreSQLAdapter{}).Backup(context.Background(), pgInstanceRunContext(t, exec))
	if err == nil {
		t.Fatalf("列库非零退出必须让整实例备份失败，实际成功，manifest=%+v", artifact.Manifest)
	}
	if artifact != nil {
		t.Fatalf("失败时不应返回产物: %+v", artifact)
	}
}

// 列库成功但一个库都没列出（实例异常）同样必须失败，而不是上传空备份。
func TestPostgreSQLInstanceBackupFailsWhenNoDatabaseListed(t *testing.T) {
	exec := &fakeListExec{listExit: 0}
	artifact, err := (&PostgreSQLAdapter{}).Backup(context.Background(), pgInstanceRunContext(t, exec))
	if err == nil {
		t.Fatalf("未列出任何库必须失败，实际成功，manifest=%+v", artifact.Manifest)
	}
	if artifact != nil {
		t.Fatalf("失败时不应返回产物: %+v", artifact)
	}
}

// 库名以 '-' 开头时必须作为位置参数出现在 `--` 之后：否则 pg_dump 把它当选项
// 解析（实测 "no matching extensions were found"），实例上只要有一个这种库，
// 整实例备份就整体失败。
func TestPostgreSQLInstanceBackupTerminatesOptionsBeforeDashDatabase(t *testing.T) {
	exec := &fakeListExec{listExit: 0, listOut: []string{"postgres", "-weird"}}
	if _, err := (&PostgreSQLAdapter{}).Backup(context.Background(), pgInstanceRunContext(t, exec)); err != nil {
		t.Fatalf("Backup = %v", err)
	}
	for _, args := range exec.cmds {
		for i, a := range args {
			if a != "-weird" {
				continue
			}
			if i == 0 || args[i-1] != "--" {
				t.Fatalf("库名 -weird 之前必须有 -- 终止选项，实际 args=%v", args)
			}
			return
		}
	}
	t.Fatalf("未观察到针对 -weird 的 pg_dump 调用: %v", exec.cmds)
}

// 对照组：列库正常时必须照旧成功，确保上面的守卫没有把整实例备份一并打死。
func TestPostgreSQLInstanceBackupSucceedsWithListedDatabases(t *testing.T) {
	exec := &fakeListExec{listExit: 0, listOut: []string{"postgres", "appdb"}}
	artifact, err := (&PostgreSQLAdapter{}).Backup(context.Background(), pgInstanceRunContext(t, exec))
	if err != nil {
		t.Fatalf("列库正常时整实例备份应成功: %v", err)
	}
	if artifact == nil || artifact.Manifest == nil {
		t.Fatal("应返回带 manifest 的产物")
	}
	got := map[string]bool{}
	for _, db := range artifact.Manifest.Databases {
		got[db.Database] = true
	}
	for _, want := range []string{"globals", "postgres", "appdb"} {
		if !got[want] {
			t.Fatalf("manifest 缺少 %q 条目，实际 %v", want, got)
		}
	}
}
