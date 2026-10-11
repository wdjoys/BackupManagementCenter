package model

import (
	"strings"
	"testing"
)

// 系统库绝不能作为恢复目标：服务端在受理阶段就拒绝（否则 agent 侧拒绝时阶段机
// 已推进，一个安全的策略拒绝会变成 rollback_failed / 需人工确认的运行）。
func TestIsSystemDatabase(t *testing.T) {
	cases := []struct {
		kind, name string
		want       bool
	}{
		{KindMySQL, "mysql", true},
		{KindMySQL, "information_schema", true},
		{KindMySQL, "performance_schema", true},
		{KindMySQL, "sys", true},
		{KindMySQL, "MySQL", true}, // MySQL 库名大小写不敏感
		{KindMySQL, "appdb", false},
		{KindPostgreSQL, "postgres", true},
		{KindPostgreSQL, "template0", true},
		{KindPostgreSQL, "template1", true},
		{KindPostgreSQL, "Postgres", false}, // 其余类型按原样比较
		{KindPostgreSQL, "appdb", false},
		{KindMongoDB, "admin", true},
		{KindMongoDB, "local", true},
		{KindMongoDB, "config", true},
		{KindMongoDB, "appdb", false},
		{KindSQLite, "mysql", false}, // 未登记的类型不拦
		{"unknown", "admin", false},  // 未知类型不拦
		{KindMySQL, "", false},       // 空名不是系统库
	}
	for _, c := range cases {
		if got := IsSystemDatabase(c.kind, c.name); got != c.want {
			t.Fatalf("IsSystemDatabase(%q, %q) = %v, want %v", c.kind, c.name, got, c.want)
		}
	}
}

// extra_args 白名单：只允许非路由/非认证开关，且拒绝时必须指出具体选项名
// （服务端与 agent 曾各自维护同一清单，漏改一侧会让参数永远被拒）。
func TestValidateExtraArgs(t *testing.T) {
	if err := ValidateExtraArgs(KindMySQL, []string{"--skip-routines"}); err != nil {
		t.Fatalf("--skip-routines must be allowed (mysqldump 单引号库名的规避手段): %v", err)
	}
	// 授权不足时的降级通道与正当的子集备份都必须可用（此前白名单里一个都没有）。
	for _, args := range [][]string{
		{"--skip-events"}, {"--skip-triggers"},
		{"--tables=t1,t3"}, {"--ignore-table=appdb.t2"},
		{"--tables=t1", "--ignore-table=appdb.t2", "--single-transaction"},
	} {
		if err := ValidateExtraArgs(KindMySQL, args); err != nil {
			t.Fatalf("%v must be allowed: %v", args, err)
		}
	}
	// 前缀规则不得放行空值或无关参数。
	for _, args := range [][]string{{"--tables="}, {"--ignore-table="}, {"--tables"}, {"--skip-events=x"}} {
		if err := ValidateExtraArgs(KindMySQL, args); err == nil {
			t.Fatalf("%v must be rejected", args)
		}
	}
	if err := ValidateExtraArgs(KindPostgreSQL, []string{"--inserts"}); err != nil {
		t.Fatalf("--inserts must be allowed: %v", err)
	}
	// --tables 是 MySQL 专属前缀，PG 不得放行。
	if err := ValidateExtraArgs(KindPostgreSQL, []string{"--tables=t1"}); err == nil {
		t.Fatal("postgresql must not accept mysql-only --tables")
	}
	err := ValidateExtraArgs(KindMySQL, []string{"--result-file=/tmp/x.sql"})
	if err == nil || !strings.Contains(err.Error(), "--result-file=/tmp/x.sql") {
		t.Fatalf("disallowed option must be named in the error, got %v", err)
	}
	if err := ValidateExtraArgs(KindMongoDB, []string{"--oplog"}); err == nil {
		t.Fatal("mongodb accepts no extra_args")
	}
	if err := ValidateExtraArgs(KindSQLite, []string{""}); err == nil {
		t.Fatal("empty option must be rejected")
	}
}
