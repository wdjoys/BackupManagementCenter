package pipeline

import (
	"context"
	"encoding/json"
	"errors"
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

	_, err = runVerifyRemote(
		context.Background(),
		Deps{},
		t.TempDir(),
		params,
		backup.SecretBundle{RcloneConf: "[backup]\ntype = local\n"},
	)
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
