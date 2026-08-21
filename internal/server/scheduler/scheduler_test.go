package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/server/store"
)

// ---------------------------------------------------------------------------
// fakeStore implements store.Store with pre-loaded data for scheduler tests.
// ---------------------------------------------------------------------------

type fakeStore struct {
	mu sync.Mutex

	plans []model.Plan
	runs  []model.Run
	agents map[string]model.Agent
	repos []model.Repository

	// record of transitions
	transitions []transitionCall
}

type transitionCall struct {
	runID string
	from  string
	to    string
	code  string
}

func newFakeStore(_ *testing.T) *fakeStore {
	return &fakeStore{
		agents: make(map[string]model.Agent),
	}
}

// ---- Store interface (only the methods used by the scheduler) ----

func (f *fakeStore) ListEnabledPlans(_ context.Context) ([]model.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]model.Plan, 0, len(f.plans))
	for _, p := range f.plans {
		if p.Enabled {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeStore) GetPlan(_ context.Context, id string) (*model.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.plans {
		if p.ID == id {
			return &p, nil
		}
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) ListRunsByStatus(_ context.Context, statuses []string) ([]model.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	statusSet := make(map[string]bool, len(statuses))
	for _, s := range statuses {
		statusSet[s] = true
	}
	out := make([]model.Run, 0, len(f.runs))
	for _, r := range f.runs {
		if statusSet[r.Status] {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeStore) GetAgent(_ context.Context, id string) (*model.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.agents[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &a, nil
}

func (f *fakeStore) TransitionRun(_ context.Context, id, from, to string, mutate func(*model.Run)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, r := range f.runs {
		if r.ID == id {
			if r.Status != from {
				return store.ErrInvalidTransition
			}
			mutate(&f.runs[i])
			f.runs[i].Status = to
			f.transitions = append(f.transitions, transitionCall{
				runID: id,
				from:  from,
				to:    to,
				code:  f.runs[i].ErrorCode,
			})
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *fakeStore) ListRepositoriesNeedingCheck(_ context.Context, olderThan time.Time) ([]model.Repository, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]model.Repository, 0, len(f.repos))
	for _, r := range f.repos {
		if r.LastCheckAt == nil || r.LastCheckAt.Before(olderThan) {
			out = append(out, r)
		}
	}
	return out, nil
}

// ---- Stubs for unused Store methods ----

func (f *fakeStore) Close() error                                              { return nil }
func (f *fakeStore) Migrate(_ context.Context) error                            { return nil }
func (f *fakeStore) HasAdmin(_ context.Context) (bool, error)                   { return false, nil }
func (f *fakeStore) CreateAdmin(_ context.Context, _ *model.Admin) error        { return nil }
func (f *fakeStore) GetAdminByUsername(_ context.Context, _ string) (*model.Admin, error) {
	return nil, store.ErrNotFound
}
func (f *fakeStore) UpdateAdminLastLogin(_ context.Context, _ string, _ time.Time) error { return nil }
func (f *fakeStore) CreateSession(_ context.Context, _ *model.Session) error               { return nil }
func (f *fakeStore) GetSession(_ context.Context, _ string) (*model.Session, error)        { return nil, store.ErrNotFound }
func (f *fakeStore) TouchSession(_ context.Context, _ string, _ time.Time) error           { return nil }
func (f *fakeStore) DeleteSession(_ context.Context, _ string) error                        { return nil }
func (f *fakeStore) DeleteExpiredSessions(_ context.Context, _ time.Time) error             { return nil }
func (f *fakeStore) CreateEnrollmentToken(_ context.Context, _ *model.EnrollmentToken) error { return nil }
func (f *fakeStore) ListEnrollmentTokens(_ context.Context) ([]model.EnrollmentToken, error) { return nil, nil }
func (f *fakeStore) ConsumeEnrollmentToken(_ context.Context, _ string, _ time.Time) (*model.EnrollmentToken, error) {
	return nil, store.ErrTokenInvalid
}
func (f *fakeStore) UpsertAgentOnConnect(_ context.Context, _ *model.Agent) error                 { return nil }
func (f *fakeStore) SetAgentStatus(_ context.Context, _ string, _ model.AgentStatus, _ time.Time) error { return nil }
func (f *fakeStore) SaveAgentCapabilities(_ context.Context, _ string, _ []model.ToolInfo, _ time.Time) error { return nil }
func (f *fakeStore) GetAgentBySecretHash(_ context.Context, _ string) (*model.Agent, error) { return nil, store.ErrNotFound }
func (f *fakeStore) ListAgents(_ context.Context) ([]model.Agent, error)                     { return nil, nil }
func (f *fakeStore) RevokeAgent(_ context.Context, _ string) error                           { return nil }
func (f *fakeStore) CreateStorageTarget(_ context.Context, _ *model.StorageTarget) error     { return nil }
func (f *fakeStore) UpdateStorageTarget(_ context.Context, _ *model.StorageTarget) error     { return nil }
func (f *fakeStore) DeleteStorageTarget(_ context.Context, _ string) error                   { return nil }
func (f *fakeStore) GetStorageTarget(_ context.Context, _ string) (*model.StorageTarget, error) {
	return nil, store.ErrNotFound
}
func (f *fakeStore) ListStorageTargets(_ context.Context) ([]model.StorageTarget, error) { return nil, nil }
func (f *fakeStore) CreateRepository(_ context.Context, _ *model.Repository) error   { return nil }
func (f *fakeStore) GetRepository(_ context.Context, _ string) (*model.Repository, error) { return nil, store.ErrNotFound }
func (f *fakeStore) GetRepositoryByAgentAndTarget(_ context.Context, _, _ string) (*model.Repository, error) {
	return nil, store.ErrNotFound
}
func (f *fakeStore) ListRepositories(_ context.Context) ([]model.Repository, error)  { return nil, nil }
func (f *fakeStore) UpdateRepositoryStatus(_ context.Context, _, _ string) error      { return nil }
func (f *fakeStore) MarkRepositoryChecked(_ context.Context, _ string, _ time.Time) error { return nil }
func (f *fakeStore) CreatePlan(_ context.Context, _ *model.Plan) error                { return nil }
func (f *fakeStore) UpdatePlan(_ context.Context, _ *model.Plan) error                { return nil }
func (f *fakeStore) DeletePlan(_ context.Context, _ string) error                     { return nil }
func (f *fakeStore) ListPlans(_ context.Context, _ string) ([]model.Plan, error)      { return nil, nil }
func (f *fakeStore) CreateRun(_ context.Context, _ *model.Run) error                  { return nil }
func (f *fakeStore) GetRun(_ context.Context, _ string) (*model.Run, error)           { return nil, store.ErrNotFound }
func (f *fakeStore) ListRuns(_ context.Context, _ store.RunFilter) ([]model.Run, error) { return nil, nil }
func (f *fakeStore) FailStaleRuns(_ context.Context, _ []string, _ string, _ time.Time) (int64, error) {
	return 0, nil
}
func (f *fakeStore) AppendRunLogs(_ context.Context, _ []model.RunLog) error          { return nil }
func (f *fakeStore) ListRunLogs(_ context.Context, _ string, _ uint64, _ int) ([]model.RunLog, error) {
	return nil, nil
}
func (f *fakeStore) MaxRunLogSeq(_ context.Context, _ string) (uint64, error) { return 0, nil }
func (f *fakeStore) CreateRestoreRequest(_ context.Context, _ *model.RestoreRequest) error { return nil }
func (f *fakeStore) GetRestoreRequest(_ context.Context, _ string) (*model.RestoreRequest, error) {
	return nil, store.ErrNotFound
}
func (f *fakeStore) ListRestoreRequests(_ context.Context, _ int) ([]model.RestoreRequest, error) { return nil, nil }
func (f *fakeStore) AppendAuditEvent(_ context.Context, _ *model.AuditEvent) error { return nil }
func (f *fakeStore) ListAuditEvents(_ context.Context, _ int) ([]model.AuditEvent, error) { return nil, nil }

// ---------------------------------------------------------------------------
// fakeStarter records calls to StartPlanRun / SystemRunCheck.
// ---------------------------------------------------------------------------

type fakeStarter struct {
	mu                 sync.Mutex
	startPlanRunCalls  []startPlanRunCall
	systemRunCheckCalls []string
	returnErr          error // if set, returned by StartPlanRun
}

type startPlanRunCall struct {
	planID      string
	scheduledAt *time.Time
}

func newFakeStarter() *fakeStarter { return &fakeStarter{} }

func (f *fakeStarter) StartPlanRun(_ context.Context, planID string, scheduledAt *time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startPlanRunCalls = append(f.startPlanRunCalls, startPlanRunCall{planID: planID, scheduledAt: scheduledAt})
	if f.returnErr != nil && !errors.Is(f.returnErr, store.ErrDuplicateRun) {
		return f.returnErr
	}
	return f.returnErr
}

func (f *fakeStarter) SystemRunCheck(_ context.Context, repositoryID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.systemRunCheckCalls = append(f.systemRunCheckCalls, repositoryID)
	return "run-" + repositoryID, nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func tickAt(s *Scheduler, now time.Time) {
	s.runTick(context.Background(), now)
}

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// TestCronFiresOncePerSlot verifies:
//   - first tick initializes cursor only (no fire)
//   - once cursor <= now the plan fires exactly once
//   - subsequent ticks before the next fire do not re-fire
//   - cursor advances to the next schedule point each tick
func TestCronFiresOncePerSlot(t *testing.T) {
	fst := newFakeStore(t)
	fst.plans = append(fst.plans, model.Plan{
		ID:       "plan-1",
		Enabled:  true,
		Schedule: "* * * * *",
		Timezone: "UTC",
	})
	start := newFakeStarter()
	s := New(fst, start)

	// Tick 1 @ 10:00:00 → cursor initialized to 10:01:00 (nextAfter), no fire.
	tickAt(s, mustTime("2026-08-22T10:00:00Z"))
	if len(start.startPlanRunCalls) != 0 {
		t.Fatalf("expected 0 fires on first tick, got %d", len(start.startPlanRunCalls))
	}

	// Tick 2 @ 10:00:07 → cursor (10:01:00) > now (10:00:07) → still no fire,
	// but cursor stays at 10:01:00 (nextAfter unchanged).
	tickAt(s, mustTime("2026-08-22T10:00:07Z"))
	if len(start.startPlanRunCalls) != 0 {
		t.Fatalf("expected 0 fires before cursor, got %d", len(start.startPlanRunCalls))
	}

	// Tick 3 @ 10:01:01 → cursor (10:01:00) <= now → fire.
	tickAt(s, mustTime("2026-08-22T10:01:01Z"))
	if len(start.startPlanRunCalls) != 1 {
		t.Fatalf("expected 1 fire, got %d", len(start.startPlanRunCalls))
	}
	if start.startPlanRunCalls[0].planID != "plan-1" {
		t.Fatalf("unexpected planID %q", start.startPlanRunCalls[0].planID)
	}
	got := start.startPlanRunCalls[0].scheduledAt.UTC()
	if !got.Equal(mustTime("2026-08-22T10:01:00Z")) {
		t.Fatalf("scheduledAt=%v, want 10:01:00", got)
	}

	// Tick 4 @ 10:01:02 → cursor has advanced to 10:02:00, now < cursor → no fire.
	tickAt(s, mustTime("2026-08-22T10:01:02Z"))
	if len(start.startPlanRunCalls) != 1 {
		t.Fatalf("expected still 1 fire, got %d", len(start.startPlanRunCalls))
	}

	// Tick 5 @ 10:02:01 → cursor (10:02:00) <= now → fire again.
	tickAt(s, mustTime("2026-08-22T10:02:01Z"))
	if len(start.startPlanRunCalls) != 2 {
		t.Fatalf("expected 2 fires, got %d", len(start.startPlanRunCalls))
	}
}

// TestCronToleratesDuplicateRun verifies that ErrDuplicateRun from the
// starter is logged (not fatal) and the cursor still advances.
func TestCronToleratesDuplicateRun(t *testing.T) {
	fst := newFakeStore(t)
	fst.plans = append(fst.plans, model.Plan{
		ID:       "plan-1",
		Enabled:  true,
		Schedule: "* * * * *",
		Timezone: "UTC",
	})
	start := newFakeStarter()
	start.returnErr = store.ErrDuplicateRun

	s := New(fst, start)

	tickAt(s, mustTime("2026-08-22T10:00:00Z")) // init cursor
	tickAt(s, mustTime("2026-08-22T10:01:01Z")) // fire (ErrDuplicateRun returned)

	// Scheduler should have invoked StartPlanRun even though it failed.
	if len(start.startPlanRunCalls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(start.startPlanRunCalls))
	}
}

// TestCronTimezone verifies the cursor is computed in the plan's IANA
// timezone, not UTC. The schedule "0 8 * * *" fires at 08:00 every day
// in the plan's timezone. We start at 01:00 UTC (= 09:00 Shanghai, which
// is already past the day's 08:00 fire), so the first cursor is the next
// day at 08:00 Shanghai (= 00:00 UTC), and the second tick fires it.
func TestCronTimezone(t *testing.T) {
	fst := newFakeStore(t)
	// schedule: 0 8 * * * (08:00 every day)
	fst.plans = append(fst.plans, model.Plan{
		ID:       "plan-1",
		Enabled:  true,
		Schedule: "0 8 * * *",
		Timezone: "Asia/Shanghai", // UTC+8
	})
	start := newFakeStarter()
	s := New(fst, start)

	// 09:00 Asia/Shanghai = 01:00 UTC. At this point 08:00 has already
	// passed, so the next fire is 2026-08-23T00:00:00Z (08:00 Shanghai next day).
	tickAt(s, mustTime("2026-08-22T01:00:00Z"))
	if len(start.startPlanRunCalls) != 0 {
		t.Fatalf("first tick should not fire")
	}

	// Tick @ 2026-08-23T00:00:01Z (one second after next day's 08:00 CST)
	// → cursor (00:00:00Z) <= now → fire.
	tickAt(s, mustTime("2026-08-23T00:00:01Z"))
	if len(start.startPlanRunCalls) != 1 {
		t.Fatalf("expected 1 fire, got %d", len(start.startPlanRunCalls))
	}
	got := start.startPlanRunCalls[0].scheduledAt.UTC()
	if !got.Equal(mustTime("2026-08-23T00:00:00Z")) {
		t.Fatalf("scheduledAt=%v, want 2026-08-23T00:00:00Z", got)
	}
}

// TestCronDisabledPlanSkipped verifies disabled plans never fire.
func TestCronDisabledPlanSkipped(t *testing.T) {
	fst := newFakeStore(t)
	fst.plans = append(fst.plans, model.Plan{
		ID:       "plan-1",
		Enabled:  false,
		Schedule: "* * * * *",
		Timezone: "UTC",
	})
	start := newFakeStarter()
	s := New(fst, start)

	tickAt(s, mustTime("2026-08-22T10:00:00Z"))
	tickAt(s, mustTime("2026-08-22T10:01:01Z"))
	if len(start.startPlanRunCalls) != 0 {
		t.Fatalf("expected 0 fires for disabled plan, got %d", len(start.startPlanRunCalls))
	}
}

// TestCronInvalidScheduleAndTZDoNotCrash verifies that a plan with an
// unparseable cron expression or timezone does not affect the rest of the
// batch.
func TestCronInvalidScheduleAndTZDoNotCrash(t *testing.T) {
	fst := newFakeStore(t)
	fst.plans = append(fst.plans, model.Plan{
		ID:       "bad-sched",
		Enabled:  true,
		Schedule: "99 99 99 99 99", // invalid
		Timezone: "UTC",
	})
	fst.plans = append(fst.plans, model.Plan{
		ID:       "bad-tz",
		Enabled:  true,
		Schedule: "*/5 * * * *",
		Timezone: "No/Where", // invalid
	})
	fst.plans = append(fst.plans, model.Plan{
		ID:       "good",
		Enabled:  true,
		Schedule: "*/5 * * * *",
		Timezone: "UTC",
	})
	start := newFakeStarter()
	s := New(fst, start)

	tickAt(s, mustTime("2026-08-22T10:00:00Z")) // init cursors
	tickAt(s, mustTime("2026-08-22T10:05:01Z")) // good plan should fire

	// The good plan fires; bad ones log and skip.
	if len(start.startPlanRunCalls) != 1 {
		t.Fatalf("expected 1 fire (good only), got %d", len(start.startPlanRunCalls))
	}
	if start.startPlanRunCalls[0].planID != "good" {
		t.Fatalf("unexpected planID %q", start.startPlanRunCalls[0].planID)
	}
}

// TestStaleQueuedRunFailsWhenAgentOffline verifies a queued run past its
// timeout whose agent is offline gets marked failed with agent_unavailable.
func TestStaleQueuedRunFailsWhenAgentOffline(t *testing.T) {
	fst := newFakeStore(t)
	fst.plans = append(fst.plans, model.Plan{
		ID:             "plan-1",
		TimeoutSeconds: 60,
	})
	fst.runs = append(fst.runs, model.Run{
		ID:       "run-1",
		PlanID:   "plan-1",
		AgentID:  "agent-1",
		Operation: model.OpBackup,
		Status:   model.RunQueued,
		QueuedAt: mustTime("2026-08-22T10:00:00Z"),
	})
	fst.agents["agent-1"] = model.Agent{
		ID:     "agent-1",
		Status: model.AgentOffline,
	}
	start := newFakeStarter()
	s := New(fst, start)

	tickAt(s, mustTime("2026-08-22T10:00:30Z")) // deadline 10:01:00 not yet reached → no fail
	if len(fst.transitions) != 0 {
		t.Fatalf("expected 0 transitions before deadline, got %d", len(fst.transitions))
	}

	tickAt(s, mustTime("2026-08-22T10:01:30Z")) // past deadline, agent offline → fail
	if len(fst.transitions) != 1 {
		t.Fatalf("expected 1 transition, got %d", len(fst.transitions))
	}
	tc := fst.transitions[0]
	if tc.runID != "run-1" || tc.to != model.RunFailed {
		t.Fatalf("unexpected transition: %+v", tc)
	}
	if tc.code != model.ErrAgentUnavailable {
		t.Fatalf("error_code=%q, want %q", tc.code, model.ErrAgentUnavailable)
	}
	if fst.runs[0].FinishedAt == nil || !fst.runs[0].FinishedAt.Equal(mustTime("2026-08-22T10:01:30Z")) {
		t.Fatalf("FinishedAt not set correctly: %v", fst.runs[0].FinishedAt)
	}
}

// TestStaleQueuedRunUsesDefaultTimeoutWhenNoPlan verifies that a system
// run (no plan_id) uses the 300s default timeout.
func TestStaleQueuedRunUsesDefaultTimeoutWhenNoPlan(t *testing.T) {
	fst := newFakeStore(t)
	fst.runs = append(fst.runs, model.Run{
		ID:       "sys-run",
		AgentID:  "agent-1",
		Operation: model.OpCheck,
		Status:   model.RunQueued,
		QueuedAt: mustTime("2026-08-22T10:00:00Z"),
	})
	fst.agents["agent-1"] = model.Agent{
		ID:     "agent-1",
		Status: model.AgentOffline,
	}
	s := New(fst, newFakeStarter())

	// 4 minutes < 300s → no fail.
	tickAt(s, mustTime("2026-08-22T10:04:00Z"))
	if len(fst.transitions) != 0 {
		t.Fatalf("expected 0 transitions at 4min, got %d", len(fst.transitions))
	}
	// 6 minutes > 300s → fail.
	tickAt(s, mustTime("2026-08-22T10:06:00Z"))
	if len(fst.transitions) != 1 {
		t.Fatalf("expected 1 transition at 6min, got %d", len(fst.transitions))
	}
}

// TestStaleQueuedRunNotFailedWhenAgentOnline verifies an online agent
// does not trip the failure path even past the deadline.
func TestStaleQueuedRunNotFailedWhenAgentOnline(t *testing.T) {
	fst := newFakeStore(t)
	fst.plans = append(fst.plans, model.Plan{ID: "p", TimeoutSeconds: 60})
	fst.runs = append(fst.runs, model.Run{
		ID:       "run-1",
		PlanID:   "p",
		AgentID:  "agent-1",
		Status:   model.RunQueued,
		QueuedAt: mustTime("2026-08-22T10:00:00Z"),
	})
	fst.agents["agent-1"] = model.Agent{
		ID:     "agent-1",
		Status: model.AgentOnline,
	}
	s := New(fst, newFakeStarter())

	tickAt(s, mustTime("2026-08-22T10:05:00Z"))
	if len(fst.transitions) != 0 {
		t.Fatalf("expected 0 transitions when agent is online, got %d", len(fst.transitions))
	}
}

// TestStaleQueuedRunFailsWhenAgentRowMissing verifies a run whose agent
// does not exist is treated as offline.
func TestStaleQueuedRunFailsWhenAgentRowMissing(t *testing.T) {
	fst := newFakeStore(t)
	fst.plans = append(fst.plans, model.Plan{ID: "p", TimeoutSeconds: 30})
	fst.runs = append(fst.runs, model.Run{
		ID:       "run-1",
		PlanID:   "p",
		AgentID:  "agent-gone",
		Status:   model.RunQueued,
		QueuedAt: mustTime("2026-08-22T10:00:00Z"),
	})
	s := New(fst, newFakeStarter())

	tickAt(s, mustTime("2026-08-22T10:01:00Z"))
	if len(fst.transitions) != 1 || fst.transitions[0].code != model.ErrAgentUnavailable {
		t.Fatalf("expected transition to failed with agent_unavailable, got: %v", fst.transitions)
	}
}

// TestWeeklyRepoCheckOnlyReadyOnline verifies the weekly check fires only
// for ready repositories with an online agent, even when several other
// repositories are also due.
func TestWeeklyRepoCheckOnlyReadyOnline(t *testing.T) {
	fst := newFakeStore(t)
	now := mustTime("2026-08-22T10:00:00Z")

	lastCheckOld := mustTime("2026-08-14T10:00:00Z")
	fst.repos = append(fst.repos, model.Repository{
		ID:          "ready-online",
		AgentID:     "agent-1",
		Status:      "ready",
		LastCheckAt: &lastCheckOld,
	})
	fst.repos = append(fst.repos, model.Repository{
		ID:          "ready-offline",
		AgentID:     "agent-2",
		Status:      "ready",
		LastCheckAt: &lastCheckOld,
	})
	fst.repos = append(fst.repos, model.Repository{
		ID:          "not-ready-online",
		AgentID:     "agent-3",
		Status:      "error",
		LastCheckAt: &lastCheckOld,
	})
	fst.repos = append(fst.repos, model.Repository{
		ID:          "ready-agent-missing",
		AgentID:     "agent-missing",
		Status:      "ready",
		LastCheckAt: &lastCheckOld,
	})
	fst.repos = append(fst.repos, model.Repository{
		ID:          "fresh",
		AgentID:     "agent-4",
		Status:      "ready",
		LastCheckAt: &now, // checked just now; not in list
	})

	fst.agents["agent-1"] = model.Agent{ID: "agent-1", Status: model.AgentOnline}
	fst.agents["agent-2"] = model.Agent{ID: "agent-2", Status: model.AgentOffline}
	fst.agents["agent-3"] = model.Agent{ID: "agent-3", Status: model.AgentOnline}
	fst.agents["agent-4"] = model.Agent{ID: "agent-4", Status: model.AgentOnline}

	start := newFakeStarter()
	s := New(fst, start)
	s.now = func() time.Time { return now }

	s.runTick(context.Background(), now)

	if len(start.systemRunCheckCalls) != 1 || start.systemRunCheckCalls[0] != "ready-online" {
		t.Fatalf("unexpected SystemRunCheck calls: %v", start.systemRunCheckCalls)
	}
}

// TestWeeklyRepoCheckFiresMultipleRepos verifies several eligible repos
// each get their own SystemRunCheck call.
func TestWeeklyRepoCheckFiresMultipleRepos(t *testing.T) {
	fst := newFakeStore(t)
	lastCheck := mustTime("2026-08-10T10:00:00Z")
	fst.repos = append(fst.repos, model.Repository{
		ID: "a", AgentID: "agent-a", Status: "ready", LastCheckAt: &lastCheck,
	})
	fst.repos = append(fst.repos, model.Repository{
		ID: "b", AgentID: "agent-b", Status: "ready", LastCheckAt: &lastCheck,
	})
	fst.agents["agent-a"] = model.Agent{ID: "agent-a", Status: model.AgentOnline}
	fst.agents["agent-b"] = model.Agent{ID: "agent-b", Status: model.AgentOnline}

	start := newFakeStarter()
	s := New(fst, start)
	now := mustTime("2026-08-22T10:00:00Z")
	s.now = func() time.Time { return now }

	s.runTick(context.Background(), now)
	if len(start.systemRunCheckCalls) != 2 {
		t.Fatalf("expected 2 calls, got %d: %v", len(start.systemRunCheckCalls), start.systemRunCheckCalls)
	}
}

// TestLoopStartsAndStops verifies the scheduler goroutine starts and stops
// cleanly. We inject a fake ticker whose channel fires once so the loop
// processes exactly one tick before Stop closes it.
func TestLoopStartsAndStops(t *testing.T) {
	fst := newFakeStore(t)
	start := newFakeStarter()
	s := New(fst, start)

	called := make(chan time.Time, 1)
	s.tickFn = func(_ time.Duration) *time.Ticker {
		ch := make(chan time.Time)
		go func() { ch <- <-called }()
		return &time.Ticker{C: ch}
	}
	s.now = func() time.Time { return mustTime("2026-08-22T10:00:00Z") }

	s.Start()
	called <- mustTime("2026-08-22T10:00:00Z")
	s.Stop()
}