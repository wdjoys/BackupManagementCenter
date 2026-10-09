// Package pipeline executes one dispatched operation end-to-end on the agent:
// adapter work plus the surrounding restic invocation. The agent transport
// layer calls Execute; adapters are selected by task kind.
package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	bmcv1 "backupmanagementcenter/api/proto/v1"
	"backupmanagementcenter/internal/agent/backup"
	"backupmanagementcenter/internal/agent/rclone"
	"backupmanagementcenter/internal/agent/restic"
	"backupmanagementcenter/internal/model"
)

// PipelineError carries a stable error code for the agent runner.
type PipelineError struct {
	Code     string
	Message  string
	Cause    error
	ExitCode int // restic exit code when available
	// ResultJSON 是无秘密的失败载荷（例如恢复的 phase 与保护快照 ID）。
	// Runner 会把它带进 FAILED 结果的 result_json，确保取消/断线不会吞掉它。
	ResultJSON []byte
}

func (e *PipelineError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

func (e *PipelineError) Unwrap() error { return e.Cause }

// Deps are process-wide facilities provided by the agent runtime.
type Deps struct {
	Tools               map[string]model.ToolInfo
	Exec                backup.Executor
	Logf                func(level, format string, args ...any)
	Progress            func(model.Progress)
	SourceRoots         []string
	RestoreRoots        []string
	SourcePathMappings  []model.PathMapping
	RestorePathMappings []model.PathMapping
	ScratchMinFreeBytes int64
	MaxConcurrency      int
	ResticCacheDir      string
	// ResticCheckReadDataSubset 透传给 restic check 的 --read-data-subset。
	ResticCheckReadDataSubset string
}

// Result mirrors proto RunResult payload fields produced by successful ops.
type Result struct {
	SnapshotIDs []string
	ResultJSON  []byte
}

// logf 在 Logf 未注入时静默跳过。
func (d Deps) logf(level, format string, args ...any) {
	if d.Logf != nil {
		d.Logf(level, format, args...)
	}
}

// progress 在 Progress 未注入时静默跳过。
func (d Deps) progress(p model.Progress) {
	if d.Progress != nil {
		d.Progress(p)
	}
}

// humanBytes 以 1024 进制格式化字节数，与前端 formatBytes 保持一致。
func humanBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	v := float64(n) / 1024
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return fmt.Sprintf("%.2f %s", v, units[i])
}

// describeBackupSource 生成不含凭据的备份源描述，用于运行日志。
func describeBackupSource(task model.BackupTask) string {
	switch task.Kind {
	case "filesystem":
		return strings.Join(task.Source.Paths, ", ")
	case "sqlite":
		return task.Source.Path
	default:
		return fmt.Sprintf("%s:%d/%s", task.Source.Host, task.Source.Port, task.Source.Database)
	}
}

// Execute runs the operation synchronously. tempDir is private and already
// created; the caller wipes it afterwards regardless of outcome.
func Execute(ctx context.Context, d Deps, tempDir string, op bmcv1.ExecuteCommand_Operation, params []byte, secrets backup.SecretBundle) (*Result, error) {
	// Tell the backup package which tool paths to use
	backup.SetToolPaths(d.Tools)

	switch op {
	case bmcv1.ExecuteCommand_BACKUP:
		res, err := runBackup(ctx, d, tempDir, params, secrets)
		if err != nil {
			if ctx.Err() != nil {
				d.logf("warn", "备份已取消")
			} else {
				d.logf("error", "备份失败：%v", err)
			}
		}
		return res, err
	case bmcv1.ExecuteCommand_RESTORE:
		return runRestore(ctx, d, tempDir, params, secrets, false)
	case bmcv1.ExecuteCommand_RESTORE_DRY_RUN:
		return runRestore(ctx, d, tempDir, params, secrets, true)
	case bmcv1.ExecuteCommand_CHECK:
		return runCheck(ctx, d, tempDir, params, secrets)
	case bmcv1.ExecuteCommand_FORGET:
		return runForget(ctx, d, tempDir, params, secrets)
	case bmcv1.ExecuteCommand_SNAPSHOTS:
		return runSnapshots(ctx, d, tempDir, params, secrets)
	case bmcv1.ExecuteCommand_SNAPSHOT_LS:
		return runSnapshotLs(ctx, d, tempDir, params, secrets)
	case bmcv1.ExecuteCommand_VERIFY_STORAGE_REMOTE:
		return runVerifyRemote(ctx, d, tempDir, params, secrets)
	case bmcv1.ExecuteCommand_VALIDATE_PATHS:
		return runValidatePaths(ctx, d, tempDir, params, secrets)
	case bmcv1.ExecuteCommand_PROBE_CAPABILITIES:
		return runProbeCapabilities(ctx, d, tempDir, params, secrets)
	default:
		return nil, ErrUnsupportedOperation
	}
}

// toolExe returns the absolute path of a tool, or the bare name as fallback.
func toolExe(d Deps, name string) string {
	info, ok := d.Tools[name]
	if ok && info.Path != "" {
		return info.Path
	}
	return name
}

// runBackup handles OPERATION_BACKUP.
func runBackup(ctx context.Context, d Deps, tempDir string, params []byte, secrets backup.SecretBundle) (*Result, error) {
	var task model.BackupTask
	if err := json.Unmarshal(params, &task); err != nil {
		return nil, &PipelineError{Code: "invalid_params", Message: "unmarshal backup task", Cause: err}
	}
	// 源路径映射会把"宿主机路径"改写成"源根 + 路径"，失败信息里带上运维
	// 实际填写的路径，否则只能看到一个自己没写过的路径。
	requested := strings.Join(task.Source.Paths, ", ")
	if task.Kind == "sqlite" {
		requested = task.Source.Path
	}
	if err := mapBackupSource(&task, d.SourcePathMappings, d.SourceRoots); err != nil {
		return nil, &PipelineError{Code: "path_not_allowed", Message: "source path mapping failed", Cause: err}
	}

	adapter, ok := backup.For(task.Kind)
	if !ok {
		return nil, &PipelineError{Code: "invalid_plan", Message: "unknown kind: " + task.Kind}
	}
	d.progress(model.Progress{Phase: model.BackupPhasePreparing})
	d.logf("info", "开始备份：类型 %s，源 %s", task.Kind, describeBackupSource(task))
	if task.Kind == "filesystem" && len(task.Source.Excludes) > 0 {
		d.logf("info", "排除规则 %d 条", len(task.Source.Excludes))
	}
	if task.Kind == "filesystem" || task.Kind == "sqlite" {
		paths := task.Source.Paths
		if task.Kind == "sqlite" {
			paths = []string{task.Source.Path}
		}
		if err := validateAllowedPaths(paths, d.SourceRoots, false); err != nil {
			return nil, &PipelineError{Code: "path_not_allowed", Message: "source path validation failed", Cause: fmt.Errorf("requested %s: %w", requested, err)}
		}
	}
	spec := backup.PlanSpec{Kind: task.Kind, Source: task.Source, AgentID: ""}
	if err := adapter.Validate(ctx, spec); err != nil {
		return nil, &PipelineError{Code: "invalid_plan", Message: "validation failed", Cause: fmt.Errorf("requested %s: %w", requested, err)}
	}
	d.logf("info", "源校验通过")

	// Space check for database kinds
	if task.Kind != "filesystem" {
		const maxLogicalBackupBytes int64 = 100 << 30
		estimated := task.Source.EstimatedDumpBytes
		if task.Kind == "sqlite" && estimated <= 0 {
			if info, statErr := os.Stat(task.Source.Path); statErr == nil {
				estimated = info.Size()
			}
		}
		if estimated > maxLogicalBackupBytes {
			return nil, &PipelineError{Code: model.ErrPhysicalBackupRequired, Message: "logical backup exceeds 100 GiB; physical/incremental backup required"}
		}
		if estimated > 0 {
			required := estimated * 13 / 10
			if d.ScratchMinFreeBytes > required {
				required = d.ScratchMinFreeBytes
			}
			if err := checkTempSpace(tempDir, required); err != nil {
				return nil, err
			}
		}
	}

	rc := &backup.RunContext{
		RunID:    "",
		Task:     task,
		Secrets:  secrets,
		TempDir:  tempDir,
		Exec:     d.Exec,
		Logf:     d.Logf,
		Progress: d.Progress,
	}

	var dumpStart time.Time
	if task.Kind != "filesystem" {
		d.progress(model.Progress{Phase: model.BackupPhaseDumping})
		d.logf("info", "开始导出数据")
		dumpStart = time.Now()
	}
	artifact, err := adapter.Backup(ctx, rc)
	if err != nil {
		return nil, &PipelineError{Code: "backup_failed", Message: "adapter backup failed", Cause: err}
	}
	if task.Kind != "filesystem" {
		d.logf("info", "导出完成，用时 %s", time.Since(dumpStart).Round(time.Second))
	}

	resticOpts, err := newResticOpts(d, task.Repository.RepositoryPath, tempDir, secrets)
	if err != nil {
		return nil, err
	}

	var snapshotID string
	// A retry after an agent disconnect may have completed the upload but lost
	// its terminal response. The run tag makes the backup idempotent: reuse the
	// existing snapshot instead of creating a duplicate.
	for _, tag := range task.Tags {
		if strings.HasPrefix(tag, "run:") {
			d.logf("info", "检查本次运行是否已有快照")
			if snapshots, snapErr := restic.Snapshots(ctx, d.Exec, resticOpts); snapErr == nil {
				for _, snap := range snapshots {
					for _, existingTag := range snap.Tags {
						if existingTag == tag {
							snapshotID = snap.ID
							break
						}
					}
					if snapshotID != "" {
						break
					}
				}
			}
			break
		}
	}
	if snapshotID != "" {
		if task.Kind != "filesystem" && artifact.StagingDir != "" {
			_ = os.RemoveAll(artifact.StagingDir)
		}
		d.progress(model.Progress{Phase: model.BackupPhaseDone, Percent: 100})
		d.logf("info", "检测到本次运行已上传的快照 %s，跳过重复上传", snapshotID)
		return &Result{SnapshotIDs: []string{snapshotID}}, nil
	}
	var summary restic.BackupSummary
	lastStep := 0
	onProgress := func(p model.Progress) {
		d.progress(p)
		if p.Phase != model.BackupPhaseUploading {
			return
		}
		// 每跨过一个 10% 记录一条；restic 扫描期间总量会增长，百分比可能回退，只记录更高的档位。
		step := int(p.Percent) / 10
		if step > lastStep && step < 10 {
			lastStep = step
			d.logf("info", "上传进度 %d%%：%s / %s，文件 %d / %d", step*10, humanBytes(p.BytesDone), humanBytes(p.BytesTotal), p.FilesDone, p.FilesTotal)
		}
	}
	d.logf("info", "开始上传到 restic 仓库")
	if task.Kind == "filesystem" {
		summary, err = restic.Backup(ctx, d.Exec, resticOpts, artifact.LivePaths, artifact.ExcludeFile, task.Tags, artifact.OneFileSystem, onProgress)
	} else {
		resticOpts.WorkingDir = artifact.StagingDir
		summary, err = restic.Backup(ctx, d.Exec, resticOpts, []string{"."}, "", task.Tags, false, onProgress)
	}
	if err != nil {
		return nil, &PipelineError{Code: "backup_failed", Message: "restic backup failed", Cause: err}
	}
	snapshotID = summary.SnapshotID
	d.logf("info", "上传完成：新增文件 %d，修改 %d，未变 %d；新增数据 %s（压缩后 %s）；共处理 %d 个文件 / %s，用时 %.1f 秒", summary.FilesNew, summary.FilesChanged, summary.FilesUnmodified, humanBytes(summary.DataAdded), humanBytes(summary.DataAddedPacked), summary.TotalFilesProcessed, humanBytes(summary.TotalBytesProcessed), summary.TotalDuration)
	if snapshotID != "" {
		d.logf("info", "快照已创建：%s", snapshotID)
	} else {
		d.logf("warn", "restic 未返回快照 ID")
	}

	if task.Kind != "filesystem" && artifact.StagingDir != "" {
		os.RemoveAll(artifact.StagingDir)
	}

	return &Result{SnapshotIDs: []string{snapshotID}}, nil
}

// runRestore handles OPERATION_RESTORE and OPERATION_RESTORE_DRY_RUN.
// restoreSafeFail 上报一个"恢复目标尚未被触碰"的失败。
//
// 服务端据此把恢复记录标记为 failed；若不上报阶段，服务端只能保守判为
// manual_recovery_required，进而阻塞该来源仓库的所有后继任务（含快照浏览），
// 要求人工介入——即便失败发生在任何破坏性操作之前。
func restoreSafeFail(code, message string, cause error) error {
	return &PipelineError{Code: code, Message: message, Cause: cause,
		ResultJSON: restoreResultJSON(model.RestorePhaseFailed, "")}
}

// restoreSafeWrap 给已构造的错误补上安全阶段；非 PipelineError 按内部错误包装。
func restoreSafeWrap(err error, message string) error {
	var pe *PipelineError
	if errors.As(err, &pe) {
		if len(pe.ResultJSON) == 0 {
			pe.ResultJSON = restoreResultJSON(model.RestorePhaseFailed, "")
		}
		return pe
	}
	return restoreSafeFail("internal", message, err)
}

func runRestore(ctx context.Context, d Deps, tempDir string, params []byte, secrets backup.SecretBundle, dryRun bool) (*Result, error) {
	var task model.RestoreTask
	if err := json.Unmarshal(params, &task); err != nil {
		return nil, restoreSafeFail("invalid_params", "unmarshal restore task", err)
	}

	resticOpts, err := newResticOpts(d, task.Repository.RepositoryPath, tempDir, secrets)
	if err != nil {
		return nil, restoreSafeWrap(err, "prepare restic options")
	}

	if task.Kind == "filesystem" {
		return runFilesystemRestore(ctx, d, resticOpts, task, dryRun)
	}
	return runDatabaseRestore(ctx, d, resticOpts, task, tempDir, secrets, dryRun)
}

// runFilesystemRestore handles filesystem restore/dry-run.
func runFilesystemRestore(ctx context.Context, d Deps, opts restic.Options, task model.RestoreTask, dryRun bool) (*Result, error) {
	fs := task.Filesystem
	if fs == nil {
		return nil, restoreSafeFail("invalid_params", "missing filesystem restore spec", nil)
	}
	execFS := *fs
	mapped, err := mapPath(fs.TargetPath, d.RestorePathMappings, false)
	if err != nil {
		return nil, restoreSafeFail("path_not_allowed", "restore target path mapping failed", err)
	}
	execFS.TargetPath = mapped
	if err := validateAllowedPaths([]string{execFS.TargetPath}, d.RestoreRoots, true); err != nil {
		return nil, restoreSafeFail("path_not_allowed", "restore target is outside configured allowlist", err)
	}

	// 非空目标 + never 的校验必须在 dry-run 之前：否则预演会给出"将成功"的
	// 统计，而真实恢复立刻失败（restore_target_not_empty），预演结果不可信。
	if execFS.OverwriteMode == "never" {
		if entries, err := os.ReadDir(execFS.TargetPath); err == nil && len(entries) > 0 {
			return nil, restoreSafeFail("restore_target_not_empty", "target path not empty and overwrite_mode=never", nil)
		}
	}

	if dryRun {
		prog, err := restic.RestoreDryRunWithOverwrite(ctx, d.Exec, opts, execFS.SnapshotID, execFS.TargetPath, execFS.IncludePaths, execFS.OverwriteMode)
		if err != nil {
			return nil, &PipelineError{Code: "restore_failed", Message: "dry run failed", Cause: err}
		}
		resultJSON, _ := json.Marshal(map[string]any{
			"add":     prog.FilesAdded,
			"changed": prog.FilesChanged,
			"skipped": prog.FilesSkipped,
			"delete":  prog.FilesDeleted,
			"sample":  prog.Sample,
		})
		return &Result{ResultJSON: resultJSON}, nil
	}

	if err := restic.RestoreWithOverwrite(ctx, d.Exec, opts, execFS.SnapshotID, execFS.TargetPath, execFS.IncludePaths, execFS.OverwriteMode); err != nil {
		return nil, &PipelineError{Code: "restore_failed", Message: "restore failed", Cause: err}
	}
	if err := validateRestoredSymlinks(execFS.TargetPath); err != nil {
		return nil, &PipelineError{Code: "path_not_allowed", Message: "restored symlink escapes target root", Cause: err}
	}
	return &Result{}, nil
}

// databaseRestoreMu 串行化本 Agent 上的数据库恢复：存在性检查、预备份、导入、
// 验证、回滚与清理必须在同一临界区内，避免两个恢复同时改写同一实际目标。
// ponytail: 进程内全局串行；需要并行时按目标地址/实例做资源锁。
var databaseRestoreMu sync.Mutex

// databaseRestoreLock 获取数据库恢复的进程内互斥。
func databaseRestoreLock(ctx context.Context) (func(), error) {
	if !databaseRestoreMu.TryLock() {
		// 语义明确地等待：另一个恢复正在执行，本次排队。
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		databaseRestoreMu.Lock()
	}
	return databaseRestoreMu.Unlock, nil
}

// runDatabaseRestore handles database restore/dry-run.
//
// The order is deliberate: the snapshot is downloaded and its manifest scope is
// validated before the local restore mutex is taken, and no destructive step
// runs before the pre-restore protection snapshot is safely uploaded.
func runDatabaseRestore(ctx context.Context, d Deps, opts restic.Options, task model.RestoreTask, tempDir string, secrets backup.SecretBundle, dryRun bool) (*Result, error) {
	db := task.Database
	if db == nil {
		return nil, restoreSafeFail("invalid_params", "missing database restore spec", nil)
	}
	execDB := *db
	if task.Kind == "sqlite" {
		mapped, err := mapPath(db.TargetDatabase, d.RestorePathMappings, false)
		if err != nil {
			return nil, restoreSafeFail("path_not_allowed", "sqlite restore target path mapping failed", err)
		}
		execDB.TargetDatabase = mapped
		if err := validateAllowedPaths([]string{execDB.TargetDatabase}, d.RestoreRoots, true); err != nil {
			return nil, restoreSafeFail("path_not_allowed", "sqlite restore target is outside configured allowlist", err)
		}
	}
	adapter, ok := backup.For(task.Kind)
	if !ok {
		return nil, restoreSafeFail("invalid_plan", "unknown kind: "+task.Kind, nil)
	}
	engine, ok := adapter.(backup.DatabaseRestorer)
	if !ok {
		return nil, restoreSafeFail("invalid_plan", "adapter does not support database restore: "+task.Kind, nil)
	}

	stagingDir := filepath.Join(tempDir, "restore_staging")
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return nil, restoreSafeFail("internal", "mkdir staging", err)
	}
	if err := restic.Restore(ctx, d.Exec, opts, execDB.SnapshotID, stagingDir, nil); err != nil {
		return nil, restoreSafeFail("restore_failed", "restic restore snapshot failed", err)
	}

	manifestPath, artifactRoot, err := findRestoredManifest(stagingDir)
	if err != nil {
		return nil, restoreSafeFail(model.ErrRestoreVerification, "locate manifest failed", err)
	}
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, restoreSafeFail(model.ErrRestoreVerification, "read manifest failed", err)
	}
	var manifest backup.Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, restoreSafeFail(model.ErrRestoreVerification, "unmarshal manifest", err)
	}
	if manifest.Adapter != task.Kind {
		return nil, restoreSafeFail(model.ErrRestoreVerification, "manifest adapter mismatch: "+manifest.Adapter, nil)
	}
	// 单库范围：多库、globals 与 all 快照在这里被拒绝，目标尚未被触碰。
	artifact, err := singleDatabaseArtifact(&manifest, artifactRoot)
	if err != nil {
		return nil, restoreSafeFail(model.ErrUnsupportedRestoreManifest, err.Error(), err)
	}

	if dryRun {
		resultJSON, _ := json.Marshal(map[string]any{
			"databases": manifest.Databases,
			"adapter":   manifest.Adapter,
		})
		return &Result{ResultJSON: resultJSON}, nil
	}

	if task.RunID == "" {
		return nil, restoreSafeFail("invalid_params", "restore task is missing its run id", nil)
	}

	unlock, err := databaseRestoreLock(ctx)
	if err != nil {
		return nil, restoreSafeFail(model.ErrCancelled, "database restore cancelled while waiting for the local mutex", err)
	}
	defer unlock()

	spec := &backup.RestoreSpec{
		SnapshotID:        execDB.SnapshotID,
		Kind:              task.Kind,
		StagingDir:        artifactRoot,
		Database:          &execDB,
		Secrets:           secrets,
		Tools:             d.Tools,
		Logf:              d.Logf,
		Progress:          d.Progress,
		Exec:              d.Exec,
		RunID:             task.RunID,
		ArtifactFile:      artifact.file,
		ArtifactDatabase:  artifact.database,
		ArtifactFormat:    artifact.format,
		ArtifactCharset:   artifact.charset,
		ArtifactCollation: artifact.collation,
	}

	// 存在性判断：权限/连接错误必须失败，不能被当作“目标不存在”。
	exists, err := engine.TargetExists(ctx, spec)
	if err != nil {
		return nil, restoreSafeFail(model.ErrRestoreVerification, "cannot determine whether the target exists", err)
	}
	if exists && !execDB.ReplaceExisting {
		return nil, restoreSafeFail(model.ErrRestoreTargetExists, "target already exists and overwrite is not enabled", nil)
	}
	spec.TargetIsNew = !exists

	// 覆盖旧目标前先做可定位的保护备份；在它写入并返回快照 ID 之前绝不修改目标。
	protectionSnapshotID := ""
	if exists {
		d.Progress(model.Progress{Phase: model.RestorePhasePreBackup})
		snapID, backupErr := uploadProtectionBackup(ctx, d, opts, tempDir, task, &execDB, secrets)
		if backupErr != nil {
			return nil, &PipelineError{
				Code:       model.ErrPreRestoreBackupFailed,
				Message:    "pre-restore protection backup failed; target was not modified",
				Cause:      backupErr,
				ResultJSON: restoreResultJSON(model.RestorePhasePreBackupFailed, ""),
			}
		}
		protectionSnapshotID = snapID
		// 提前把保护快照 ID 上报给服务端，供展示与防删定位（不承担唯一依据）。
		d.Progress(model.Progress{
			Phase:      model.RestorePhasePreBackup,
			DetailJSON: string(restoreResultJSON(model.RestorePhasePreBackup, snapID)),
		})
	}

	d.Progress(model.Progress{Phase: model.RestorePhaseRestoring})
	if importErr := engine.Import(ctx, spec); importErr != nil {
		phase, rollbackErr := rollbackDatabaseRestore(d, opts, tempDir, engine, spec, protectionSnapshotID)
		message := "database import failed: " + importErr.Error()
		if rollbackErr != nil {
			message += "; rollback also failed: " + rollbackErr.Error()
		}
		return nil, &PipelineError{
			Code:       restoreFailureCode(phase, model.ErrRestoreImportFailed),
			Message:    message,
			Cause:      importErr,
			ResultJSON: restoreResultJSON(phase, protectionSnapshotID),
		}
	}
	if verifyErr := engine.VerifyRestored(ctx, spec); verifyErr != nil {
		phase, rollbackErr := rollbackDatabaseRestore(d, opts, tempDir, engine, spec, protectionSnapshotID)
		message := "database restore verification failed: " + verifyErr.Error()
		if rollbackErr != nil {
			message += "; rollback also failed: " + rollbackErr.Error()
		}
		return nil, &PipelineError{
			Code:       restoreFailureCode(phase, model.ErrRestoreVerification),
			Message:    message,
			Cause:      verifyErr,
			ResultJSON: restoreResultJSON(phase, protectionSnapshotID),
		}
	}

	os.RemoveAll(stagingDir)
	return &Result{
		ResultJSON: restoreResultJSON(model.RestorePhaseSucceeded, protectionSnapshotID),
	}, nil
}

// restoreFailureCode 选择失败码：回滚未成功时统一报 rollback_failed（仍需人工
// 介入）；回滚成功时用调用方给出的失败类别，避免把所有导入失败都笼统报成
// restore_verification_failed —— 例如"权限不足无法建库"与"导入后校验不一致"
// 是完全不同的排查方向。
func restoreFailureCode(phase, category string) string {
	switch phase {
	case model.RestorePhaseRolledBack, model.RestorePhaseNewTargetCleaned:
		return category
	default:
		return model.ErrRollbackFailed
	}
}

// restoreResultJSON 构造安全恢复结果的非秘密载荷。
func restoreResultJSON(phase, rollbackSnapshotID string) []byte {
	payload := map[string]string{"phase": phase}
	if rollbackSnapshotID != "" {
		payload["rollback_snapshot_id"] = rollbackSnapshotID
	}
	data, _ := json.Marshal(payload)
	return data
}

// manifestArtifact 是本 run 唯一需要导入的产物。
type manifestArtifact struct {
	file      string
	database  string
	format    string
	charset   string
	collation string
}

// singleDatabaseArtifact 校验快照只包含一个可导入的库，并返回产物路径。
// 多库、globals 与 "all" 快照一律拒绝：整实例还原不开放。
func singleDatabaseArtifact(manifest *backup.Manifest, artifactRoot string) (manifestArtifact, error) {
	if len(manifest.Databases) == 0 {
		return manifestArtifact{}, errors.New("snapshot contains no database export")
	}
	if len(manifest.Databases) > 1 {
		return manifestArtifact{}, fmt.Errorf("snapshot contains %d database exports; only single-database restores are supported", len(manifest.Databases))
	}
	exp := manifest.Databases[0]
	if exp.Database == "globals" || exp.Database == "all" {
		return manifestArtifact{}, fmt.Errorf("snapshot scope %q is not supported", exp.Database)
	}
	if exp.File == "" || exp.File == ".." || filepath.IsAbs(exp.File) || filepath.Clean(exp.File) != exp.File || strings.HasPrefix(exp.File, ".."+string(filepath.Separator)) {
		return manifestArtifact{}, errors.New("manifest contains an unsafe artifact path")
	}
	path := filepath.Join(artifactRoot, exp.File)
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return manifestArtifact{}, errors.New("manifest artifact is missing or not a regular file")
	}
	// 库级默认字符集/排序规则由备份侧写入 manifest 的 restore_hints。
	return manifestArtifact{
		file:      path,
		database:  exp.Database,
		format:    exp.Format,
		charset:   manifest.RestoreHints["charset"],
		collation: manifest.RestoreHints["collation"],
	}, nil
}

// uploadProtectionBackup 把目标现状导出并上传到来源仓库，返回保护快照 ID。
// 只打 restore-protection:<runID> 与 kind:<kind> 标签，不加 plan:/run:，
// 以免被保留策略或孤儿扫描认领。
func uploadProtectionBackup(ctx context.Context, d Deps, opts restic.Options, tempDir string, task model.RestoreTask, target *model.DatabaseRestore, secrets backup.SecretBundle) (string, error) {
	adapter, ok := backup.For(task.Kind)
	if !ok {
		return "", fmt.Errorf("unknown kind: %s", task.Kind)
	}
	backupDir := filepath.Join(tempDir, "protection_backup")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", fmt.Errorf("mkdir protection backup: %w", err)
	}
	source, err := protectionSource(task.Kind, target)
	if err != nil {
		return "", err
	}
	rc := &backup.RunContext{
		RunID:    task.RunID,
		Task:     model.BackupTask{PlanID: task.RunID, Kind: task.Kind, Repository: task.Repository, Source: source},
		Secrets:  secrets,
		TempDir:  backupDir,
		Exec:     d.Exec,
		Logf:     d.Logf,
		Progress: d.Progress,
	}
	artifact, err := adapter.Backup(ctx, rc)
	if err != nil {
		return "", fmt.Errorf("export target for protection backup: %w", err)
	}
	paths := artifact.LivePaths
	if artifact.StagingDir != "" {
		paths = []string{artifact.StagingDir}
	}
	if len(paths) == 0 {
		return "", errors.New("protection backup produced no paths")
	}
	d.Progress(model.Progress{Phase: model.RestorePhasePreBackup})
	summary, err := restic.Backup(ctx, d.Exec, opts, paths, artifact.ExcludeFile,
		[]string{restoreProtectionTagPrefix + task.RunID, "kind:" + task.Kind}, artifact.OneFileSystem, nil)
	if err != nil {
		return "", fmt.Errorf("upload protection backup: %w", err)
	}
	if summary.SnapshotID == "" {
		return "", errors.New("protection backup returned no snapshot id")
	}
	return summary.SnapshotID, nil
}

// restoreProtectionTagPrefix 与 server 端保护标签保持一致。
const restoreProtectionTagPrefix = "restore-protection:"

// protectionSource 由恢复目标构造备份源：保护备份导出的必须是目标现状。
func protectionSource(kind string, target *model.DatabaseRestore) (model.PlanSource, error) {
	switch kind {
	case model.KindSQLite:
		if target.TargetDatabase == "" {
			return model.PlanSource{}, errors.New("sqlite protection backup needs the target path")
		}
		return model.PlanSource{Path: target.TargetDatabase}, nil
	case model.KindPostgreSQL, model.KindMySQL, model.KindMongoDB:
		if target.TargetDatabase == "" || target.TargetDatabase == "all" {
			return model.PlanSource{}, errors.New("protection backup requires a single target database")
		}
		return model.PlanSource{
			Host:       target.TargetHost,
			Port:       target.TargetPort,
			Username:   target.TargetUsername,
			Database:   target.TargetDatabase,
			AuthSource: target.TargetAuthSource,
		}, nil
	default:
		return model.PlanSource{}, fmt.Errorf("kind %s has no protection backup source", kind)
	}
}

// rollbackDatabaseRestore 尽力把目标恢复到修改前状态，返回最终 phase。
// 使用独立的有界 context：调用方的取消不能打断回滚。
func rollbackDatabaseRestore(d Deps, opts restic.Options, tempDir string, engine backup.DatabaseRestorer, spec *backup.RestoreSpec, protectionSnapshotID string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), rollbackBudget)
	defer cancel()
	d.Progress(model.Progress{Phase: model.RestorePhaseRollingBack})

	if spec.TargetIsNew {
		if err := engine.RemoveTarget(ctx, spec); err != nil {
			d.Logf("error", "removing the newly created target failed: %v", err)
			return model.RestorePhaseRollbackFailed, err
		}
		spec.TargetIsNew = false
		return model.RestorePhaseNewTargetCleaned, nil
	}
	if protectionSnapshotID == "" {
		// 没有保护快照就没有可信的回滚来源。
		return model.RestorePhaseRollbackFailed, errors.New("no protection snapshot available for rollback")
	}
	rollbackDir := filepath.Join(tempDir, "rollback_staging")
	if err := os.RemoveAll(rollbackDir); err != nil {
		return model.RestorePhaseRollbackFailed, err
	}
	if err := os.MkdirAll(rollbackDir, 0o700); err != nil {
		return model.RestorePhaseRollbackFailed, err
	}
	if err := restic.Restore(ctx, d.Exec, opts, protectionSnapshotID, rollbackDir, nil); err != nil {
		return model.RestorePhaseRollbackFailed, fmt.Errorf("restore protection snapshot: %w", err)
	}
	rollbackManifestPath, rollbackArtifactRoot, err := findRestoredManifest(rollbackDir)
	if err != nil {
		return model.RestorePhaseRollbackFailed, fmt.Errorf("locate protection manifest: %w", err)
	}
	// 必须从保护清单重新解析产物：只改 StagingDir 会让 spec 仍指向原始（失败）
	// 的产物，回滚实际会重复导入同一份坏数据。此前即因此回滚必然失败——目标被
	// 覆盖后无法恢复，只能转入人工处理。
	rollbackManifestData, err := os.ReadFile(rollbackManifestPath)
	if err != nil {
		return model.RestorePhaseRollbackFailed, fmt.Errorf("read protection manifest: %w", err)
	}
	var rollbackManifest backup.Manifest
	if err := json.Unmarshal(rollbackManifestData, &rollbackManifest); err != nil {
		return model.RestorePhaseRollbackFailed, fmt.Errorf("unmarshal protection manifest: %w", err)
	}
	rollbackArtifact, err := singleDatabaseArtifact(&rollbackManifest, rollbackArtifactRoot)
	if err != nil {
		return model.RestorePhaseRollbackFailed, fmt.Errorf("resolve protection artifact: %w", err)
	}
	restoreSpec := *spec
	restoreSpec.StagingDir = rollbackArtifactRoot
	restoreSpec.ArtifactFile = rollbackArtifact.file
	restoreSpec.ArtifactDatabase = rollbackArtifact.database
	restoreSpec.ArtifactFormat = rollbackArtifact.format
	restoreSpec.TargetIsNew = false
	if err := engine.Import(ctx, &restoreSpec); err != nil {
		return model.RestorePhaseRollbackFailed, fmt.Errorf("re-import protection data: %w", err)
	}
	if err := engine.VerifyRestored(ctx, &restoreSpec); err != nil {
		return model.RestorePhaseRollbackFailed, fmt.Errorf("verify rollback: %w", err)
	}
	return model.RestorePhaseRolledBack, nil
}

// rollbackBudget 是回滚的独立预算；超时不等于安全回滚，必须上报失败。
const rollbackBudget = 2 * time.Minute

func findRestoredManifest(root string) (manifestPath, artifactRoot string, err error) {
	var found string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() != "manifest.json" || !entry.Type().IsRegular() {
			return nil
		}
		if found != "" {
			return fmt.Errorf("multiple manifest.json files in restored snapshot")
		}
		found = path
		return nil
	})
	if err != nil {
		return "", "", err
	}
	if found == "" {
		return "", "", fmt.Errorf("manifest.json not found under %s", root)
	}
	return found, filepath.Dir(found), nil
}

func mapPath(path string, mappings []model.PathMapping, reverse bool) (string, error) {
	if len(mappings) == 0 {
		return path, nil
	}
	clean := filepath.Clean(path)
	best := -1
	bestRoot := ""
	for i, mapping := range mappings {
		root := mapping.HostPath
		if reverse {
			root = mapping.RuntimePath
		}
		root = filepath.Clean(root)
		rel, err := filepath.Rel(root, clean)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		if best < 0 || len(root) > len(bestRoot) {
			best, bestRoot = i, root
		}
	}
	if best < 0 {
		if reverse {
			return path, nil
		}
		return "", fmt.Errorf("path %q is not covered by a path mapping", path)
	}
	mapping := mappings[best]
	targetRoot := mapping.RuntimePath
	if reverse {
		targetRoot = mapping.HostPath
	}
	rel, err := filepath.Rel(bestRoot, clean)
	if err != nil {
		return "", err
	}
	if rel == "." {
		rel = ""
	}
	target := filepath.Clean(filepath.Join(targetRoot, rel))
	check, err := filepath.Rel(filepath.Clean(targetRoot), target)
	if err != nil || check == ".." || strings.HasPrefix(check, ".."+string(filepath.Separator)) || filepath.IsAbs(check) {
		return "", fmt.Errorf("mapped path %q escapes target root", path)
	}
	return target, nil
}

// HostDisplayText converts mapped runtime paths back to host paths for user-facing text.
// It only replaces path-boundary matches and leaves unrelated runtime paths unchanged.
func HostDisplayText(text string, mappings []model.PathMapping) string {
	for _, mapping := range mappings {
		runtime := filepath.Clean(mapping.RuntimePath)
		host := filepath.Clean(mapping.HostPath)
		for _, sep := range []string{"/", "\\"} {
			text = replacePathBoundary(text, runtime, host, sep)
		}
	}
	return text
}

func replacePathBoundary(text, from, to, sep string) string {
	if from == "" || from == "." || !strings.Contains(text, from) {
		return text
	}
	for start := 0; ; {
		i := strings.Index(text[start:], from)
		if i < 0 {
			return text
		}
		i += start
		beforeOK := i == 0 || isPathBoundary(text[i-1], sep)

		after := i + len(from)
		afterOK := after == len(text) || isPathBoundary(text[after], sep)
		if beforeOK && afterOK {
			text = text[:i] + to + text[after:]
			start = i + len(to)
		} else {
			start = after
		}
		if start >= len(text) {
			return text
		}
	}
}

func isPathBoundary(ch byte, sep string) bool {
	return ch == sep[0] || ch == '/' || ch == '\\' || ch == ' ' || ch == '\t' || ch == '\n' || ch == ':' || ch == '(' || ch == ')' || ch == '[' || ch == ']' || ch == ',' || ch == ';'
}

// mapBackupSource translates plan source paths from host paths to runtime
// paths. Without an explicit BMC_SOURCE_PATH_MAPPINGS the host paths are
// mirrored onto the first source root (host /etc -> /backup-sources/etc),
// which matches the container mount convention; paths already inside a source
// root keep working as container paths.
// ponytail: 镜像规则只使用第一个 source root；多根或非镜像挂载布局时显式配置 BMC_SOURCE_PATH_MAPPINGS。
func mapBackupSource(task *model.BackupTask, mappings []model.PathMapping, sourceRoots []string) error {
	implicit := len(mappings) == 0
	effective := mappings
	if implicit {
		effective = implicitSourceMapping(sourceRoots)
	}
	mapOne := func(path string) (string, error) {
		if implicit && pathWithinAnyRoot(path, sourceRoots) {
			return path, nil
		}
		return mapPath(path, effective, false)
	}
	for i, path := range task.Source.Paths {
		mapped, err := mapOne(path)
		if err != nil {
			return err
		}
		task.Source.Paths[i] = mapped
	}
	for i, path := range task.Source.Excludes {
		if !filepath.IsAbs(path) && !strings.HasPrefix(path, "/") {
			continue
		}
		mapped, err := mapOne(path)
		if err != nil {
			return err
		}
		task.Source.Excludes[i] = mapped
	}
	if task.Kind == model.KindSQLite {
		mapped, err := mapOne(task.Source.Path)
		if err != nil {
			return err
		}
		task.Source.Path = mapped
	}
	return nil
}

// implicitSourceMapping mirrors the host filesystem onto the first source
// root. Container paths are POSIX, so the rules use slash semantics.
func implicitSourceMapping(sourceRoots []string) []model.PathMapping {
	for _, root := range sourceRoots {
		clean := path.Clean(filepath.ToSlash(root))
		if clean == "/" || !(path.IsAbs(clean) || filepath.IsAbs(filepath.FromSlash(clean))) {
			continue
		}
		return []model.PathMapping{{HostPath: "/", RuntimePath: filepath.FromSlash(clean)}}
	}
	return nil
}

// pathWithinAnyRoot reports whether path already lives inside a source root.
func pathWithinAnyRoot(p string, roots []string) bool {
	clean := path.Clean(filepath.ToSlash(p))
	if !path.IsAbs(clean) {
		return false
	}
	for _, root := range roots {
		r := path.Clean(filepath.ToSlash(root))
		if r == "/" || !(path.IsAbs(r) || filepath.IsAbs(filepath.FromSlash(r))) {
			continue
		}
		if clean == r || strings.HasPrefix(clean, r+"/") {
			return true
		}
	}
	return false
}

// validateAllowedPaths rejects paths that escape the explicitly configured
// source/restore roots. An empty allowlist preserves local-development
// compatibility; production Docker deployments must configure both lists.
func validateAllowedPaths(paths, roots []string, allowMissing bool) error {
	for _, p := range paths {
		if p == "" {
			return fmt.Errorf("empty path")
		}
		abs, err := filepath.Abs(filepath.Clean(p))
		if err != nil {
			return err
		}
		clean := filepath.ToSlash(filepath.Clean(abs))
		volumeRoot := filepath.VolumeName(abs) + string(filepath.Separator)
		if clean == "/" || (volumeRoot != string(filepath.Separator) && filepath.Clean(abs) == filepath.Clean(volumeRoot)) || strings.HasSuffix(clean, "/var/run/docker.sock") || clean == "/docker.sock" {
			return fmt.Errorf("forbidden path: %s", p)
		}
	}
	if len(roots) == 0 {
		return nil
	}
	cleanRoots := make([]string, 0, len(roots))
	for _, root := range roots {
		abs, err := filepath.Abs(filepath.Clean(root))
		if err != nil {
			return err
		}
		rootClean := filepath.ToSlash(filepath.Clean(abs))
		rootVolume := filepath.VolumeName(abs) + string(filepath.Separator)
		if rootClean == "/" || (rootVolume != string(filepath.Separator) && filepath.Clean(abs) == filepath.Clean(rootVolume)) || strings.HasSuffix(rootClean, "/var/run/docker.sock") || rootClean == "/docker.sock" {
			return fmt.Errorf("forbidden allowlist root: %s", root)
		}
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			abs = real
		}
		cleanRoots = append(cleanRoots, filepath.Clean(abs))
	}
	for _, p := range paths {
		if p == "" {
			return fmt.Errorf("empty path")
		}
		abs, err := filepath.Abs(filepath.Clean(p))
		if err != nil {
			return err
		}
		resolved, err := resolvePathForCheck(abs, allowMissing)
		if err != nil {
			// 路径不可访问（不存在或无法解析）与"超出白名单"是两回事，
			// 这里带上原始路径，避免调用方把原因笼统归到白名单上。
			return fmt.Errorf("path %q not accessible: %w", p, err)
		}
		abs = resolved
		if abs == filepath.VolumeName(abs)+string(filepath.Separator) {
			return fmt.Errorf("root path is not allowed: %s", p)
		}
		ok := false
		for _, root := range cleanRoots {
			rel, err := filepath.Rel(root, abs)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("path %q is outside configured roots", p)
		}
	}
	return nil
}

func validateRestoredSymlinks(root string) error {
	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return err
	}
	if real, evalErr := filepath.EvalSymlinks(rootAbs); evalErr == nil {
		rootAbs = real
	}
	return filepath.WalkDir(rootAbs, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink == 0 {
			return nil
		}
		target, readErr := os.Readlink(path)
		if readErr != nil {
			return readErr
		}
		resolved := target
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(filepath.Dir(path), resolved)
		}
		resolved, err = filepath.Abs(filepath.Clean(resolved))
		if err != nil {
			return err
		}
		if real, evalErr := filepath.EvalSymlinks(path); evalErr == nil {
			resolved = real
		} else {
			_ = os.Remove(path)
			return fmt.Errorf("symlink %q target cannot be resolved: %w", path, evalErr)
		}
		rel, relErr := filepath.Rel(rootAbs, resolved)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			// Remove only the newly restored link; never follow it and never
			// delete a target outside the restore root.
			_ = os.Remove(path)
			return fmt.Errorf("symlink %q points outside restore root", path)
		}
		return nil
	})
}

func resolvePathForCheck(abs string, allowMissing bool) (string, error) {
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(real), nil
	} else if !allowMissing {
		return "", err
	}
	// Resolve the nearest existing parent so a symlinked directory cannot
	// escape the allowlist even when the final restore file is new.
	missing := []string{}
	cur := abs
	for {
		if _, err := os.Lstat(cur); err == nil {
			real, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", err
			}
			for i := len(missing) - 1; i >= 0; i-- {
				real = filepath.Join(real, missing[i])
			}
			return filepath.Clean(real), nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs, nil
		}
		missing = append(missing, filepath.Base(cur))
		cur = parent
	}
}

// runCheck runs restic check.
func runCheck(ctx context.Context, d Deps, tempDir string, params []byte, secrets backup.SecretBundle) (*Result, error) {
	var task model.CheckTask
	if err := json.Unmarshal(params, &task); err != nil {
		return nil, &PipelineError{Code: "invalid_params", Message: "unmarshal check task", Cause: err}
	}
	opts, err := newResticOpts(d, task.Repository.RepositoryPath, tempDir, secrets)
	if err != nil {
		return nil, err
	}
	if err := restic.Check(ctx, d.Exec, opts); err != nil {
		return nil, &PipelineError{Code: "check_failed", Message: "restic check failed", Cause: err}
	}
	resultJSON, _ := json.Marshal(map[string]bool{"checked": true})
	return &Result{ResultJSON: resultJSON}, nil
}

// runForget runs restic forget (without prune) with retention policy, or restic init if
// params is an InitTask (ResticInit=true). InitTask uses no Tags field.
func runForget(ctx context.Context, d Deps, tempDir string, params []byte, secrets backup.SecretBundle) (*Result, error) {
	// Try InitTask first
	var initTask model.InitTask
	if err := json.Unmarshal(params, &initTask); err == nil && initTask.ResticInit {
		opts, err := newResticOpts(d, initTask.Repository.RepositoryPath, tempDir, secrets)
		if err != nil {
			return nil, err
		}
		if err := restic.Init(ctx, d.Exec, opts); err != nil {
			return nil, &PipelineError{Code: "init_failed", Message: "restic init failed", Cause: err}
		}
		resultJSON, _ := json.Marshal(map[string]bool{"initialized": true})
		return &Result{ResultJSON: resultJSON}, nil
	}

	// ForgetTask 可由计划删除流程调用，按计划标签删除快照并按需 prune。
	var task model.ForgetTask
	if err := json.Unmarshal(params, &task); err != nil {
		return nil, &PipelineError{Code: "invalid_params", Message: "unmarshal forget task", Cause: err}
	}
	opts, err := newResticOpts(d, task.Repository.RepositoryPath, tempDir, secrets)
	if err != nil {
		return nil, err
	}
	// SnapshotIDs 分支：手动/自动删除单个快照，按完整 snapshot ID 精确 forget。
	// 互斥校验：SnapshotIDs 非空时不得同时携带 Tags、DeleteAll 或 retention 规则。
	if len(task.SnapshotIDs) > 0 {
		if len(task.Tags) > 0 || task.DeleteAll || task.Retention.KeepLast > 0 || task.Retention.KeepDaily > 0 || task.Retention.KeepWeekly > 0 || task.Retention.KeepMonthly > 0 {
			return nil, &PipelineError{Code: "invalid_params", Message: "snapshot_ids cannot be combined with tags/delete_all/retention", Cause: nil}
		}
		if d.Logf != nil {
			d.Logf("info", "开始删除快照 %s（等待仓库锁最多 5 分钟）", strings.Join(task.SnapshotIDs, ","))
		}
		if err := restic.DeleteSnapshots(ctx, d.Exec, opts, task.SnapshotIDs, task.Prune); err != nil {
			if d.Logf != nil {
				d.Logf("error", "restic 删除快照失败：%v", err)
			}
			return nil, &PipelineError{Code: "forget_failed", Message: "restic forget failed", Cause: err}
		}
		if d.Logf != nil {
			d.Logf("info", "restic 删除快照命令已成功完成")
		}
		return &Result{}, nil
	}
	if task.DeleteAll {
		if err := restic.DeleteByTags(ctx, d.Exec, opts, task.Tags); err != nil {
			return nil, &PipelineError{Code: "forget_failed", Message: "delete plan backups failed", Cause: err}
		}
	} else if task.Prune {
		if err := restic.Forget(ctx, d.Exec, opts, task.Retention, task.Tags); err != nil {
			return nil, &PipelineError{Code: "forget_failed", Message: "restic forget failed", Cause: err}
		}
	} else if err := restic.ForgetOnly(ctx, d.Exec, opts, task.Retention, task.Tags); err != nil {
		return nil, &PipelineError{Code: "forget_failed", Message: "restic forget failed", Cause: err}
	}
	return &Result{}, nil
}

// runSnapshots runs restic snapshots --json.
func runSnapshots(ctx context.Context, d Deps, tempDir string, params []byte, secrets backup.SecretBundle) (*Result, error) {
	var task model.SnapshotsTask
	if err := json.Unmarshal(params, &task); err != nil {
		return nil, &PipelineError{Code: "invalid_params", Message: "unmarshal snapshots task", Cause: err}
	}
	opts, err := newResticOpts(d, task.Repository.RepositoryPath, tempDir, secrets)
	if err != nil {
		return nil, err
	}
	snaps, err := restic.Snapshots(ctx, d.Exec, opts)
	if err != nil {
		return nil, &PipelineError{Code: "snapshots_failed", Message: "restic snapshots failed", Cause: err}
	}
	for i := range snaps {
		for j, path := range snaps[i].Paths {
			if mapped, mapErr := mapPath(path, d.SourcePathMappings, true); mapErr == nil {
				snaps[i].Paths[j] = mapped
			}
		}
	}
	resultJSON, _ := json.Marshal(snaps)
	return &Result{ResultJSON: resultJSON}, nil
}

// runSnapshotLs runs restic ls <snapshot> --json.
func runSnapshotLs(ctx context.Context, d Deps, tempDir string, params []byte, secrets backup.SecretBundle) (*Result, error) {
	var task model.SnapshotLsTask
	if err := json.Unmarshal(params, &task); err != nil {
		return nil, &PipelineError{Code: "invalid_params", Message: "unmarshal snapshot_ls task", Cause: err}
	}
	opts, err := newResticOpts(d, task.Repository.RepositoryPath, tempDir, secrets)
	if err != nil {
		return nil, err
	}
	entries, err := restic.Ls(ctx, d.Exec, opts, task.SnapshotID, task.Path)
	if err != nil {
		return nil, &PipelineError{Code: "snapshot_ls_failed", Message: "restic ls failed", Cause: err}
	}

	// restic filters at the repository, so browsing a directory does not scan
	// every node in the snapshot before returning its direct children.
	norm := func(p string) string {
		p = strings.ReplaceAll(p, "\\", "/")
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
			p = p[:2] + p[3:]
		}
		return strings.TrimSuffix(p, "/")
	}
	dir := norm(task.Path)
	filtered := entries[:0]
	for _, entry := range entries {
		entry.Path = norm(entry.Path)
		if entry.Path != dir {
			filtered = append(filtered, entry)
		}
	}

	resultJSON, _ := json.Marshal(map[string]any{
		"entries": filtered,
		"path":    dir,
	})
	return &Result{ResultJSON: resultJSON}, nil
}

// verifyDeadline bounds one verify-remote run (listremotes + lsd). It must
// stay below the server's verifyRemoteWait so the rclone stderr tail reaches
// the user as a storage_remote_unreachable failure rather than a wait timeout.
const verifyDeadline = 100 * time.Second

// runVerifyRemote validates rclone remote.
//
// The whole verify is bounded by verifyDeadline: a remote that black-holes
// (unreachable endpoint, DNS hang) would otherwise keep rclone running until
// the server watchdog force-fails the run with a generic timeout. Killing at
// verifyDeadline — slightly below the server's verifyRemoteWait — lets the
// captured rclone stderr surface as storage_remote_unreachable instead.
func runVerifyRemote(ctx context.Context, d Deps, tempDir string, params []byte, secrets backup.SecretBundle) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, verifyDeadline)
	defer cancel()
	var task model.VerifyRemoteTask
	if err := json.Unmarshal(params, &task); err != nil {
		return nil, &PipelineError{Code: "invalid_params", Message: "unmarshal verify remote task", Cause: err}
	}
	if !task.ConfigProvided {
		return nil, &PipelineError{Code: "invalid_params", Message: "config_provided must be true"}
	}
	confPath, err := rclone.WriteConf(tempDir, secrets.RcloneConf)
	if err != nil {
		return nil, &PipelineError{Code: "internal", Message: "write rclone conf", Cause: err}
	}
	remotes, err := rclone.ListRemotes(ctx, d.Exec, confPath)
	if err != nil {
		return nil, &PipelineError{Code: "storage_remote_unreachable", Message: "listremotes failed", Cause: err}
	}
	found := false
	remoteType := task.RemoteName
	for _, r := range remotes {
		if r == task.RemoteName {
			found = true
			break
		}
	}
	if !found {
		// 这是配置问题（remote 未在提供的 rclone 配置里定义），不是"远程不可达"：
		// 此前两者同码，运维无法据此判断该改配置还是查网络。
		return nil, &PipelineError{Code: model.ErrStorageRemoteNotFound,
			Message: "remote " + task.RemoteName + " is not defined in the provided rclone config"}
	}
	entries, err := rclone.Lsd(ctx, d.Exec, confPath, task.RemoteName)
	if err != nil {
		// 端点黑洞时 rclone 会持续重试且 stderr 只有 provider NOTICE，运维看不出
		// 到底发生了什么；到点被杀（verifyDeadline）时补一句可行动的说明。
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, &PipelineError{Code: "storage_remote_unreachable",
				Message: fmt.Sprintf("rclone lsd timed out after %s; the endpoint may be unreachable or black-holing", verifyDeadline),
				Cause:   err}
		}
		return nil, &PipelineError{Code: "storage_remote_unreachable", Message: "lsd failed", Cause: err}
	}
	resultJSON, _ := json.Marshal(map[string]any{"remote_type": remoteType, "entries": len(entries)})
	return &Result{ResultJSON: resultJSON}, nil
}

// runValidatePaths validates filesystem paths via the filesystem adapter.
func runValidatePaths(ctx context.Context, d Deps, tempDir string, params []byte, secrets backup.SecretBundle) (*Result, error) {
	var task model.ValidatePathsTask
	if err := json.Unmarshal(params, &task); err != nil {
		return nil, &PipelineError{Code: "invalid_params", Message: "unmarshal validate paths task", Cause: err}
	}
	// 源路径映射会把"宿主机路径"改写成"源根 + 路径"，失败信息里带上运维
	// 实际填写的路径，否则只能看到一个自己没写过的路径。
	requested := strings.Join(task.Paths, ", ")
	backupTask := model.BackupTask{Kind: model.KindFilesystem, Source: model.PlanSource{Paths: task.Paths, Excludes: task.Excludes}}
	if err := mapBackupSource(&backupTask, d.SourcePathMappings, d.SourceRoots); err != nil {
		return nil, &PipelineError{Code: "path_not_allowed", Message: "source path mapping failed", Cause: err}
	}
	if err := validateAllowedPaths(backupTask.Source.Paths, d.SourceRoots, false); err != nil {
		return nil, &PipelineError{Code: "path_not_allowed", Message: "source path validation failed", Cause: fmt.Errorf("requested %s: %w", requested, err)}
	}
	adapter, ok := backup.For(model.KindFilesystem)
	if !ok {
		return nil, &PipelineError{Code: "invalid_plan", Message: "filesystem adapter not found"}
	}
	if err := adapter.Validate(ctx, backup.PlanSpec{Kind: model.KindFilesystem, Source: backupTask.Source}); err != nil {
		return nil, &PipelineError{Code: "path_validation_failed", Message: "path validation failed", Cause: fmt.Errorf("requested %s: %w", requested, err)}
	}
	return &Result{}, nil
}

// runProbeCapabilities returns the current tool info.
func runProbeCapabilities(_ context.Context, d Deps, _ string, _ []byte, _ backup.SecretBundle) (*Result, error) {
	resultJSON, err := json.Marshal(d.Tools)
	if err != nil {
		return nil, &PipelineError{Code: "internal", Message: "marshal capabilities", Cause: err}
	}
	return &Result{ResultJSON: resultJSON}, nil
}

// ErrUnsupportedOperation is returned for ops without an adapter.
var ErrUnsupportedOperation = errors.New("unsupported operation")

func newResticOpts(d Deps, repoPath, tempDir string, secrets backup.SecretBundle) (restic.Options, error) {
	pwPath, err := backup.WriteSecretFile(tempDir, "restic_pw", secrets.ResticPassword)
	if err != nil {
		return restic.Options{}, &PipelineError{Code: "internal", Message: "write restic password", Cause: err}
	}
	opts := restic.Options{
		Exe:            toolExe(d, "restic"),
		RepoPath:       restic.NormalizeRepoPath(repoPath),
		PasswordFile:   pwPath,
		ReadDataSubset: d.ResticCheckReadDataSubset,
		CacheDir:       d.ResticCacheDir,
		Logf: func(line string) {
			if d.Logf != nil {
				d.Logf("error", "restic stderr: %s", line)
			}
		},
	}
	if opts.Exe == "" {
		opts.Exe = "restic"
	}
	if secrets.RcloneConf != "" {
		confPath, err := rclone.WriteConf(tempDir, secrets.RcloneConf)
		if err != nil {
			return restic.Options{}, &PipelineError{Code: "internal", Message: "write rclone conf", Cause: err}
		}
		opts.RcloneConfFile = confPath
	}
	return opts, nil
}
