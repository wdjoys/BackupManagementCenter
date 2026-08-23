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

// MongoDBAdapter implements Adapter for MongoDB plans.
type MongoDBAdapter struct{}

// Validate checks that required tools are present and source spec is sane.
func (a *MongoDBAdapter) Validate(ctx context.Context, spec PlanSpec) error {
	if spec.Kind != KindMongoDB {
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
	if err := ValidateExtraArgs(KindMongoDB, s.ExtraArgs); err != nil {
		return err
	}
	return nil
}

// Backup runs mongodump, writes manifest, returns BackupArtifact with staging dir.
func (a *MongoDBAdapter) Backup(ctx context.Context, rc *RunContext) (*BackupArtifact, error) {
	source := rc.Task.Source
	stagingDir := filepath.Join(rc.TempDir, "staging")
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir staging: %w", err)
	}

	// Write mongodb config YAML (0600)
	configContent := buildMongoConfig(source.Host, source.Port, source.Username, rc.Secrets.DBPassword, source.Database, source.AuthSource)
	configFile, err := writeSecretFile(rc.TempDir, "mongo.yml", configContent)
	if err != nil {
		return nil, fmt.Errorf("write mongo config: %w", err)
	}

	toolVersions := make(map[string]string)
	mongodumpPath := toolPath("mongodump")

	archiveFile := filepath.Join(stagingDir, fmt.Sprintf("%s.archive", rc.Task.PlanID))
	args := []string{
		"--archive=" + archiveFile, "--gzip",
		"--config=" + configFile,
	}
	if source.Database != "all" {
		args = append(args, "--db="+source.Database)
	}
	if source.CaptureOplog {
		args = append(args, "--oplog")
	}
	args = append(args, source.ExtraArgs...)

	logLine := func(l string) { rc.Logf("info", "%s", l) }
	exitCode, err := rc.Exec.Run(ctx, Cmd{Exe: mongodumpPath, Args: args, Env: nil}, logLine, logLine)
	if err != nil || exitCode != 0 {
		return nil, fmt.Errorf("mongodump failed (exit %d): %w", exitCode, err)
	}
	toolVersions["mongodump"] = getToolVersion(ctx, rc.Exec, mongodumpPath, nil)

	manifest := &Manifest{
		Adapter:      KindMongoDB,
		ToolVersions: toolVersions,
		Databases:    []DbExport{{Database: source.Database, File: filepath.Base(archiveFile), Format: "archive"}},
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

// Restore imports restored snapshot data into target MongoDB.
func (a *MongoDBAdapter) Restore(ctx context.Context, spec *RestoreSpec) error {
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
	if manifest.Adapter != KindMongoDB {
		return fmt.Errorf("manifest adapter mismatch: %s", manifest.Adapter)
	}
	if len(manifest.Databases) > 0 && manifest.Databases[0].Database == "all" && db.TargetDatabase != "all" {
		return errors.New("all-databases MongoDB snapshot requires target_database=all")
	}

	// Find the archive file
	archiveFile := ""
	for _, dbe := range manifest.Databases {
		if dbe.Format == "archive" {
			archiveFile = filepath.Join(stagingDir, dbe.File)
			break
		}
	}
	if archiveFile == "" {
		entries, _ := os.ReadDir(stagingDir)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".archive") {
				archiveFile = filepath.Join(stagingDir, e.Name())
				break
			}
		}
	}
	if archiveFile == "" {
		return errors.New("no archive file found in staging dir")
	}

	// mongorestore must use the restore target, not the source hints embedded
	// in the manifest. Keep credentials in a 0600 config file and never put
	// them in argv or a shell command.
	authSource := db.TargetAuthSource
	if authSource == "" {
		authSource = "admin"
	}
	configFile, err := WriteSecretFile(stagingDir, "mongo-restore.yml",
		buildMongoConfig(db.TargetHost, db.TargetPort, db.TargetUsername, spec.Secrets.DBPassword, db.TargetDatabase, authSource))
	if err != nil {
		return fmt.Errorf("write mongo restore config: %w", err)
	}

	mongorestorePath := toolPath("mongorestore")
	logLine := func(l string) { spec.Logf("info", "%s", l) }

	args := []string{
		"--archive=" + archiveFile, "--gzip", "--config=" + configFile,
	}
	if db.ReplaceExisting {
		args = append(args, "--drop")
	}
	if db.TargetDatabase != "" && db.TargetDatabase != "all" && len(manifest.Databases) > 0 && manifest.Databases[0].Database != db.TargetDatabase {
		args = append(args,
			"--nsFrom="+manifest.Databases[0].Database+".*",
			"--nsTo="+db.TargetDatabase+".*")
	}

	exitCode, err := spec.Exec.Run(ctx, Cmd{Exe: mongorestorePath, Args: args, Env: nil}, logLine, logLine)
	if err != nil || exitCode != 0 {
		return fmt.Errorf("mongorestore failed (exit %d): %w", exitCode, err)
	}

	spec.Logf("info", "mongodb restore completed; verification skipped (no mongosh)")
	return nil
}

// buildMongoConfig builds the YAML config for mongodump/mongorestore.
func buildMongoConfig(host string, port int, username, password, database, authSource string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("host: %s\n", strconv.Quote(host)))
	b.WriteString(fmt.Sprintf("port: %d\n", port))
	if username != "" {
		b.WriteString(fmt.Sprintf("username: %s\n", strconv.Quote(username)))
	}
	if password != "" {
		b.WriteString(fmt.Sprintf("password: %s\n", strconv.Quote(password)))
	}
	if authSource == "" {
		authSource = "admin"
	}
	b.WriteString(fmt.Sprintf("authSource: %s\n", strconv.Quote(authSource)))
	return b.String()
}
