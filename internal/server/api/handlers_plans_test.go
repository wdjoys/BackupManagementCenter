package api

import (
	"strings"
	"testing"

	"backupmanagementcenter/internal/model"
)

// capture_oplog 配单库时 mongodump 必然失败（--oplog 只支持整实例 dump：
// "bad option: --oplog mode only supported on full dumps"），必须在建计划时就
// 拒绝，而不是让该计划每次备份都失败。
func TestValidateDatabaseEstimateRejectsOplogWithSingleDatabase(t *testing.T) {
	bad := model.PlanSource{Host: "h", Port: 27017, Username: "u", Database: "appdb",
		EstimatedDumpBytes: 1 << 20, CaptureOplog: true}
	if msg := validateDatabaseEstimate(model.KindMongoDB, bad); msg == "" {
		t.Fatal("capture_oplog with a single database must be rejected")
	}

	ok := model.PlanSource{Host: "h", Port: 27017, Username: "u", Database: "all",
		EstimatedDumpBytes: 1 << 20, CaptureOplog: true}
	if msg := validateDatabaseEstimate(model.KindMongoDB, ok); msg != "" {
		t.Fatalf("capture_oplog with all scope must be accepted, got %q", msg)
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
	// 去掉误用字段后同一请求必须通过，确认拒绝范围没有扩大。
	body.Password = nil
	if msg, ok := body.validate(); !ok {
		t.Fatalf("valid body must pass, got %q", msg)
	}
}
