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
	pgpassContent := fmt.Sprintf("%s:%d:*:%s:%s\n", source.Host, source.Port, source.Username, rc.Secrets.DBPassword)
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
		args := []string{"--globals-only", "--file=" + globalsFile}
		args = append(args, source.ExtraArgs...)
		exitCode, err := rc.Exec.Run(ctx, Cmd{Exe: toolPath("pg_dumpall"), Args: args, Env: env}, logLine, logLine)
		if err != nil || exitCode != 0 {
			return nil, fmt.Errorf("pg_dumpall globals failed (exit %d): %w", exitCode, err)
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
			dumpFile := filepath.Join(stagingDir, fmt.Sprintf("db-%s.pgdump", db))
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
				return nil, fmt.Errorf("pg_dump %s failed (exit %d): %w", db, exitCode, err)
			}
			dbExports = append(dbExports, DbExport{Database: db, File: fmt.Sprintf("db-%s.pgdump", db), Format: "pgdump"})
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
			return nil, fmt.Errorf("pg_dump failed (exit %d): %w", exitCode, err)
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

// Restore imports restored snapshot data into target PostgreSQL.
func (a *PostgreSQLAdapter) Restore(ctx context.Context, spec *RestoreSpec) error {
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
	if manifest.Adapter != KindPostgreSQL {
		return fmt.Errorf("manifest adapter mismatch: %s", manifest.Adapter)
	}

	// Write PGPASSFILE for target in stagingDir (the only writable temp dir we have)
	pgpassContent := fmt.Sprintf("%s:%d:*:%s:%s\n", db.TargetHost, db.TargetPort, db.TargetUsername, spec.Secrets.DBPassword)
	pgpassFile, err := writeSecretFile(stagingDir, "pgpass_restore", pgpassContent)
	if err != nil {
		return fmt.Errorf("write pgpass: %w", err)
	}
	env := []string{"PGPASSFILE=" + pgpassFile}

	pgRestorePath := toolPath("pg_restore")
	psqlPath := toolPath("psql")
	logLine := func(l string) { spec.Logf("info", "%s", l) }

	if db.TargetDatabase == "all" {
		globalsFile := filepath.Join(stagingDir, "globals.sql")
		if _, err := os.Stat(globalsFile); err == nil {
			args := []string{"-h", db.TargetHost, "-p", strconv.Itoa(db.TargetPort), "-U", db.TargetUsername, "-d", "postgres", "-f", globalsFile}
			exitCode, err := spec.Exec.Run(ctx, Cmd{Exe: psqlPath, Args: args, Env: env}, logLine, logLine)
			if err != nil || exitCode != 0 {
				return fmt.Errorf("restore globals failed (exit %d): %w", exitCode, err)
			}
		}
		for _, dbe := range manifest.Databases {
			if dbe.Database == "globals" {
				continue
			}
			dumpFile := filepath.Join(stagingDir, dbe.File)
			args := []string{
				"--exit-on-error", "--clean", "--if-exists", "--no-owner",
				"--dbname=" + dbe.Database,
				"-h", db.TargetHost, "-p", strconv.Itoa(db.TargetPort), "-U", db.TargetUsername,
				dumpFile,
			}
			exitCode, err := spec.Exec.Run(ctx, Cmd{Exe: pgRestorePath, Args: args, Env: env}, logLine, logLine)
			if err != nil || exitCode != 0 {
				return fmt.Errorf("pg_restore %s failed (exit %d): %w", dbe.Database, exitCode, err)
			}
		}
	} else {
		for _, dbe := range manifest.Databases {
			if dbe.Database != "globals" {
				dumpFile := filepath.Join(stagingDir, dbe.File)
				args := []string{
					"--exit-on-error", "--clean", "--if-exists", "--no-owner",
					"--dbname=" + db.TargetDatabase,
					"-h", db.TargetHost, "-p", strconv.Itoa(db.TargetPort), "-U", db.TargetUsername,
					dumpFile,
				}
				exitCode, err := spec.Exec.Run(ctx, Cmd{Exe: pgRestorePath, Args: args, Env: env}, logLine, logLine)
				if err != nil || exitCode != 0 {
					return fmt.Errorf("pg_restore failed (exit %d): %w", exitCode, err)
				}
				break
			}
		}
	}

	// Verification: count user schemas
	verifyArgs := []string{"-h", db.TargetHost, "-p", strconv.Itoa(db.TargetPort), "-U", db.TargetUsername, "-d", db.TargetDatabase, "-t", "-c", "SELECT count(*) FROM pg_namespace WHERE nspname NOT IN ('pg_catalog','information_schema')"}
	var countStr string
	_, err = spec.Exec.Run(ctx, Cmd{Exe: psqlPath, Args: verifyArgs, Env: env},
		func(line string) { countStr = strings.TrimSpace(line) }, logLine)
	if err != nil {
		spec.Logf("warn", "postgresql verification query failed: %v", err)
	} else {
		spec.Logf("info", "postgresql verification: %s user schemas", countStr)
	}
	return nil
}