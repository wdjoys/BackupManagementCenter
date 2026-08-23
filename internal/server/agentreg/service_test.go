package agentreg

import (
	"context"
	"sync"
	"testing"
	"time"

	bmcv1 "backupmanagementcenter/api/proto/v1"
	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/server/events"
	"backupmanagementcenter/internal/server/store"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeStore embeds the nil Store interface: only methods exercised by
// handleRunResult are implemented; any other call panics loudly.
type fakeStore struct {
	store.Store
	mu           sync.Mutex
	runs         map[string]*model.Run
	repoStatuses map[string]string
	repoChecks   map[string]time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		runs:         make(map[string]*model.Run),
		repoStatuses: make(map[string]string),
		repoChecks:   make(map[string]time.Time),
	}
}

func (f *fakeStore) addRun(r model.Run) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := r
	f.runs[r.ID] = &cp
}

func (f *fakeStore) GetPlan(_ context.Context, _ string) (*model.Plan, error) {
	return nil, store.ErrNotFound
}

func (f *fakeStore) snapshot(id string) *model.Run {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.runs[id]; ok {
		cp := *r
		return &cp
	}
	return nil
}

func (f *fakeStore) GetRun(_ context.Context, id string) (*model.Run, error) {
	if r := f.snapshot(id); r != nil {
		return r, nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) TransitionRun(_ context.Context, id, from, to string, mutate func(*model.Run)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.runs[id]
	if !ok {
		return store.ErrNotFound
	}
	if r.Status != from {
		return store.ErrInvalidTransition
	}
	mutate(r)
	r.Status = to
	return nil
}

func (f *fakeStore) UpdateRepositoryStatus(_ context.Context, id, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repoStatuses[id] = status
	return nil
}

func (f *fakeStore) MarkRepositoryChecked(_ context.Context, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repoChecks[id] = at
	return nil
}

type recordingNotifier struct {
	mu  sync.Mutex
	ids []string
	err error
}

func (r *recordingNotifier) NotifyPlanFailure(_ context.Context, runID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, runID)
	return r.err
}

func (r *recordingNotifier) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

func newTestService(st *fakeStore) (*Service, *recordingNotifier) {
	rec := &recordingNotifier{}
	svc := NewService(st, NewRegistry(), events.New(), DefaultConfig(), rec)
	return svc, rec
}

func failedResult(runID string) *bmcv1.RunResult {
	return &bmcv1.RunResult{
		RunId:        runID,
		Status:       bmcv1.RunResult_FAILED,
		ErrorCode:    "restic_backup_failed",
		ErrorMessage: "snapshot exited 3",
	}
}

func TestIsRepositoryInitRun(t *testing.T) {
	initRun := model.Run{
		Operation:    model.OpForget,
		RepositoryID: "repo-1",
		ProgressJSON: `{"repository":{"repository_path":"rclone:remote:/bmc"},"restic_init":true}`,
	}
	if !isRepositoryInitRun(&initRun) {
		t.Fatal("expected restic init run to be recognized")
	}

	retentionRun := initRun
	retentionRun.ProgressJSON = `{"repository":{"repository_path":"rclone:remote:/bmc"},"retention":{"keep_last":3}}`
	if isRepositoryInitRun(&retentionRun) {
		t.Fatal("retention run must not be treated as repository init")
	}

	missingRepo := initRun
	missingRepo.RepositoryID = ""
	if isRepositoryInitRun(&missingRepo) {
		t.Fatal("init without repository must not be reconciled")
	}
}

func TestHandleInitRunReconcilesRepositoryStatus(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   bmcv1.RunResult_Status
		expected string
	}{
		{name: "success", status: bmcv1.RunResult_SUCCEEDED, expected: "ready"},
		{name: "failure", status: bmcv1.RunResult_FAILED, expected: "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newFakeStore()
			run := model.Run{
				ID:           "init-run-" + tc.name,
				AgentID:      "agent-1",
				Operation:    model.OpForget,
				Status:       model.RunRunning,
				RepositoryID: "repo-1",
				ProgressJSON: `{"repository":{"repository_path":"rclone:remote:/bmc"},"restic_init":true}`,
				QueuedAt:     time.Now().UTC(),
			}
			st.addRun(run)
			svc, _ := newTestService(st)
			result := &bmcv1.RunResult{RunId: run.ID, Status: tc.status}
			if err := svc.handleRunResult(context.Background(), "agent-1", result); err != nil {
				t.Fatalf("handleRunResult: %v", err)
			}
			st.mu.Lock()
			got := st.repoStatuses[run.RepositoryID]
			st.mu.Unlock()
			if got != tc.expected {
				t.Fatalf("repository status = %q, want %q", got, tc.expected)
			}
		})
	}
}

func TestHandleRunResultCheckMarksSystemRepository(t *testing.T) {
	st := newFakeStore()
	run := model.Run{
		ID:           "check-run-1",
		AgentID:      "agent-1",
		Operation:    model.OpCheck,
		RepositoryID: "repo-1",
		Status:       model.RunRunning,
		QueuedAt:     time.Now().UTC(),
		ProgressJSON: `{"repository":{"repository_path":"rclone:remote:/bmc"}}`,
	}
	st.addRun(run)
	svc, _ := newTestService(st)
	if err := svc.handleRunResult(context.Background(), "agent-1", &bmcv1.RunResult{
		RunId: run.ID,
		Status: bmcv1.RunResult_SUCCEEDED,
	}); err != nil {
		t.Fatalf("handleRunResult: %v", err)
	}
	st.mu.Lock()
	_, ok := st.repoChecks[run.RepositoryID]
	st.mu.Unlock()
	if !ok {
		t.Fatalf("successful system check did not update repository %s", run.RepositoryID)
	}
}

func baseRun(id, planID, status string) model.Run {
	now := time.Now().UTC()
	return model.Run{
		ID: id, PlanID: planID, AgentID: "agent-1",
		Operation: model.OpBackup, Status: status,
		QueuedAt: now, ProgressJSON: "{}",
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestHandleRunResultRunningFailureNotifies(t *testing.T) {
	st := newFakeStore()
	st.addRun(baseRun("run-1", "plan-1", model.RunRunning))
	svc, notifier := newTestService(st)

	if err := svc.handleRunResult(context.Background(), "agent-1", failedResult("run-1")); err != nil {
		t.Fatalf("handleRunResult: %v", err)
	}

	run := st.snapshot("run-1")
	if run == nil || run.Status != model.RunFailed || run.ErrorCode != "restic_backup_failed" {
		t.Fatalf("run not persisted as failed: %+v", run)
	}
	got := notifier.calls()
	if len(got) != 1 || got[0] != "run-1" {
		t.Fatalf("expected exactly one notification for run-1, got %v", got)
	}
}

func TestHandleRunResultFastFinishedDispatchedFailureNotifies(t *testing.T) {
	st := newFakeStore()
	st.addRun(baseRun("run-2", "plan-1", model.RunDispatched))
	svc, notifier := newTestService(st)

	if err := svc.handleRunResult(context.Background(), "agent-1", failedResult("run-2")); err != nil {
		t.Fatalf("handleRunResult: %v", err)
	}

	run := st.snapshot("run-2")
	if run == nil || run.Status != model.RunFailed {
		t.Fatalf("fast-finished run not persisted as failed: %+v", run)
	}
	if run.StartedAt == nil || run.FinishedAt == nil {
		t.Fatalf("fast-finish timestamps not set: %+v", run)
	}
	got := notifier.calls()
	if len(got) != 1 || got[0] != "run-2" {
		t.Fatalf("expected exactly one notification for run-2, got %v", got)
	}
}

func TestHandleRunResultDuplicateResultNotifiesOnce(t *testing.T) {
	st := newFakeStore()
	st.addRun(baseRun("run-3", "plan-1", model.RunRunning))
	svc, notifier := newTestService(st)

	if err := svc.handleRunResult(context.Background(), "agent-1", failedResult("run-3")); err != nil {
		t.Fatalf("first handleRunResult: %v", err)
	}
	// Duplicate delivery (e.g. agent retry): idempotent no-op.
	if err := svc.handleRunResult(context.Background(), "agent-1", failedResult("run-3")); err != nil {
		t.Fatalf("duplicate handleRunResult must be nil, got %v", err)
	}

	run := st.snapshot("run-3")
	if run.ErrorCode != "restic_backup_failed" {
		t.Fatalf("duplicate result mutated stored run: %+v", run)
	}
	if got := notifier.calls(); len(got) != 1 {
		t.Fatalf("expected exactly one notification across duplicates, got %v", got)
	}
}

func TestHandleRunResultSuccessDoesNotNotify(t *testing.T) {
	st := newFakeStore()
	st.addRun(baseRun("run-4", "plan-1", model.RunRunning))
	svc, notifier := newTestService(st)

	res := failedResult("run-4")
	res.Status = bmcv1.RunResult_SUCCEEDED
	if err := svc.handleRunResult(context.Background(), "agent-1", res); err != nil {
		t.Fatalf("handleRunResult: %v", err)
	}
	if got := notifier.calls(); len(got) != 0 {
		t.Fatalf("succeeded run must not notify, got %v", got)
	}
}

func TestHandleRunResultNotifierErrorKeepsStoredFailure(t *testing.T) {
	st := newFakeStore()
	st.addRun(baseRun("run-5", "plan-1", model.RunRunning))
	svc, notifier := newTestService(st)
	notifier.err = context.DeadlineExceeded

	if err := svc.handleRunResult(context.Background(), "agent-1", failedResult("run-5")); err != nil {
		t.Fatalf("notification failure must not fail the result handler, got %v", err)
	}
	run := st.snapshot("run-5")
	if run.Status != model.RunFailed || run.ErrorMessage != "snapshot exited 3" {
		t.Fatalf("stored failure changed after notifier error: %+v", run)
	}
}
