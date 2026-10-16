package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	bmcv1 "backupmanagementcenter/api/proto/v1"
	"backupmanagementcenter/internal/agent/backup"
	"backupmanagementcenter/internal/model"
)

// 仓库检查成功时此前不写任何日志，运维只能看到"已分发/运行成功"三行，无法判断这次
// 到底只做了结构校验还是读了数据子集 —— 而后者才是位腐检测的关键。
func TestRunCheckLogsScope(t *testing.T) {
	cases := []struct{ subset, want string }{
		{"1/10", "结构校验 + 数据子集 1/10"},
		{"0", "仅结构校验"},
		{"", "仅结构校验"},
	}
	for _, tc := range cases {
		var logged []string
		deps := Deps{
			Exec:                      fakeExecutor{},
			ResticCheckReadDataSubset: tc.subset,
			Logf: func(_ string, format string, args ...any) {
				logged = append(logged, fmt.Sprintf(format, args...))
			},
		}
		params, err := json.Marshal(model.CheckTask{
			Repository: model.RepoAccess{RepositoryPath: "rclone:local:/tmp/repo"},
		})
		if err != nil {
			t.Fatal(err)
		}
		res, err := Execute(context.Background(), deps, t.TempDir(), bmcv1.ExecuteCommand_CHECK, params,
			backup.SecretBundle{ResticPassword: "pw"})
		if err != nil {
			t.Fatalf("subset=%q Execute CHECK: %v", tc.subset, err)
		}
		if res == nil {
			t.Fatalf("subset=%q 应返回结果", tc.subset)
		}
		joined := strings.Join(logged, "\n")
		if !strings.Contains(joined, "仓库检查完成") || !strings.Contains(joined, tc.want) {
			t.Fatalf("subset=%q 日志应含 %q，实际:\n%s", tc.subset, tc.want, joined)
		}
	}
}
