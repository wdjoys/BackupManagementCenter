package backup

import (
	"context"
	"strings"
	"testing"

	"backupmanagementcenter/internal/model"
)

// fakeStderrExec 以非零退出并输出 stderr，模拟 mongosh 目标认证失败。
type fakeStderrExec struct{ stderr []string }

func (f fakeStderrExec) Run(_ context.Context, _ Cmd, _ func(string), onStderr func(string)) (int, error) {
	if onStderr != nil {
		for _, line := range f.stderr {
			onStderr(line)
		}
	}
	return 1, nil
}

// mongosh 的失败原因只出现在 stderr。运行错误必须带上它，否则凭据错、认证源错与
// 网络错在 run 层都是同一句 "mongosh query failed (exit 1)"（mysql 侧早已修成拼接
// stderr tail，mongo 侧漏了）。
func TestMongoQueryErrorCarriesStderrTail(t *testing.T) {
	db := &model.DatabaseRestore{
		TargetHost: "db", TargetPort: 27017,
		TargetUsername: "bmc", TargetDatabase: "app",
	}
	c := &mongoCtx{
		db:    db,
		shell: "mongosh",
		logf:  func(string) {},
	}
	spec := &RestoreSpec{
		Kind:       KindMongoDB,
		StagingDir: t.TempDir(),
		Database:   db,
		Exec:       fakeStderrExec{stderr: []string{"MongoServerError: Authentication failed."}},
		Logf:       func(string, string, ...any) {},
	}
	err := c.runJS(context.Background(), spec, "db.runCommand({ping:1})", nil)
	if err == nil {
		t.Fatal("非零退出必须返回错误")
	}
	if !strings.Contains(err.Error(), "Authentication failed") {
		t.Fatalf("运行错误应包含 stderr 中的失败原因，实际: %v", err)
	}
}
