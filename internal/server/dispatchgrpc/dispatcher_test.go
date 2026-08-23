package dispatchgrpc

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	bmcv1 "backupmanagementcenter/api/proto/v1"
	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/server/agentreg"
	"backupmanagementcenter/internal/server/jobs"
	"backupmanagementcenter/internal/server/store"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeStore embeds the nil Store interface: only methods exercised by
// processJob / checkTimeouts are implemented; anything else panics loudly.
type fakeStore struct {
	store.Store
	mu              sync.Mutex
	runs            map[string]*model.Run
	transitionError error
}

func newFakeStore() *fakeStore { return &fakeStore{runs: make(map[string]*model.Run)} }

func (f *fakeStore) addRun(r model.Run) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := r
	f.runs[r.ID] = &cp
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

func (f *fakeStore) GetPlan(_ context.Context, _ string) (*model.Plan, error) {
	return nil, store.ErrNotFound
}

func (f *fakeStore) MaxRunLogSeq(_ context.Context, _ string) (uint64, error) {
	return 0, nil
}

func (f *fakeStore) AppendRunLogs(_ context.Context, _ []model.RunLog) error {
	return nil
}

func (f *fakeStore) ListRunsByStatus(_ context.Context, statuses []string) ([]model.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	set := make(map[string]bool, len(statuses))
	for _, s := range statuses {
		set[s] = true
	}
	var out []model.Run
	for _, r := range f.runs {
		if set[r.Status] {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (f *fakeStore) TransitionRun(_ context.Context, id, from, to string, mutate func(*model.Run)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.transitionError != nil {
		return f.transitionError
	}
	r, ok := f.runs[id]
	if !ok {
		return store.ErrNotFound
	}
	if r.Status != from {
		return store.ErrInvalidTransition
	}
	if mutate != nil {
		mutate(r)
	}
	r.Status = to
	return nil
}

type fakeSource struct {
	err error // non-nil -> BuildCommand fails
}

func (s *fakeSource) BuildCommand(context.Context, string) (string, *bmcv1.ExecuteCommand, error) {
	if s.err != nil {
		return "", nil, s.err
	}
	return "cmd-1", &bmcv1.ExecuteCommand{}, nil
}

type recordingNotifier struct {
	mu  sync.Mutex
	ids []string
}

func (r *recordingNotifier) NotifyPlanFailure(_ context.Context, runID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, runID)
	return nil
}

func (r *recordingNotifier) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

func newTestDispatcher(st store.Store, src jobs.CommandSource) (*Dispatcher, *recordingNotifier) {
	notifier := &recordingNotifier{}
	d := NewDispatcher(st, agentreg.NewRegistry(), DefaultConfig(), notifier)
	d.Src = src
	return d, notifier
}

func queuedRun(id string) model.Run {
	now := time.Now().UTC().Add(-time.Hour)
	return model.Run{
		ID: id, PlanID: "plan-1", AgentID: "agent-1",
		Operation: model.OpBackup, Status: model.RunQueued,
		QueuedAt: now, ProgressJSON: "{}",
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestProcessJobBuildFailureNotifiesOnce(t *testing.T) {
	st := newFakeStore()
	st.addRun(queuedRun("run-1"))
	src := &fakeSource{err: errors.New("repository password mismatch")}
	d, notifier := newTestDispatcher(st, src)

	// Agent must look connected so processJob reaches the build step.
	reg := agentreg.NewRegistry()
	_, _ = reg.Register(context.Background(), "agent-1")
	d.reg = reg

	j := &job{runID: "run-1", agentID: "agent-1", repositoryID: "repo-1"}
	d.processJob(j)

	run := st.snapshot("run-1")
	if run == nil || run.Status != model.RunFailed || run.ErrorCode != model.ErrInvalidPlan {
		t.Fatalf("run not persisted as invalid_plan failure: %+v", run)
	}
	got := notifier.calls()
	if len(got) != 1 || got[0] != "run-1" {
		t.Fatalf("expected exactly one notification for run-1, got %v", got)
	}
}

func TestProcessJobClaimFailureRequeuesInsteadOfOrphaning(t *testing.T) {
	st := newFakeStore()
	st.addRun(queuedRun("run-lock"))
	st.transitionError = errors.New("transition run update: database is locked (5) (SQLITE_BUSY)")
	d, _ := newTestDispatcher(st, &fakeSource{})
	d.cfg.ClaimRetryDelay = time.Nanosecond
	d.cfg.OfflineRetryDelay = 0
	reg := agentreg.NewRegistry()
	_, _ = reg.Register(context.Background(), "agent-1")
	d.reg = reg

	j := &job{runID: "run-lock", agentID: "agent-1", repositoryID: "repo-1"}
	d.mu.Lock()
	d.enqueuedRuns[j.runID] = true
	rq := &repoQueue{jobs: make([]*job, 0), workerCh: make(chan struct{}, 1), stopCh: make(chan struct{})}
	d.repoQueues[j.repositoryID] = rq
	d.mu.Unlock()
	d.processJob(j)

	rq.mu.Lock()
	defer rq.mu.Unlock()
	if len(rq.jobs) != 1 || rq.jobs[0].runID != j.runID {
		t.Fatalf("claim failure orphaned the run instead of requeueing: %+v", rq.jobs)
	}
	if !d.enqueuedRuns[j.runID] {
		t.Fatalf("requeued run lost idempotency marker")
	}
}

func TestCheckTimeoutsRunningTimeoutNotifies(t *testing.T) {
	st := newFakeStore()
	r := queuedRun("run-r")
	r.Status = model.RunRunning
	started := time.Now().UTC().Add(-2 * time.Hour)
	r.StartedAt = &started
	st.addRun(r)

	d, notifier := newTestDispatcher(st, &fakeSource{})

	d.checkTimeouts()

	run := st.snapshot("run-r")
	if run == nil || run.Status != model.RunFailed || run.ErrorCode != model.ErrTimeout {
		t.Fatalf("running run not timed out to failed: %+v", run)
	}
	got := notifier.calls()
	if len(got) != 1 || got[0] != "run-r" {
		t.Fatalf("expected exactly one notification for run-r, got %v", got)
	}

	// Rescan sees no dispatched/running runs: no duplicate notification.
	d.checkTimeouts()
	if got := notifier.calls(); len(got) != 1 {
		t.Fatalf("rescan re-notified: %v", got)
	}
}

func TestCheckTimeoutsDispatchedTimeoutNotifies(t *testing.T) {
	st := newFakeStore()
	r := queuedRun("run-d") // QueuedAt one hour ago exceeds the 300s default
	r.Status = model.RunDispatched
	st.addRun(r)

	// Agent disconnected: cancel-send skipped, transition still happens.
	d, notifier := newTestDispatcher(st, &fakeSource{})

	d.checkTimeouts()

	run := st.snapshot("run-d")
	if run == nil || run.Status != model.RunFailed || run.ErrorCode != model.ErrTimeout {
		t.Fatalf("dispatched run not timed out to failed: %+v", run)
	}
	got := notifier.calls()
	if len(got) != 1 || got[0] != "run-d" {
		t.Fatalf("expected exactly one notification for run-d, got %v", got)
	}
}
