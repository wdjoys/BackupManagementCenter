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
	// mongodump 限制：--oplog 仅支持整实例 dump，配单库时必然以
	// "bad option: --oplog mode only supported on full dumps" 失败，故在此拒绝。
	if s.CaptureOplog && !strings.EqualFold(s.Database, "all") {
		return errors.New("capture_oplog requires a full instance dump (database must be \"all\")")
	}
	if s.EstimatedDumpBytes <= 0 {
		return errors.New("estimated_dump_bytes must be > 0")
	}
	if err := model.ValidateExtraArgs(model.KindMongoDB, s.ExtraArgs); err != nil {
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
	configContent := buildMongoConfig(source.Host, source.Port, source.Username, rc.Secrets.DBPassword, source.AuthSource)
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
		return nil, exitError("mongodump", exitCode, err)
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
	// admin 存放用户与角色，local/config 存放副本集与分片元数据：恢复进去会
	// 覆盖它们。此前 mongodb 侧没有这层守卫，仅靠目标已存在时的 pre-check 兜底。
	if model.IsSystemDatabase(model.KindMongoDB, db.TargetDatabase) {
		return nil, fmt.Errorf("refusing to restore into system database %q", db.TargetDatabase)
	}
	authSource := db.TargetAuthSource
	if authSource == "" {
		authSource = "admin"
	}
	configFile, err := WriteSecretFile(spec.StagingDir, "mongo-restore.yml",
		buildMongoConfig(db.TargetHost, db.TargetPort, db.TargetUsername, spec.Secrets.DBPassword, authSource))
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
		return exitError("mongosh query", exit, err)
	}
	return nil
}

// uri 构造带凭据的 mongodb URI（仅写入 0600 脚本文件，不进入 argv）。
func (c *mongoCtx) uri() string {
	authSource := c.db.TargetAuthSource
	if authSource == "" {
		authSource = "admin"
	}
	return mongoURI(c.db.TargetHost, c.db.TargetPort, c.db.TargetUsername, c.password, authSource)
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
	js := mongoTargetExistsScript(c.db.TargetDatabase)
	if err := c.runJS(ctx, spec, js, func(line string) { found = line }); err != nil {
		return false, err
	}
	return found == c.db.TargetDatabase, nil
}

// mongoTargetExistsScript 生成用于判断目标库是否存在的 JS。
// adminCommand 挂在 Database 对象上；Mongo(uri) 返回的连接对象没有该方法，
// 直接调用会抛 "conn.adminCommand is not a function"。
func mongoTargetExistsScript(database string) string {
	return fmt.Sprintf(
		"const r = db.adminCommand({listDatabases:1, filter:{name:%s}});\n"+
			"r.databases.forEach(function(d){print(d.name);});", strconv.Quote(database))
}

// mongoCollectionNamesScript 列出当前库的集合名。
func mongoCollectionNamesScript() string {
	return "db.getCollectionNames().forEach(function(n){print(n);});"
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
		// 覆盖语义要求“完整替换目标内容”。mongorestore --drop 只会重建归档里
		// 存在的集合，归档之外的集合会原样残留（实测：目标 pre-existing 的集合
		// 在覆盖恢复后仍在），因此先整库删除——与 MySQL/PostgreSQL 适配器的
		// DROP DATABASE 等价。
		if err := c.runJS(ctx, spec, "db.dropDatabase();", nil); err != nil {
			return fmt.Errorf("drop mongodb target database before overwrite: %w", err)
		}
		args = append(args, "--drop")
	}
	if spec.ArtifactDatabase != "" && spec.ArtifactDatabase != c.db.TargetDatabase {
		args = append(args,
			"--nsFrom="+spec.ArtifactDatabase+".*",
			"--nsTo="+c.db.TargetDatabase+".*")
	}
	exit, err := spec.Exec.Run(ctx, Cmd{Exe: toolPath("mongorestore"), Args: args}, c.logf, c.logf)
	if err != nil || exit != 0 {
		return exitError("mongorestore", exit, err)
	}
	return nil
}

// VerifyRestored lists the namespaces the archive would restore and requires
// every one of them to exist in the target.
//
// 解析失败必须中止（fail-closed）：把“解析不出来”当成“归档为空”会让一次
// 完全没落地的导入静默通过验证。
func (a *MongoDBAdapter) VerifyRestored(ctx context.Context, spec *RestoreSpec) error {
	c, err := mongoPrepare(spec)
	if err != nil {
		return err
	}
	namespaces := map[string]map[string]struct{}{}
	var out strings.Builder
	// mongorestore 把 "found collection ..." 与 "archive prelude ..." 全部写到
	// stderr，只收集 stdout 会得到空输出并把每次恢复都判成失败。
	collect := func(line string) { out.WriteString(line); out.WriteString("\n") }
	exit, err := spec.Exec.Run(ctx, Cmd{Exe: toolPath("mongorestore"),
		Args: []string{"--archive=" + spec.ArtifactFile, "--gzip", "--config=" + c.config, "--dryRun", "--verbose"}},
		collect, func(line string) { collect(line); c.logf(line) })
	if err != nil || exit != 0 {
		return exitError("mongorestore dry run", exit, err)
	}
	parsed := mongoDumpNamespaces(out.String())
	if len(parsed) == 0 {
		return errors.New("mongodb restore verification failed: could not read any namespace from the mongorestore --dryRun output")
	}
	for _, ns := range parsed {
		parts := strings.SplitN(ns, ".", 2)
		if len(parts) != 2 {
			continue
		}
		if namespaces[parts[0]] == nil {
			namespaces[parts[0]] = map[string]struct{}{}
		}
		namespaces[parts[0]][parts[1]] = struct{}{}
	}
	var missing []string
	for db, cols := range namespaces {
		present := map[string]struct{}{}
		_ = db
		// 目标库用 TargetDatabase：归档里的库名可能与目标名不同
		// （Import 用 --nsFrom/--nsTo 重命名）。
		js := mongoCollectionNamesScript()
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

// mongoDumpNamespaces 从 mongorestore --dryRun --verbose 输出里抽取“归档中的”
// 命名空间。
//
// archive 模式下当前工具（Database Tools 100.x）输出：
//
//	found collection `appdb.users` bson to restore to `appdb.users`
//	found collection metadata from `appdb.users` to restore to `appdb.users`
//
// 而旧版 / 目录模式下是 "reading metadata for appdb.users from …"。这里两种
// 都认，并优先取 `from` 一侧（`to` 一侧可能是 --nsFrom/--nsTo 重命名后的目标名）。
func mongoDumpNamespaces(out string) []string {
	seen := map[string]struct{}{}
	var namespaces []string
	add := func(ns string) {
		ns = strings.TrimSpace(strings.Trim(ns, "`"))
		if ns == "" || strings.ContainsAny(ns, " \t") {
			return
		}
		if _, ok := seen[ns]; ok {
			return
		}
		seen[ns] = struct{}{}
		namespaces = append(namespaces, ns)
	}
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if idx := strings.Index(trimmed, "reading metadata for "); idx >= 0 {
			rest := strings.TrimSpace(trimmed[idx+len("reading metadata for "):])
			if fields := strings.Fields(rest); len(fields) > 0 {
				// 目录模式为 "ns from path"；archive 模式无 "from"。
				add(strings.TrimSuffix(fields[0], "..."))
			}
			continue
		}
		if idx := strings.Index(trimmed, "found collection metadata from "); idx >= 0 {
			add(firstToken(trimmed[idx+len("found collection metadata from "):]))
			continue
		}
		if idx := strings.Index(trimmed, "archive prelude "); idx >= 0 {
			add(firstToken(trimmed[idx+len("archive prelude "):]))
		}
	}
	return namespaces
}

// firstToken 返回 s 中第一个空白分隔的片段。
func firstToken(s string) string {
	if fields := strings.Fields(s); len(fields) > 0 {
		return fields[0]
	}
	return ""
}

// mongoURI 构造带凭据的连接串。只会写进 0600 配置文件，绝不进入 argv。
func mongoURI(host string, port int, username, password, authSource string) string {
	if authSource == "" {
		authSource = "admin"
	}
	// 无凭据时不能省略 "@"：否则解析出空用户信息段。
	creds := ""
	if username != "" {
		creds = url.QueryEscape(username) + ":" + url.QueryEscape(password) + "@"
	}
	return fmt.Sprintf("mongodb://%s%s:%d/?authSource=%s",
		creds, host, port, url.QueryEscape(authSource))
}

// buildMongoConfig builds the YAML config for mongodump/mongorestore.
//
// Database Tools 的 --config 只认识 password / uri / sslPEMKeyPassword /
// destinationPassword 四个字段；host、port、username、authSource 写在扁平
// YAML 里会被拒绝（"field host not found in type struct {...}"）并直接退出。
// 因此连接信息统一编码进 uri。
func buildMongoConfig(host string, port int, username, password, authSource string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("uri: %s\n", strconv.Quote(mongoURI(host, port, username, password, authSource))))
	// uri 已含密码时这个字段是可选的，但显式写出可保证 uri 被改写或
	// 查询参数丢失时仍能认证。
	if password != "" {
		b.WriteString(fmt.Sprintf("password: %s\n", strconv.Quote(password)))
	}
	return b.String()
}
