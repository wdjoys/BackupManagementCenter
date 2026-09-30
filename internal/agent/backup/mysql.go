// Package backup implements plan-kind adapters.
package backup

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"backupmanagementcenter/internal/model"
)

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
	if err := ValidateExtraArgs(KindMySQL, s.ExtraArgs); err != nil {
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
	cnfContent := fmt.Sprintf("[client]\nuser=%s\npassword=\"%s\"\nhost=\"%s\"\nport=%d\n", mysqlOptionValue(source.Username), mysqlOptionValue(rc.Secrets.DBPassword), mysqlOptionValue(source.Host), source.Port)
	cnfFile, err := writeSecretFile(rc.TempDir, "my.cnf", cnfContent)
	if err != nil {
		return nil, fmt.Errorf("write my.cnf: %w", err)
	}

	toolVersions := make(map[string]string)
	mysqldumpPath := toolPath("mysqldump")
	logLine := func(l string) { rc.Logf("info", "%s", l) }
	// Non-transactional tables are not protected by --single-transaction and
	// can change while the dump is running. Surface the count before starting
	// the backup so operators can schedule a maintenance window if needed.
	nonTransactionalQuery := "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema NOT IN ('information_schema','mysql','performance_schema','sys') AND engine IS NOT NULL AND UPPER(engine) NOT IN ('INNODB','NDBCLUSTER')"
	if source.Database != "" && source.Database != "all" {
		nonTransactionalQuery += " AND table_schema = '" + strings.ReplaceAll(source.Database, "'", "''") + "'"
	}
	var nonTransactional string
	if _, checkErr := rc.Exec.Run(ctx, Cmd{Exe: toolPath("mysql"), Args: append([]string{"--defaults-extra-file=" + cnfFile}, "-N", "-s", "-e", nonTransactionalQuery)}, func(line string) {
		nonTransactional = strings.TrimSpace(line)
	}, logLine); checkErr == nil {
		if count, parseErr := strconv.Atoi(nonTransactional); parseErr == nil && count > 0 {
			rc.Logf("warn", "detected %d non-transactional MySQL tables; dump may be inconsistent", count)
		}
	} else {
		rc.Logf("warn", "could not check non-transactional MySQL tables: %v", checkErr)
	}

	dumpFile := filepath.Join(stagingDir, fmt.Sprintf("%s.sql", rc.Task.PlanID))
	// --defaults-extra-file 必须是第一个参数，出现在后面会被客户端当成未知变量。
	args := []string{
		"--defaults-extra-file=" + cnfFile,
		"--single-transaction", "--quick", "--routines", "--events", "--triggers",
		"--hex-blob", "--no-tablespaces",
		"--result-file=" + dumpFile,
	}
	if source.Database == "all" {
		args = append(args, "--all-databases")
	} else {
		args = append(args, source.Database)
	}
	args = append(args, source.ExtraArgs...)

	exitCode, err := rc.Exec.Run(ctx, Cmd{Exe: mysqldumpPath, Args: args, Env: nil}, logLine, logLine)
	if err != nil || exitCode != 0 {
		return nil, exitError("mysqldump", exitCode, err)
	}
	toolVersions["mysqldump"] = getToolVersion(ctx, rc.Exec, mysqldumpPath, nil)

	manifest := &Manifest{
		Adapter:      KindMySQL,
		ToolVersions: toolVersions,
		Databases:    []DbExport{{Database: source.Database, File: filepath.Base(dumpFile), Format: "sql"}},
		StartedAt:    time.Now().UTC(),
		FinishedAt:   time.Now().UTC(),
		RestoreHints: map[string]string{
			"host":     source.Host,
			"port":     strconv.Itoa(source.Port),
			"username": source.Username,
		},
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

// mysqlCtx groups the target connection details a restore needs.
type mysqlCtx struct {
	db    *model.DatabaseRestore
	cnf   string
	mysql string
	logf  func(string)
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
	if mysqlSystemSchemas[strings.ToLower(db.TargetDatabase)] {
		return nil, fmt.Errorf("refusing to restore into system schema %q", db.TargetDatabase)
	}
	cnfContent := fmt.Sprintf("[client]\nuser=%s\npassword=\"%s\"\nhost=\"%s\"\nport=%d\n",
		mysqlOptionValue(db.TargetUsername), mysqlOptionValue(spec.Secrets.DBPassword), mysqlOptionValue(db.TargetHost), db.TargetPort)
	cnfFile, err := writeSecretFile(spec.StagingDir, "my.cnf", cnfContent)
	if err != nil {
		return nil, fmt.Errorf("write my.cnf: %w", err)
	}
	return &mysqlCtx{
		db:    db,
		cnf:   cnfFile,
		mysql: toolPath("mysql"),
		logf:  func(l string) { spec.Logf("info", "%s", l) },
	}, nil
}

// mysqlSystemSchemas 是绝不允许作为恢复目标的系统库。
var mysqlSystemSchemas = map[string]bool{
	"mysql": true, "information_schema": true, "performance_schema": true, "sys": true,
}

// args 构造 mysql 客户端参数：-e 执行语句，或 stdin 导入 dump。
// --defaults-extra-file 必须是第一个参数，否则客户端报 unknown variable。
func (c *mysqlCtx) args(query string) []string {
	return []string{
		"--defaults-extra-file=" + c.cnf,
		"--binary-mode",
		"-h", c.db.TargetHost, "-P", strconv.Itoa(c.db.TargetPort), "-u", c.db.TargetUsername,
		"-e", query,
	}
}

// runQuery 执行一条语句并把首个非空 stdout 行交给 scan。
func (c *mysqlCtx) runQuery(ctx context.Context, spec *RestoreSpec, query string, scan func(string)) error {
	exit, err := spec.Exec.Run(ctx, Cmd{Exe: c.mysql, Args: c.args(query)},
		func(line string) {
			if trimmed := strings.TrimSpace(line); trimmed != "" && scan != nil {
				scan(trimmed)
			}
		}, c.logf)
	if err != nil || exit != 0 {
		return exitError("mysql query", exit, err)
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
		return false, err
	}
	return found != "", nil
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
	// 新建不允许 IF NOT EXISTS：并发执行者创建的库必须冲突失败。
	setup := "CREATE DATABASE " + quoted
	if !spec.TargetIsNew {
		setup = "DROP DATABASE IF EXISTS " + quoted + "; CREATE DATABASE " + quoted
	}
	if err := c.runQuery(ctx, spec, setup, nil); err != nil {
		return fmt.Errorf("prepare mysql target database: %w", err)
	}
	dumpArgs := []string{
		"--defaults-extra-file=" + c.cnf,
		"--binary-mode",
		"-h", c.db.TargetHost, "-P", strconv.Itoa(c.db.TargetPort), "-u", c.db.TargetUsername,
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
	present := map[string]struct{}{}
	query := "SELECT table_name FROM information_schema.tables WHERE table_schema = '" +
		strings.ReplaceAll(c.db.TargetDatabase, "'", "''") + "'"
	if err := c.runQuery(ctx, spec, query, func(line string) { present[strings.ToLower(line)] = struct{}{} }); err != nil {
		return err
	}
	var missing []string
	for name := range want {
		if _, ok := present[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
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

// mysqlDumpTableNames 从 mysqldump 输出里抽取 CREATE TABLE 的表名。
func mysqlDumpTableNames(path string) (map[string]struct{}, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open mysql dump: %w", err)
	}
	defer f.Close()
	names := map[string]struct{}{}
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
		if idx := strings.Index(rest, "`"); idx > 0 {
			names[strings.ToLower(rest[:idx])] = struct{}{}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan mysql dump: %w", err)
	}
	return names, nil
}
