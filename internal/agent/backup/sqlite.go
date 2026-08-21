// Package backup implements plan-kind adapters.
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	return nil
}

// Backup runs sqlite3 .backup then integrity_check.
func (a *SQLiteAdapter) Backup(ctx context.Context, rc *RunContext) (*BackupArtifact, error) {
	source := rc.Task.Source
	stagingDir := filepath.Join(rc.TempDir, "staging")
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir staging: %w", err)
	}

	sqlite3Path := toolPath("sqlite3")
	logLine := func(l string) { rc.Logf("info", "%s", l) }

	backupFile := filepath.Join(stagingDir, fmt.Sprintf("%s.sqlite", rc.Task.PlanID))

	// Step 1: .backup
	backupCmd := fmt.Sprintf(".backup '%s'", backupFile)
	exitCode, err := rc.Exec.Run(ctx, Cmd{Exe: sqlite3Path, Args: []string{source.Path, backupCmd}}, logLine, logLine)
	if err != nil || exitCode != 0 {
		return nil, fmt.Errorf("sqlite3 backup failed (exit %d): %w", exitCode, err)
	}

	// Step 2: integrity_check on the copy
	var integrityOutput string
	exitCode, err = rc.Exec.Run(ctx, Cmd{Exe: sqlite3Path, Args: []string{backupFile, "PRAGMA integrity_check;"}},
		func(line string) {
			integrityOutput = strings.TrimSpace(line)
		}, logLine)
	if err != nil || exitCode != 0 {
		return nil, fmt.Errorf("sqlite3 integrity_check failed (exit %d): %w", exitCode, err)
	}
	if !strings.EqualFold(integrityOutput, "ok") {
		return nil, fmt.Errorf("sqlite3 integrity_check failed: %s", integrityOutput)
	}

	toolVersions := make(map[string]string)
	toolVersions["sqlite3"] = getToolVersion(ctx, rc.Exec, sqlite3Path, nil)

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

	sqlite3Path := toolPath("sqlite3")
	logLine := func(l string) { spec.Logf("info", "%s", l) }

	// Copy the backup file to the target path
	exitCode, err := spec.Exec.Run(ctx, Cmd{Exe: "sh", Args: []string{"-c", fmt.Sprintf("cp '%s' '%s'", sqliteFile, targetPath)}}, logLine, logLine)
	if err != nil || exitCode != 0 {
		return fmt.Errorf("copy sqlite file failed (exit %d): %w", exitCode, err)
	}

	// Verify integrity
	var integrityOutput string
	exitCode, err = spec.Exec.Run(ctx, Cmd{Exe: sqlite3Path, Args: []string{targetPath, "PRAGMA integrity_check;"}},
		func(line string) {
			integrityOutput = strings.TrimSpace(line)
		}, logLine)
	if err != nil || exitCode != 0 {
		return fmt.Errorf("sqlite restore integrity_check failed (exit %d): %w", exitCode, err)
	}
	if !strings.EqualFold(integrityOutput, "ok") {
		return fmt.Errorf("sqlite restore integrity_check failed: %s", integrityOutput)
	}
	return nil
}