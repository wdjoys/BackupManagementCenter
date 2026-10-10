package backup

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"backupmanagementcenter/internal/model"
)

// pgCmdRecorder 记录 psql/pg_restore 的参数，并对权限探测返回 ok。
type pgCmdRecorder struct{ cmds [][]string }

func (f *pgCmdRecorder) Run(_ context.Context, c Cmd, onStdout, _ func(string)) (int, error) {
	joined := strings.Join(c.Args, " ")
	f.cmds = append(f.cmds, c.Args)
	if onStdout != nil {
		switch {
		case strings.Contains(joined, "rolsuper"):
			onStdout("ok")
		case strings.Contains(joined, "version"):
			onStdout("psql (PostgreSQL) 18.6")
		}
	}
	return 0, nil
}

// 覆盖恢复的 DROP DATABASE 必须幂等（IF EXISTS）：第一次尝试的 DROP 可能已被服务端
// 执行完毕，但 psql 客户端因取消/超时被杀（exit -1）而报错退出——此时库已不存在，
// 回滚重建若再执行非幂等的 DROP 就会失败，回滚在用到保护快照之前中止（实测：
// rollback_failed + 目标库数据丢失 + 全局数据库恢复互斥被占住直到人工 resolve）。
// 紧随其后的 CREATE DATABASE 必须**不带** IF NOT EXISTS，保留并发创建者冲突失败的保护。
func TestPostgreSQLOverwriteDropIsIdempotent(t *testing.T) {
	rec := &pgCmdRecorder{}
	spec := pgRestoreSpec(t)
	spec.TargetIsNew = false // 覆盖恢复：走 DROP + CREATE 分支
	spec.Exec = rec
	if err := (&PostgreSQLAdapter{}).Import(context.Background(), spec); err != nil {
		t.Fatalf("Import: %v", err)
	}
	var drop, create string
	for _, args := range rec.cmds {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "DROP DATABASE"):
			drop = joined
		case strings.Contains(joined, "CREATE DATABASE"):
			create = joined
		}
	}
	if drop == "" || !strings.Contains(drop, "DROP DATABASE IF EXISTS") {
		t.Fatalf("覆盖恢复的 DROP 必须带 IF EXISTS（幂等），得到 %q", drop)
	}
	if create == "" || strings.Contains(create, "IF NOT EXISTS") {
		t.Fatalf("CREATE 必须不带 IF NOT EXISTS 以保留并发创建者冲突保护，得到 %q", create)
	}
}

// pgRestoreSpec 构造一个"目标已存在、走覆盖"的恢复 spec，使 Import 走到
// pg_restore 调用。
func pgRestoreSpec(t *testing.T) *RestoreSpec {
	t.Helper()
	dir := t.TempDir()
	return &RestoreSpec{
		SnapshotID: "snap-1",
		Kind:       KindPostgreSQL,
		StagingDir: dir,
		Database: &model.DatabaseRestore{
			TargetHost:     "db",
			TargetPort:     5432,
			TargetUsername: "bmc",
			TargetDatabase: "appdb",
		},
		Secrets:      SecretBundle{DBPassword: "pw"},
		ArtifactFile: filepath.Join(dir, "appdb.pgdump"),
		TargetIsNew:  true,
		Logf:         func(string, string, ...any) {},
	}
}

// pg_dump/pg_restore 自 17 起无条件在 dump 前置写入
// "SET transaction_timeout = 0;"，该 GUC 在 PG 16 及更早不存在。pg_restore
// 只要忽略过任何错误就以 exit 1 结束，因此只放行这一条已知版本偏斜语句，
// 其余错误必须照旧失败。
func TestPostgreSQLRestoreVersionSkewErrors(t *testing.T) {
	skew := []string{
		`pg_restore: error: could not execute query: ERROR:  unrecognized configuration parameter "transaction_timeout"`,
		"Command was: SET transaction_timeout = 0;",
		"pg_restore: warning: errors ignored on restore: 1",
	}
	if n := pgIgnorableRestoreErrors(skew); n != 1 {
		t.Fatalf("pgIgnorableRestoreErrors = %d, want 1", n)
	}
	if !pgOnlyIgnorableRestoreErrors(skew) {
		t.Fatal("pure version-skew output must be ignorable")
	}

	// 混入任何真实错误都必须拒绝放行。
	mixed := append([]string{}, skew...)
	mixed = append(mixed,
		`pg_restore: error: could not execute query: ERROR:  relation "users" already exists`,
		"Command was: CREATE TABLE users (id int);")
	if pgOnlyIgnorableRestoreErrors(mixed) {
		t.Fatal("real error must not be treated as version skew")
	}

	// 未知 GUC 但语句不在白名单里同样拒绝。
	unknown := []string{
		`pg_restore: error: could not execute query: ERROR:  unrecognized configuration parameter "future_guc"`,
		"Command was: SET future_guc = 0;",
	}
	if pgOnlyIgnorableRestoreErrors(unknown) {
		t.Fatal("unknown statement must not be ignorable")
	}
	if pgOnlyIgnorableRestoreErrors(nil) {
		t.Fatal("no errors must not be reported as ignorable")
	}
}

// pg_dump/pg_restore 自 17 起无条件在 dump 前置写入
// "SET transaction_timeout = 0;"，该 GUC 在 PG 16 及更早不存在。带
// --exit-on-error 时高版本客户端恢复到旧服务端会直接失败，正确性改由
// VerifyRestored 的关系集合对比兜底。
func TestPostgreSQLRestoreOmitsExitOnError(t *testing.T) {
	rec := &argRecorder{}
	spec := pgRestoreSpec(t)
	spec.Exec = rec

	if err := (&PostgreSQLAdapter{}).Import(context.Background(), spec); err != nil {
		t.Fatalf("Import: %v", err)
	}
	call := rec.find("pg_restore")
	if call == nil {
		t.Fatal("pg_restore was not invoked")
	}
	for _, arg := range call.Args {
		if arg == "--exit-on-error" {
			t.Fatalf("pg_restore must not use --exit-on-error (version-skew preamble), got args %v", call.Args)
		}
	}
}

// pg_restore -l 的类型字段可能是多个词（TABLE DATA / SEQUENCE OWNED BY /
// SEQUENCE SET），schema/name/owner 恒为末尾三项。按固定下标取字段会产出
// "DATA.public" 这类垃圾关系名，使每次恢复校验都误报缺失。
func TestPGDumpRelationNamesHandlesMultiWordTypes(t *testing.T) {
	list := `;
; Selected TOC Entries:
;
216; 1259 16415 TABLE public users bmc
219; 1259 16437 VIEW public active_users bmc
218; 1259 16425 TABLE public orders bmc
217; 1259 16424 SEQUENCE public orders_id_seq bmc
3473; 0 0 SEQUENCE OWNED BY public orders_id_seq bmc
3466; 0 16425 TABLE DATA public orders bmc
3464; 0 16415 TABLE DATA public users bmc
3475; 0 0 SEQUENCE SET public orders_id_seq bmc
3316; 2606 16430 CONSTRAINT public orders orders_pkey bmc
`
	got := pgDumpRelationNames(list)
	want := map[string]struct{}{
		"public.users": {}, "public.orders": {}, "public.active_users": {},
		"public.orders_id_seq": {},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			t.Fatalf("missing %s in %v", name, got)
		}
	}
}

// pg_restore -l 会把 INDEX 列为独立条目；校验查询若不统计索引（relkind 'i'），
// 每次带索引的恢复都会被误判为 missing。这里锚定两侧集合口径一致。
func TestPostgreSQLVerifyQueryCountsIndexes(t *testing.T) {
	dumpList := "3317; 1259 16436 INDEX public orders_user_idx bmc\n" +
		"216; 1259 16415 TABLE public users bmc\n"
	expected := pgDumpRelationNames(dumpList)
	if _, ok := expected["public.orders_user_idx"]; !ok {
		t.Fatalf("dump parse must list the index, got %v", expected)
	}

	rec := &argRecorder{}
	spec := pgRestoreSpec(t)
	spec.Exec = rec
	if err := (&PostgreSQLAdapter{}).VerifyRestored(context.Background(), spec); err != nil {
		// 目标为空库，本用例只关心查询文本。
		_ = err
	}
	call := rec.find("psql")
	if call == nil {
		t.Fatal("psql was not invoked")
	}
	query := strings.Join(call.Args, " ")
	if !strings.Contains(query, "'i'") {
		t.Fatalf("verification query must count indexes, got %q", query)
	}
	// TOAST 表与其索引由服务端自动创建，pg_dump 不列出，必须排除，
	// 否则会全部落进 unexpected。
	if !strings.Contains(query, "pg_toast") {
		t.Fatalf("verification query must exclude pg_toast schemas, got %q", query)
	}
	// 约束背后的隐式索引在归档里是 CONSTRAINT 行而非 INDEX 条目，必须排除。
	if !strings.Contains(query, "pg_constraint") {
		t.Fatalf("verification query must exclude constraint-backing indexes, got %q", query)
	}
}

// pg_restore -l 的名字可以含空格（"It's W"）。按固定下标取末尾三项会把名字错位，
// 该关系在 dump 侧缺失，目标侧却被列出，于是校验报 unexpected 并触发回滚。
func TestPGDumpRelationNamesHandlesSpacesInNames(t *testing.T) {
	list := `;
215; 1259 17125 TABLE public It's W bmc
3445; 0 17125 TABLE DATA public It's W bmc
3301; 2606 17131 CONSTRAINT public It's W It's W_pkey bmc
216; 1259 16415 TABLE public users bmc
3317; 1259 16436 INDEX public It's W idx bmc
`
	got := pgDumpRelationNames(list)
	want := map[string]struct{}{"public.It's W": {}, "public.It's W idx": {}, "public.users": {}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			t.Fatalf("missing %q in %v", name, got)
		}
	}
}

// schema 名也可以含空格（"app schema"）。SCHEMA 条目形如
// "<id>; <oid> <oid> SCHEMA - <schema...> <owner>"，"-" 与末尾 owner 之间整体即
// schema 名，可无歧义取得；再按最长 schema 前缀切分关系条目即可正确得到
// "app schema.t1"。此前按首个 token 切分会得到 "app.schema t1"，与目标侧的
// "app schema.t1" 不等，恢复被误判失败并自动回滚。
func TestPGDumpRelationNamesHandlesSpacesInSchema(t *testing.T) {
	list := `;
; Selected TOC Entries:
;
6; 2615 17177 SCHEMA - app schema bmc
216; 1259 17178 TABLE app schema t1 bmc
217; 1259 17183 TABLE public t2 bmc
3450; 0 17178 TABLE DATA app schema t1 bmc
3306; 2606 17182 CONSTRAINT app schema t1 t1_pkey bmc
`
	got := pgDumpRelationNames(list)
	want := map[string]struct{}{"app schema.t1": {}, "public.t2": {}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			t.Fatalf("missing %q in %v", name, got)
		}
	}
}

// 合法库名可以以 '-' 开头（引号标识符）。pg_dump 的库名是位置参数，必须用 --
// 结束选项，否则会被当选项解析（实测 "no matching extensions were found"），
// 该库永远无法备份。同时锚定 extra_args 仍排在 -- 之前（它们是选项）。
func TestPGDumpArgsEndOptionsBeforeDatabase(t *testing.T) {
	rec := &argRecorder{}
	rc := &RunContext{
		Task: model.BackupTask{
			PlanID: "plan-1",
			Kind:   KindPostgreSQL,
			Source: model.PlanSource{
				Host: "127.0.0.1", Port: 5432, Username: "bmc",
				Database: "-weird", EstimatedDumpBytes: 1 << 20,
				ExtraArgs: []string{"--no-comments"},
			},
		},
		Secrets: SecretBundle{DBPassword: "pw"},
		TempDir: t.TempDir(),
		Exec:    rec,
		Logf:    func(string, string, ...any) {},
	}
	if _, err := (&PostgreSQLAdapter{}).Backup(context.Background(), rc); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	dump := rec.find("pg_dump")
	if dump == nil {
		t.Fatal("pg_dump was never invoked")
	}
	dash := -1
	for i, a := range dump.Args {
		if a == "--" {
			dash = i
			break
		}
	}
	if dash < 0 || dash != len(dump.Args)-2 || dump.Args[len(dump.Args)-1] != "-weird" {
		t.Fatalf("-- must immediately precede the database name, got %v", dump.Args)
	}
	ea := -1
	for i, a := range dump.Args {
		if a == "--no-comments" {
			ea = i
		}
	}
	if ea < 0 || ea > dash {
		t.Fatalf("extra_args must stay before -- (they are options), got %v", dump.Args)
	}
}
