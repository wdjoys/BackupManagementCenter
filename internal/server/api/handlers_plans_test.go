package api

import (
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
