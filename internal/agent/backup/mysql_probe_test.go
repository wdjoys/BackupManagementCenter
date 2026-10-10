package backup

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"backupmanagementcenter/internal/model"
)

// fakeProbeExec 让"非事务表探测"查询以非零退出（err==nil）返回，其余命令成功。
type fakeProbeExec struct{}

func (fakeProbeExec) Run(_ context.Context, c Cmd, onStdout, onStderr func(line string)) (int, error) {
	if strings.Contains(strings.Join(c.Args, " "), "information_schema.tables") {
		return 1, nil
	}
	return 0, nil
}

// Executor 对非零退出返回 (exitCode, nil)：探测查询失败时若不看退出码，
// 这条"可能存在非事务表"的一致性告警会被静默丢掉。
func TestMySQLBackupWarnsWhenNonTransactionalProbeFails(t *testing.T) {
	var logs []string
	rc := &RunContext{
		RunID: "run-1",
		Task: model.BackupTask{
			PlanID: "plan-1",
			Kind:   KindMySQL,
			Source: model.PlanSource{
				Host: "db", Port: 3306, Username: "root", Database: "appdb", EstimatedDumpBytes: 1024,
			},
		},
		Secrets: SecretBundle{DBPassword: "pw"},
		TempDir: t.TempDir(),
		Exec:    fakeProbeExec{},
		Logf: func(level, format string, args ...any) {
			logs = append(logs, level+" "+fmt.Sprintf(format, args...))
		},
	}
	if _, err := (&MySQLAdapter{}).Backup(context.Background(), rc); err != nil {
		t.Fatalf("Backup = %v", err)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "could not check non-transactional MySQL tables") {
		t.Fatalf("探测查询非零退出必须留下告警，实际日志:\n%s", joined)
	}
	if !strings.Contains(joined, "mysql client exit 1") {
		t.Fatalf("告警应说明是客户端退出码问题，实际日志:\n%s", joined)
	}
}
