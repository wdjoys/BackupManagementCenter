package api

import (
	"strings"
	"testing"

	"backupmanagementcenter/internal/model"
)

// capture_oplog 在本平台内不可能端到端可用，必须在建计划时就拒绝，否则该计划
// 只会产出"能备份、永远不能恢复"的快照：
//   - mongodump --oplog 只支持整实例 dump（配 --db 时工具报
//     "bad option: --oplog mode only supported on full dumps"）；
//   - 整实例（database=all）快照在恢复侧被一律拒绝，且 mongorestore --oplogReplay
//     禁止与 --nsFrom/--nsTo 组合，无法重映射成单库恢复。
func TestValidateDatabaseEstimateRejectsOplog(t *testing.T) {
	bad := model.PlanSource{Host: "h", Port: 27017, Username: "u", Database: "appdb",
		EstimatedDumpBytes: 1 << 20, CaptureOplog: true}
	if msg := validateDatabaseEstimate(model.KindMongoDB, bad); msg == "" {
		t.Fatal("capture_oplog with a single database must be rejected")
	}

	all := model.PlanSource{Host: "h", Port: 27017, Username: "u", Database: "all",
		EstimatedDumpBytes: 1 << 20, CaptureOplog: true}
	msg := validateDatabaseEstimate(model.KindMongoDB, all)
	if msg == "" || !strings.Contains(msg, "capture_oplog is not supported") {
		t.Fatalf("capture_oplog with all scope must be rejected with a readable reason, got %q", msg)
	}

	// 关掉 capture_oplog 后整实例计划仍照常放行。
	plain := all
	plain.CaptureOplog = false
	if msg := validateDatabaseEstimate(model.KindMongoDB, plain); msg != "" {
		t.Fatalf("database=all without capture_oplog must stay accepted, got %q", msg)
	}

	// 非 mongodb 类型不受该规则影响。
	pg := model.PlanSource{Host: "h", Port: 5432, Username: "u", Database: "appdb", EstimatedDumpBytes: 1 << 20}
	if msg := validateDatabaseEstimate(model.KindPostgreSQL, pg); msg != "" {
		t.Fatalf("postgresql must be unaffected, got %q", msg)
	}
}

// 顶层 password 不参与读取（口令只从 source.password / credentials.password 取），
// 必须显式拒绝：否则计划创建成功但口令为空，备份才会以
// "Access denied ... (using password: NO)" 失败，排查成本高。
func TestPlanBodyRejectsTopLevelPassword(t *testing.T) {
	pw := "secret"
	body := planBody{
		Name: "p", Kind: model.KindMySQL, AgentID: "a", RepositoryID: "r",
		Schedule: "0 9 * * *", Timezone: "UTC", TimeoutSeconds: 3600,
		Retention: model.Retention{KeepLast: 1}, Password: &pw,
	}
	if msg, ok := body.validate(); ok || !strings.Contains(msg, "credentials.password") {
		t.Fatalf("top-level password must be rejected with a hint, got ok=%v msg=%q", ok, msg)
	}
	// extra_args 属于 source，放顶层同样会被静默丢弃（同 password 一类误用）。
	args := []string{"--skip-routines"}
	body.Password = nil
	body.ExtraArgs = &args
	if msg, ok := body.validate(); ok || !strings.Contains(msg, "source.extra_args") {
		t.Fatalf("top-level extra_args must be rejected with a hint, got ok=%v msg=%q", ok, msg)
	}

	// 去掉误用字段后同一请求必须通过，确认拒绝范围没有扩大。
	body.ExtraArgs = nil
	if msg, ok := body.validate(); !ok {
		t.Fatalf("valid body must pass, got %q", msg)
	}
}
