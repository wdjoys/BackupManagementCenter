package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
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

type maintenanceStarter interface {
	StartRetentionRun(ctx context.Context, repositoryID string) error
}

type snapshotCleanupStarter interface {
	TickSnapshotCleanup(ctx context.Context, now time.Time) error
}

const (
	tickInterval    = 15 * time.Second
	repoCheckWindow = 7 * 24 * time.Hour
	// A failed integrity check should be visible and retryable, but must not
	// enqueue a new restic process every scheduler tick while the failure is
	// unresolved.
	repoCheckRetryCooldown = time.Hour
	defaultTimeout         = 300 * time.Second
)

type Scheduler struct {
	store                store.Store
	starter              RunStarter
	notifier             notification.FailureNotifier
	historyRetentionDays int
	// lastHistoryPrune 是上次历史剪枝的 unix 秒（0 表示从未跑过），用于每日节流。
	lastHistoryPrune atomic.Int64

	// test knobs
	tickFn func(time.Duration) *time.Ticker
	now    func() time.Time

	parser  cron.Parser
	closeCh chan struct{}
	wg      sync.WaitGroup

	// planID -> next fire time (UTC). Re-computed every tick from the plan's
	// schedule+timezone.
	cursors map[string]time.Time

	// retentionSkipMu/retentionSkipAt 给"保留策略被跳过"的日志限频：tick 每 15s 一次，
	// 不限频会变成新的刷屏；同一仓库同一原因每小时最多记一条。此前因 active/recent
	// 跳过时完全静默，运维无法判断保留为何没跑。
	retentionSkipMu sync.Mutex
	retentionSkipAt map[string]time.Time
}

// New builds a Scheduler. notifier may be nil; a no-op is used then.
func New(st store.Store, starter RunStarter, notifier notification.FailureNotifier, historyRetentionDays int) *Scheduler {
	if notifier == nil {
		notifier = notification.NopNotifier{}
	}
	p := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	return &Scheduler{
		store:                st,
		starter:              starter,
		notifier:             notifier,
		historyRetentionDays: historyRetentionDays,
		parser:               p,
		tickFn:               time.NewTicker,
		now:                  func() time.Time { return time.Now().UTC() },
		closeCh:              make(chan struct{}),
		cursors:              make(map[string]time.Time),
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
	s.tickMaintenance(ctx, now)
	s.tickSnapshotCleanup(ctx, now)
	s.tickHistoryPrune(ctx, now)
}

// tickHistoryPrune 每日一次清理超出保留窗口的运行历史（runs/日志/审计事件）。
// 默认关闭（HistoryRetentionDays=0 时什么都不做）——升级不删除既有数据。
func (s *Scheduler) tickHistoryPrune(ctx context.Context, now time.Time) {
	if s.historyRetentionDays <= 0 {
		return
	}
	last := s.lastHistoryPrune.Load()
	if last != 0 && now.Sub(time.Unix(last, 0)) < 24*time.Hour {
		return
	}
	if !s.lastHistoryPrune.CompareAndSwap(last, now.Unix()) {
		return
	}
	cutoff := now.AddDate(0, 0, -s.historyRetentionDays)
	res, err := s.store.PruneHistory(ctx, cutoff)
	if err != nil {
		slog.Error("scheduler: PruneHistory", "error", err)
		return
	}
	slog.Info("scheduler: pruned run history",
		"cutoff", cutoff.UTC().Format(time.RFC3339),
		"runs", res.Runs, "restore_requests", res.RestoreRequests, "audit_events", res.AuditEvents)
}

// tickSnapshotCleanup 触发每 tick 一次删除状态机与孤儿扫描。失败不阻断
// 其他阶段，也不等待远端 I/O——远端操作全部通过 dispatcher FIFO 异步执行。
func (s *Scheduler) tickSnapshotCleanup(ctx context.Context, now time.Time) {
	cs, ok := s.starter.(snapshotCleanupStarter)
	if !ok {
		return
	}
	if err := cs.TickSnapshotCleanup(ctx, now); err != nil {
		slog.Error("scheduler: TickSnapshotCleanup", "error", err)
	}
}

// forgetRunIsRetention 判断一次 forget 运行是否由"保留策略"触发。
//
// 只有保留运行才应推迟下一次保留：绑定仓库时的初始化、计划清理（delete_all）与
// 手动删除单个快照（snapshot_ids）同样产生 forget 运行，若一并计入，频繁删除会让
// 保留策略长期不再运行、快照无界累积。实测：新绑定的仓库因初始化 forget 运行而被
// 跳过保留超过 100 秒（守卫窗口为 24 小时）。
//
// 参数从两处读取：运行创建时写入的 ProgressJSON（保留运行不会被进度覆盖）与不可变的
// DedupKey 尾部参数，任一处显示非零保留策略即认定为保留运行。
func forgetRunIsRetention(run model.Run) bool {
	for _, payload := range []string{run.ProgressJSON, dedupKeyParams(run.DedupKey)} {
		if payload == "" {
			continue
		}
		var task model.ForgetTask
		if err := json.Unmarshal([]byte(payload), &task); err != nil {
			continue
		}
		r := task.Retention
		if r.KeepLast+r.KeepDaily+r.KeepWeekly+r.KeepMonthly > 0 {
			return true
		}
	}
	return false
}

// dedupKeyParams 取出系统运行去重键尾部的任务参数 JSON（见 jobs.systemRunDedupKey）。
func dedupKeyParams(key string) string {
	parts := strings.Split(key, "\x00")
	if len(parts) == 0 {
		return ""
	}
	last := parts[len(parts)-1]
	if strings.HasPrefix(strings.TrimSpace(last), "{") {
		return last
	}
	return ""
}

// logRetentionSkip 记录一次"保留策略被跳过"，同一仓库同一原因每小时最多一条，
// 返回是否真的记录了（便于测试断言限频语义）。
func (s *Scheduler) logRetentionSkip(repoID, reason, message string) bool {
	key := repoID + "|" + reason
	now := time.Now()
	s.retentionSkipMu.Lock()
	if s.retentionSkipAt == nil {
		s.retentionSkipAt = map[string]time.Time{}
	}
	if last, ok := s.retentionSkipAt[key]; ok && now.Sub(last) < time.Hour {
		s.retentionSkipMu.Unlock()
		return false
	}
	// 顺手清理过期条目，避免长时间运行后无界增长。
	if len(s.retentionSkipAt) > 1024 {
		for k, ts := range s.retentionSkipAt {
			if now.Sub(ts) > 24*time.Hour {
				delete(s.retentionSkipAt, k)
			}
		}
	}
	s.retentionSkipAt[key] = now
	s.retentionSkipMu.Unlock()
	slog.Info(message, "repositoryID", repoID)
	return true
}

// tickMaintenance schedules forget (without prune) at most once per day per
// repository. The repository queue serializes it after any active backup.
func (s *Scheduler) tickMaintenance(ctx context.Context, now time.Time) {
	ms, ok := s.starter.(maintenanceStarter)
	if !ok {
		return
	}
	repos, err := s.store.ListRepositories(ctx)
	if err != nil {
		slog.Error("scheduler: ListRepositories", "error", err)
		return
	}
	for _, repo := range repos {
		if s.agentOffline(ctx, repo.AgentID) {
			// 同样走限频：该分支此前是裸 slog.Info，每 15s 一行，长期离线仓库会持续刷屏
			// （本环境 22 个离线 agent，实测单仓库 1m46s 内 7 行）。
			s.logRetentionSkip(repo.ID, "offline", "scheduler: skip retention for offline agent")
			continue
		}
		runs, err := s.store.ListRuns(ctx, store.RunFilter{RepositoryID: repo.ID, Limit: 100,
			Statuses: []string{model.RunQueued, model.RunDispatched, model.RunRunning, model.RunSucceeded}})
		if err != nil {
			continue
		}
		active := false
		for _, run := range runs {
			if run.Operation == model.OpBackup && (run.Status == model.RunQueued || run.Status == model.RunDispatched || run.Status == model.RunRunning) {
				active = true
				break
			}
		}
		// 24h 节流只按 **forget** 运行判断：此前在最近 100 条**任意**运行里找守卫
		// 运行，繁忙仓库（每次快照浏览都产生运行）会把守卫运行挤出窗口 —— 实测 17
		// 分钟后即被挤出，保留策略于同日再次运行，与"每仓库每日至多一次"不符。
		// 同理，只取 forget 仍不够：定向删除（delete_all / snapshot_ids）产生的
		// forget 运行与保留无关，实测 20 条就能把保留型运行挤出窗口，因此这里把
		// 它们从窗口里排除，窗口只留保留型（以及初始化型）forget 运行。
		recent := false
		forgetRuns, ferr := s.store.ListRuns(ctx, store.RunFilter{RepositoryID: repo.ID,
			Operation: model.OpForget, Limit: 20,
			Statuses: []string{model.RunQueued, model.RunDispatched, model.RunRunning, model.RunSucceeded},
			ExcludeDedupKeySubstrings: []string{
				`"delete_all":true`, `"snapshot_ids":[`,
			},
		})
		if ferr != nil {
			continue
		}
		for _, run := range forgetRuns {
			if !forgetRunIsRetention(run) {
				continue
			}
			at := run.FinishedAt
			if at == nil {
				at = &run.QueuedAt
			}
			if now.Sub(*at) < 24*time.Hour {
				recent = true
				break
			}
		}
		if active || recent {
			// 跳过原因必须可见（每仓库每原因每小时最多一条，避免 15s tick 刷屏）：
			// 此前因 active/recent 跳过时完全静默，实测 keep_last=1 的每分钟计划在
			// 24h 节流窗口内快照持续累积，而界面与日志都没有任何"保留被跳过"的提示。
			if active {
				s.logRetentionSkip(repo.ID, "active", "scheduler: skip retention while a backup is active")
			} else {
				s.logRetentionSkip(repo.ID, "recent", "scheduler: skip retention (already ran within 24h)")
			}
			continue
		}
		if err := ms.StartRetentionRun(ctx, repo.ID); err != nil {
			slog.Error("scheduler: StartRetentionRun", "repositoryID", repo.ID, "error", err)
		}
	}
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

	next := sched.Next(now.In(loc)).UTC()
	prev, exists := s.cursors[p.ID]
	if !exists {
		if cs, ok := s.store.(interface {
			GetScheduleCursor(context.Context, string) (time.Time, error)
		}); ok {
			if persisted, perr := cs.GetScheduleCursor(ctx, p.ID); perr == nil {
				prev, exists = persisted, true
			} else if !errors.Is(perr, store.ErrNotFound) {
				slog.Error("scheduler: GetScheduleCursor", "planID", p.ID, "error", perr)
			}
		}
	}
	if !exists || prev.IsZero() {
		s.cursors[p.ID] = next
		s.saveCursor(ctx, p.ID, next)
		return
	}

	if prev.Before(now) || prev.Equal(now) {
		if p.AgentID != "" && s.agentOffline(ctx, p.AgentID) {
			slog.Info("scheduler: skip plan run for offline agent", "planID", p.ID, "agentID", p.AgentID)
		} else if err := s.starter.StartPlanRun(ctx, p.ID, &prev); err != nil {
			if errors.Is(err, store.ErrDuplicateRun) {
				slog.Info("scheduler: duplicate run, slot already claimed", "planID", p.ID)
			} else {
				slog.Error("scheduler: StartPlanRun", "planID", p.ID, "error", err)
			}
		}
	}
	s.cursors[p.ID] = next
	s.saveCursor(ctx, p.ID, next)
}

func (s *Scheduler) saveCursor(ctx context.Context, planID string, next time.Time) {
	if cs, ok := s.store.(interface {
		SaveScheduleCursor(context.Context, string, time.Time) error
	}); ok {
		if err := cs.SaveScheduleCursor(ctx, planID, next); err != nil {
			slog.Error("scheduler: SaveScheduleCursor", "planID", planID, "error", err)
		}
	}
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
		if rs, ok := s.store.(interface {
			DeleteRunSecrets(context.Context, string) error
		}); ok {
			_ = rs.DeleteRunSecrets(ctx, r.ID)
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
		checkRuns, err := s.store.ListRuns(ctx, store.RunFilter{
			RepositoryID: r.ID,
			Operation:    model.OpCheck,
			Statuses:     []string{model.RunQueued, model.RunDispatched, model.RunRunning, model.RunSucceeded, model.RunFailed},
			Limit:        1,
		})
		if err != nil {
			slog.Error("scheduler: ListRuns for repository check", "repositoryID", r.ID, "error", err)
			continue
		}
		if len(checkRuns) > 0 {
			latest := checkRuns[0]
			if latest.Status == model.RunQueued || latest.Status == model.RunDispatched || latest.Status == model.RunRunning {
				// Do not enqueue a duplicate check while one is active or while
				// the in-flight operation is still being persisted.
				continue
			}
			if latest.Status == model.RunSucceeded && latest.FinishedAt != nil && now.Sub(*latest.FinishedAt) < repoCheckWindow {
				// Compatibility with runs created before system checks updated
				// last_check_at; do not immediately duplicate a recent success.
				continue
			}
			if latest.Status == model.RunFailed && latest.FinishedAt != nil && now.Sub(*latest.FinishedAt) < repoCheckRetryCooldown {
				continue
			}
		}
		if _, err := s.starter.SystemRunCheck(ctx, r.ID); err != nil {
			slog.Error("scheduler: SystemRunCheck", "repositoryID", r.ID, "error", err)
		}
	}
}
