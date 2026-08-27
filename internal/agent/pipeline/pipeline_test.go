package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"backupmanagementcenter/internal/agent/backup"
	"backupmanagementcenter/internal/model"
)

func TestRunVerifyRemote_NilExecutorReturnsError(t *testing.T) {
	params, err := json.Marshal(model.VerifyRemoteTask{ConfigProvided: true, RemoteName: "backup"})
	if err != nil { t.Fatal(err) }
	_, err = runVerifyRemote(context.Background(), Deps{}, t.TempDir(), params, backup.SecretBundle{RcloneConf: "[backup]\ntype = local\n"})
	if err == nil { t.Fatal("expected verify remote error") }
	var pipelineErr *PipelineError
	if !errors.As(err, &pipelineErr) { t.Fatalf("expected PipelineError, got %T: %v", err, err) }
	if pipelineErr.Code != "storage_remote_unreachable" { t.Fatalf("unexpected error code: %s", pipelineErr.Code) }
	if !strings.Contains(err.Error(), "executor is nil") { t.Fatalf("missing executor detail: %v", err) }
}

func TestNewResticOptsUsesPersistentCache(t *testing.T) {
	tempDir := t.TempDir()
	cacheDir := filepath.Join(t.TempDir(), "restic-cache")
	opts, err := newResticOpts(Deps{ResticCacheDir: cacheDir}, "/repo", tempDir, backup.SecretBundle{ResticPassword: "pw", RcloneConf: "[remote]\ntype = local\n"})
	if err != nil { t.Fatal(err) }
	if opts.CacheDir != cacheDir { t.Fatalf("cache dir = %q, want %q", opts.CacheDir, cacheDir) }
	if !strings.HasPrefix(opts.PasswordFile, tempDir) || !strings.HasPrefix(opts.RcloneConfFile, tempDir) { t.Fatalf("secret files outside temp dir: %#v", opts) }
	if strings.HasPrefix(opts.CacheDir, tempDir) { t.Fatalf("cache dir must not be in temp dir: %q", opts.CacheDir) }
}
