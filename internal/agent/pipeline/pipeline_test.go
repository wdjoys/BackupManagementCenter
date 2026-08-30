package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"backupmanagementcenter/internal/agent/backup"
	"backupmanagementcenter/internal/model"
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
	want := []string{"forget", "abc123def456", "--prune", "--retry-lock", "5m", "--repo", "rclone:remote:/repo"}
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

	if err := mapBackupSource(&task, mappings); err != nil {
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
	if err := mapBackupSource(&task, []model.PathMapping{mapping}); err == nil {
		t.Fatal("expected unmapped path error")
	}
}

func TestMapBackupSourceMapsSQLitePath(t *testing.T) {
	hostRoot := filepath.Join(t.TempDir(), "host")
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	task := model.BackupTask{Kind: model.KindSQLite, Source: model.PlanSource{Path: filepath.Join(hostRoot, "db", "app.sqlite")}}
	if err := mapBackupSource(&task, []model.PathMapping{{HostPath: hostRoot, RuntimePath: runtimeRoot}}); err != nil {
		t.Fatal(err)
	}
	if got, want := task.Source.Path, filepath.Join(runtimeRoot, "db", "app.sqlite"); got != want {
		t.Fatalf("sqlite path = %q, want %q", got, want)
	}
}
