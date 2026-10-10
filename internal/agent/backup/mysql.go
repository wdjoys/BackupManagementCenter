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
	args = append(args, source.ExtraArgs...)
	// 位置参数形式的库名必须以 -- 结束选项：合法的库名可以以 '-' 开头（引号标识符），
	// 否则会被客户端当作选项簇解析（实测 mysqldump 报 unknown option '-s'）。
	if source.Database == "all" {
		args = append(args, "--all-databases")
	} else {
		args = append(args, "--", source.Database)
	}

	// 收集 stderr：MySQL ≤5.7 默认 character_set_server=latin1，官方 8.0 客户端
	// 请求 utf8mb4 时会因排序规则 utf8mb4_0900_ai_ci 不存在而回退 latin1，导致
	// 非 ASCII 库名被按 latin1 解释、报 "Unknown database"（库其实存在）。
	// 该场景无法在客户端修复（utf8mb3 会损坏 4 字节字符），因此把误导性的
	// "库不存在" 转成可诊断的错误。
	var stderrTail []string
	captureStderr := func(line string) {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			stderrTail = append(stderrTail, trimmed)
			if len(stderrTail) > 20 {
				stderrTail = stderrTail[len(stderrTail)-20:]
			}
		}
		logLine(line)
	}
	exitCode, err := rc.Exec.Run(ctx, Cmd{Exe: mysqldumpPath, Args: args, Env: nil}, logLine, captureStderr)
	if err != nil || exitCode != 0 {
		// 单引号库名的原因更具体，优先给出；字符集提示只在 8.0 客户端路径下成立。
		if hint := mysqlDumpFailureHint(source.Database, stderrTail, tools.legacy); hint != "" {
			rc.Logf("warn", "%s", hint)
		}
		return nil, exitError("mysqldump", exitCode, err)
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

// mysqlDumpFailureHint 选出与失败原因匹配的诊断提示（无匹配返回空串）。
//
// legacy 表示本次用的是 5.7 客户端：此时不可能发生 8.0 客户端的 utf8mb4 排序规则
// 回退，同样的 "Unknown database" 就是库确实不存在，不能再提示字符集问题（实测
// bmc-mysql56 上不存在的非 ASCII 库名被误诊为"库实际存在"的字符集问题）。
func mysqlDumpFailureHint(database string, stderrTail []string, legacy bool) string {
	// 单引号库名的原因更具体，优先给出（含单引号且非 ASCII 时避免被字符集提示误导）。
	if hint := mysqlDumpQuoteNameHint(database, stderrTail); hint != "" {
		return hint
	}
	if legacy {
		return ""
	}
	return mysqlDumpNameCharsetHint(database, stderrTail)
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
		name, ok := mysqlUnquoteIdentifier(rest)
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
// 把两个连续反引号还原为单个反引号，返回名字与是否找到结束反引号。
func mysqlUnquoteIdentifier(s string) (string, bool) {
	var sb strings.Builder
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
		return sb.String(), sb.Len() > 0
	}
	return "", false
}
