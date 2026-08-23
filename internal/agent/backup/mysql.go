// Package backup implements plan-kind adapters.
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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
	if _, checkErr := rc.Exec.Run(ctx, Cmd{Exe: toolPath("mysql"), Args: []string{"--defaults-extra-file=" + cnfFile, "-N", "-s", "-e", nonTransactionalQuery}}, func(line string) {
		nonTransactional = strings.TrimSpace(line)
	}, logLine); checkErr == nil {
		if count, parseErr := strconv.Atoi(nonTransactional); parseErr == nil && count > 0 {
			rc.Logf("warn", "detected %d non-transactional MySQL tables; dump may be inconsistent", count)
		}
	} else {
		rc.Logf("warn", "could not check non-transactional MySQL tables: %v", checkErr)
	}

	dumpFile := filepath.Join(stagingDir, fmt.Sprintf("%s.sql", rc.Task.PlanID))
	args := []string{
		"--single-transaction", "--quick", "--routines", "--events", "--triggers",
		"--hex-blob", "--no-tablespaces",
		"--defaults-extra-file=" + cnfFile,
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
		return nil, fmt.Errorf("mysqldump failed (exit %d): %w", exitCode, err)
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

// Restore imports restored snapshot data into target MySQL/MariaDB.
func (a *MySQLAdapter) Restore(ctx context.Context, spec *RestoreSpec) error {
	db := spec.Database
	if db == nil {
		return errors.New("database restore spec missing")
	}
	stagingDir := spec.StagingDir

	// Read manifest
	manifestPath := filepath.Join(stagingDir, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("unmarshal manifest: %w", err)
	}
	if manifest.Adapter != KindMySQL {
		return fmt.Errorf("manifest adapter mismatch: %s", manifest.Adapter)
	}
	if len(manifest.Databases) > 0 && manifest.Databases[0].Database == "all" && db.TargetDatabase != "all" {
		return errors.New("all-databases MySQL snapshot requires target_database=all")
	}
	if len(manifest.Databases) > 0 && manifest.Databases[0].Database != "all" && db.TargetDatabase == "all" {
		return errors.New("single-database MySQL snapshot requires an explicit target_database")
	}

	// Write defaults-extra-file for target in stagingDir
	cnfContent := fmt.Sprintf("[client]\nuser=%s\npassword=\"%s\"\nhost=\"%s\"\nport=%d\n",
		mysqlOptionValue(db.TargetUsername), mysqlOptionValue(spec.Secrets.DBPassword), mysqlOptionValue(db.TargetHost), db.TargetPort)
	cnfFile, err := writeSecretFile(stagingDir, "my.cnf", cnfContent)
	if err != nil {
		return fmt.Errorf("write my.cnf: %w", err)
	}

	mysqlPath := toolPath("mysql")
	logLine := func(l string) { spec.Logf("info", "%s", l) }
	if db.TargetDatabase != "" && db.TargetDatabase != "all" {
		quoted := "`" + strings.ReplaceAll(db.TargetDatabase, "`", "``") + "`"
		sql := "CREATE DATABASE IF NOT EXISTS " + quoted
		if db.ReplaceExisting {
			sql = "DROP DATABASE IF EXISTS " + quoted + "; CREATE DATABASE " + quoted
		}
		setupArgs := []string{
			"--binary-mode", "--defaults-extra-file=" + cnfFile,
			"-h", db.TargetHost, "-P", strconv.Itoa(db.TargetPort), "-u", db.TargetUsername,
			"-e", sql,
		}
		if exit, setupErr := spec.Exec.Run(ctx, Cmd{Exe: mysqlPath, Args: setupArgs}, logLine, logLine); setupErr != nil || exit != 0 {
			return fmt.Errorf("prepare mysql target database failed (exit %d): %w", exit, setupErr)
		}
	}

	for _, dbe := range manifest.Databases {
		dumpFile := filepath.Join(stagingDir, dbe.File)
		// Feed the dump through stdin. Keeping the dump path out of a shell
		// command avoids command injection and preserves spaces/quotes safely.
		mysqlArgs := []string{
			"--binary-mode", "--defaults-extra-file=" + cnfFile,
			"-h", db.TargetHost, "-P", strconv.Itoa(db.TargetPort), "-u", db.TargetUsername,
		}
		if db.TargetDatabase != "" && db.TargetDatabase != "all" {
			mysqlArgs = append(mysqlArgs, db.TargetDatabase)
		}
		exitCode, err := spec.Exec.Run(ctx, Cmd{Exe: mysqlPath, Args: mysqlArgs, StdinPath: dumpFile}, logLine, logLine)
		if err != nil || exitCode != 0 {
			return fmt.Errorf("mysql restore failed (exit %d): %w", exitCode, err)
		}
	}

	// Verification: count tables
	verifyArgs := []string{
		"--defaults-extra-file=" + cnfFile,
		"-h", db.TargetHost, "-P", strconv.Itoa(db.TargetPort), "-u", db.TargetUsername,
		"-e", "SHOW TABLES",
	}
	if db.TargetDatabase != "" && db.TargetDatabase != "all" {
		verifyArgs = append(verifyArgs, db.TargetDatabase)
	}
	var tableCount int
	_, err = spec.Exec.Run(ctx, Cmd{Exe: mysqlPath, Args: verifyArgs},
		func(line string) {
			if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "Tables_in") {
				tableCount++
			}
		}, logLine)
	if err != nil {
		spec.Logf("warn", "mysql verification query failed: %v", err)
	} else {
		spec.Logf("info", "mysql verification: %d tables", tableCount)
	}
	return nil
}
