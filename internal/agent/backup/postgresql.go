// Package backup implements plan-kind adapters.
package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// PostgreSQLAdapter implements Adapter for PostgreSQL plans.
type PostgreSQLAdapter struct{}

// Validate checks that required tools are present and source spec is sane.
func (a *PostgreSQLAdapter) Validate(ctx context.Context, spec PlanSpec) error {
	if spec.Kind != KindPostgreSQL {
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
	if err := ValidateExtraArgs(KindPostgreSQL, s.ExtraArgs); err != nil {
		return err
	}
	return nil
}

// Backup runs pg_dump/pg_dumpall, writes manifest, returns BackupArtifact with staging dir.
func (a *PostgreSQLAdapter) Backup(ctx context.Context, rc *RunContext) (*BackupArtifact, error) {
	source := rc.Task.Source
	stagingDir := filepath.Join(rc.TempDir, "staging")
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir staging: %w", err)
	}

	// Write PGPASSFILE
	pgpassContent := fmt.Sprintf("%s:%d:*:%s:%s\n", pgpassField(source.Host), source.Port, pgpassField(source.Username), pgpassField(rc.Secrets.DBPassword))
	pgpassFile, err := writeSecretFile(rc.TempDir, "pgpass", pgpassContent)
	if err != nil {
		return nil, fmt.Errorf("write pgpass: %w", err)
	}

	env := []string{"PGPASSFILE=" + pgpassFile}
	toolVersions := make(map[string]string)

	manifest := &Manifest{
		Adapter:      KindPostgreSQL,
		ToolVersions: toolVersions,
		StartedAt:    time.Now().UTC(),
		RestoreHints: map[string]string{
			"host":     source.Host,
			"port":     strconv.Itoa(source.Port),
			"username": source.Username,
		},
	}

	var dbExports []DbExport
	logLine := func(l string) { rc.Logf("info", "%s", l) }

	if source.Database == "all" {
		// globals dump
		globalsFile := filepath.Join(stagingDir, "globals.sql")
		args := []string{
			"--globals-only", "--file=" + globalsFile,
			"--host", source.Host, "--port", strconv.Itoa(source.Port), "--username", source.Username,
		}
		args = append(args, source.ExtraArgs...)
		exitCode, err := rc.Exec.Run(ctx, Cmd{Exe: toolPath("pg_dumpall"), Args: args, Env: env}, logLine, logLine)
		if err != nil || exitCode != 0 {
			return nil, exitError("pg_dumpall globals failed", exitCode, err)
		}
		dbExports = append(dbExports, DbExport{Database: "globals", File: "globals.sql", Format: "sql"})
		toolVersions["pg_dumpall"] = getToolVersion(ctx, rc.Exec, toolPath("pg_dumpall"), env)

		// list databases
		listArgs := []string{"-h", source.Host, "-p", strconv.Itoa(source.Port), "-U", source.Username, "-d", "postgres", "-t", "-c", "SELECT datname FROM pg_database WHERE NOT datistemplate AND datallowconn"}
		var dbNames []string
		_, err = rc.Exec.Run(ctx, Cmd{Exe: toolPath("psql"), Args: listArgs, Env: env},
			func(line string) {
				name := strings.TrimSpace(line)
				if name != "" {
					dbNames = append(dbNames, name)
				}
			}, logLine)
		if err != nil {
			return nil, fmt.Errorf("list databases: %w", err)
		}

		for _, db := range dbNames {
			dumpFile := filepath.Join(stagingDir, postgresDumpFilename(db))
			args := []string{
				"--format=custom",
				"--file=" + dumpFile,
				"--host", source.Host,
				"--port", strconv.Itoa(source.Port),
				"--username", source.Username,
				db,
			}
			args = append(args, source.ExtraArgs...)
			exitCode, err := rc.Exec.Run(ctx, Cmd{Exe: toolPath("pg_dump"), Args: args, Env: env}, logLine, logLine)
			if err != nil || exitCode != 0 {
				return nil, exitError(fmt.Sprintf("pg_dump %s", db), exitCode, err)
			}
			dbExports = append(dbExports, DbExport{Database: db, File: filepath.Base(dumpFile), Format: "pgdump"})
		}
		toolVersions["pg_dump"] = getToolVersion(ctx, rc.Exec, toolPath("pg_dump"), env)
		toolVersions["psql"] = getToolVersion(ctx, rc.Exec, toolPath("psql"), env)
	} else {
		// single database
		dumpFile := filepath.Join(stagingDir, fmt.Sprintf("%s.pgdump", rc.Task.PlanID))
		args := []string{
			"--format=custom",
			"--file=" + dumpFile,
			"--host", source.Host,
			"--port", strconv.Itoa(source.Port),
			"--username", source.Username,
			source.Database,
		}
		args = append(args, source.ExtraArgs...)
		exitCode, err := rc.Exec.Run(ctx, Cmd{Exe: toolPath("pg_dump"), Args: args, Env: env}, logLine, logLine)
		if err != nil || exitCode != 0 {
			return nil, exitError("pg_dump failed", exitCode, err)
		}
		dbExports = append(dbExports, DbExport{Database: source.Database, File: filepath.Base(dumpFile), Format: "pgdump"})
		toolVersions["pg_dump"] = getToolVersion(ctx, rc.Exec, toolPath("pg_dump"), env)
	}

	manifest.Databases = dbExports
	manifest.FinishedAt = time.Now().UTC()

	// Write manifest.json
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

// postgresDumpFilename never incorporates a database name directly into a
// path. PostgreSQL identifiers may contain slashes and other path separators;
// a stable digest keeps artifacts inside the staging directory and avoids
// collisions between unusual names.
func postgresDumpFilename(database string) string {
	sum := sha256.Sum256([]byte(database))
	return "db-" + hex.EncodeToString(sum[:8]) + ".pgdump"
}

func pgpassField(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, ":", `\:`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	return strings.ReplaceAll(v, "\r", `\r`)
}

// pgRestoreCtx groups the target connection details a restore needs.
type pgRestoreCtx struct {
	db     *model.DatabaseRestore
	env    []string
	psql   string
	pgRest string
	logf   func(string)
}

// pgPrepare writes the target PGPASSFILE into the (private) staging dir and
// resolves the client binaries. Credentials never appear in argv.
func pgPrepare(spec *RestoreSpec) (*pgRestoreCtx, error) {
	db := spec.Database
	if db == nil {
		return nil, errors.New("database restore spec missing")
	}
	if db.TargetDatabase == "" || db.TargetDatabase == "all" {
		return nil, errors.New("postgresql restore requires a single target database")
	}
	pgpassContent := fmt.Sprintf("%s:%d:*:%s:%s\n", pgpassField(db.TargetHost), db.TargetPort, pgpassField(db.TargetUsername), pgpassField(spec.Secrets.DBPassword))
	pgpassFile, err := writeSecretFile(spec.StagingDir, "pgpass_restore", pgpassContent)
	if err != nil {
		return nil, fmt.Errorf("write pgpass: %w", err)
	}
	return &pgRestoreCtx{
		db:     db,
		env:    []string{"PGPASSFILE=" + pgpassFile},
		psql:   toolPath("psql"),
		pgRest: toolPath("pg_restore"),
		logf:   func(l string) { spec.Logf("info", "%s", l) },
	}, nil
}

// pgMaintenanceArgs 返回以维护库 postgres 为目标的 psql 参数。
func (c *pgRestoreCtx) maintenanceQuery(sql string) []string {
	return []string{"-h", c.db.TargetHost, "-p", strconv.Itoa(c.db.TargetPort), "-U", c.db.TargetUsername,
		"-d", "postgres", "-v", "ON_ERROR_STOP=1", "-tAc", sql}
}

// targetQuery 返回以目标库为目标的 psql 参数。
func (c *pgRestoreCtx) targetQuery(sql string) []string {
	return []string{"-h", c.db.TargetHost, "-p", strconv.Itoa(c.db.TargetPort), "-U", c.db.TargetUsername,
		"-d", c.db.TargetDatabase, "-v", "ON_ERROR_STOP=1", "-tAc", sql}
}

// pgSystemDatabases 是绝不允许作为恢复目标的集群维护库。
var pgSystemDatabases = map[string]bool{
	"postgres": true, "template0": true, "template1": true,
}

// TargetExists reports whether the target database exists. A failed query
// (auth, connection, permission) is an error, never "absent".
func (a *PostgreSQLAdapter) TargetExists(ctx context.Context, spec *RestoreSpec) (bool, error) {
	c, err := pgPrepare(spec)
	if err != nil {
		return false, err
	}
	var out string
	exit, err := spec.Exec.Run(ctx, Cmd{Exe: c.psql, Args: c.maintenanceQuery(
		"SELECT 1 FROM pg_database WHERE datname = '" + strings.ReplaceAll(c.db.TargetDatabase, "'", "''") + "'"),
		Env: c.env}, func(line string) { out = strings.TrimSpace(line) }, c.logf)
	if err != nil || exit != 0 {
		return false, exitError("check postgres target database failed", exit, err)
	}
	return out != "", nil
}

// Import creates (TargetIsNew) or fully rebuilds (overwrite) the target database
// and loads the dump. It refuses system databases and clusters where the
// current role cannot drop/recreate the database, before any destructive step.
func (a *PostgreSQLAdapter) Import(ctx context.Context, spec *RestoreSpec) error {
	c, err := pgPrepare(spec)
	if err != nil {
		return err
	}
	if pgSystemDatabases[c.db.TargetDatabase] {
		return fmt.Errorf("refusing to restore into system database %q", c.db.TargetDatabase)
	}
	quoted := `"` + strings.ReplaceAll(c.db.TargetDatabase, `"`, `""`) + `"`

	if !spec.TargetIsNew {
		// 覆盖前确认具备重建权限：非 owner 且非 superuser 时必须拒绝，不能依赖
		// --clean 之类的不完整替换。
		var ability string
		exit, err := spec.Exec.Run(ctx, Cmd{Exe: c.psql, Args: c.maintenanceQuery(
			"SELECT CASE WHEN (SELECT rolsuper FROM pg_roles WHERE rolname = current_user) OR EXISTS (SELECT 1 FROM pg_database d WHERE d.datname = '" +
				strings.ReplaceAll(c.db.TargetDatabase, "'", "''") + "' AND d.datdba = (SELECT oid FROM pg_roles WHERE rolname = current_user)) THEN 'ok' ELSE 'denied' END"),
			Env: c.env}, func(line string) { ability = strings.TrimSpace(line) }, c.logf)
		if err != nil || exit != 0 {
			return exitError("check postgres rebuild permission failed", exit, err)
		}
		if ability != "ok" {
			return fmt.Errorf("current role cannot drop and recreate database %q; refusing a partial overwrite", c.db.TargetDatabase)
		}
		// 断开其他连接后才能 DROP DATABASE。
		termSQL := "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '" +
			strings.ReplaceAll(c.db.TargetDatabase, "'", "''") + "' AND pid <> pg_backend_pid()"
		if exit, err := spec.Exec.Run(ctx, Cmd{Exe: c.psql, Args: c.maintenanceQuery(termSQL), Env: c.env}, c.logf, c.logf); err != nil || exit != 0 {
			return exitError("terminate postgres target connections failed", exit, err)
		}
		if exit, err := spec.Exec.Run(ctx, Cmd{Exe: c.psql, Args: c.maintenanceQuery("DROP DATABASE " + quoted), Env: c.env}, c.logf, c.logf); err != nil || exit != 0 {
			return exitError("drop postgres target database failed", exit, err)
		}
	}
	// CREATE DATABASE 不带 IF NOT EXISTS：并发的其他执行者创建的库必须冲突失败。
	if exit, err := spec.Exec.Run(ctx, Cmd{Exe: c.psql, Args: c.maintenanceQuery("CREATE DATABASE " + quoted), Env: c.env}, c.logf, c.logf); err != nil || exit != 0 {
		return exitError("create postgres target database failed", exit, err)
	}

	if spec.ArtifactFile == "" {
		return errors.New("postgresql restore artifact is missing")
	}
	args := []string{
		"--exit-on-error", "--no-owner",
		"--dbname=" + c.db.TargetDatabase,
		"-h", c.db.TargetHost, "-p", strconv.Itoa(c.db.TargetPort), "-U", c.db.TargetUsername,
		spec.ArtifactFile,
	}
	if exit, err := spec.Exec.Run(ctx, Cmd{Exe: c.pgRest, Args: args, Env: c.env}, c.logf, c.logf); err != nil || exit != 0 {
		return exitError("pg_restore failed", exit, err)
	}
	return nil
}

// VerifyRestored compares the relation set listed in the dump with the target's
// relation set. A rebuilt database must contain exactly the dumped relations;
// anything else means objects were left over from a failed import.
func (a *PostgreSQLAdapter) VerifyRestored(ctx context.Context, spec *RestoreSpec) error {
	c, err := pgPrepare(spec)
	if err != nil {
		return err
	}
	dumpObjects := map[string]struct{}{}
	var listOut strings.Builder
	exit, err := spec.Exec.Run(ctx, Cmd{Exe: c.pgRest, Args: []string{"-l", spec.ArtifactFile}},
		func(line string) { listOut.WriteString(line); listOut.WriteString("\n") }, c.logf)
	if err != nil || exit != 0 {
		return exitError("pg_restore -l failed", exit, err)
	}
	for name := range pgDumpRelationNames(listOut.String()) {
		dumpObjects[name] = struct{}{}
	}
	targetObjects := map[string]struct{}{}
	var got strings.Builder
	query := "SELECT n.nspname || '.' || c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace " +
		"WHERE c.relkind IN ('r','p','v','m','S') AND n.nspname NOT IN ('pg_catalog','information_schema') ORDER BY 1"
	exit, err = spec.Exec.Run(ctx, Cmd{Exe: c.psql, Args: c.targetQuery(query), Env: c.env},
		func(line string) { got.WriteString(strings.TrimSpace(line)); got.WriteString("\n") }, c.logf)
	if err != nil || exit != 0 {
		return exitError("postgresql verification query failed", exit, err)
	}
	for _, line := range strings.Split(got.String(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			targetObjects[line] = struct{}{}
		}
	}
	missing, extra := setDiff(dumpObjects, targetObjects), setDiff(targetObjects, dumpObjects)
	if len(missing) > 0 || len(extra) > 0 {
		return fmt.Errorf("postgresql restore verification failed: missing=%v unexpected=%v", missing, extra)
	}
	spec.Logf("info", "postgresql verification: %d relations match the snapshot", len(dumpObjects))
	return nil
}

// RemoveTarget drops the database created by this run.
func (a *PostgreSQLAdapter) RemoveTarget(ctx context.Context, spec *RestoreSpec) error {
	if !spec.TargetIsNew {
		return errors.New("refusing to remove a target this run did not create")
	}
	c, err := pgPrepare(spec)
	if err != nil {
		return err
	}
	termSQL := "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '" +
		strings.ReplaceAll(c.db.TargetDatabase, "'", "''") + "' AND pid <> pg_backend_pid()"
	if exit, err := spec.Exec.Run(ctx, Cmd{Exe: c.psql, Args: c.maintenanceQuery(termSQL), Env: c.env}, c.logf, c.logf); err != nil || exit != 0 {
		return exitError("terminate postgres target connections failed", exit, err)
	}
	quoted := `"` + strings.ReplaceAll(c.db.TargetDatabase, `"`, `""`) + `"`
	if exit, err := spec.Exec.Run(ctx, Cmd{Exe: c.psql, Args: c.maintenanceQuery("DROP DATABASE IF EXISTS " + quoted), Env: c.env}, c.logf, c.logf); err != nil || exit != 0 {
		return exitError("drop postgres target database failed", exit, err)
	}
	return nil
}

// pgDumpRelationNames extracts relation identifiers from `pg_restore -l` output.
// Format: "<dumpId>; <oid> <oid> <TYPE> <schema> <name> <owner>".
func pgDumpRelationNames(list string) map[string]struct{} {
	names := map[string]struct{}{}
	for _, line := range strings.Split(list, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || !strings.HasSuffix(fields[0], ";") {
			continue
		}
		switch fields[3] {
		case "TABLE", "SEQUENCE", "VIEW", "INDEX":
			names[fields[4]+"."+fields[5]] = struct{}{}
		case "MATERIALIZED":
			if len(fields) >= 7 {
				names[fields[5]+"."+fields[6]] = struct{}{}
			}
		}
	}
	return names
}

// setDiff 返回 a 中不属于 b 的元素（升序，便于稳定报错）。
func setDiff(a, b map[string]struct{}) []string {
	var out []string
	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
