package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"backupmanagementcenter/internal/agent/backup"
	"backupmanagementcenter/internal/agent/restic"
	"backupmanagementcenter/internal/model"

	bmcv1 "backupmanagementcenter/api/proto/v1"
)

func TestRunVerifyRemote_NilExecutorReturnsError(t *testing.T) {
	params, err := json.Marshal(model.VerifyRemoteTask{ConfigProvided: true, RemoteName: "backup"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runVerifyRemote(context.Background(), Deps{}, t.TempDir(), params, backup.SecretBundle{RcloneConf: "[backup]\ntype = local\n"})
	if err == nil {
		t.Fatal("expected verify remote error")
	}
	var pipelineErr *PipelineError
	if !errors.As(err, &pipelineErr) {
		t.Fatalf("expected PipelineError, got %T: %v", err, err)
	}
	if pipelineErr.Code != "storage_remote_unreachable" {
		t.Fatalf("unexpected error code: %s", pipelineErr.Code)
	}
	if !strings.Contains(err.Error(), "executor is nil") {
		t.Fatalf("missing executor detail: %v", err)
	}
}

func TestNewResticOptsUsesPersistentCache(t *testing.T) {
	tempDir := t.TempDir()
	cacheDir := filepath.Join(t.TempDir(), "restic-cache")
	opts, err := newResticOpts(Deps{ResticCacheDir: cacheDir}, "/repo", tempDir, backup.SecretBundle{ResticPassword: "pw", RcloneConf: "[remote]\ntype = local\n"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.CacheDir != cacheDir {
		t.Fatalf("cache dir = %q, want %q", opts.CacheDir, cacheDir)
	}
	if !strings.HasPrefix(opts.PasswordFile, tempDir) || !strings.HasPrefix(opts.RcloneConfFile, tempDir) {
		t.Fatalf("secret files outside temp dir: %#v", opts)
	}
	if strings.HasPrefix(opts.CacheDir, tempDir) {
		t.Fatalf("cache dir must not be in temp dir: %q", opts.CacheDir)
	}
}

// fakeExecutor records the command it receives and returns a fixed exit code.
type fakeExecutor struct {
	cmd      *backup.Cmd
	exitCode int
	err      error
}

func (e fakeExecutor) Run(_ context.Context, cmd backup.Cmd, _, _ func(string)) (int, error) {
	if e.cmd != nil {
		*e.cmd = cmd
	}
	return e.exitCode, e.err
}

func TestRunForget_SnapshotIDs_Succeeds(t *testing.T) {
	var cmd backup.Cmd
	params, _ := json.Marshal(model.ForgetTask{
		Repository:  model.RepoAccess{RepositoryPath: "rclone:remote:/repo"},
		SnapshotIDs: []string{"abc123def456"},
		Prune:       true,
	})
	res, err := runForget(context.Background(), Deps{
		Exec:           fakeExecutor{cmd: &cmd},
		ResticCacheDir: t.TempDir(),
	}, t.TempDir(), params, backup.SecretBundle{ResticPassword: "pw", RcloneConf: "[remote]\ntype = local\n"})
	if err != nil {
		t.Fatalf("runForget: %v", err)
	}
	if res == nil {
		t.Fatal("expected result")
	}
	want := []string{"forget", "abc123def456", "--prune", "--repo", "rclone:remote:/repo"}
	if len(cmd.Args) < len(want) {
		t.Fatalf("args too short: %q", cmd.Args)
	}
	for i, w := range want {
		if cmd.Args[i] != w {
			t.Fatalf("args[%d] = %q, want %q", i, cmd.Args[i], w)
		}
	}
}

func TestRunForget_SnapshotIDs_RejectsTags(t *testing.T) {
	params, _ := json.Marshal(model.ForgetTask{
		Repository:  model.RepoAccess{RepositoryPath: "repo"},
		SnapshotIDs: []string{"snap-1"},
		Tags:        []string{"plan:foo"},
	})
	_, err := runForget(context.Background(), Deps{Exec: fakeExecutor{}}, t.TempDir(), params, backup.SecretBundle{ResticPassword: "pw"})
	if err == nil {
		t.Fatal("expected error")
	}
	var pErr *PipelineError
	if !errors.As(err, &pErr) {
		t.Fatalf("expected PipelineError, got %T", err)
	}
	if pErr.Code != "invalid_params" {
		t.Fatalf("code = %q, want invalid_params", pErr.Code)
	}
}

func TestRunForget_SnapshotIDs_RejectsDeleteAll(t *testing.T) {
	params, _ := json.Marshal(model.ForgetTask{
		Repository:  model.RepoAccess{RepositoryPath: "repo"},
		SnapshotIDs: []string{"snap-1"},
		DeleteAll:   true,
	})
	_, err := runForget(context.Background(), Deps{Exec: fakeExecutor{}}, t.TempDir(), params, backup.SecretBundle{ResticPassword: "pw"})
	if err == nil {
		t.Fatal("expected error")
	}
	var pErr *PipelineError
	if !errors.As(err, &pErr) {
		t.Fatalf("expected PipelineError, got %T", err)
	}
	if pErr.Code != "invalid_params" {
		t.Fatalf("code = %q, want invalid_params", pErr.Code)
	}
}

func TestRunForget_SnapshotIDs_RejectsRetention(t *testing.T) {
	params, _ := json.Marshal(model.ForgetTask{
		Repository:  model.RepoAccess{RepositoryPath: "repo"},
		SnapshotIDs: []string{"snap-1"},
		Retention:   model.Retention{KeepLast: 1},
	})
	_, err := runForget(context.Background(), Deps{Exec: fakeExecutor{}}, t.TempDir(), params, backup.SecretBundle{ResticPassword: "pw"})
	if err == nil {
		t.Fatal("expected error")
	}
	var pErr *PipelineError
	if !errors.As(err, &pErr) {
		t.Fatalf("expected PipelineError, got %T", err)
	}
	if pErr.Code != "invalid_params" {
		t.Fatalf("code = %q, want invalid_params", pErr.Code)
	}
}

func TestRunForget_SnapshotIDs_ResticFailureMapsToForgetFailed(t *testing.T) {
	params, _ := json.Marshal(model.ForgetTask{
		Repository:  model.RepoAccess{RepositoryPath: "repo"},
		SnapshotIDs: []string{"snap-1"},
		Prune:       true,
	})
	_, err := runForget(context.Background(), Deps{
		Exec:           fakeExecutor{exitCode: 1, err: fmt.Errorf("exit")},
		ResticCacheDir: t.TempDir(),
	}, t.TempDir(), params, backup.SecretBundle{ResticPassword: "pw", RcloneConf: "[remote]\ntype = local\n"})
	if err == nil {
		t.Fatal("expected error")
	}
	var pErr *PipelineError
	if !errors.As(err, &pErr) {
		t.Fatalf("expected PipelineError, got %T", err)
	}
	if pErr.Code != "forget_failed" {
		t.Fatalf("code = %q, want forget_failed", pErr.Code)
	}
}

type pipelineScriptExecutor struct {
	steps []struct {
		code int
		out  string
	}
	logs *[]string
}

func (e *pipelineScriptExecutor) Run(_ context.Context, _ backup.Cmd, _, onStderr func(string)) (int, error) {
	step := e.steps[0]
	e.steps = e.steps[1:]
	if step.out != "" && onStderr != nil {
		onStderr(step.out)
	}
	return step.code, nil
}

func TestRunForget_StaleLockLogsRecoveryStages(t *testing.T) {
	steps := []struct {
		code int
		out  string
	}{{code: 11, out: "locked"}, {code: 0}, {code: 0}}
	var logs []string
	params, _ := json.Marshal(model.ForgetTask{
		Repository: model.RepoAccess{RepositoryPath: "repo"}, SnapshotIDs: []string{"snap"},
	})
	_, err := runForget(context.Background(), Deps{
		Exec: &pipelineScriptExecutor{steps: steps}, Logf: func(level, format string, args ...any) { logs = append(logs, level+" "+fmt.Sprintf(format, args...)) },
	}, t.TempDir(), params, backup.SecretBundle{ResticPassword: "pw"})
	if err != nil {
		t.Fatalf("runForget: %v", err)
	}
	joined := strings.Join(logs, "\n")
	for _, want := range []string{"检测到仓库锁", "已清理陈旧锁", "重新删除快照"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("logs missing %q: %s", want, joined)
		}
	}
}

func TestRunForget_UnlockFailurePreservesStderr(t *testing.T) {
	steps := []struct {
		code int
		out  string
	}{{code: 11, out: "locked"}, {code: 1, out: "unlock denied"}}
	var logs []string
	params, _ := json.Marshal(model.ForgetTask{
		Repository: model.RepoAccess{RepositoryPath: "repo"}, SnapshotIDs: []string{"snap"},
	})
	_, err := runForget(context.Background(), Deps{
		Exec: &pipelineScriptExecutor{steps: steps}, Logf: func(level, format string, args ...any) { logs = append(logs, level+" "+fmt.Sprintf(format, args...)) },
	}, t.TempDir(), params, backup.SecretBundle{ResticPassword: "pw"})
	if err == nil || !strings.Contains(err.Error(), "unlock denied") {
		t.Fatalf("err = %v", err)
	}
	var pErr *PipelineError
	if !errors.As(err, &pErr) || pErr.Code != "forget_failed" {
		t.Fatalf("error = %T %v", err, err)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "unlock denied") {
		t.Fatalf("logs missing unlock stderr: %v", logs)
	}
}

func TestMapBackupSourceUsesLongestBoundaryMapping(t *testing.T) {
	hostRoot := filepath.Join(t.TempDir(), "host")
	hostApp := filepath.Join(hostRoot, "srv", "app")
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	runtimeApp := filepath.Join(runtimeRoot, "app")
	task := model.BackupTask{
		Kind: model.KindFilesystem,
		Source: model.PlanSource{
			Paths:    []string{filepath.Join(hostApp, "data")},
			Excludes: []string{filepath.Join(hostApp, "data", "cache"), "*.tmp"},
		},
	}
	mappings := []model.PathMapping{
		{HostPath: hostRoot, RuntimePath: runtimeRoot},
		{HostPath: hostApp, RuntimePath: runtimeApp},
	}

	if err := mapBackupSource(&task, mappings, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := task.Source.Paths[0], filepath.Join(runtimeApp, "data"); got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	if got, want := task.Source.Excludes[0], filepath.Join(runtimeApp, "data", "cache"); got != want {
		t.Fatalf("exclude = %q, want %q", got, want)
	}
	if task.Source.Excludes[1] != "*.tmp" {
		t.Fatalf("relative exclude changed: %q", task.Source.Excludes[1])
	}
}

func TestMapBackupSourceRejectsUnmappedPath(t *testing.T) {
	task := model.BackupTask{
		Kind:   model.KindFilesystem,
		Source: model.PlanSource{Paths: []string{filepath.Join(t.TempDir(), "outside")}},
	}
	mapping := model.PathMapping{HostPath: filepath.Join(t.TempDir(), "host"), RuntimePath: filepath.Join(t.TempDir(), "runtime")}
	if err := mapBackupSource(&task, []model.PathMapping{mapping}, nil); err == nil {
		t.Fatal("expected unmapped path error")
	}
}

func TestMapBackupSourceMapsSQLitePath(t *testing.T) {
	hostRoot := filepath.Join(t.TempDir(), "host")
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	task := model.BackupTask{Kind: model.KindSQLite, Source: model.PlanSource{Path: filepath.Join(hostRoot, "db", "app.sqlite")}}
	if err := mapBackupSource(&task, []model.PathMapping{{HostPath: hostRoot, RuntimePath: runtimeRoot}}, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := task.Source.Path, filepath.Join(runtimeRoot, "db", "app.sqlite"); got != want {
		t.Fatalf("sqlite path = %q, want %q", got, want)
	}
}

func TestMapPathReverseLongestBoundaryAndUnmapped(t *testing.T) {
	host := filepath.Join(t.TempDir(), "host")
	runtime := filepath.Join(t.TempDir(), "runtime")
	childHost := filepath.Join(host, "app")
	childRuntime := filepath.Join(runtime, "app")
	mappings := []model.PathMapping{{HostPath: host, RuntimePath: runtime}, {HostPath: childHost, RuntimePath: childRuntime}}
	got, err := mapPath(filepath.Join(childRuntime, "data"), mappings, true)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(childHost, "data")
	if got != want {
		t.Fatalf("reverse = %q, want %q", got, want)
	}
	outsideRuntime := runtime + "-application"
	if got, err := mapPath(filepath.Join(outsideRuntime, "x"), mappings, true); err != nil || got != filepath.Join(outsideRuntime, "x") {
		t.Fatalf("boundary/unmapped = %q, %v", got, err)
	}
	if _, err := mapPath(filepath.Join(host, "outside2"), []model.PathMapping{{HostPath: childHost, RuntimePath: childRuntime}}, false); err == nil {
		t.Fatal("expected unmapped error")
	}
}

func TestMapBackupSourceMirrorsHostPathsIntoSourceRoot(t *testing.T) {
	task := model.BackupTask{
		Kind: model.KindFilesystem,
		Source: model.PlanSource{
			Paths:    []string{"/etc", "/srv/app/data", "/backup-sources/etc", "/backup-sources-other/x"},
			Excludes: []string{"/etc/cache", "*.tmp"},
		},
	}
	if err := mapBackupSource(&task, nil, []string{"/backup-sources"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"/backup-sources/etc", "/backup-sources/srv/app/data", "/backup-sources/etc", "/backup-sources/backup-sources-other/x"}
	for i, w := range want {
		// 已在 source root 内的路径原样保留（比较时不做分隔符转换）。
		if i == 2 {
			if task.Source.Paths[i] != w {
				t.Fatalf("path[%d] = %q, want %q", i, task.Source.Paths[i], w)
			}
			continue
		}
		if task.Source.Paths[i] != filepath.FromSlash(w) {
			t.Fatalf("path[%d] = %q, want %q", i, task.Source.Paths[i], filepath.FromSlash(w))
		}
	}
	if task.Source.Excludes[0] != filepath.FromSlash("/backup-sources/etc/cache") || task.Source.Excludes[1] != "*.tmp" {
		t.Fatalf("excludes = %#v", task.Source.Excludes)
	}
}

func TestMapBackupSourceWithoutRootsKeepsPathsUntouched(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x")
	task := model.BackupTask{Kind: model.KindFilesystem, Source: model.PlanSource{Paths: []string{p}}}
	if err := mapBackupSource(&task, nil, nil); err != nil {
		t.Fatal(err)
	}
	if task.Source.Paths[0] != p {
		t.Fatalf("path = %q, want %q", task.Source.Paths[0], p)
	}
}

func TestMapBackupSourceExplicitMappingWinsOverMirror(t *testing.T) {
	task := model.BackupTask{Kind: model.KindFilesystem, Source: model.PlanSource{Paths: []string{"/opt/data"}}}
	mappings := []model.PathMapping{{HostPath: "/opt", RuntimePath: "/backup-sources/opt-mount"}}
	if err := mapBackupSource(&task, mappings, []string{"/backup-sources"}); err != nil {
		t.Fatal(err)
	}
	if task.Source.Paths[0] != filepath.FromSlash("/backup-sources/opt-mount/data") {
		t.Fatalf("path = %q", task.Source.Paths[0])
	}
}

func TestRunValidatePathsMirrorsHostPathIntoSourceRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "backup-sources")
	if err := os.MkdirAll(filepath.Join(root, "etc", "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	params, err := json.Marshal(model.ValidatePathsTask{Paths: []string{"/etc/data"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runValidatePaths(context.Background(), Deps{SourceRoots: []string{root}}, t.TempDir(), params, backup.SecretBundle{}); err != nil {
		t.Fatalf("validate paths = %v", err)
	}
}

// backupScriptExecutor 按子命令返回预置输出，用于驱动完整的 runBackup 流程。
type backupScriptExecutor struct{}

func (backupScriptExecutor) Run(_ context.Context, cmd backup.Cmd, onStdout func(string), _ func(string)) (int, error) {
	if len(cmd.Args) == 0 {
		return 1, nil
	}
	switch cmd.Args[0] {
	case "snapshots":
		onStdout("[]")
	case "backup":
		// restic --json 的真实输出是扁平 JSON，逐行回调。
		onStdout(`{"message_type":"status","percent_done":0.5,"total_files":2,"files_done":1,"total_bytes":2048,"bytes_done":1024}`)
		onStdout(`{"message_type":"summary","snapshot_id":"abc123","files_new":2,"total_files_processed":2,"total_bytes_processed":2048,"total_duration":0.5}`)
	}
	return 0, nil
}

func TestRunBackupReportsSnapshotProgressAndLogs(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	params, err := json.Marshal(model.BackupTask{
		PlanID:     "p1",
		Kind:       model.KindFilesystem,
		Repository: model.RepoAccess{RepositoryPath: "repo"},
		Source:     model.PlanSource{Paths: []string{src}},
		Tags:       []string{"run:r1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	var (
		logged   []string
		phases   []string
		progress []model.Progress
	)
	deps := Deps{
		Exec: backupScriptExecutor{},
		Logf: func(level, format string, args ...any) {
			logged = append(logged, level+" "+fmt.Sprintf(format, args...))
		},
		Progress: func(p model.Progress) {
			progress = append(progress, p)
			phases = append(phases, p.Phase)
		},
	}

	res, err := Execute(context.Background(), deps, t.TempDir(), bmcv1.ExecuteCommand_BACKUP, params, backup.SecretBundle{ResticPassword: "pw"})
	if err != nil {
		t.Fatalf("Execute BACKUP: %v", err)
	}
	if len(res.SnapshotIDs) != 1 || res.SnapshotIDs[0] != "abc123" {
		t.Fatalf("SnapshotIDs = %v, want [abc123]", res.SnapshotIDs)
	}

	joined := strings.Join(logged, "\n")
	if !strings.Contains(joined, "快照已创建：abc123") {
		t.Errorf("missing snapshot log:\n%s", joined)
	}
	if !strings.Contains(joined, "开始备份：类型 filesystem") {
		t.Errorf("missing start log:\n%s", joined)
	}

	wantPhases := []string{model.BackupPhasePreparing, model.BackupPhaseUploading, model.BackupPhaseDone}
	got := strings.Join(phases, ",")
	for _, want := range wantPhases {
		if !strings.Contains(got, want) {
			t.Errorf("progress phases %q missing %q", got, want)
		}
	}
	_ = progress
}

// protectionRestoreExecutor 在收到 `restic restore` 时把预置的保护快照内容写到
// --target 指定目录（模拟 restic 还原保护快照）。
type protectionRestoreExecutor struct {
	manifest string
	dumpName string
}

func (e *protectionRestoreExecutor) Run(_ context.Context, cmd backup.Cmd, _, _ func(string)) (int, error) {
	if len(cmd.Args) > 0 && cmd.Args[0] == "restore" {
		target := ""
		for i, a := range cmd.Args {
			if a == "--target" && i+1 < len(cmd.Args) {
				target = cmd.Args[i+1]
			}
		}
		if target == "" {
			return 1, nil
		}
		if err := os.MkdirAll(target, 0o700); err != nil {
			return 1, err
		}
		if err := os.WriteFile(filepath.Join(target, "manifest.json"), []byte(e.manifest), 0o600); err != nil {
			return 1, err
		}
		if err := os.WriteFile(filepath.Join(target, e.dumpName), []byte("PROTECTION"), 0o600); err != nil {
			return 1, err
		}
	}
	return 0, nil
}

// recordingRestorer 记录回滚的 Import 收到的产物路径。
type recordingRestorer struct {
	importedFiles []string
}

func (r *recordingRestorer) TargetExists(context.Context, *backup.RestoreSpec) (bool, error) {
	return true, nil
}
func (r *recordingRestorer) Import(_ context.Context, spec *backup.RestoreSpec) error {
	r.importedFiles = append(r.importedFiles, spec.ArtifactFile)
	return nil
}
func (r *recordingRestorer) VerifyRestored(context.Context, *backup.RestoreSpec) error { return nil }
func (r *recordingRestorer) RemoveTarget(context.Context, *backup.RestoreSpec) error   { return nil }

// 回滚必须从保护清单重新解析产物：只改 StagingDir 会让 spec 仍指向原始（失败的）
// 产物，回滚会重复导入同一份坏数据，目标被覆盖后无法恢复。
func TestRollbackReimportsProtectionArtifact(t *testing.T) {
	tempDir := t.TempDir()
	manifest := `{"adapter":"mysql","databases":[{"database":"appdb","file":"protection.sql","format":"sql"}]}`
	exec := &protectionRestoreExecutor{manifest: manifest, dumpName: "protection.sql"}
	restorer := &recordingRestorer{}

	spec := &backup.RestoreSpec{
		Kind:             "mysql",
		StagingDir:       "/staging-from-snapshot",
		ArtifactFile:     "/staging-from-snapshot/original.sql",
		ArtifactDatabase: "appdb",
		ArtifactFormat:   "sql",
		TargetIsNew:      false,
		Logf:             func(string, string, ...any) {},
		Progress:         func(model.Progress) {},
		Exec:             exec,
	}
	deps := Deps{
		Exec:     exec,
		Logf:     func(string, string, ...any) {},
		Progress: func(model.Progress) {},
	}
	phase, err := rollbackDatabaseRestore(deps, restic.Options{Exe: "restic"}, tempDir, restorer, spec, "prot-snap-id")
	if err != nil {
		t.Fatalf("rollbackDatabaseRestore: %v", err)
	}
	if phase != model.RestorePhaseRolledBack {
		t.Fatalf("phase = %q, want %q", phase, model.RestorePhaseRolledBack)
	}
	if len(restorer.importedFiles) != 1 {
		t.Fatalf("Import calls = %d, want 1", len(restorer.importedFiles))
	}
	got := restorer.importedFiles[0]
	if !strings.HasSuffix(got, "protection.sql") {
		t.Fatalf("rollback must import the protection artifact, got %q", got)
	}
	if strings.Contains(got, "original.sql") {
		t.Fatalf("rollback must not re-import the original artifact, got %q", got)
	}
}

// multiDBManifestExecutor 模拟 `restic restore`：把包含多个库的清单写到 target。
type multiDBManifestExecutor struct{ manifest string }

func (e *multiDBManifestExecutor) Run(_ context.Context, cmd backup.Cmd, _, _ func(string)) (int, error) {
	if len(cmd.Args) > 0 && cmd.Args[0] == "restore" {
		target := ""
		for i, a := range cmd.Args {
			if a == "--target" && i+1 < len(cmd.Args) {
				target = cmd.Args[i+1]
			}
		}
		if target != "" {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return 1, err
			}
			if err := os.WriteFile(filepath.Join(target, "manifest.json"), []byte(e.manifest), 0o600); err != nil {
				return 1, err
			}
		}
	}
	return 0, nil
}

// 目标被触碰之前的失败（例：快照含多库、单库恢复不支持）必须上报安全阶段
// failed。否则服务端只能保守判为 manual_recovery_required，进而阻塞该仓库
// 的所有后继任务（实测：一次 unsupported_restore_manifest 让 8 个备份 + 快照
// 浏览全部卡在 queued，直到人工解决）。
func TestRunDatabaseRestorePreflightFailureReportsSafePhase(t *testing.T) {
	manifest := `{"adapter":"mysql","databases":[{"database":"a","file":"a.sql","format":"sql"},{"database":"b","file":"b.sql","format":"sql"}]}`
	exec := &multiDBManifestExecutor{manifest: manifest}
	deps := Deps{
		Exec:     exec,
		Logf:     func(string, string, ...any) {},
		Progress: func(model.Progress) {},
	}
	task := model.RestoreTask{
		RunID: "run-1",
		Kind:  "mysql",
		Database: &model.DatabaseRestore{
			SnapshotID: "snap-1", TargetHost: "h", TargetPort: 3306,
			TargetUsername: "u", TargetDatabase: "appdb",
		},
	}
	_, err := runDatabaseRestore(context.Background(), deps, restic.Options{Exe: "restic"},
		task, t.TempDir(), backup.SecretBundle{}, false)
	if err == nil {
		t.Fatal("multi-database manifest must be rejected")
	}
	var pe *PipelineError
	if !errors.As(err, &pe) {
		t.Fatalf("want PipelineError, got %T", err)
	}
	if pe.Code != model.ErrUnsupportedRestoreManifest {
		t.Fatalf("code = %q, want %q", pe.Code, model.ErrUnsupportedRestoreManifest)
	}
	if len(pe.ResultJSON) == 0 {
		t.Fatal("preflight failure must report a phase; otherwise the server blocks the whole repository")
	}
	var payload struct {
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal(pe.ResultJSON, &payload); err != nil {
		t.Fatalf("unmarshal result json: %v", err)
	}
	if payload.Phase != model.RestorePhaseFailed {
		t.Fatalf("phase = %q, want %q (failed = 目标未被修改)", payload.Phase, model.RestorePhaseFailed)
	}
}

// failingRestoreExecutor 模拟 `restic restore` 失败（含被取消杀进程的情形）。
type failingRestoreExecutor struct{}

func (failingRestoreExecutor) Run(_ context.Context, cmd backup.Cmd, _, _ func(string)) (int, error) {
	if len(cmd.Args) > 0 && cmd.Args[0] == "restore" {
		return 1, nil
	}
	return 0, nil
}

// 取消/失败发生在快照下载阶段时，目标尚未被触碰，必须上报安全阶段 failed，
// 否则服务端会判为 manual_recovery_required 并阻塞整个仓库（实测该阻塞会让
// 备份长期停在 queued、快照列表返回 504）。
func TestRunDatabaseRestoreCancelBeforeTargetTouchIsSafe(t *testing.T) {
	deps := Deps{
		Exec:     failingRestoreExecutor{},
		Logf:     func(string, string, ...any) {},
		Progress: func(model.Progress) {},
	}
	task := model.RestoreTask{
		RunID: "run-1",
		Kind:  "mysql",
		Database: &model.DatabaseRestore{
			SnapshotID: "snap-1", TargetHost: "h", TargetPort: 3306,
			TargetUsername: "u", TargetDatabase: "appdb",
		},
	}
	_, err := runDatabaseRestore(context.Background(), deps, restic.Options{Exe: "restic"},
		task, t.TempDir(), backup.SecretBundle{}, false)
	if err == nil {
		t.Fatal("snapshot download failure must abort the restore")
	}
	var pe *PipelineError
	if !errors.As(err, &pe) {
		t.Fatalf("want PipelineError, got %T", err)
	}
	var payload struct {
		Phase string `json:"phase"`
	}
	if len(pe.ResultJSON) == 0 {
		t.Fatal("failure before touching the target must report a phase")
	}
	if err := json.Unmarshal(pe.ResultJSON, &payload); err != nil {
		t.Fatalf("unmarshal result json: %v", err)
	}
	if payload.Phase != model.RestorePhaseFailed {
		t.Fatalf("phase = %q, want %q", payload.Phase, model.RestorePhaseFailed)
	}
}

// 文件系统恢复的前置失败同样必须上报安全阶段：overwrite_mode=never 命中非空目标
// 只是读目录判断，目标完全未被触碰，却曾被判为 manual_recovery_required 并阻塞
// 整个仓库（实测：紧随其后的另一次恢复长期停在 queued）。
func TestRunFilesystemRestorePreflightFailureReportsSafePhase(t *testing.T) {
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "existing.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	deps := Deps{
		Exec:     &protectionRestoreExecutor{},
		Logf:     func(string, string, ...any) {},
		Progress: func(model.Progress) {},
	}
	task := model.RestoreTask{
		RunID: "run-1",
		Kind:  "filesystem",
		Filesystem: &model.FilesystemRestore{
			SnapshotID: "snap-1", TargetPath: target, OverwriteMode: "never",
		},
	}
	_, err := runFilesystemRestore(context.Background(), deps, restic.Options{Exe: "restic"}, task, false)
	if err == nil {
		t.Fatal("non-empty target with overwrite_mode=never must be rejected")
	}
	var pe *PipelineError
	if !errors.As(err, &pe) {
		t.Fatalf("want PipelineError, got %T", err)
	}
	if pe.Code != "restore_target_not_empty" {
		t.Fatalf("code = %q, want restore_target_not_empty", pe.Code)
	}
	var payload struct {
		Phase string `json:"phase"`
	}
	if len(pe.ResultJSON) == 0 {
		t.Fatal("preflight failure must report a phase; otherwise the whole repository is blocked")
	}
	if err := json.Unmarshal(pe.ResultJSON, &payload); err != nil {
		t.Fatalf("unmarshal result json: %v", err)
	}
	if payload.Phase != model.RestorePhaseFailed {
		t.Fatalf("phase = %q, want %q", payload.Phase, model.RestorePhaseFailed)
	}
}
