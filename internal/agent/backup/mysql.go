// Package backup implements plan-kind adapters.
package backup

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"backupmanagementcenter/internal/model"
)

// mysqlErrLockWaitTimeout 判断是否为 MySQL 的锁等待超时（ERROR 1205），即本次覆盖
// 恢复的 DROP 在有界等待内没拿到目标库的 schema metadata lock。
func mysqlErrLockWaitTimeout(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "1205") || strings.Contains(strings.ToLower(msg), "lock wait timeout")
}

// mysqlDropLockWaitSeconds 是覆盖恢复时 DROP DATABASE 的锁等待上限（秒）。MySQL
// 客户端默认 lock_wait_timeout=31536000s，配合 12h 运行期限会让"预检之后才出现的锁"
// 长期占住全局数据库恢复互斥。预检已排除已知占用，这里只需兜住竞态。
const mysqlDropLockWaitSeconds = 30

// MySQLAdapter implements Adapter for MySQL/MariaDB plans.
type MySQLAdapter struct{}

// Validate checks that required tools are present and source spec is sane.
func (a *MySQLAdapter) Validate(ctx context.Context, spec PlanSpec) error {
	if spec.Kind != KindMySQL {
		return fmt.Errorf("invalid kind: %s", spec.Kind)
	}
	s := spec.Source
	if s.Host == "" {
		return errors.New("host is required")
	}
	if s.Port <= 0 {
		return errors.New("port must be > 0")
	}
	if s.Username == "" {
		return errors.New("username is required")
	}
	if s.Database == "" {
		return errors.New("database is required (single name or 'all')")
	}
	if s.EstimatedDumpBytes <= 0 {
		return errors.New("estimated_dump_bytes must be > 0")
	}
	if err := model.ValidateExtraArgs(model.KindMySQL, s.ExtraArgs); err != nil {
		return err
	}
	// 整实例备份不接受表名（mysqldump 的 --all-databases 与位置参数表名互斥），
	// 否则用户以为只备份了部分表、实际拿到整实例 dump。
	if s.Database == "all" {
		for _, a := range s.ExtraArgs {
			if strings.HasPrefix(a, "--tables=") {
				return errors.New("--tables 只能用于单库备份（database=all 时 mysqldump 不接受表名）")
			}
		}
	}
	return nil
}

// Backup runs mysqldump, writes manifest, returns BackupArtifact with staging dir.
func (a *MySQLAdapter) Backup(ctx context.Context, rc *RunContext) (*BackupArtifact, error) {
	source := rc.Task.Source
	stagingDir := filepath.Join(rc.TempDir, "staging")
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir staging: %w", err)
	}

	// Write defaults-extra-file (0600)
	// 显式指定连接字符集：客户端默认字符集由镜像 locale 推导（agent 镜像无 UTF-8
	// locale，会退到 latin1），非 ASCII 标识符与数据都会受影响。
	cnfContent := fmt.Sprintf("[client]\nuser=%s\npassword=\"%s\"\nhost=\"%s\"\nport=%d\ndefault-character-set=utf8mb4\n", mysqlOptionValue(source.Username), mysqlOptionValue(rc.Secrets.DBPassword), mysqlOptionValue(source.Host), source.Port)
	cnfFile, err := writeSecretFile(rc.TempDir, "my.cnf", cnfContent)
	if err != nil {
		return nil, fmt.Errorf("write my.cnf: %w", err)
	}

	toolVersions := make(map[string]string)
	logLine := func(l string) { rc.Logf("info", "%s", l) }
	// 官方 8.0 客户端在 MySQL ≤5.7 上会把连接字符集回退到 latin1（8.0 专有的
	// 默认排序规则 utf8mb4_0900_ai_ci 在旧服务端不存在），导致非 ASCII 标识符
	// 无法解析；MariaDB 与旧服务端改用 5.7 客户端。
	tools := selectMySQLTools(ctx, rc.Exec, nil, cnfFile)
	mysqldumpPath := tools.dump
	if tools.legacy {
		rc.Logf("info", "使用 MySQL 5.7 客户端连接旧版服务端/MariaDB（避免 utf8mb4 排序规则回退）")
	}
	// 导出前的授权范围检查：mysqldump 按账号权限枚举表/触发器/例程，对"只有部分表
	// SELECT"或"缺 TRIGGER/EVENT"的账号会正常退出（rc=0）却静默丢对象，备份报
	// succeeded、恢复校验也比对不出来（VerifyRestored 只比 dump↔恢复后）。
	// PostgreSQL 在同场景下因覆盖全表的 LOCK TABLE 而失败，这里对齐该行为。
	if err := checkMySQLReadScope(ctx, rc.Exec, tools.client, cnfFile, source.Username, source.Database, source.ExtraArgs, func(level, msg string) {
		rc.Logf(level, "%s", msg)
	}); err != nil {
		return nil, err
	}
	// Non-transactional tables are not protected by --single-transaction and
	// can change while the dump is running. Surface the count before starting
	// the backup so operators can schedule a maintenance window if needed.
	nonTransactionalQuery := "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema NOT IN ('information_schema','mysql','performance_schema','sys') AND engine IS NOT NULL AND UPPER(engine) NOT IN ('INNODB','NDBCLUSTER')"
	if source.Database != "" && source.Database != "all" {
		nonTransactionalQuery += " AND table_schema = '" + strings.ReplaceAll(source.Database, "'", "''") + "'"
	}
	var nonTransactional string
	if checkExit, checkErr := rc.Exec.Run(ctx, Cmd{Exe: tools.client, Args: append([]string{"--defaults-extra-file=" + cnfFile}, "-N", "-s", "-e", nonTransactionalQuery)}, func(line string) {
		nonTransactional = strings.TrimSpace(line)
	}, logLine); checkErr != nil {
		rc.Logf("warn", "could not check non-transactional MySQL tables: %v", checkErr)
	} else if checkExit != 0 {
		// Executor 对非零退出返回 (exitCode, nil)：不检查退出码会把查询失败
		// 当成"没有非事务表"，静默丢掉这条一致性告警。
		rc.Logf("warn", "could not check non-transactional MySQL tables: mysql client exit %d", checkExit)
	} else if count, parseErr := strconv.Atoi(nonTransactional); parseErr == nil && count > 0 {
		rc.Logf("warn", "detected %d non-transactional MySQL tables; dump may be inconsistent", count)
	}

	dumpFile := filepath.Join(stagingDir, fmt.Sprintf("%s.sql", rc.Task.PlanID))
	// --defaults-extra-file 必须是第一个参数，出现在后面会被客户端当成未知变量。
	args := []string{
		"--defaults-extra-file=" + cnfFile,
		"--single-transaction", "--quick", "--routines", "--events", "--triggers",
		"--hex-blob", "--no-tablespaces",
	}
	if tools.modern {
		// 官方 MySQL 8.0 客户端默认开启 --column-statistics，会先查
		// information_schema.COLUMN_STATISTICS。该表是 MySQL 8.0 专有的，
		// MariaDB（以及 MySQL 5.x）没有，dump 会直接失败：
		//   Unknown table 'COLUMN_STATISTICS' in information_schema (1109)
		// 该统计仅用于优化器直方图，与备份内容无关，统一关闭。
		// 5.7 客户端不认识该参数，因此仅在 8.0 客户端上添加。
		args = append(args, "--column-statistics=0")
	}
	args = append(args, "--result-file="+dumpFile)
	// extra_args 必须排在 -- 之前（它们是选项；-- 之后的一切都会被当作位置参数）。
	// --tables=表名,表名 由 mysqlTableSelection 翻译成位置参数（mysqldump 的
	// --tables 是布尔开关，传值直接失败：option '--tables' cannot take an argument）。
	extraArgs, selectedTables := mysqlTableSelection(source.ExtraArgs)
	args = append(args, extraArgs...)
	// 位置参数形式的库名必须以 -- 结束选项：合法的库名可以以 '-' 开头（引号标识符），
	// 否则会被客户端当作选项簇解析（实测 mysqldump 报 unknown option '-s'）。
	if source.Database == "all" {
		args = append(args, "--all-databases")
	} else {
		args = append(args, "--", source.Database)
		// 表名同样以位置参数跟在库名之后：mysqldump db t1 t3 只导出这两张表。
		args = append(args, selectedTables...)
	}

	// 收集 stderr：MySQL ≤5.7 默认 character_set_server=latin1，官方 8.0 客户端
	// 请求 utf8mb4 时会因排序规则 utf8mb4_0900_ai_ci 不存在而回退 latin1，导致
	// 非 ASCII 库名被按 latin1 解释、报 "Unknown database"（库其实存在）。
	// 该场景无法在客户端修复（utf8mb3 会损坏 4 字节字符），因此把误导性的
	// "库不存在" 转成可诊断的错误。
	capture := &stderrCapture{logf: logLine}
	exitCode, err := rc.Exec.Run(ctx, Cmd{Exe: mysqldumpPath, Args: args, Env: nil}, logLine, capture.line)
	if err != nil || exitCode != 0 {
		// 单引号库名的原因更具体，优先给出；字符集提示只在 8.0 客户端路径下成立。
		if hint := mysqlDumpFailureHint(source.Database, source.Username, capture.tail, tools.legacy); hint != "" {
			rc.Logf("warn", "%s", hint)
		}
		return nil, exitErrorWithStderr("mysqldump", exitCode, err, capture.tail)
	}
	toolVersions["mysqldump"] = getToolVersion(ctx, rc.Exec, mysqldumpPath, nil)

	restoreHints := map[string]string{
		"host":     source.Host,
		"port":     strconv.Itoa(source.Port),
		"username": source.Username,
	}
	// 记录源库自身的默认字符集/排序规则：mysqldump 以单库位置参数调用时不输出
	// CREATE DATABASE，恢复侧若不显式指定，目标库会继承**目标服务器**的默认值 ——
	// 表/列/例程的字符集随 dump 显式带出不受影响，但之后在该库中新建且未指定
	// 字符集的对象会与源库不同。
	if source.Database != "" && source.Database != "all" {
		schemaQuery := "SELECT DEFAULT_CHARACTER_SET_NAME, DEFAULT_COLLATION_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = '" +
			strings.ReplaceAll(source.Database, "'", "''") + "'"
		var charset, collation string
		if qExit, qErr := rc.Exec.Run(ctx, Cmd{Exe: tools.client, Args: append([]string{"--defaults-extra-file=" + cnfFile}, "-N", "-s", "-e", schemaQuery)}, func(line string) {
			if f := strings.Fields(strings.TrimSpace(line)); len(f) >= 2 {
				charset, collation = f[0], f[1]
			}
		}, logLine); qErr == nil && qExit == 0 {
			if validCharsetName(charset) {
				restoreHints["charset"] = charset
			}
			if validCharsetName(collation) {
				restoreHints["collation"] = collation
			}
		}
	}

	manifest := &Manifest{
		Adapter:      KindMySQL,
		ToolVersions: toolVersions,
		Databases:    []DbExport{{Database: source.Database, File: filepath.Base(dumpFile), Format: "sql"}},
		StartedAt:    time.Now().UTC(),
		FinishedAt:   time.Now().UTC(),
		RestoreHints: restoreHints,
	}

	manifestPath := filepath.Join(stagingDir, "manifest.json")
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}
	if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
		return nil, fmt.Errorf("write manifest: %w", err)
	}

	var stagingFiles []string
	entries, _ := os.ReadDir(stagingDir)
	for _, e := range entries {
		stagingFiles = append(stagingFiles, e.Name())
	}

	return &BackupArtifact{
		StagingDir:   stagingDir,
		StagingFiles: stagingFiles,
		Manifest:     manifest,
	}, nil
}

func mysqlOptionValue(v string) string {
	return strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(v, `\`, `\\`), `"`, `\"`), "\n", `\n`)
}

// mysqlToolSet 是一次备份/恢复所用的 MySQL 客户端路径。
type mysqlToolSet struct {
	dump   string // mysqldump 可执行文件
	client string // mysql 可执行文件
	// modern 表示使用官方 8.0 客户端（支持 --column-statistics，
	// 且对 MySQL >=8.0 服务端不会发生 utf8mb4 排序规则回退）。
	modern bool
	// legacy 表示退回到镜像内的 5.7 客户端（供旧服务端/MariaDB 使用）。
	legacy bool
}

// selectMySQLTools 探测服务端版本并选择客户端。
//
// 官方 8.0 客户端请求 utf8mb4 时会带上 8.0 专有的默认排序规则
// utf8mb4_0900_ai_ci；MySQL ≤5.7 不认识该排序规则，连接字符集被回退到
// latin1，非 ASCII 标识符（库名/表名）随即无法解析、备份失败。MariaDB 与
// MySQL <8.0 改用 5.7 客户端可避免该回退（且不会探测 COLUMN_STATISTICS）。
// MySQL >=8.0 仍用 8.0 客户端，避免旧客户端漏掉 8.0 的新对象类型。
//
// 探测失败或镜像未提供 5.7 客户端时保持既有行为（8.0 客户端），不阻断备份。
func selectMySQLTools(ctx context.Context, exec Executor, env []string, cnfFile string) mysqlToolSet {
	modern := mysqlToolSet{dump: toolPath("mysqldump"), client: toolPath("mysql"), modern: true}
	dump57, dumpErr := osexec.LookPath("mysqldump57")
	client57, clientErr := osexec.LookPath("mysql57")
	if dumpErr != nil || clientErr != nil {
		return modern
	}
	var version string
	if _, err := exec.Run(ctx, Cmd{Exe: modern.client, Args: []string{"--defaults-extra-file=" + cnfFile, "-N", "-s", "-e", "SELECT VERSION()"}, Env: env},
		func(line string) {
			if v := strings.TrimSpace(line); v != "" && version == "" {
				version = v
			}
		}, nil); err != nil || version == "" {
		return modern
	}
	if mysqlNeedsLegacyClient(version) {
		return mysqlToolSet{dump: dump57, client: client57, legacy: true}
	}
	return modern
}

// mysqlNeedsLegacyClient 判断某服务端版本是否必须使用 5.7 客户端。
// MariaDB 与 MySQL <8.0 都会在 8.0 客户端上触发 utf8mb4 排序规则回退。
func mysqlNeedsLegacyClient(version string) bool {
	return isMariaDBVersion(version) || mysqlMajorVersion(version) < 8
}

// isMariaDBVersion 判断版本串是否来自 MariaDB（形如 5.5.5-10.6.16-MariaDB）。
func isMariaDBVersion(version string) bool {
	return strings.Contains(strings.ToLower(version), "mariadb")
}

// mysqlMajorVersion 取版本串的主版本号；无法解析时返回 0。
func mysqlMajorVersion(version string) int {
	i := 0
	for i < len(version) && version[i] >= '0' && version[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0
	}
	n, err := strconv.Atoi(version[:i])
	if err != nil {
		return 0
	}
	return n
}

// mysqlReadScope 汇总 SHOW GRANTS 里与「导出内容完整性」有关的授权范围。
type mysqlReadScope struct {
	global  bool                // SELECT ON *.*（含 ALL PRIVILEGES）
	schemas map[string]bool     // 库级 SELECT 的库名
	tables  map[string][]string // 仅有表级 SELECT 的表（key 为库名）

	// 对象类型权限：缺 TRIGGER 时 mysqldump 会静默丢弃触发器（实测），
	// 缺 EVENT 时在 SHOW EVENTS 处响亮失败（1044）。
	triggerGlobal  bool
	triggerSchemas map[string]bool
	eventGlobal    bool
	eventSchemas   map[string]bool
	// 例程可见性：information_schema.ROUTINES 只对拥有全局 SHOW_ROUTINE
	// （MySQL 8.0.20+）或全局 SELECT 的账号完整可见，否则例程被静默过滤。
	showRoutineGlobal bool
}

// routineVisible 报告账号能否看见库中的全部例程。
//
// 实测 MySQL 8.0.46：库级 SELECT+EVENT+TRIGGER 的账号执行 --routines 时 dump 里
// 存储过程/函数计数为 0（root 为 2/2），mysqldump 仍以 rc=0 退出——即例程被静默
// 丢弃；补 SHOW_ROUTINE ON *.* 或 SELECT ON *.* 后例程才出现。库级 EXECUTE /
// ALTER ROUTINE / ALL 只能让例程"可见"但 SHOW CREATE 仍会失败，因此不算可见。
func (s mysqlReadScope) routineVisible() bool { return s.global || s.showRoutineGlobal }

// checkMySQLReadScope 在导出前证明账号的授权足以产出完整 dump：表范围 + 本次
// 启用的对象类型（触发器/例程/事件）。无法证明时宁可明确失败，也不静默丢数据。
//
// 判定依据是 SHOW GRANTS FOR CURRENT_USER()：它按**有效权限**输出（默认/已激活的
// 角色会被展开成具体的 GRANT 行，实测 MySQL 8.0.46），因此不需要另行处理角色；
// information_schema 的授权视图则**不含**角色带来的权限（同环境实测为 0 行），
// 用它会把"权限来自角色"的账号误判为部分授权。读取授权失败时同样拒绝备份。
//
// extraArgs 决定本次真正启用的对象类型与表范围：用户显式选表（--tables/
// --ignore-table）或显式跳过某项（--skip-triggers/--skip-events/--skip-routines）
// 时按显式意图放行，只对「未声明意图的静默丢失」拒绝。
//
// 无法证实、只做提示的情形（例程可见性、整实例备份的对象权限、显式跳过的对象）
// 通过 logf 以 info/warn 级别落日志。
func checkMySQLReadScope(ctx context.Context, exec Executor, client, cnfFile, username, database string, extraArgs []string, logf func(level, msg string)) error {
	var stderrTail []string
	var grants []string
	exit, err := exec.Run(ctx, Cmd{Exe: client, Args: []string{
		"--defaults-extra-file=" + cnfFile, "-N", "-s", "-e", "SHOW GRANTS FOR CURRENT_USER()",
	}}, func(line string) {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			grants = append(grants, trimmed)
		}
	}, func(line string) {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			stderrTail = append(stderrTail, trimmed)
		}
		if logf != nil {
			logf("info", line)
		}
	})
	if err != nil || exit != 0 {
		return fmt.Errorf("无法确认账号 %q 的授权范围，拒绝备份（无法证明不会漏表）：%w",
			username, exitErrorWithStderr("mysql client SHOW GRANTS", exit, err, stderrTail))
	}
	scope := mysqlSelectScope(grants)
	// 显式选表（--tables/--ignore-table）表示用户已声明本次只导出哪些表，
	// 表级账号做子集备份是正当用法，不再按「部分授权」拒绝。
	if !mysqlExplicitTableSelection(extraArgs) {
		if err := mysqlCheckTableScope(scope, username, database); err != nil {
			return err
		}
	}
	return mysqlCheckObjectPrivileges(scope, username, database, extraArgs, logf)
}

// mysqlCheckTableScope 判定账号能否读到目标库（或整实例）的全部表。
//
// mysqldump 按账号权限从 information_schema 枚举表，对只被授予部分表 SELECT 的
// 账号会正常退出（rc=0）却只导出已授权的表：run 报 succeeded、VerifyRestored 也
// 只比对 dump↔恢复后，未授权表的数据静默消失（实测 MySQL 8.0.46：账号仅被授予
// `db`.`t1`/`db`.`t3` 的表级 SELECT + 库级 EVENT 时，t2 的 2500 行无声消失）。
// PostgreSQL 在同一场景下因覆盖全表的 LOCK TABLE 而响亮失败，这里对齐该行为：
// 判定为部分授权时明确拒绝，而不是产出不完整的 dump。
//
// 已知缺口：database="all" 且账号对某些库**完全没有**授权时，SHOW GRANTS 里没有
// 任何可判断的行，这类库仍会被静默跳过；只有能证实的部分授权才会被拦下。
func mysqlCheckTableScope(scope mysqlReadScope, username, database string) error {
	if scope.global {
		return nil
	}
	if database == "all" {
		// 整实例备份：只有"表级 SELECT 未被同库的库级 SELECT 覆盖"才是可证实的部分授权。
		var partial []string
		for db, tables := range scope.tables {
			if !scope.schemas[db] {
				sort.Strings(tables)
				partial = append(partial, db+"("+strings.Join(tables, ", ")+")")
			}
		}
		if len(partial) == 0 {
			return nil
		}
		sort.Strings(partial)
		return fmt.Errorf("mysql 导出被拒绝：账号 %q 只被授予部分表的 SELECT 权限（%s），"+
			"mysqldump 会静默跳过未授权的表，整实例备份将缺失这些表的数据；"+
			"请为该账号授予库级 SELECT（GRANT SELECT ON `<库名>`.* TO %q）后重试",
			username, strings.Join(partial, "；"), username)
	}
	if scope.schemas[database] {
		return nil
	}
	// 单库：没有全局/库级 SELECT 时，dump 只可能包含被显式授权的表（或一张表都没有），
	// 与"整库备份"的语义不符。
	detail := "该账号在该库上没有任何 SELECT 授权"
	if granted := scope.tables[database]; len(granted) > 0 {
		sort.Strings(granted)
		if len(granted) > 10 {
			granted = append(granted[:10:10], "…")
		}
		detail = "已授权的表：" + strings.Join(granted, ", ")
	}
	return fmt.Errorf("mysql 导出被拒绝：账号 %q 只被授予数据库 %q 中部分表的 SELECT 权限（%s），"+
		"mysqldump 会静默跳过未授权的表，备份将缺失这些表的数据；"+
		"请为该账号授予库级 SELECT（GRANT SELECT ON `%s`.* TO %q）后重试；"+
		"若确实只想备份其中部分表，请在该计划 extra_args 中显式指定 \"--tables=表名,表名\" 或 \"--ignore-table=库.表\"（显式选表即不再按部分授权拒绝）",
		username, database, detail, strings.ReplaceAll(database, "`", "``"), username)
}

// mysqlDumpObjectTypes 依据 extra_args 推导本次 dump 是否包含触发器/例程/事件。
//
// BMC 的基础参数恒定带 --triggers --routines --events（见 Backup），extra_args 追加
// 在它们之后；mysqldump 的布尔开关以最后一次出现为准，因此显式的 --skip-* 生效。
func mysqlDumpObjectTypes(extraArgs []string) (triggers, routines, events bool) {
	triggers, routines, events = true, true, true
	for _, a := range extraArgs {
		switch a {
		case "--triggers":
			triggers = true
		case "--skip-triggers":
			triggers = false
		case "--routines":
			routines = true
		case "--skip-routines":
			routines = false
		case "--events":
			events = true
		case "--skip-events":
			events = false
		}
	}
	return triggers, routines, events
}

// mysqlExplicitTableSelection 报告 extra_args 是否显式指定了要导出/排除的表。
// 显式选表意味着用户已声明本次的表范围，未选中的表不算"静默丢失"。
func mysqlExplicitTableSelection(extraArgs []string) bool {
	for _, a := range extraArgs {
		if a == "--tables" || a == "--ignore-table" ||
			strings.HasPrefix(a, "--tables=") || strings.HasPrefix(a, "--ignore-table=") {
			return true
		}
	}
	return false
}

// mysqlTableSelection 把 BMC 暴露的 --tables=表名,表名 拆成 mysqldump 需要的位置参数。
//
// mysqldump 的 --tables 是布尔开关（传值直接失败：option '--tables' cannot take an
// argument），且它把其后的所有名字参数都当成表名；而 BMC 固定把库名放在最后的位置
// 参数位置，因此 --tables=t1,t3 原样透传必然失败（实测 exit 4），退化成
// --tables=<单表> 时更会静默导出空 dump。这里把该参数摘出来，表名由调用方追加在库名
// 之后（等价于 mysqldump db t1 t3）。
func mysqlTableSelection(extraArgs []string) (rest, tables []string) {
	for _, a := range extraArgs {
		if !strings.HasPrefix(a, "--tables=") {
			rest = append(rest, a)
			continue
		}
		for _, t := range strings.Split(strings.TrimPrefix(a, "--tables="), ",") {
			if t = strings.TrimSpace(t); t != "" {
				tables = append(tables, t)
			}
		}
	}
	return rest, tables
}

// mysqlCheckObjectPrivileges 判定本次启用的触发器/例程/事件是否会因权限不足而
// 静默缺失（或失败）：能证实的直接拒绝，无法证实的给出告警。
//
// 实测（MySQL 8.0.46）：账号只有库级 SELECT+EVENT（无 TRIGGER）时，mysqldump
// --triggers 正常退出（rc=0）却完全不输出触发器，且没有任何 warn/error
// （information_schema.TRIGGERS 按权限过滤）——run 报 succeeded，恢复后触发器无声
// 消失，与「部分表 SELECT 静默漏表」同类，因此导出前直接拒绝。缺 EVENT 时
// SHOW EVENTS 是响亮失败（1044），提前拒绝只是把 "Access denied ... to database"
// 这种误导措辞换成精确的授权建议。
//
// 例程（information_schema.ROUTINES）缺可见权限时同样静默丢弃（实测 --routines 下
// 存储过程/函数计数为 0，mysqldump 仍 rc=0），但"库中是否存在例程"在无权限时**无法
// 证明**：硬拒绝会误伤大量不含例程的库，以及没有 SHOW_ROUTINE 权限的 MariaDB，
// 因此只给出告警，由 --skip-routines 显式关闭。
//
// 整实例备份（database="all"）无法枚举账号在各库的授权，同样只告警不拒绝。
// 显式跳过某项（--skip-triggers/--skip-events/--skip-routines）时按用户意图放行，
// 但会留下一条说明，避免"日志里看不出本次 dump 少了什么"。
func mysqlCheckObjectPrivileges(scope mysqlReadScope, username, database string, extraArgs []string, logf func(level, msg string)) error {
	triggers, routines, events := mysqlDumpObjectTypes(extraArgs)
	all := database == "all"
	log := func(level, msg string) {
		if logf != nil {
			logf(level, msg)
		}
	}
	for _, skip := range []struct {
		enabled bool
		name    string
		flag    string
	}{{triggers, "触发器", "--skip-triggers"}, {routines, "存储过程/函数", "--skip-routines"}, {events, "事件", "--skip-events"}} {
		if !skip.enabled {
			log("info", fmt.Sprintf("本次导出按 extra_args 显式跳过%s（%s），dump 不含%s", skip.name, skip.flag, skip.name))
		}
	}
	if triggers && !scope.triggerGlobal && !(all || scope.triggerSchemas[database]) {
		return fmt.Errorf("mysql 导出被拒绝：账号 %q 在库 %q 上没有 TRIGGER 权限，"+
			"mysqldump --triggers 会静默跳过该库的全部触发器（run 仍报成功，恢复后触发器无声消失）；"+
			"请执行 GRANT TRIGGER ON `%s`.* TO %q; 后重试，"+
			"或在该计划 extra_args 中加入 \"--skip-triggers\" 显式声明本次不含触发器",
			username, database, strings.ReplaceAll(database, "`", "``"), username)
	}
	if events && !scope.eventGlobal && !(all || scope.eventSchemas[database]) {
		return fmt.Errorf("mysql 导出被拒绝：账号 %q 在库 %q 上没有 EVENT 权限，"+
			"mysqldump --events 会在 'show events' 阶段被拒绝（MySQL 的报错写作 "+
			"\"Access denied ... to database '%s'\"，与库级 SELECT 是否缺失无关）；"+
			"请执行 GRANT EVENT ON `%s`.* TO %q; 后重试，"+
			"或在该计划 extra_args 中加入 \"--skip-events\" 显式跳过事件",
			username, database, database, strings.ReplaceAll(database, "`", "``"), username)
	}
	if all {
		if triggers && !scope.triggerGlobal {
			log("warn", fmt.Sprintf("账号 %q 没有全局 TRIGGER 权限，整实例备份中未授权的库会静默丢失触发器；"+
				"如需触发器请执行 GRANT TRIGGER ON *.* TO %q;，或在该计划 extra_args 中加入 \"--skip-triggers\"", username, username))
		}
		if events && !scope.eventGlobal {
			log("warn", fmt.Sprintf("账号 %q 没有全局 EVENT 权限，整实例备份中未授权的库会在导出事件时失败；"+
				"如需事件请执行 GRANT EVENT ON *.* TO %q;，或在该计划 extra_args 中加入 \"--skip-events\"", username, username))
		}
	}
	if routines && !scope.routineVisible() {
		log("warn", fmt.Sprintf("账号 %q 缺少全局 SHOW_ROUTINE（MySQL 8.0.20+）或全局 SELECT，"+
			"mysqldump --routines 会静默跳过该库的存储过程/函数（实测 dump 内例程计数为 0 且 rc=0）；"+
			"如需例程请执行 GRANT SHOW_ROUTINE ON *.* TO %q;（或改用拥有全局 SELECT 的账号），"+
			"或在该计划 extra_args 中加入 \"--skip-routines\" 显式声明本次不含例程", username, username))
	}
	return nil
}

// mysqlSelectScope 解析 SHOW GRANTS 的每一行，汇总账号的表范围与对象类型权限。
func mysqlSelectScope(grants []string) mysqlReadScope {
	scope := mysqlReadScope{schemas: map[string]bool{}, tables: map[string][]string{},
		triggerSchemas: map[string]bool{}, eventSchemas: map[string]bool{}}
	for _, line := range grants {
		object, privs, allPriv, ok := mysqlGrantObject(line)
		if !ok {
			continue
		}
		if object == "*.*" {
			// 同一账号可能有多行 *.* 授权（如 mysql8.4 的 root 分成权限行与
			// 管理权限行），必须累加而不能被后一行覆盖。
			scope.global = scope.global || allPriv || privs["SELECT"]
			scope.triggerGlobal = scope.triggerGlobal || allPriv || privs["TRIGGER"]
			scope.eventGlobal = scope.eventGlobal || allPriv || privs["EVENT"]
			scope.showRoutineGlobal = scope.showRoutineGlobal || allPriv || privs["SHOW_ROUTINE"]
			continue
		}
		db, rest, ok := mysqlUnquoteIdentifier(strings.TrimPrefix(object, "`"))
		if !ok {
			continue
		}
		switch {
		case rest == ".*":
			if allPriv || privs["SELECT"] {
				scope.schemas[db] = true
			}
			if allPriv || privs["TRIGGER"] {
				scope.triggerSchemas[db] = true
			}
			if allPriv || privs["EVENT"] {
				scope.eventSchemas[db] = true
			}
		case strings.HasPrefix(rest, ".`") && (allPriv || privs["SELECT"]):
			if tbl, _, ok := mysqlUnquoteIdentifier(rest[2:]); ok {
				scope.tables[db] = append(scope.tables[db], tbl)
			}
		}
	}
	return scope
}

// mysqlGrantObject 解析一行 SHOW GRANTS，返回被授权对象与权限清单。
// allPriv 表示清单里含 ALL/ALL PRIVILEGES（等价于全部权限）。
// ok=false 表示该行不是"ON <对象>"形式的授权（如 GRANT <角色> TO <账号>）。
// 对象形如 *.*（全局）、`db`.*（库级）、`db`.`tbl`（表级）。
func mysqlGrantObject(line string) (object string, privs map[string]bool, allPriv bool, ok bool) {
	s := strings.TrimSpace(line)
	if len(s) <= len("GRANT ") || !strings.EqualFold(s[:len("GRANT ")], "GRANT ") {
		return "", nil, false, false
	}
	rest := s[len("GRANT "):]
	on := strings.Index(strings.ToUpper(rest), " ON ")
	if on < 0 {
		return "", nil, false, false
	}
	// 合法库名可以含 " TO "，因此用最后一个 " TO " 作分隔；权限清单里不可能出现
	// " ON "，所以第一个 " ON " 就是权限与对象的分隔符。
	to := strings.LastIndex(strings.ToUpper(rest), " TO ")
	if to < on {
		return "", nil, false, false
	}
	privs = map[string]bool{}
	for _, p := range strings.Split(rest[:on], ",") {
		switch name := strings.ToUpper(strings.TrimSpace(p)); name {
		case "ALL", "ALL PRIVILEGES":
			allPriv = true
		case "":
		default:
			privs[name] = true
		}
	}
	return strings.TrimSpace(rest[on+len(" ON ") : to]), privs, allPriv, true
}

// mysqlDumpFailureHint 选出与失败原因匹配的诊断提示（无匹配返回空串）。
//
// legacy 表示本次用的是 5.7 客户端：此时不可能发生 8.0 客户端的 utf8mb4 排序规则
// 回退，同样的 "Unknown database" 就是库确实不存在，不能再提示字符集问题（实测
// bmc-mysql56 上不存在的非 ASCII 库名被误诊为"库实际存在"的字符集问题）。
func mysqlDumpFailureHint(database, username string, stderrTail []string, legacy bool) string {
	// 单引号库名的原因更具体，优先给出（含单引号且非 ASCII 时避免被字符集提示误导）。
	if hint := mysqlDumpQuoteNameHint(database, stderrTail); hint != "" {
		return hint
	}
	// 权限类错误与库名无关，任何客户端路径下都应给出（缺权限在 5.7 客户端上同样发生）。
	if hint := mysqlDumpPrivilegeHint(database, username, stderrTail); hint != "" {
		return hint
	}
	if legacy {
		return ""
	}
	return mysqlDumpNameCharsetHint(database, stderrTail)
}

// mysqlDumpPrivilegeHint 在 mysqldump 因权限不足（1044/1142）失败时给出精确的授权
// 建议。
//
// 这类报错在 MySQL 里对 SHOW EVENTS/SHOW TRIGGERS/SHOW CREATE PROCEDURE 都写作
// "Access denied for user 'u'@'%' to database 'db'"，字面像是"库级授权缺失"，但被拒
// 的库级 SELECT 往往早已授予（实测 SELECT+EVENT 账号报 'show events' 1044 而库级
// SELECT 正常，事件只是缺 EVENT 权限）。因此这里点名真正缺的权限，且不得再建议
// "改用全局 SELECT"——实测 GRANT SELECT ON *.* 仍无法执行 SHOW EVENTS。
func mysqlDumpPrivilegeHint(database, username string, stderrTail []string) string {
	if database == "" {
		return ""
	}
	all := database == "all"
	missing := ""
	for _, line := range stderrTail {
		switch {
		case strings.Contains(line, "show events"), strings.Contains(line, "SHOW EVENTS"):
			missing = "EVENT"
		case strings.Contains(line, "show triggers"), strings.Contains(line, "SHOW TRIGGERS"):
			missing = "TRIGGER"
		case strings.Contains(line, "SHOW CREATE PROCEDURE"), strings.Contains(line, "SHOW CREATE FUNCTION"):
			missing = "SHOW_ROUTINE"
		}
		if missing != "" {
			break
		}
	}
	// 整实例备份报错里的库名可能是任意库（实测缺 EVENT 时先卡在 'mysql' 库），
	// 因此只能给全局授权；单库给出精确到库的授权。
	scope := fmt.Sprintf("`%s`.*", strings.ReplaceAll(database, "`", "``"))
	target := fmt.Sprintf("库 %q", database)
	if all {
		scope, target = "*.*", "整实例备份涉及的各库"
	}
	var grant string
	switch missing {
	case "EVENT":
		grant = fmt.Sprintf("GRANT EVENT ON %s TO %q;", scope, username)
	case "TRIGGER":
		grant = fmt.Sprintf("GRANT TRIGGER ON %s TO %q;", scope, username)
	case "SHOW_ROUTINE":
		grant = fmt.Sprintf("GRANT SHOW_ROUTINE ON *.* TO %q;（MySQL 8.0.20+；MariaDB/旧版可用全局 SELECT 代替）", username)
	default:
		// 未识别的 1044/1142：只纠正误导性的措辞，不猜具体权限。
		for _, line := range stderrTail {
			if strings.Contains(line, "Access denied") && strings.Contains(line, "to database") {
				missing = "未知（见下方 mysqldump 原文）"
				break
			}
		}
		if missing == "" {
			return ""
		}
		grant = fmt.Sprintf("按报错语句补齐对应权限（常见：GRANT EVENT ON %s TO %q; / GRANT TRIGGER ON %s TO %q; / GRANT SHOW_ROUTINE ON *.* TO %q;）",
			scope, username, scope, username, username)
	}
	return fmt.Sprintf("mysqldump 因权限不足失败，真正缺少的权限是 %s；报错里的 "+
		"\"Access denied ... to database '...'\" 是 MySQL 对 SHOW EVENTS/SHOW TRIGGERS/SHOW CREATE 的措辞，"+
		"**不代表**该账号缺少库级 SELECT（它通常已有）。请针对%s执行 %s 后重试；"+
		"也可在该计划 extra_args 中显式跳过对应对象（--skip-events / --skip-triggers / --skip-routines）。"+
		"注意：全局 SELECT（GRANT SELECT ON *.*）**不能**替代 EVENT/TRIGGER 权限。",
		missing, target, grant)
}

// mysqlDumpQuoteNameHint 在"库名含单引号"且 mysqldump 报 1049 时给出可诊断提示。
// mysqldump 的 --routines 会把库名里的单引号转义成反斜杠形式（形如 use `a\'b`，反引号内本不需要转义），
// 转义单引号），属上游缺陷；BMC 默认传 --routines，因此这类库目前无法直接备份。
// 规避：在 extra_args 中加 "--skip-routines"（后置参数覆盖默认的 --routines），
// 代价是本次备份不含存储过程/函数；彻底解决需重命名数据库（去掉单引号）。
func mysqlDumpQuoteNameHint(database string, stderrTail []string) string {
	if database == "" || database == "all" || !strings.Contains(database, "'") {
		return ""
	}
	unknownDB := false
	for _, line := range stderrTail {
		if strings.Contains(line, "Unknown database") {
			unknownDB = true
			break
		}
	}
	if !unknownDB {
		return ""
	}
	return fmt.Sprintf("数据库名 %q 含单引号：mysqldump 在 --routines 下把它错误转义成 use `a\\'b`，"+
		"服务端因此报 \"Unknown database\"（若该库确实存在，则属 mysqldump 上游缺陷；库名拼写错误同样会报这个错，请先确认库名）。"+
		"规避：在该计划 extra_args 中加入 \"--skip-routines\"（后置参数覆盖默认的 --routines），"+
		"代价是本次备份不含存储过程/函数；彻底解决需重命名数据库去掉单引号。", database)
}

// mysqlDumpNameCharsetHint 在"库名含非 ASCII 字符"且 mysqldump 报 1049 时给出
// 可诊断提示。MySQL ≤5.7 的 character_set_server 默认为 latin1，官方 8.0 客户端
// 请求 utf8mb4 会因 utf8mb4_0900_ai_ci 排序规则不存在而回退 latin1，于是库名被
// 按 latin1 解释，服务端报 "Unknown database"——若该库确实存在，直接照字面理解会
// 让运维误判。因此该提示只在官方 8.0 客户端路径下给出（见 mysqlDumpFailureHint）：
// legacy 路径用的是 5.7 客户端，不可能发生这种回退，同样的报错就是库确实不存在。
// 客户端侧无可用修复（改用 utf8mb3 会损坏 4 字节字符），因此只做提示。
func mysqlDumpNameCharsetHint(database string, stderrTail []string) string {
	if database == "" || database == "all" || isASCII(database) {
		return ""
	}
	unknownDB := false
	for _, line := range stderrTail {
		if strings.Contains(line, "Unknown database") {
			unknownDB = true
			break
		}
	}
	if !unknownDB {
		return ""
	}
	return fmt.Sprintf("数据库名 %q 含非 ASCII 字符，而目标 MySQL 服务端字符集为 latin1（MySQL ≤5.7 的默认值）："+
		"官方 8.0 客户端请求 utf8mb4 时因排序规则 utf8mb4_0900_ai_ci 在旧服务端不存在而回退 latin1，"+
		"库名被错误解释，服务端因此报 \"Unknown database\"（仅当该库确实存在时才是此原因；库名拼写错误同样会报这个错）。"+
		"请将该服务端/库改为 utf8mb4（如启动参数 --character-set-server=utf8mb4），或改用 ASCII 库名。"+
		"不要改用 utf8mb3 规避：它会损坏 4 字节字符（如 emoji）。", database)
}

// isASCII 报告 s 是否只含 ASCII 字符。
func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// mysqlCtx groups the target connection details a restore needs.
type mysqlCtx struct {
	db    *model.DatabaseRestore
	cnf   string
	mysql string
	logf  func(string)
}

// validCharsetName 只接受 MySQL 字符集/排序规则名允许的字符（字母、数字、下划线）。
// 这些值来自快照 manifest（存在仓库里），拼接进 CREATE DATABASE 前必须校验。
func validCharsetName(v string) bool {
	if v == "" || len(v) > 64 {
		return false
	}
	for i := 0; i < len(v); i++ {
		ch := v[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' {
			continue
		}
		return false
	}
	return true
}

// mysqlPrepare writes the target credentials into a 0600 config file inside the
// private staging dir and resolves the client binary. Credentials never enter
// argv.
func mysqlPrepare(spec *RestoreSpec) (*mysqlCtx, error) {
	db := spec.Database
	if db == nil {
		return nil, errors.New("database restore spec missing")
	}
	if db.TargetDatabase == "" || db.TargetDatabase == "all" {
		return nil, errors.New("mysql restore requires a single target database")
	}
	if model.IsSystemDatabase(model.KindMySQL, db.TargetDatabase) {
		return nil, fmt.Errorf("refusing to restore into system schema %q", db.TargetDatabase)
	}
	// 同备份：显式指定 utf8mb4，否则建库/校验语句中的非 ASCII 库名会被按
	// latin1 解释（客户端默认字符集由 locale 推导）。
	cnfContent := fmt.Sprintf("[client]\nuser=%s\npassword=\"%s\"\nhost=\"%s\"\nport=%d\ndefault-character-set=utf8mb4\n",
		mysqlOptionValue(db.TargetUsername), mysqlOptionValue(spec.Secrets.DBPassword), mysqlOptionValue(db.TargetHost), db.TargetPort)
	cnfFile, err := writeSecretFile(spec.StagingDir, "my.cnf", cnfContent)
	if err != nil {
		return nil, fmt.Errorf("write my.cnf: %w", err)
	}
	// 恢复同样按目标服务端版本选择客户端：旧服务端/MariaDB 上用 8.0 客户端会因
	// utf8mb4 排序规则回退 latin1，非 ASCII 库名/表名的导入与校验都会失败。
	tools := selectMySQLTools(context.Background(), spec.Exec, nil, cnfFile)
	return &mysqlCtx{
		db:    db,
		cnf:   cnfFile,
		mysql: tools.client,
		logf:  func(l string) { spec.Logf("info", "%s", l) },
	}, nil
}

// args 构造 mysql 客户端参数：-e 执行语句，或 stdin 导入 dump。
// --defaults-extra-file 必须是第一个参数，否则客户端报 unknown variable。
func (c *mysqlCtx) args(query string) []string {
	return []string{
		"--defaults-extra-file=" + c.cnf,
		"--binary-mode",
		"-h", c.db.TargetHost, "-P", strconv.Itoa(c.db.TargetPort), "-u", c.db.TargetUsername,
		// -N 去掉列头：有结果行时 mysql 会先打印 TABLE_NAME 头，被 scan 计入
		// present 后，校验日志的计数会恒为 N+1。
		"-N",
		"-e", query,
	}
}

// runQuery 执行一条语句并把首个非空 stdout 行交给 scan。
func (c *mysqlCtx) runQuery(ctx context.Context, spec *RestoreSpec, query string, scan func(string)) error {
	var stderrTail strings.Builder
	exit, err := spec.Exec.Run(ctx, Cmd{Exe: c.mysql, Args: c.args(query)},
		func(line string) {
			if trimmed := strings.TrimSpace(line); trimmed != "" && scan != nil {
				scan(trimmed)
			}
		}, func(line string) {
			// 收集 stderr：目标不可达（2003）与凭据错误（1045）此前只体现在 agent
			// 日志里，运行错误只剩 "mysql query failed (exit 1)"，两者不可区分。
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				stderrTail.WriteString(trimmed + "\n")
			}
			if c.logf != nil {
				c.logf(line)
			}
		})
	if err != nil || exit != 0 {
		base := exitError("mysql query", exit, err)
		if tail := strings.TrimSpace(stderrTail.String()); tail != "" {
			return fmt.Errorf("%w: %s", base, tail)
		}
		return base
	}
	return nil
}

// TargetExists reports whether the target schema exists. A failed query is an
// error, never "absent".
func (a *MySQLAdapter) TargetExists(ctx context.Context, spec *RestoreSpec) (bool, error) {
	c, err := mysqlPrepare(spec)
	if err != nil {
		return false, err
	}
	found := ""
	query := "SELECT 1 FROM information_schema.schemata WHERE schema_name = '" +
		strings.ReplaceAll(c.db.TargetDatabase, "'", "''") + "'"
	if err := c.runQuery(ctx, spec, query, func(line string) { found = line }); err != nil {
		// 带上意图：恢复尚未触碰目标就失败时，调用方需要知道是"无法判定目标是否存在"。
		return false, fmt.Errorf("cannot determine whether the target exists: %w", err)
	}
	return found != "", nil
}

// PreflightRestore 在任何写入之前确认目标库没有被其它会话占用。
//
// MySQL 的 DROP DATABASE 需要目标库上的 schema metadata lock：占用方持有表 MDL 时
// 会无界等待（实测阻塞 52s，整次恢复从 4-5s 变成 59s），而这一步一旦失败/被取消，
// 回滚会用同一个 DROP 再失败一次，阶段机落到 rollback_failed 并阻塞**所有**数据库
// 恢复。与 SQLite 的预检同一思路：写之前就拒绝，让运维先断开目标库的会话。
//
// 判定必须按 MDL 而非仅按连接默认库：实测占用方**不设默认库**、只用限定名
// （BEGIN; SELECT * FROM <target>.<table> LIMIT 1;）持表锁时 processlist.db 为
// NULL，只看 db 会漏检并继续阻塞在 DROP DATABASE（实测阻塞 23.6s）。因此两条判据
// 都查：① 默认库是目标库的会话；② 目标库上仍有 GRANTED 的 metadata lock。
func (a *MySQLAdapter) PreflightRestore(ctx context.Context, spec *RestoreSpec) error {
	c, err := mysqlPrepare(spec)
	if err != nil {
		return err
	}
	target := c.db.TargetDatabase
	if target == "" || strings.EqualFold(target, "all") {
		return nil
	}
	quoted := strings.ReplaceAll(target, "'", "''")
	// CONNECTION_ID() 排除本次检查自身的连接。
	var busy []string
	query := "SELECT CONCAT('session ', id, ' user=', user, ' host=', host, ' cmd=', command, ' time=', time, ' state=', state) FROM information_schema.processlist WHERE db = '" +
		quoted + "' AND id <> CONNECTION_ID()"
	if err := c.runQuery(ctx, spec, query, func(line string) { busy = append(busy, line) }); err != nil {
		return fmt.Errorf("check target database sessions: %w", err)
	}
	// 按 MDL 兜住"默认库为空但锁着目标库表"的会话。performance_schema.metadata_locks
	// 在 MySQL 5.7+ 才有，且可能未启用：查不到不能阻断恢复，按尽力而为处理。
	mdlQuery := "SELECT CONCAT('metadata lock ', LOCK_TYPE, ' on ', OBJECT_SCHEMA, '.', IFNULL(OBJECT_NAME, '-')) FROM performance_schema.metadata_locks WHERE OBJECT_SCHEMA = '" +
		quoted + "' AND LOCK_STATUS = 'GRANTED'"
	var mdl []string
	if err := c.runQuery(ctx, spec, mdlQuery, func(line string) { mdl = append(mdl, line) }); err != nil {
		if spec.Logf != nil {
			spec.Logf("warn", "无法查询 performance_schema.metadata_locks（旧服务端或未启用），仅按连接默认库判断目标占用：%v", err)
		}
	}
	busy = append(busy, mdl...)
	if len(busy) > 0 {
		return fmt.Errorf("target database %q still has %d active lock(s)/session(s) (%s): close all target connections and retry",
			target, len(busy), strings.Join(busy, "; "))
	}
	return nil
}

// Import creates (TargetIsNew) or fully replaces (overwrite) the target schema
// and loads the dump through stdin.
func (a *MySQLAdapter) Import(ctx context.Context, spec *RestoreSpec) error {
	c, err := mysqlPrepare(spec)
	if err != nil {
		return err
	}
	if spec.ArtifactFile == "" {
		return errors.New("mysql restore artifact is missing")
	}
	quoted := "`" + strings.ReplaceAll(c.db.TargetDatabase, "`", "``") + "`"
	// 库级默认字符集/排序规则来自快照 manifest，恢复时显式带上，避免继承目标
	// 服务器的默认值。值必须先过白名单校验：manifest 存在仓库里，不能当作可信
	// SQL 片段拼接。
	suffix := ""
	if validCharsetName(spec.ArtifactCharset) {
		suffix = " CHARACTER SET " + spec.ArtifactCharset
		if validCharsetName(spec.ArtifactCollation) {
			suffix += " COLLATE " + spec.ArtifactCollation
		}
	}
	// 新建不允许 IF NOT EXISTS：并发执行者创建的库必须冲突失败。
	setup := "CREATE DATABASE " + quoted + suffix
	if !spec.TargetIsNew {
		// DROP 需要目标库上的 schema metadata lock，客户端默认 lock_wait_timeout 极大
		// （31536000s）、运行期限 12h：预检之后才出现的锁（竞态）会让恢复长期占住全局
		// 数据库恢复互斥。给这一批语句加有界等待，超时即失败——预检已排除已知占用，
		// 这里只需兜住竞态，故 30s 足够；失败后回滚重新导入时锁通常已释放。
		setup = fmt.Sprintf("SET SESSION lock_wait_timeout = %d; DROP DATABASE IF EXISTS %s; CREATE DATABASE %s%s",
			mysqlDropLockWaitSeconds, quoted, quoted, suffix)
	}
	if err := c.runQuery(ctx, spec, setup, nil); err != nil {
		// 有界等待超时（1205）说明目标库被其它会话锁住（预检之后才出现的锁，竞态）。
		// 给出与预检一致的处置提示，而不是把原始 1205 直接抛给运维。
		if mysqlErrLockWaitTimeout(err) {
			return fmt.Errorf("target database %q is locked by another session (lock wait timeout %ds); close all target connections and retry: %w",
				c.db.TargetDatabase, mysqlDropLockWaitSeconds, err)
		}
		return fmt.Errorf("prepare mysql target database: %w", err)
	}
	dumpArgs := []string{
		"--defaults-extra-file=" + c.cnf,
		"--binary-mode",
		"-h", c.db.TargetHost, "-P", strconv.Itoa(c.db.TargetPort), "-u", c.db.TargetUsername,
		// 以 -- 结束选项：目标库名是位置参数，且合法库名可以以 '-' 开头。
		"--",
		c.db.TargetDatabase,
	}
	exit, err := spec.Exec.Run(ctx, Cmd{Exe: c.mysql, Args: dumpArgs, StdinPath: spec.ArtifactFile}, c.logf, c.logf)
	if err != nil || exit != 0 {
		return exitError("mysql restore", exit, err)
	}
	return nil
}

// VerifyRestored requires every table named in the dump to exist in the target.
// A recreated schema containing fewer tables means the import did not land.
func (a *MySQLAdapter) VerifyRestored(ctx context.Context, spec *RestoreSpec) error {
	c, err := mysqlPrepare(spec)
	if err != nil {
		return err
	}
	want, err := mysqlDumpTableNames(spec.ArtifactFile)
	if err != nil {
		return err
	}
	// 是否折叠表名由服务端大小写敏感性决定：lower_case_table_names=0（Linux 默认）
	// 下表名区分大小写，必须按原始名字比对，否则"只恢复了同名异大小写表之一"会被
	// 折叠掩盖；=1/2 时服务端自身折叠名字，必须折叠后再比以免误报缺表。
	// 读取失败时按不敏感处理（保守，等价于此前的行为）。
	fold := true
	if err := c.runQuery(ctx, spec, "SELECT @@lower_case_table_names", func(line string) {
		if n, convErr := strconv.Atoi(strings.TrimSpace(line)); convErr == nil {
			fold = n != 0
		}
	}); err != nil {
		spec.Logf("warn", "mysql verification: 无法读取 lower_case_table_names，按大小写不敏感比对表名")
	}
	var present []string
	// 只统计基表：information_schema.tables 也包含视图，而 want 来自 dump 的
	// CREATE TABLE（不含 CREATE VIEW），否则含视图的库两个数字永不相等、日志易被
	// 误读成"多出表"（实测 9 tables present, 8 expected）。
	query := "SELECT table_name FROM information_schema.tables WHERE table_type = 'BASE TABLE' AND table_schema = '" +
		strings.ReplaceAll(c.db.TargetDatabase, "'", "''") + "'"
	if err := c.runQuery(ctx, spec, query, func(line string) { present = append(present, line) }); err != nil {
		return err
	}
	if missing := verifyTableSets(want, present, fold); len(missing) > 0 {
		return fmt.Errorf("mysql restore verification failed: missing tables %v", missing)
	}
	spec.Logf("info", "mysql verification: %d tables present, %d expected", len(present), len(want))
	return nil
}

// RemoveTarget drops the schema created by this run.
func (a *MySQLAdapter) RemoveTarget(ctx context.Context, spec *RestoreSpec) error {
	if !spec.TargetIsNew {
		return errors.New("refusing to remove a target this run did not create")
	}
	c, err := mysqlPrepare(spec)
	if err != nil {
		return err
	}
	quoted := "`" + strings.ReplaceAll(c.db.TargetDatabase, "`", "``") + "`"
	if err := c.runQuery(ctx, spec, "DROP DATABASE IF EXISTS "+quoted, nil); err != nil {
		return fmt.Errorf("drop mysql target database: %w", err)
	}
	return nil
}

// mysqlDumpTableNames 从 mysqldump 输出里抽取 CREATE TABLE 的表名（保留原始大小写，
// 由调用方按服务端大小写敏感性决定是否折叠）。表名用反引号包围，且名字内部的反引号
// 用两个反引号转义（合法表名可以含反引号）。
func mysqlDumpTableNames(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open mysql dump: %w", err)
	}
	defer f.Close()
	var names []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(strings.ToUpper(line), "CREATE TABLE ") {
			continue
		}
		rest := strings.TrimSpace(line[len("CREATE TABLE "):])
		if strings.HasPrefix(rest, "IF NOT EXISTS ") {
			rest = strings.TrimSpace(rest[len("IF NOT EXISTS "):])
		}
		rest = strings.TrimPrefix(rest, "`")
		name, _, ok := mysqlUnquoteIdentifier(rest)
		if ok {
			names = append(names, name)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan mysql dump: %w", err)
	}
	return names, nil
}

// verifyTableSets 比对 dump 的表集合与目标库实际表集合，返回缺失的表名（升序）。
//
// fold 表示服务端表名大小写不敏感（lower_case_table_names != 0）：此时服务端自身
// 会把名字折叠，dump 里的原始大小写与 information_schema 返回的必然不同，必须折叠
// 后再比，否则误报缺表。fold=false（Linux 默认 0）时按原始名字比较——折叠会让
// "只恢复了其中一张同名异大小写的表"永远通过校验（实测 p_hint_Foo/p_hint_foo 被
// 折叠成 1，日志显示 "1 tables present, 1 expected"，漏检）。
func verifyTableSets(want, present []string, fold bool) []string {
	norm := func(s string) string {
		if fold {
			return strings.ToLower(s)
		}
		return s
	}
	have := make(map[string]struct{}, len(present))
	for _, p := range present {
		have[norm(p)] = struct{}{}
	}
	var missing []string
	for _, w := range want {
		if _, ok := have[norm(w)]; !ok {
			missing = append(missing, w)
		}
	}
	sort.Strings(missing)
	return missing
}

// mysqlUnquoteIdentifier 读取 s 开头（已去掉起始反引号）的反引号标识符，
// 把两个连续反引号还原为单个反引号，返回名字、结束反引号之后的部分与是否解析成功。
func mysqlUnquoteIdentifier(s string) (string, string, bool) {
	var sb strings.Builder
	// 三句式 for：转义分支里的 i++ 必须真正跳过下一个字节（range-over-int 会在下一轮
	// 重新赋值 i，跳过失效，实测把 `a``b` 解析成 a` 而不是 a`b）。
	for i := 0; i < len(s); i++ {
		if s[i] != '`' {
			sb.WriteByte(s[i])
			continue
		}
		if i+1 < len(s) && s[i+1] == '`' {
			sb.WriteByte('`')
			i++
			continue
		}
		return sb.String(), s[i+1:], sb.Len() > 0
	}
	return "", "", false
}
