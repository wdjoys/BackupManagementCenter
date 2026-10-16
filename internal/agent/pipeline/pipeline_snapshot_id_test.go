package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	bmcv1 "backupmanagementcenter/api/proto/v1"
	"backupmanagementcenter/internal/agent/backup"
	"backupmanagementcenter/internal/model"
)

// noSnapshotIDExecutor 模拟 restic backup 成功退出，但 summary 行里没有 snapshot_id。
type noSnapshotIDExecutor struct{}

func (noSnapshotIDExecutor) Run(_ context.Context, cmd backup.Cmd, onStdout func(string), _ func(string)) (int, error) {
	if len(cmd.Args) == 0 {
		return 1, nil
	}
	switch cmd.Args[0] {
	case "snapshots":
		onStdout("[]")
	case "backup":
		onStdout(`{"message_type":"summary","files_new":1,"total_files_processed":1,"total_bytes_processed":10,"total_duration":0.1}`)
	}
	return 0, nil
}

// 没有快照 ID 的运行不可寻址、也无法恢复；此前只写一条 warn 就返回成功，
// 服务端会把 run 标成 succeeded 且 snapshot_id 为空串。
func TestRunBackupFailsWhenResticOmitsSnapshotID(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	params, err := json.Marshal(model.BackupTask{
		PlanID:     "p1",
		Kind:       model.KindFilesystem,
		Repository: model.RepoAccess{RepositoryPath: "repo"},
		Source:     model.PlanSource{Paths: []string{src}},
		Tags:       []string{"run:r1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{
		Exec: noSnapshotIDExecutor{},
		Logf: func(string, string, ...any) {},
	}
	res, err := Execute(context.Background(), deps, t.TempDir(), bmcv1.ExecuteCommand_BACKUP, params, backup.SecretBundle{ResticPassword: "pw"})
	if err == nil {
		t.Fatalf("缺少 snapshot id 必须让运行失败，实际返回成功: %+v", res)
	}
	if res != nil {
		t.Fatalf("失败时不应返回结果: %+v", res)
	}
	var pipelineErr *PipelineError
	if !errors.As(err, &pipelineErr) || pipelineErr.Code != "backup_failed" {
		t.Fatalf("错误码应为 backup_failed，实际: %v", err)
	}
}
