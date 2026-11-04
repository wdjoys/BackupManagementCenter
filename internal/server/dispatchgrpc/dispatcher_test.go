package dispatchgrpc

import (
	"context"
	"errors"
	"strings"
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
	mu               sync.Mutex
	runs             map[string]*model.Run
	restoreRequests  map[string]*model.RestoreRequest // by run id
	stopDeadlines    map[string]time.Time
	finishedRestores []store.FinishRestoreRunInput
	repoBlocked      bool
	transitionError  error
	logs             []model.RunLog
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		runs:            make(map[string]*model.Run),
		restoreRequests: make(map[string]*model.RestoreRequest),
		stopDeadlines:   make(map[string]time.Time),
	}
}

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

func (f *fakeStore) GetRestoreRequestByRunID(_ context.Context, runID string) (*model.RestoreRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if rr, ok := f.restoreRequests[runID]; ok {
		cp := *rr
		return &cp, nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) RepositoryRestoreBlocked(_ context.Context, _, _ string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.repoBlocked, nil
}

func (f *fakeStore) RequestRestoreStop(_ context.Context, runID string, deadline time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopDeadlines[runID] = deadline
	return nil
}

func (f *fakeStore) FinishRestoreRun(_ context.Context, in store.FinishRestoreRunInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.runs[in.RunID]
	if !ok {
		return store.ErrNotFound
	}
	if r.Status == model.RunSucceeded || r.Status == model.RunFailed || r.Status == model.RunCancelled {
		return nil
	}
	r.Status = in.ToStatus
	finished := in.FinishedAt
	r.FinishedAt = &finished
	r.ErrorCode = in.ErrorCode
	r.ErrorMessage = in.ErrorMessage
	if rr, ok := f.restoreRequests[in.RunID]; ok && in.Phase != "" {
		rr.Phase = in.Phase
	}
	f.finishedRestores = append(f.finishedRestores, in)
	return nil
}

func (f *fakeStore) DeleteRunSecrets(context.Context, string) error { return nil }

func (f *fakeStore) ProtectedRestoreSnapshotIDs(context.Context, string) (map[string]struct{}, error) {
	return nil, nil
}

func (f *fakeStore) ActiveDatabaseRestoreRunID(context.Context) (string, error) { return "", nil }

func (f *fakeStore) CreateDatabaseRestoreRun(context.Context, *model.Run, *model.RestoreRequest) error {
	return nil
}

func (f *fakeStore) UpdateRestoreRollbackSnapshot(context.Context, string, string) error { return nil }

func (f *fakeStore) ResolveRestoreRequest(context.Context, string, string, string, string, time.Time) error {
	return nil
}

func (f *fakeStore) PruneHistory(context.Context, time.Time) (store.HistoryPruneResult, error) {
	return store.HistoryPruneResult{}, nil
}

func (f *fakeStore) AppendRunLogs(_ context.Context, logs []model.RunLog) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, logs...)
	return nil
}

func (f *fakeStore) logsFor(runID string) []model.RunLog {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.RunLog
	for _, l := range f.logs {
		if l.RunID == runID {
			out = append(out, l)
		}
	}
	return out
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

func TestRetryLeasePreservesStartAndTimeoutUsesIt(t *testing.T) {
	st := newFakeStore()
	started := time.Now().UTC().Add(-time.Minute)
	expired := time.Now().UTC().Add(-time.Second)
	run := queuedRun("run-retry")
	run.Status = model.RunDispatched
	run.StartedAt = &started
	run.LeaseExpiresAt = &expired
	st.addRun(run)
	d, _ := newTestDispatcher(st, &fakeSource{})
	d.enqueuedRuns[run.ID] = true // 模拟仍被现有仓库 worker 持有
	d.checkTimeouts()
	got := st.snapshot(run.ID)
	if got.Status != model.RunQueued || got.StartedAt == nil || !got.StartedAt.Equal(started) {
		t.Fatalf("租约过期重排应保留首次开始时间: %+v", got)
	}
	lease := time.Now().UTC().Add(time.Minute)
	if err := st.TransitionRun(context.Background(), run.ID, model.RunQueued, model.RunDispatched, func(r *model.Run) {
		r.LeaseExpiresAt = &lease
	}); err != nil {
		t.Fatal(err)
	}
	d.checkTimeouts()
	if got := st.snapshot(run.ID); got.Status != model.RunDispatched {
		t.Fatalf("尚未超过首次开始时间后的超时，不应按更早的入队时间失败: %+v", got)
	}
	old := time.Now().UTC().Add(-10 * time.Minute)
	overdue := queuedRun("run-overdue")
	overdue.Status = model.RunDispatched
	overdue.StartedAt = &old
	overdue.LeaseExpiresAt = &lease
	st.addRun(overdue)
	d.checkTimeouts()
	if got := st.snapshot(overdue.ID); got.Status != model.RunFailed || got.ErrorCode != model.ErrTimeout {
		t.Fatalf("重派仍需按首次开始时间超时: %+v", got)
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

// 运行中超时必须同时通知 agent 停止，否则 dump/上传会继续跑完并创建快照，
// 而 run 已是终态 failed，产出 run 记录里看不到的孤儿快照。
func TestCheckTimeoutsRunningTimeoutCancelsAgent(t *testing.T) {
	st := newFakeStore()
	r := queuedRun("run-cancel")
	r.Status = model.RunRunning
	started := time.Now().UTC().Add(-2 * time.Hour)
	r.StartedAt = &started
	st.addRun(r)

	d, _ := newTestDispatcher(st, &fakeSource{})
	reg := agentreg.NewRegistry()
	sendCh, _ := reg.Register(context.Background(), "agent-1")
	d.reg = reg

	d.checkTimeouts()

	select {
	case msg := <-sendCh:
		if msg.GetCancelCommand().GetRunId() != "run-cancel" {
			t.Fatalf("expected cancel command for run-cancel, got %+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("running run timed out without telling the agent to stop (orphan snapshot risk)")
	}
	if run := st.snapshot("run-cancel"); run == nil || run.Status != model.RunFailed || run.ErrorCode != model.ErrTimeout {
		t.Fatalf("run not marked as timeout failure: %+v", run)
	}
}

// 仓库校验在大仓库上本就超过 5 分钟：watchdog 必须按 check 的 30 分钟上限，
// 而不是系统任务默认 300s，否则运行会被误杀；且超时失败必须留下可读日志，
// 否则界面上只有一条没有明细的「失败」。
func TestCheckTimeoutsUsesCheckTimeoutAndLogs(t *testing.T) {
	st := newFakeStore()
	started := time.Now().UTC().Add(-10 * time.Minute)
	r := queuedRun("run-check")
	r.PlanID = ""
	r.Operation = model.OpCheck
	r.Status = model.RunRunning
	r.StartedAt = &started
	st.addRun(r)

	d, _ := newTestDispatcher(st, &fakeSource{})
	d.checkTimeouts()
	if got := st.snapshot("run-check"); got.Status != model.RunRunning {
		t.Fatalf("10 分钟的仓库校验不应按 5 分钟超时: %+v", got)
	}

	long := time.Now().UTC().Add(-31 * time.Minute)
	overdue := queuedRun("run-check-overdue")
	overdue.PlanID = ""
	overdue.Operation = model.OpCheck
	overdue.Status = model.RunRunning
	overdue.StartedAt = &long
	st.addRun(overdue)

	d.checkTimeouts()
	got := st.snapshot("run-check-overdue")
	if got.Status != model.RunFailed || got.ErrorCode != model.ErrTimeout {
		t.Fatalf("超过 30 分钟的仓库校验应超时失败: %+v", got)
	}
	logs := st.logsFor("run-check-overdue")
	if len(logs) == 0 || !strings.Contains(logs[0].Message, "运行超时") {
		t.Fatalf("超时失败必须留下可读日志: %+v", logs)
	}
}
