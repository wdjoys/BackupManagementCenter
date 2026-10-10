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

	"backupmanagementcenter/internal/model"
	"sort"
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
	if err := model.ValidateExtraArgs(model.KindSQLite, s.ExtraArgs); err != nil {
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
		// 只读挂载下 SQLite 无法创建/打开 -shm 来索引 WAL：源库是 WAL 模式且
		// -shm 缺失（例如所在主机被 kill -9 后未重建）时，VACUUM INTO 直接报
		// "unable to open database file (14)"，与数据本身无关。
		// 退一步：把 db 与 -wal/-shm 侧车文件复制到可写的临时目录（三者合起来
		// 才是一份有效的 SQLite 状态），再对副本做一致性导出。副本若被并发写
		// 撕裂，紧随其后的 integrity_check 会拒绝它，不会静默产出坏备份。
		//
		// 副本必须放在 staging 之外：staging 目录会被整体上传进快照，放里面会
		// 把源库原始文件（含 -wal/-shm）一并打包，白白撑大快照。
		copyDir := filepath.Join(rc.TempDir, "source-copy")
		if mkErr := os.MkdirAll(copyDir, 0o700); mkErr != nil {
			return nil, fmt.Errorf("sqlite online backup failed: %w", err)
		}
		copied, copyErr := copySQLiteForBackup(source.Path, copyDir)
		if copyErr != nil {
			return nil, fmt.Errorf("sqlite online backup failed: %w", err)
		}
		if retryErr := sqliteVacuumInto(ctx, copied, backupFile); retryErr != nil {
			return nil, fmt.Errorf("sqlite online backup failed (retry against a writable copy also failed: %v): %w", retryErr, err)
		}
		rc.Logf("warn", "源库 %s 无法直接打开（挂载只读或 -shm 缺失），已复制主库与 WAL 侧车文件后再做一致性导出", source.Path)
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

// TargetExists reports whether the target database file exists. A stat failure
// other than "not exist" (permission, I/O) is returned as an error so the
// pipeline never mistakes an unreachable file for a new target.
func (a *SQLiteAdapter) TargetExists(_ context.Context, spec *RestoreSpec) (bool, error) {
	path, err := sqliteTargetPath(spec)
	if err != nil {
		return false, err
	}
	_, statErr := os.Stat(path)
	if statErr == nil {
		return true, nil
	}
	if errors.Is(statErr, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("stat sqlite target: %w", statErr)
}

// Import replaces the target database file with the staged snapshot copy.
//
// SQLite requires an exclusive maintenance window: the operator must stop the
// application so every connection is closed and the WAL is checkpointed before
// the main file is replaced. A live -wal/-shm pair is never deleted or reused;
// if the writer is still active the import fails before touching the target.
// PreflightRestore 在任何写入之前确认目标处于可恢复状态：目标被其它连接占用时
// 在这里就失败，运行按安全失败落地，不再走回滚（回滚本身也会因目标占用失败，
// 进而升级为 rollback_failed 并阻塞全局数据库恢复）。
func (a *SQLiteAdapter) PreflightRestore(ctx context.Context, spec *RestoreSpec) error {
	if spec == nil || spec.Database == nil || spec.Database.TargetDatabase == "" {
		return errors.New("target_database (path) is required for sqlite restore")
	}
	return sqliteCheckMaintenanceWindow(ctx, spec.Database.TargetDatabase)
}

func (a *SQLiteAdapter) Import(ctx context.Context, spec *RestoreSpec) error {
	db := spec.Database
	if db == nil {
		return errors.New("database restore spec missing")
	}
	sqliteFile := spec.ArtifactFile
	if sqliteFile == "" {
		return errors.New("sqlite restore artifact is missing")
	}
	targetPath := db.TargetDatabase
	if targetPath == "" {
		return errors.New("target_database (path) is required for sqlite restore")
	}
	if err := sqliteCheckMaintenanceWindow(ctx, targetPath); err != nil {
		return err
	}
	if !spec.TargetIsNew && !db.ReplaceExisting {
		return errors.New("sqlite restore target exists and replace_existing=false")
	}
	if spec.TargetIsNew {
		// 新建必须保留“已存在”冲突：并发执行者创建的同名库不能被覆盖。
		if _, statErr := os.Stat(targetPath); statErr == nil {
			return errors.New("sqlite restore target already exists; refusing to overwrite another actor's database")
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("stat sqlite restore target: %w", statErr)
		}
	}
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
	// 替换主文件前清掉目标遗留的侧车文件：旧 WAL 与新的主文件不属于同一数据库。
	if err := sqliteRemoveSidecars(targetPath); err != nil {
		return err
	}
	if err := os.Rename(tmpName, targetPath); err != nil {
		return fmt.Errorf("replace sqlite target: %w", err)
	}
	return nil
}

// VerifyRestored proves the restored database is structurally sound and carries
// the snapshot's schema identity: same object names, not just "some content".
func (a *SQLiteAdapter) VerifyRestored(ctx context.Context, spec *RestoreSpec) error {
	path, err := sqliteTargetPath(spec)
	if err != nil {
		return err
	}
	if err := sqliteIntegrityCheck(ctx, path); err != nil {
		return fmt.Errorf("sqlite integrity_check failed: %w", err)
	}
	if spec.ArtifactFile == "" {
		return errors.New("sqlite restore artifact is missing")
	}
	want, err := sqliteSchemaObjects(ctx, spec.ArtifactFile)
	if err != nil {
		return fmt.Errorf("read snapshot sqlite schema: %w", err)
	}
	got, err := sqliteSchemaObjects(ctx, path)
	if err != nil {
		return fmt.Errorf("read restored sqlite schema: %w", err)
	}
	missing := make([]string, 0)
	for name := range want {
		if _, ok := got[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("restored sqlite database is missing snapshot objects %v", missing)
	}
	return nil
}

// RemoveTarget deletes the database file created by this run, including its
// sidecars. It only applies to targets this run created.
func (a *SQLiteAdapter) RemoveTarget(_ context.Context, spec *RestoreSpec) error {
	if !spec.TargetIsNew {
		return errors.New("refusing to remove a target this run did not create")
	}
	path, err := sqliteTargetPath(spec)
	if err != nil {
		return err
	}
	if err := sqliteRemoveSidecars(path); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove sqlite target: %w", err)
	}
	return nil
}

// sqliteTargetPath resolves the absolute target path of a sqlite restore.
func sqliteTargetPath(spec *RestoreSpec) (string, error) {
	if spec.Database == nil || spec.Database.TargetDatabase == "" {
		return "", errors.New("target_database (path) is required for sqlite restore")
	}
	return spec.Database.TargetDatabase, nil
}

// sqliteCheckMaintenanceWindow rejects the import unless the target has no live
// WAL sidecars or open writers. It deliberately does not checkpoint for the
// operator: the documented procedure is to stop the application first.
func sqliteCheckMaintenanceWindow(ctx context.Context, targetPath string) error {
	if _, err := os.Stat(targetPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat sqlite target: %w", err)
	}
	if err := sqliteIntegrityCheck(ctx, targetPath); err != nil {
		return fmt.Errorf("target database is not in a clean maintenance state (integrity check failed); close all connections, checkpoint and retry: %w", err)
	}
	// 尝试取得独占锁；活动写入/连接会立即失败。
	db, err := sql.Open("sqlite", targetPath)
	if err != nil {
		return fmt.Errorf("open target for exclusive check: %w", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "PRAGMA locking_mode=EXCLUSIVE"); err != nil {
		return fmt.Errorf("target database is busy; close all target connections and retry: %w", err)
	}
	if _, err := db.ExecContext(ctx, "BEGIN IMMEDIATE; COMMIT;"); err != nil {
		return fmt.Errorf("target database is busy; close all target connections and retry: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("checkpoint target WAL: %w", err)
	}
	return nil
}

// sqliteRemoveSidecars deletes -wal/-shm files. Only valid inside an established
// exclusive maintenance window.
func sqliteRemoveSidecars(targetPath string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if err := os.Remove(targetPath + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove sqlite sidecar %s: %w", suffix, err)
		}
	}
	return nil
}

// sqliteSchemaObjects lists the user schema object names of a database file.
// Comparing the snapshot's set with the target's set is how a restore proves it
// actually landed the snapshot content rather than merely leaving a valid file.
func sqliteSchemaObjects(ctx context.Context, databasePath string) (map[string]string, error) {
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx,
		"SELECT name, type FROM sqlite_master WHERE type IN ('table','view','index','trigger') AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return nil, err
		}
		out[name] = typ
	}
	return out, rows.Err()
}

// copySQLiteForBackup 把主库及其 -wal/-shm/-journal 侧车文件复制到可写目录，
// 返回副本主文件路径。主库与侧车文件合起来才构成一份有效的 SQLite 状态；
// 只复制主文件会丢掉尚未 checkpoint 的 WAL 帧。
func copySQLiteForBackup(sourcePath, dir string) (string, error) {
	dst := filepath.Join(dir, "source-copy.sqlite")
	if err := copySQLiteFile(sourcePath, dst); err != nil {
		return "", err
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		side := sourcePath + suffix
		if _, err := os.Stat(side); err != nil {
			continue
		}
		if err := copySQLiteFile(side, dst+suffix); err != nil {
			return "", err
		}
	}
	return dst, nil
}

// copySQLiteFile 复制单个文件并 fsync；副本权限固定 0600，不继承源权限。
func copySQLiteFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy %s: %w", src, err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return fmt.Errorf("sync %s: %w", dst, err)
	}
	return out.Close()
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
