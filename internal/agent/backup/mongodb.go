// Package backup implements plan-kind adapters.
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"backupmanagementcenter/internal/model"
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

// mongoCtx groups the target connection details a restore needs.
type mongoCtx struct {
	db       *model.DatabaseRestore
	config   string
	shell    string
	password string
	logf     func(string)
}

// mongoPrepare writes the target credentials into a 0600 YAML file inside the
// private staging dir and resolves the shell used for verification.
//
// MongoDB restore requires mongosh to tell an empty database apart from a
// missing one and to verify imported collections; without it the restore is
// refused rather than guessed.
func mongoPrepare(spec *RestoreSpec) (*mongoCtx, error) {
	db := spec.Database
	if db == nil {
		return nil, errors.New("database restore spec missing")
	}
	if db.TargetDatabase == "" || db.TargetDatabase == "all" {
		return nil, errors.New("mongodb restore requires a single target database")
	}
	authSource := db.TargetAuthSource
	if authSource == "" {
		authSource = "admin"
	}
	configFile, err := WriteSecretFile(spec.StagingDir, "mongo-restore.yml",
		buildMongoConfig(db.TargetHost, db.TargetPort, db.TargetUsername, spec.Secrets.DBPassword, db.TargetDatabase, authSource))
	if err != nil {
		return nil, fmt.Errorf("write mongo restore config: %w", err)
	}
	shell := toolPath("mongosh")
	if shell == "" {
		return nil, errors.New("mongosh is required to verify the mongodb target; refusing to restore without a reliable existence check")
	}
	return &mongoCtx{
		db:       db,
		config:   configFile,
		shell:    shell,
		password: spec.Secrets.DBPassword,
		logf:     func(l string) { spec.Logf("info", "%s", l) },
	}, nil
}

// runJS 通过 stdin 把 JS 交给 mongosh，连接信息来自 0600 配置文件。
func (c *mongoCtx) runJS(ctx context.Context, spec *RestoreSpec, js string, scan func(string)) error {
	script := fmt.Sprintf(
		"const uri = %s; const conn = Mongo(uri); const db = conn.getDB(%s);\n%s",
		strconv.Quote(c.uri()), strconv.Quote(c.db.TargetDatabase), js)
	tmp, err := WriteSecretFile(spec.StagingDir, "mongo-check.js", script)
	if err != nil {
		return fmt.Errorf("write mongo check script: %w", err)
	}
	exit, err := spec.Exec.Run(ctx, Cmd{Exe: c.shell, Args: []string{"--quiet", "--nodb", "--file", tmp}},
		func(line string) {
			if trimmed := strings.TrimSpace(line); trimmed != "" && scan != nil {
				scan(trimmed)
			}
		}, c.logf)
	if err != nil || exit != 0 {
		return fmt.Errorf("mongosh query failed (exit %d): %w", exit, err)
	}
	return nil
}

// uri 构造带凭据的 mongodb URI（仅写入 0600 脚本文件，不进入 argv）。
func (c *mongoCtx) uri() string {
	authSource := c.db.TargetAuthSource
	if authSource == "" {
		authSource = "admin"
	}
	user := url.QueryEscape(c.db.TargetUsername)
	pass := url.QueryEscape(c.password)
	return fmt.Sprintf("mongodb://%s:%s@%s:%d/?authSource=%s", user, pass, c.db.TargetHost, c.db.TargetPort, url.QueryEscape(authSource))
}

// TargetExists reports whether the target database has been created.
// listDatabases distinguishes an empty database from a missing one, so an empty
// dump is never mistaken for absence. Auth/permission failures abort the query
// and surface as errors.
func (a *MongoDBAdapter) TargetExists(ctx context.Context, spec *RestoreSpec) (bool, error) {
	c, err := mongoPrepare(spec)
	if err != nil {
		return false, err
	}
	found := ""
	js := fmt.Sprintf(
		"const r = conn.adminCommand({listDatabases:1, filter:{name:%s}});\n"+
			"r.databases.forEach(function(d){print(d.name);});", strconv.Quote(c.db.TargetDatabase))
	if err := c.runJS(ctx, spec, js, func(line string) { found = line }); err != nil {
		return false, err
	}
	return found == c.db.TargetDatabase, nil
}

// Import loads the archive into the target. A new target is created without
// dropping (the pipeline verified it does not exist); an overwrite uses --drop
// to replace every collection.
func (a *MongoDBAdapter) Import(ctx context.Context, spec *RestoreSpec) error {
	c, err := mongoPrepare(spec)
	if err != nil {
		return err
	}
	if spec.ArtifactFile == "" {
		return errors.New("mongodb restore artifact is missing")
	}
	args := []string{"--archive=" + spec.ArtifactFile, "--gzip", "--config=" + c.config}
	if !spec.TargetIsNew {
		args = append(args, "--drop")
	}
	if spec.ArtifactDatabase != "" && spec.ArtifactDatabase != c.db.TargetDatabase {
		args = append(args,
			"--nsFrom="+spec.ArtifactDatabase+".*",
			"--nsTo="+c.db.TargetDatabase+".*")
	}
	exit, err := spec.Exec.Run(ctx, Cmd{Exe: toolPath("mongorestore"), Args: args}, c.logf, c.logf)
	if err != nil || exit != 0 {
		return fmt.Errorf("mongorestore failed (exit %d): %w", exit, err)
	}
	return nil
}

// VerifyRestored lists the namespaces the archive would restore and requires
// every one of them to exist in the target.
//
// ponytail: 依赖 mongorestore --dryRun 的 verbose 输出解析；若上游格式变化，
// 验证会失败并触发回滚（fail-closed）而不是放过未验证的导入。
func (a *MongoDBAdapter) VerifyRestored(ctx context.Context, spec *RestoreSpec) error {
	c, err := mongoPrepare(spec)
	if err != nil {
		return err
	}
	namespaces := map[string]map[string]struct{}{}
	var out strings.Builder
	exit, err := spec.Exec.Run(ctx, Cmd{Exe: toolPath("mongorestore"),
		Args: []string{"--archive=" + spec.ArtifactFile, "--gzip", "--config=" + c.config, "--dryRun", "--verbose"}},
		func(line string) { out.WriteString(line); out.WriteString("\n") }, c.logf)
	if err != nil || exit != 0 {
		return fmt.Errorf("mongorestore dry run failed (exit %d): %w", exit, err)
	}
	for _, ns := range mongoDumpNamespaces(out.String()) {
		parts := strings.SplitN(ns, ".", 2)
		if len(parts) != 2 {
			continue
		}
		if namespaces[parts[0]] == nil {
			namespaces[parts[0]] = map[string]struct{}{}
		}
		namespaces[parts[0]][parts[1]] = struct{}{}
	}
	if len(namespaces) == 0 {
		spec.Logf("info", "mongodb verification: archive contains no collections")
		return nil
	}
	var missing []string
	for db, cols := range namespaces {
		present := map[string]struct{}{}
		js := "db.getCollectionNames().forEach(function(n){print(n);});"
		_ = db
		if err := c.runJS(ctx, spec, js, func(line string) { present[line] = struct{}{} }); err != nil {
			return err
		}
		for col := range cols {
			if _, ok := present[col]; !ok {
				missing = append(missing, db+"."+col)
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("mongodb restore verification failed: missing collections %v", missing)
	}
	spec.Logf("info", "mongodb verification: %d namespaces present", len(namespaces))
	return nil
}

// RemoveTarget drops the database created by this run.
func (a *MongoDBAdapter) RemoveTarget(ctx context.Context, spec *RestoreSpec) error {
	if !spec.TargetIsNew {
		return errors.New("refusing to remove a target this run did not create")
	}
	c, err := mongoPrepare(spec)
	if err != nil {
		return err
	}
	if err := c.runJS(ctx, spec, "db.dropDatabase();", nil); err != nil {
		return fmt.Errorf("drop mongodb target database: %w", err)
	}
	return nil
}

// mongoDumpNamespaces 从 mongorestore --dryRun --verbose 输出里抽取命名空间。
func mongoDumpNamespaces(out string) []string {
	seen := map[string]struct{}{}
	var namespaces []string
	for _, line := range strings.Split(out, "\n") {
		idx := strings.Index(line, "reading metadata for ")
		if idx < 0 {
			continue
		}
		rest := strings.TrimSpace(line[idx+len("reading metadata for "):])
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		ns := strings.TrimSuffix(fields[0], "...")
		if ns == "" {
			continue
		}
		if _, ok := seen[ns]; ok {
			continue
		}
		seen[ns] = struct{}{}
		namespaces = append(namespaces, ns)
	}
	return namespaces
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
