// Package backup implements plan-kind adapters.
package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteAdapter implements Adapter for SQLite plans.
type SQLiteAdapter struct{}

// Validate checks that the path exists and is absolute.
func (a *SQLiteAdapter) Validate(ctx context.Context, spec PlanSpec) error {
	if spec.Kind != KindSQLite {
		return fmt.Errorf("invalid kind: %s", spec.Kind)
	}
	s := spec.Source
	if s.Path == "" {
		return errors.New("path is required")
	}
	if !isAbs(s.Path) {
		return fmt.Errorf("path %q must be absolute", s.Path)
	}
	if _, err := os.Stat(s.Path); err != nil {
		return fmt.Errorf("path %q not accessible: %w", s.Path, err)
	}
	if err := ValidateExtraArgs(KindSQLite, s.ExtraArgs); err != nil {
		return err
	}
	return nil
}

// Backup uses SQLite's online VACUUM INTO primitive. Keeping the operation in
// the Go driver avoids shell quoting and produces a consistent snapshot while
// the source database is serving traffic.
func (a *SQLiteAdapter) Backup(ctx context.Context, rc *RunContext) (*BackupArtifact, error) {
	source := rc.Task.Source
	stagingDir := filepath.Join(rc.TempDir, "staging")
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir staging: %w", err)
	}

	backupFile := filepath.Join(stagingDir, fmt.Sprintf("%s.sqlite", rc.Task.PlanID))

	if err := sqliteVacuumInto(ctx, source.Path, backupFile); err != nil {
		return nil, fmt.Errorf("sqlite online backup failed: %w", err)
	}
	if err := sqliteIntegrityCheck(ctx, backupFile); err != nil {
		return nil, fmt.Errorf("sqlite integrity_check failed: %w", err)
	}

	toolVersions := make(map[string]string)
	// Preserve the probed CLI version for diagnostics when available; the
	// backup itself does not invoke the CLI.
	if sqlite3Path := toolPath("sqlite3"); sqlite3Path != "" {
		toolVersions["sqlite3"] = getToolVersion(ctx, rc.Exec, sqlite3Path, nil)
	}

	manifest := &Manifest{
		Adapter:      KindSQLite,
		ToolVersions: toolVersions,
		Databases:    []DbExport{{Database: filepath.Base(source.Path), File: filepath.Base(backupFile), Format: "sqlite"}},
		StartedAt:    time.Now().UTC(),
		FinishedAt:   time.Now().UTC(),
		RestoreHints: map[string]string{"source_path": source.Path},
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

// Restore restores SQLite database to target path.
func (a *SQLiteAdapter) Restore(ctx context.Context, spec *RestoreSpec) error {
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
	if manifest.Adapter != KindSQLite {
		return fmt.Errorf("manifest adapter mismatch: %s", manifest.Adapter)
	}

	// Find the sqlite backup file
	sqliteFile := ""
	for _, dbe := range manifest.Databases {
		if dbe.Format == "sqlite" {
			sqliteFile = filepath.Join(stagingDir, dbe.File)
			break
		}
	}
	if sqliteFile == "" {
		entries, _ := os.ReadDir(stagingDir)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".sqlite") {
				sqliteFile = filepath.Join(stagingDir, e.Name())
				break
			}
		}
	}
	if sqliteFile == "" {
		return errors.New("no sqlite file found in staging dir")
	}

	targetPath := db.TargetDatabase
	if targetPath == "" {
		return errors.New("target_database (path) is required for sqlite restore")
	}

	if !db.ReplaceExisting {
		if _, statErr := os.Stat(targetPath); statErr == nil {
			return errors.New("sqlite restore target exists and replace_existing=false")
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("stat sqlite restore target: %w", statErr)
		}
	}
	// Copy into the target directory, fsync, then atomically rename. This
	// avoids leaving a half-written SQLite database after interruption.
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
		return fmt.Errorf("create sqlite target directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(targetPath), ".bmc-restore-*.sqlite")
	if err != nil {
		return fmt.Errorf("create sqlite restore temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod sqlite restore temp: %w", err)
	}
	srcFile, err := os.Open(sqliteFile)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("open sqlite backup: %w", err)
	}
	_, copyErr := io.Copy(tmp, srcFile)
	_ = srcFile.Close()
	if copyErr != nil {
		_ = tmp.Close()
		return fmt.Errorf("copy sqlite file: %w", copyErr)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync sqlite restore temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close sqlite restore temp: %w", err)
	}
	if err := os.Rename(tmpName, targetPath); err != nil {
		return fmt.Errorf("replace sqlite target: %w", err)
	}

	if err := sqliteIntegrityCheck(ctx, targetPath); err != nil {
		return fmt.Errorf("sqlite restore integrity_check failed: %w", err)
	}
	return nil
}

func sqliteVacuumInto(ctx context.Context, sourcePath, backupPath string) error {
	db, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", backupPath); err != nil {
		return err
	}
	return nil
}

func sqliteIntegrityCheck(ctx context.Context, databasePath string) error {
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return err
	}
	defer db.Close()
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	result = strings.TrimSpace(result)
	if !strings.EqualFold(result, "ok") {
		return fmt.Errorf("%s", result)
	}
	return nil
}
