package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/server/notification"
	"backupmanagementcenter/internal/server/store"
	"github.com/robfig/cron/v3"
)

// RunStarter is injected by main (backed by *jobs.Orchestrator) so this
// package only imports its narrow interface and never couples to jobs/.
type RunStarter interface {
	// StartPlanRun creates and enqueues a backup run for the plan at the
	// given cron slot. Returns store.ErrDuplicateRun when the slot was
	// already claimed by a previous tick.
	StartPlanRun(ctx context.Context, planID string, scheduledAt *time.Time) error
	// SystemRunCheck creates and enqueues a repository check run.
	SystemRunCheck(ctx context.Context, repositoryID string) (runID string, err error)
}

const (
	tickInterval    = 15 * time.Second
	repoCheckWindow = 7 * 24 * time.Hour
	defaultTimeout  = 300 * time.Second
)

type Scheduler struct {
	store    store.Store
	starter  RunStarter
	notifier notification.FailureNotifier

	// test knobs
	tickFn func(time.Duration) *time.Ticker
	now    func() time.Time

	parser  cron.Parser
	closeCh chan struct{}
	wg      sync.WaitGroup

	// planID -> next fire time (UTC). Re-computed every tick from the plan's
	// schedule+timezone.
	cursors map[string]time.Time
}

// New builds a Scheduler. notifier may be nil; a no-op is used then.
func New(st store.Store, starter RunStarter, notifier notification.FailureNotifier) *Scheduler {
	if notifier == nil {
		notifier = notification.NopNotifier{}
	}
	p := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	return &Scheduler{
		store:    st,
		starter:  starter,
		notifier: notifier,
		parser:   p,
		tickFn:   time.NewTicker,
		now:      func() time.Time { return time.Now().UTC() },
		closeCh:  make(chan struct{}),
		cursors:  make(map[string]time.Time),
	}
}

// Start spawns the scheduler's background loop. It may be called at most once.
func (s *Scheduler) Start() {
	s.wg.Add(1)
	go s.loop()
}

// Stop asks the loop to finish after the current tick. It blocks until done.
func (s *Scheduler) Stop() {
	close(s.closeCh)
	s.wg.Wait()
}

func (s *Scheduler) loop() {
	defer s.wg.Done()
	ticker := s.tickFn(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.closeCh:
			return
		case <-ticker.C:
			s.runTick(context.Background(), s.now())
		}
	}
}

// runTick is the deterministic core: every 15s it (1) fires due plan slots,
// (2) fails queued runs whose agent is offline past their timeout, (3)
// launches the weekly repository check for ready repos with online agents.
// Individual store errors log and do not abort the other phases.
func (s *Scheduler) runTick(ctx context.Context, now time.Time) {
	s.tickCron(ctx, now)
	s.tickStaleQueued(ctx, now)
	s.tickWeeklyRepoCheck(ctx, now)
}

// tickCron parses every enabled plan, advances its in-memory cursor and calls
// StartPlanRun for any slot whose cursor <= now.
func (s *Scheduler) tickCron(ctx context.Context, now time.Time) {
	plans, err := s.store.ListEnabledPlans(ctx)
	if err != nil {
		slog.Error("scheduler: ListEnabledPlans", "error", err)
		return
	}
	for _, p := range plans {
		s.tickPlan(ctx, now, p)
	}
}

func (s *Scheduler) tickPlan(ctx context.Context, now time.Time, p model.Plan) {
	sched, err := s.parser.Parse(p.Schedule)
	if err != nil {
		slog.Error("scheduler: parse cron", "planID", p.ID, "schedule", p.Schedule, "error", err)
		return
	}
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		slog.Error("scheduler: load timezone", "planID", p.ID, "timezone", p.Timezone, "error", err)
		return
	}

	// Next fire after `now` in the plan's timezone.
	next := sched.Next(now.In(loc)).UTC()

	// Cursor is the slot this plan would fulfill if consumed. On first tick
	// (or after restart) the cursor does not exist yet, so we only initialize.
	prev, exists := s.cursors[p.ID]
	if !exists || prev.IsZero() {
		s.cursors[p.ID] = next
		return
	}

	if prev.Before(now) || prev.Equal(now) {
		if err := s.starter.StartPlanRun(ctx, p.ID, &prev); err != nil {
			if errors.Is(err, store.ErrDuplicateRun) {
				slog.Info("scheduler: duplicate run, slot already claimed", "planID", p.ID)
			} else {
				slog.Error("scheduler: StartPlanRun", "planID", p.ID, "error", err)
			}
		}
	}
	s.cursors[p.ID] = next
}

// tickStaleQueued fails queued runs whose deadline has passed and whose agent
// is offline or absent. The timeout is read from the plan (or 300s default
// when the run is not plan-driven).
func (s *Scheduler) tickStaleQueued(ctx context.Context, now time.Time) {
	runsWithPlans, err := s.store.ListRunsByStatus(ctx, []string{model.RunQueued})
	if err != nil {
		slog.Error("scheduler: ListRunsByStatus", "error", err)
		return
	}
	if len(runsWithPlans) == 0 {
		return
	}

	plansByID := make(map[string]model.Plan)
	getPlan := func(id string) (*model.Plan, error) {
		if p, ok := plansByID[id]; ok {
			return &p, nil
		}
		p, err := s.store.GetPlan(ctx, id)
		if err == nil {
			plansByID[id] = *p
		}
		return p, err
	}

	for _, r := range runsWithPlans {
		timeout := defaultTimeout
		if r.PlanID != "" {
			if p, err := getPlan(r.PlanID); err == nil && p.TimeoutSeconds > 0 {
				timeout = time.Duration(p.TimeoutSeconds) * time.Second
			}
		}
		if now.Before(r.QueuedAt.Add(timeout)) {
			continue
		}
		if !s.agentOffline(ctx, r.AgentID) {
			continue
		}
		finishedAt := now
		if err := s.store.TransitionRun(ctx, r.ID, model.RunQueued, model.RunFailed, func(run *model.Run) {
			run.ErrorCode = model.ErrAgentUnavailable
			run.ErrorMessage = "agent offline past timeout"
			run.FinishedAt = &finishedAt
		}); err != nil {
			// Transition failed (e.g. concurrent terminal result): keep the
			// existing error log and do not notify.
			slog.Error("scheduler: TransitionRun", "runID", r.ID, "error", err)
			continue
		}
		// System runs (PlanID == "") are filtered inside the notifier.
		if err := s.notifier.NotifyPlanFailure(ctx, r.ID); err != nil {
			slog.Error("plan failure notification", "runID", r.ID, "error", err)
		}
	}
}

func (s *Scheduler) agentOffline(ctx context.Context, agentID string) bool {
	agent, err := s.store.GetAgent(ctx, agentID)
	if errors.Is(err, store.ErrNotFound) {
		return true
	}
	if err != nil {
		slog.Error("scheduler: GetAgent", "agentID", agentID, "error", err)
		return false // unknown; do not fail the run based on a store error
	}
	return agent.Status != model.AgentOnline
}

// tickWeeklyRepoCheck scans repositories whose last check is older than 7
// days, keeps only those in ready status with an online agent, and launches a
// system check run.
func (s *Scheduler) tickWeeklyRepoCheck(ctx context.Context, now time.Time) {
	repos, err := s.store.ListRepositoriesNeedingCheck(ctx, now.Add(-repoCheckWindow))
	if err != nil {
		slog.Error("scheduler: ListRepositoriesNeedingCheck", "error", err)
		return
	}
	agentsByID := make(map[string]model.Agent)
	getAgent := func(id string) (*model.Agent, error) {
		if a, ok := agentsByID[id]; ok {
			return &a, nil
		}
		a, err := s.store.GetAgent(ctx, id)
		if err == nil {
			agentsByID[id] = *a
		}
		return a, err
	}
	for _, r := range repos {
		if r.Status != "ready" {
			continue
		}
		agent, err := getAgent(r.AgentID)
		if err != nil || agent.Status != model.AgentOnline {
			continue
		}
		if _, err := s.starter.SystemRunCheck(ctx, r.ID); err != nil {
			slog.Error("scheduler: SystemRunCheck", "repositoryID", r.ID, "error", err)
		}
	}
}
