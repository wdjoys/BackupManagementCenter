package dispatchgrpc

import (
	"backupmanagementcenter/internal/dispatch"
	"backupmanagementcenter/internal/model"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	bmcv1 "backupmanagementcenter/api/proto/v1"
	"backupmanagementcenter/internal/server/agentreg"
	"backupmanagementcenter/internal/server/events"
	"backupmanagementcenter/internal/server/jobs"
	"backupmanagementcenter/internal/server/notification"
	"backupmanagementcenter/internal/server/store"
)

// Config holds optional configuration for the dispatcher.
type Config struct {
	// WatchdogInterval controls how often the timeout watchdog runs.
	WatchdogInterval time.Duration
	// OfflineRetryDelay is how long to wait before re-queuing a job for an offline agent.
	OfflineRetryDelay time.Duration
	// NoResponseTimeout is how long to wait for a response after dispatching before forcing failure.
	NoResponseTimeout time.Duration
	// ClaimRetryDelay controls the initial backoff when SQLite temporarily
	// rejects the queued -> dispatched write because another writer holds the
	// database lock. A zero value uses 100ms.
	ClaimRetryDelay time.Duration
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		WatchdogInterval:  15 * time.Second,
		OfflineRetryDelay: 2 * time.Second,
		NoResponseTimeout: 30 * time.Second,
		ClaimRetryDelay:   100 * time.Millisecond,
	}
}

// job represents a run waiting to be dispatched to an agent.
type job struct {
	runID        string
	agentID      string
	repositoryID string
}

const dispatchLease = 2 * time.Minute

const (
	// restoreExecutionDeadline 是单次恢复的执行兜底期限。恢复包含整仓下载、
	// 预备份上传、目标大小数据的导入与验证，远高于系统任务 300 秒的硬失败阈值。
	// ponytail: 固定上限而非按 dump 体积估算，超大库需要更长时间时再引入按体积的期限。
	restoreExecutionDeadline = 12 * time.Hour
	// restoreCancelGrace 是发出停止请求后等待 Agent 完成回滚/清理并回报结果的宽限。
	restoreCancelGrace = 30 * time.Minute
)

func retryableOperation(op string) bool {
	switch op {
	case model.OpBackup, model.OpCheck, model.OpSnapshots, model.OpSnapshotLs,
		model.OpValidatePaths, model.OpProbeCaps, model.OpVerifyRemote:
		return true
	default:
		return false
	}
}

// repoQueue is a FIFO queue for a single repository with a dedicated worker.
type repoQueue struct {
	mu       sync.Mutex
	jobs     []*job
	workerCh chan struct{}
	stopCh   chan struct{}
}

// Dispatcher implements dispatch.Dispatcher using the gRPC channel layer.
type Dispatcher struct {
	cfg      Config
	store    store.Store
	reg      *agentreg.Registry
	notifier notification.FailureNotifier
	// Src builds the actual ExecuteCommand (params + decrypted secrets).
	Src jobs.CommandSource
	// Bus 可选：设置后，派发日志落库后推送给实时订阅者。
	Bus events.Bus

	mu           sync.Mutex
	repoQueues   map[string]*repoQueue
	enqueuedRuns map[string]bool // runID -> true (for idempotency)
	// processingRuns distinguishes a job popped by a repository worker from a
	// stale in-memory enqueue marker. It lets the watchdog repair queue entries
	// lost by a transient store error without duplicating active work.
	processingRuns map[string]bool

	// watchdog
	stopWatchdog chan struct{}
	wg           sync.WaitGroup

	// dispatchLogState prevents an offline agent or a busy SQLite database from
	// generating one persisted log row every retry tick.
	dispatchLogMu    sync.Mutex
	dispatchLogState map[string]dispatchLogState
}

type dispatchLogState struct {
	message string
	at      time.Time
}

// NewDispatcher creates a new gRPC-based dispatcher. notifier may be nil; a
// no-op is used then.
func NewDispatcher(s store.Store, reg *agentreg.Registry, cfg Config, notifier notification.FailureNotifier) *Dispatcher {
	if notifier == nil {
		notifier = notification.NopNotifier{}
	}
	return &Dispatcher{
		store:            s,
		reg:              reg,
		cfg:              cfg,
		notifier:         notifier,
		repoQueues:       make(map[string]*repoQueue),
		enqueuedRuns:     make(map[string]bool),
		processingRuns:   make(map[string]bool),
		stopWatchdog:     make(chan struct{}),
		dispatchLogState: make(map[string]dispatchLogState),
	}
}

// Enqueue implements dispatch.Dispatcher.
func (d *Dispatcher) Enqueue(ctx context.Context, runID, agentID, repositoryID string) {
	d.mu.Lock()

	// Idempotency: ignore duplicate enqueues
	if d.enqueuedRuns[runID] {
		d.mu.Unlock()
		return
	}
	d.enqueuedRuns[runID] = true

	// Get or create repository queue
	rq, ok := d.repoQueues[repositoryID]
	if !ok {
		rq = &repoQueue{
			jobs:     make([]*job, 0),
			workerCh: make(chan struct{}, 1),
			stopCh:   make(chan struct{}),
		}
		d.repoQueues[repositoryID] = rq

		// Start worker for this repository
		d.wg.Add(1)
		go d.repoWorker(repositoryID, rq)
	}

	// Add job to queue
	rq.mu.Lock()
	rq.jobs = append(rq.jobs, &job{
		runID:        runID,
		agentID:      agentID,
		repositoryID: repositoryID,
	})
	rq.mu.Unlock()

	// Signal worker
	select {
	case rq.workerCh <- struct{}{}:
	default:
	}

	d.mu.Unlock()
}

// repoWorker processes jobs for a single repository sequentially.
func (d *Dispatcher) repoWorker(repositoryID string, rq *repoQueue) {
	defer d.wg.Done()

	for {
		select {
		case <-rq.stopCh:
			return
		case <-rq.workerCh:
			// Process all queued jobs
			for {
				rq.mu.Lock()
				if len(rq.jobs) == 0 {
					rq.mu.Unlock()
					break
				}
				// Pop first job
				j := rq.jobs[0]
				rq.jobs = rq.jobs[1:]
				rq.mu.Unlock()

				d.markProcessing(j.runID, true)
				d.processJob(j)
				d.markProcessing(j.runID, false)
			}
		}
	}
}

// processJob handles a single job dispatch.
func (d *Dispatcher) processJob(j *job) {
	ctx := context.Background()

	// 恢复任务的范围决定能否下发破坏性命令，也决定处置策略。
	run, runErr := d.store.GetRun(ctx, j.runID)
	if runErr != nil {
		d.removeEnqueued(j.runID)
		return
	}
	isRestore := run.Operation == model.OpRestore
	dbRestore := false
	if isRestore {
		rr, rrErr := d.store.GetRestoreRequestByRunID(ctx, j.runID)
		if rrErr != nil {
			// 无法确认恢复范围时绝不下发破坏性命令。
			d.failRestoreBeforeSend(ctx, j.runID, model.ErrInvalidPlan, "restore request is unavailable")
			d.removeEnqueued(j.runID)
			return
		}
		dbRestore = isDatabaseRestoreKind(rr.RestoreKind)
	}

	// 未安全终结的恢复阻塞同一来源仓库的后续命令，避免与仍在执行的
	// 保护上传/回滚交叉；该 run 自身除外。
	if blocked, bErr := d.store.RepositoryRestoreBlocked(ctx, j.repositoryID, j.runID); bErr == nil && blocked {
		d.appendDispatchLog(ctx, j.runID, "warn", "该仓库存在未安全终结的恢复，暂缓下发后续任务")
		d.requeueJob(j)
		return
	}

	// Check if agent is connected
	if !d.reg.IsConnected(j.agentID) {
		// 不再用裸 log.Printf（每 2s 一行，实测断连 10 分钟刷 322 行，淹没真实信号）：
		// appendDispatchLog 对同 run 同消息 10 分钟内只记一条，且写入 run 日志这一
		// 产品可见面。
		d.appendDispatchLog(ctx, j.runID, "warn", "等待 Agent 连接，任务将自动重试")
		d.requeueJob(j)
		return
	}

	// Claim the run before building or sending the command. This is the
	// durable queued -> dispatched edge that makes the delivery protocol
	// observable to the agent and safe across concurrent workers.
	leaseDuration := dispatchLease
	if isRestore {
		// 恢复使用专用期限；租约同时充当执行/取消宽限的持久化截止。
		leaseDuration = restoreExecutionDeadline
	}
	leaseUntil := time.Now().UTC().Add(leaseDuration)
	attempt := 0
	claim := func() error {
		return d.store.TransitionRun(ctx, j.runID, model.RunQueued, model.RunDispatched, func(r *model.Run) {
			r.Attempt++
			attempt = r.Attempt
			r.LeaseExpiresAt = &leaseUntil
		})
	}
	err := d.retryStoreWrite(claim)
	if err != nil {
		if err == store.ErrInvalidTransition {
			// A cancelled/terminal run may still be present in the in-memory
			// queue after a user action. Drop it idempotently.
			d.removeEnqueued(j.runID)
			return
		}
		log.Printf("dispatcher: failed to claim run %s: %v", j.runID, err)
		d.appendDispatchLog(ctx, j.runID, "error", "分发任务时暂时无法写入控制数据库，稍后自动重试: "+err.Error())
		// Never leave a run only in enqueuedRuns. The old implementation did
		// that on SQLITE_BUSY, which made the run permanently queued until a
		// server restart.
		d.requeueJob(j)
		return
	}
	d.appendDispatchLog(ctx, j.runID, "info", fmt.Sprintf("已分发到 Agent（第 %d 次尝试）", attempt))

	// CommandSource resolves repository/target/plan from the run itself.

	// Build ExecuteCommand via the CommandSource (params + decrypted secrets).
	_, cmd, err := d.Src.BuildCommand(ctx, j.runID)
	if err != nil {
		log.Printf("dispatcher: failed to build command for run %s: %v", j.runID, err)
		// Permanent build failure: fail the run instead of spinning the queue.
		if terr := d.retryStoreWrite(func() error {
			return d.store.TransitionRun(ctx, j.runID, model.RunDispatched, model.RunFailed, func(r *model.Run) {
				now := time.Now().UTC()
				r.ErrorCode = model.ErrInvalidPlan
				r.ErrorMessage = err.Error()
				r.FinishedAt = &now
				r.LeaseExpiresAt = nil
			})
		}); terr != nil {
			// Transition failed: no notification; keep the original build error.
			log.Printf("dispatcher: failed to transition run %s to failed: %v", j.runID, terr)
		} else if nerr := d.notifier.NotifyPlanFailure(ctx, j.runID); nerr != nil {
			notification.LogFailure(j.runID, nerr)
		}
		d.deleteRunSecrets(ctx, j.runID)
		d.removeEnqueued(j.runID)
		return
	}

	// Send command to agent. Database restores require the current connection to
	// have reported safe-restore capability, checked atomically with the send.
	msg := &bmcv1.ServerMessage{
		Payload: &bmcv1.ServerMessage_ExecuteCommand{ExecuteCommand: cmd},
	}
	var sendErr error
	if dbRestore {
		sendErr = d.reg.SendWithCapability(j.agentID, msg, true)
		if errors.Is(sendErr, agentreg.ErrCapabilitiesPending) || errors.Is(sendErr, agentreg.ErrUnsafeDatabaseRestore) {
			code := model.ErrAgentCapabilitiesPending
			reason := "目标 Agent 连接尚未完成能力上报，数据库恢复未下发"
			if errors.Is(sendErr, agentreg.ErrUnsafeDatabaseRestore) {
				code = model.ErrAgentUpgradeRequired
				reason = "目标 Agent 不支持受保护的数据库恢复，请升级 Agent"
			}
			d.appendDispatchLog(ctx, j.runID, "error", reason)
			d.failRestoreBeforeSend(ctx, j.runID, code, reason)
			d.removeEnqueued(j.runID)
			return
		}
	} else {
		sendErr = d.reg.Send(j.agentID, msg)
	}
	if sendErr != nil {
		log.Printf("dispatcher: failed to send command to agent %s: %v", j.agentID, sendErr)
		d.appendDispatchLog(ctx, j.runID, "warn", "发送到 Agent 失败，任务将自动重试: "+sendErr.Error())
		// Revert to queued. If the database is still locked, leave the run in
		// dispatched state and let the lease watchdog handle it; retrying the
		// command while the durable state is dispatched could duplicate work.
		if revertErr := d.retryStoreWrite(func() error {
			return d.store.TransitionRun(ctx, j.runID, model.RunDispatched, model.RunQueued, func(r *model.Run) {
				r.LeaseExpiresAt = nil
			})
		}); revertErr != nil {
			log.Printf("dispatcher: failed to requeue run %s after send failure: %v", j.runID, revertErr)
			d.appendDispatchLog(ctx, j.runID, "error", "发送失败后无法恢复排队状态，等待租约看门狗处理: "+revertErr.Error())
			d.removeEnqueued(j.runID)
			return
		}
		d.requeueJob(j)
		return
	}

	// Do not dispatch the next operation for this repository until this run
	// reaches a terminal state (or is explicitly re-queued after disconnect).
	// Restic serializes repository access; sending commands back-to-back would
	// otherwise create concurrent backup/prune/restore processes on the agent.
	for {
		run, err := d.store.GetRun(ctx, j.runID)
		if err != nil {
			d.removeEnqueued(j.runID)
			return
		}
		if run.Status == model.RunSucceeded || run.Status == model.RunFailed || run.Status == model.RunCancelled {
			d.removeEnqueued(j.runID)
			return
		}
		if run.Status == model.RunQueued {
			// The agent stream was lost and the service re-queued a retry-safe
			// operation. Put the same idempotent job back at the tail.
			d.requeueJob(j)
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// retryStoreWrite retries only transient SQLite lock errors. Other errors are
// returned immediately so invalid data or a broken database is still visible.
func (d *Dispatcher) retryStoreWrite(write func() error) error {
	delay := d.cfg.ClaimRetryDelay
	if delay <= 0 {
		delay = 100 * time.Millisecond
	}
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		err = write()
		if err == nil || err == store.ErrInvalidTransition || !isSQLiteBusy(err) {
			return err
		}
		if attempt < 5 {
			time.Sleep(delay)
			delay *= 2
		}
	}
	return err
}

func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") ||
		strings.Contains(message, "sqlite_busy") ||
		strings.Contains(message, "database table is locked")
}

func (d *Dispatcher) appendDispatchLog(ctx context.Context, runID, level, message string) {
	now := time.Now().UTC()
	d.dispatchLogMu.Lock()
	// 同一 run 的同一提示在窗口内只记一条：仓库被未安全终结的恢复长时间阻塞时
	// 每次派发都会产生同样的告警，30s 窗口仍会以约 2 条/分钟无限增长（实测某运行
	// 已积累 161 条）。10 分钟窗口对运维可见性没有损失，写入量降为原来的 1/20。
	if previous, ok := d.dispatchLogState[runID]; ok && previous.message == message && now.Sub(previous.at) < 10*time.Minute {
		d.dispatchLogMu.Unlock()
		return
	}
	d.dispatchLogState[runID] = dispatchLogState{message: message, at: now}
	d.dispatchLogMu.Unlock()

	// 单条幂等写入：id 由 store 分配，source_seq 用当前纳秒保证同一 run 内不重复。
	logs := []model.RunLog{{
		RunID:     runID,
		Source:    model.RunLogSourceServer,
		SourceSeq: uint64(now.UnixNano()),
		Timestamp: now,
		Level:     level,
		Message:   message,
	}}
	if err := d.store.AppendRunLogs(ctx, logs); err != nil {
		log.Printf("dispatcher: failed to persist run log for %s: %v", runID, err)
		return
	}
	if d.Bus != nil && logs[0].ID != 0 {
		d.Bus.Publish(runID, events.Event{Type: events.Log, Entry: &logs[0]})
	}
}

// requeueJob puts the job back at the tail of its repository queue after a delay.
func (d *Dispatcher) requeueJob(j *job) {
	time.Sleep(d.cfg.OfflineRetryDelay)

	d.mu.Lock()
	rq, ok := d.repoQueues[j.repositoryID]
	d.mu.Unlock()

	if !ok {
		// Queue disappeared, drop job
		d.removeEnqueued(j.runID)
		return
	}

	rq.mu.Lock()
	rq.jobs = append(rq.jobs, j)
	rq.mu.Unlock()

	select {
	case rq.workerCh <- struct{}{}:
	default:
	}
}

// removeEnqueued marks a run as no longer enqueued.
func (d *Dispatcher) removeEnqueued(runID string) {
	d.mu.Lock()
	delete(d.enqueuedRuns, runID)
	delete(d.processingRuns, runID)
	d.mu.Unlock()
	d.dispatchLogMu.Lock()
	delete(d.dispatchLogState, runID)
	d.dispatchLogMu.Unlock()
}

func (d *Dispatcher) markProcessing(runID string, processing bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if processing {
		d.processingRuns[runID] = true
	} else {
		delete(d.processingRuns, runID)
	}
}

// recoverQueuedRuns repairs queued rows that lost their in-memory queue entry
// after a process error. This is intentionally run periodically, not only at
// startup, because transient SQLite locks can happen while the server is live.
func (d *Dispatcher) recoverQueuedRuns() {
	ctx := context.Background()
	runs, err := d.store.ListRunsByStatus(ctx, []string{model.RunQueued})
	if err != nil {
		log.Printf("watchdog: failed to recover queued runs: %v", err)
		return
	}
	for _, run := range runs {
		if !d.prepareRecovery(run.ID, run.RepositoryID) {
			continue
		}
		log.Printf("watchdog: recovering queued run %s", run.ID)
		d.Enqueue(ctx, run.ID, run.AgentID, run.RepositoryID)
	}
}

// prepareRecovery returns true when the run is not represented by a live
// worker/job. It removes an orphaned idempotency marker left by older builds.
func (d *Dispatcher) prepareRecovery(runID, repositoryID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.processingRuns[runID] {
		return false
	}
	rq := d.repoQueues[repositoryID]
	if rq != nil {
		rq.mu.Lock()
		for _, queued := range rq.jobs {
			if queued.runID == runID {
				rq.mu.Unlock()
				return false
			}
		}
		rq.mu.Unlock()
	}
	delete(d.enqueuedRuns, runID)
	return true
}

func (d *Dispatcher) deleteRunSecrets(ctx context.Context, runID string) {
	if rs, ok := d.store.(interface {
		DeleteRunSecrets(context.Context, string) error
	}); ok {
		if err := rs.DeleteRunSecrets(ctx, runID); err != nil {
			log.Printf("dispatcher: failed to delete run secrets for %s: %v", runID, err)
		}
	}
}

// isDatabaseRestoreKind reports whether a restore kind targets a database and
// therefore needs the safe-restore capability and the global occupancy.
func isDatabaseRestoreKind(kind string) bool {
	switch kind {
	case model.KindPostgreSQL, model.KindMySQL, model.KindMongoDB, model.KindSQLite:
		return true
	default:
		return false
	}
}

// failRestoreBeforeSend terminates a restore run that could not be dispatched.
// The target is untouched at this point, so the request records the safe
// "failed" phase and releases the occupancy.
func (d *Dispatcher) failRestoreBeforeSend(ctx context.Context, runID, code, message string) {
	now := time.Now().UTC()
	err := d.store.FinishRestoreRun(ctx, store.FinishRestoreRunInput{
		RunID:        runID,
		FromStatuses: []string{model.RunQueued, model.RunDispatched},
		ToStatus:     model.RunFailed,
		ErrorCode:    code,
		ErrorMessage: message,
		Phase:        model.RestorePhaseFailed,
		FinishedAt:   now,
	})
	if err != nil && !errors.Is(err, store.ErrInvalidTransition) {
		log.Printf("dispatcher: failed to terminate restore run %s before send: %v", runID, err)
		return
	}
	d.deleteRunSecrets(ctx, runID)
	if nerr := d.notifier.NotifyPlanFailure(ctx, runID); nerr != nil {
		notification.LogFailure(runID, nerr)
	}
}

// sendCancel asks the agent to stop a run.
func (d *Dispatcher) sendCancel(ctx context.Context, runID, agentID string) {
	if !d.reg.IsConnected(agentID) {
		return
	}
	cancelMsg := &bmcv1.ServerMessage{
		Payload: &bmcv1.ServerMessage_CancelCommand{
			CancelCommand: &bmcv1.CancelCommand{RunId: runID},
		},
	}
	if err := d.reg.Send(agentID, cancelMsg); err != nil {
		log.Printf("dispatcher: failed to send cancel to agent %s: %v", agentID, err)
	}
}

// cancelRestore handles an operator cancellation of a restore run. A queued run
// ends immediately; a dispatched/running run keeps its non-terminal state until
// the agent reports the rollback outcome.
func (d *Dispatcher) cancelRestore(ctx context.Context, run *model.Run) error {
	if run.Status == model.RunQueued {
		err := d.store.FinishRestoreRun(ctx, store.FinishRestoreRunInput{
			RunID:        run.ID,
			FromStatuses: []string{model.RunQueued},
			ToStatus:     model.RunCancelled,
			ErrorCode:    model.ErrCancelled,
			ErrorMessage: "restore cancelled before dispatch",
			Phase:        model.RestorePhaseFailed,
			FinishedAt:   time.Now().UTC(),
		})
		if err != nil && !errors.Is(err, store.ErrInvalidTransition) {
			return err
		}
		d.deleteRunSecrets(ctx, run.ID)
		return nil
	}
	d.sendCancel(ctx, run.ID, run.AgentID)
	if err := d.store.RequestRestoreStop(ctx, run.ID, time.Now().UTC().Add(restoreCancelGrace)); err != nil {
		return err
	}
	d.appendDispatchLog(ctx, run.ID, "warn", "已请求 Agent 停止恢复，等待回滚结果")
	return nil
}

// restoreCancelGraceExhausted records manual recovery for a restore whose cancel
// grace expired or whose result never arrived. The run ends but the restore
// request keeps blocking its repository and the database occupancy.
func (d *Dispatcher) markRestoreManualRecovery(ctx context.Context, run *model.Run) {
	err := d.store.FinishRestoreRun(ctx, store.FinishRestoreRunInput{
		RunID:        run.ID,
		ToStatus:     model.RunFailed,
		ErrorCode:    model.ErrTimeout,
		ErrorMessage: "restore did not report a trusted result; manual recovery required",
		Phase:        model.RestorePhaseManualRecoveryNeeded,
		FinishedAt:   time.Now().UTC(),
	})
	if err != nil {
		if !errors.Is(err, store.ErrInvalidTransition) {
			log.Printf("dispatcher: failed to mark restore run %s manual recovery: %v", run.ID, err)
		}
		return
	}
	d.deleteRunSecrets(ctx, run.ID)
	if nerr := d.notifier.NotifyPlanFailure(ctx, run.ID); nerr != nil {
		notification.LogFailure(run.ID, nerr)
	}
}

// handleRestoreTimeout applies the restore-specific deadline: the run is asked
// to stop first, then given a grace window to finish rollback before manual
// recovery is recorded. Repeated ticks never extend either deadline.
func (d *Dispatcher) handleRestoreTimeout(ctx context.Context, run *model.Run, now time.Time) {
	if run.LeaseExpiresAt == nil || now.Before(*run.LeaseExpiresAt) {
		return
	}
	rr, err := d.store.GetRestoreRequestByRunID(ctx, run.ID)
	if err != nil || model.RestorePhaseIsTerminal(rr.Phase) {
		return
	}
	// 失去执行连接（含 Server 重启后 Agent 未重连）不能证明执行已经停止。
	if !d.reg.IsConnected(run.AgentID) {
		if rr.Phase != model.RestorePhaseManualRecoveryNeeded {
			d.markRestoreManualRecovery(ctx, run)
		}
		return
	}
	if rr.Phase == model.RestorePhaseCancelling {
		d.markRestoreManualRecovery(ctx, run)
		return
	}
	d.sendCancel(ctx, run.ID, run.AgentID)
	if err := d.store.RequestRestoreStop(ctx, run.ID, now.Add(restoreCancelGrace)); err != nil {
		log.Printf("dispatcher: failed to request restore stop for %s: %v", run.ID, err)
		return
	}
	d.appendDispatchLog(ctx, run.ID, "error", "恢复执行超过期限，已请求 Agent 停止并等待回滚结果")
}

// buildExecuteCommand constructs the ExecuteCommand message for a run.
// Plan may be nil for system operations (snapshots/check/forget/etc) that don't have a plan.
func (d *Dispatcher) buildExecuteCommand(run *model.Run, _ *model.Plan, repo *model.Repository, target *model.StorageTarget) (*bmcv1.ExecuteCommand, error) {
	// Map operation string to proto enum
	var op bmcv1.ExecuteCommand_Operation
	switch run.Operation {
	case model.OpBackup:
		op = bmcv1.ExecuteCommand_BACKUP
	case model.OpRestore:
		op = bmcv1.ExecuteCommand_RESTORE
	case model.OpRestoreDryRun:
		op = bmcv1.ExecuteCommand_RESTORE_DRY_RUN
	case model.OpCheck:
		op = bmcv1.ExecuteCommand_CHECK
	case model.OpForget:
		op = bmcv1.ExecuteCommand_FORGET
	case model.OpSnapshots:
		op = bmcv1.ExecuteCommand_SNAPSHOTS
	case model.OpSnapshotLs:
		op = bmcv1.ExecuteCommand_SNAPSHOT_LS
	case model.OpVerifyRemote:
		op = bmcv1.ExecuteCommand_VERIFY_STORAGE_REMOTE
	case model.OpValidatePaths:
		op = bmcv1.ExecuteCommand_VALIDATE_PATHS
	case model.OpProbeCaps:
		op = bmcv1.ExecuteCommand_PROBE_CAPABILITIES
	default:
		return nil, fmt.Errorf("unknown operation: %s", run.Operation)
	}

	// Build secrets (only if repo/target available)
	var secrets *bmcv1.SecretSet
	if repo != nil && target != nil {
		secrets = &bmcv1.SecretSet{
			RcloneConf:     string(target.EncryptedConfig),
			ResticPassword: string(repo.EncryptedPassword),
			DbPassword:     "",
		}
	}

	return &bmcv1.ExecuteCommand{
		CommandId:  model.NewUUIDv7(),
		RunId:      run.ID,
		Operation:  op,
		ParamsJson: []byte("{}"),
		Secrets:    secrets,
	}, nil
}

// Cancel implements dispatch.Dispatcher.
func (d *Dispatcher) Cancel(ctx context.Context, runID string) error {
	run, err := d.store.GetRun(ctx, runID)
	if err != nil {
		if err == store.ErrNotFound {
			return nil // idempotent
		}
		return err
	}

	// If run is already terminal, nothing to do
	if run.Status == model.RunSucceeded || run.Status == model.RunFailed || run.Status == model.RunCancelled {
		return nil
	}

	if run.Operation == model.OpRestore {
		return d.cancelRestore(ctx, run)
	}

	// If run is queued (not yet dispatched), just mark cancelled
	if run.Status == model.RunQueued {
		err := d.store.TransitionRun(ctx, runID, run.Status, model.RunCancelled, func(r *model.Run) {
			now := time.Now().UTC()
			r.FinishedAt = &now
			r.ErrorCode = model.ErrCancelled
		})
		if err == nil {
			d.deleteRunSecrets(ctx, runID)
		}
		return err
	}

	// If dispatched or running, send CancelCommand to agent
	if d.reg.IsConnected(run.AgentID) {
		cancelMsg := &bmcv1.ServerMessage{
			Payload: &bmcv1.ServerMessage_CancelCommand{
				CancelCommand: &bmcv1.CancelCommand{RunId: runID},
			},
		}
		if err := d.reg.Send(run.AgentID, cancelMsg); err != nil {
			log.Printf("dispatcher: failed to send cancel to agent %s: %v", run.AgentID, err)
		}
	}

	// If running and no response within timeout, we'll rely on watchdog to mark failed
	// For now, just transition to cancelled if we can
	err = d.store.TransitionRun(ctx, runID, run.Status, model.RunCancelled, func(r *model.Run) {
		now := time.Now().UTC()
		r.FinishedAt = &now
		r.ErrorCode = model.ErrCancelled
	})
	if err == nil {
		d.deleteRunSecrets(ctx, runID)
	}
	return err
}

// ConnectedAgents implements dispatch.Dispatcher.
func (d *Dispatcher) ConnectedAgents() []string {
	return d.reg.List()
}

// IsConnected implements dispatch.Dispatcher.
func (d *Dispatcher) IsConnected(agentID string) bool {
	return d.reg.Connected(agentID)
}

// QueueDepth returns the total number of jobs across all repository queues.
func (d *Dispatcher) QueueDepth() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	depth := 0
	for _, rq := range d.repoQueues {
		rq.mu.Lock()
		depth += len(rq.jobs)
		rq.mu.Unlock()
	}
	return depth
}

// StartWatchdog starts the timeout watchdog goroutine.
func (d *Dispatcher) StartWatchdog() {
	d.wg.Add(1)
	go d.watchdogLoop()
}

// StopWatchdog signals the watchdog to stop and waits for all workers to finish.
func (d *Dispatcher) StopWatchdog() {
	close(d.stopWatchdog)
	d.wg.Wait()
}

// watchdogLoop periodically checks for timed-out runs.
func (d *Dispatcher) watchdogLoop() {
	defer d.wg.Done()

	ticker := time.NewTicker(d.cfg.WatchdogInterval)
	defer ticker.Stop()

	for {
		select {
		case <-d.stopWatchdog:
			return
		case <-ticker.C:
			d.checkTimeouts()
		}
	}
}

// checkTimeouts finds runs that have exceeded their timeout and cancels them.
func (d *Dispatcher) checkTimeouts() {
	ctx := context.Background()

	// Repair queued rows that are not represented by a live in-memory job.
	// This also heals rows orphaned by a previous server version that returned
	// after SQLITE_BUSY without re-queueing the job.
	d.recoverQueuedRuns()

	// Find dispatched and running runs
	runs, err := d.store.ListRunsByStatus(ctx, []string{model.RunDispatched, model.RunRunning})
	if err != nil {
		log.Printf("watchdog: failed to list runs: %v", err)
		return
	}

	now := time.Now().UTC()
	for _, run := range runs {
		if run.Operation == model.OpRestore {
			// 恢复使用专用期限与取消宽限，不能沿用系统任务的硬失败逻辑。
			d.handleRestoreTimeout(ctx, &run, now)
			continue
		}
		if run.LeaseExpiresAt != nil && now.After(*run.LeaseExpiresAt) {
			if retryableOperation(run.Operation) {
				if err := d.store.TransitionRun(ctx, run.ID, run.Status, model.RunQueued, func(r *model.Run) {
					r.StartedAt = nil
					r.LeaseExpiresAt = nil
					r.ErrorCode = ""
					r.ErrorMessage = ""
					// 同上：重排队必须清掉进度，否则 queued 的 run 仍显示旧 phase。
					r.ProgressJSON = "{}"
				}); err == nil {
					d.Enqueue(ctx, run.ID, run.AgentID, run.RepositoryID)
				}
			} else {
				if err := d.store.TransitionRun(ctx, run.ID, run.Status, model.RunFailed, func(r *model.Run) {
					now := time.Now().UTC()
					r.FinishedAt = &now
					r.ErrorCode = model.ErrAgentDisconnected
					r.ErrorMessage = "dispatch lease expired"
					r.LeaseExpiresAt = nil
				}); err == nil {
					d.deleteRunSecrets(ctx, run.ID)
				}
			}
			continue
		}
		// 系统维护任务没有 Plan 可读取超时；forget 可能因 --retry-lock 等待
		// 远端锁释放，不能沿用普通 5 分钟默认值。
		timeoutSeconds := 300
		if run.Operation == model.OpForget {
			timeoutSeconds = 15 * 60
		}
		if run.PlanID != "" {
			plan, err := d.store.GetPlan(ctx, run.PlanID)
			if err == nil && plan.TimeoutSeconds > 0 {
				timeoutSeconds = plan.TimeoutSeconds
			}
		}

		timeout := time.Duration(timeoutSeconds) * time.Second

		// Check queued time for dispatched runs, started time for running runs
		var elapsed time.Duration
		if run.Status == model.RunDispatched {
			elapsed = now.Sub(run.QueuedAt)
		} else if run.Status == model.RunRunning && run.StartedAt != nil {
			elapsed = now.Sub(*run.StartedAt)
		}

		if elapsed > timeout {
			// Timeout exceeded
			log.Printf("watchdog: run %s timed out (status=%s, elapsed=%v, timeout=%v)", run.ID, run.Status, elapsed, timeout)

			if run.Status == model.RunRunning {
				// Running but no response for 30s -> force fail
				if elapsed > d.cfg.NoResponseTimeout {
					// 判定超时后必须同时通知 agent 停止：否则 dump/上传会继续跑到
					// 完成并创建快照，而 run 已是终态 failed(snapshot_id 为空)，
					// 于是产出一个 run 记录里看不到的孤儿快照（实测超时判定后
					// 53s 快照才被创建）。
					if d.reg.IsConnected(run.AgentID) {
						cancelMsg := &bmcv1.ServerMessage{
							Payload: &bmcv1.ServerMessage_CancelCommand{
								CancelCommand: &bmcv1.CancelCommand{RunId: run.ID},
							},
						}
						_ = d.reg.Send(run.AgentID, cancelMsg)
					}
					if err := d.store.TransitionRun(ctx, run.ID, model.RunRunning, model.RunFailed, func(r *model.Run) {
						now := time.Now().UTC()
						r.FinishedAt = &now
						r.ErrorCode = model.ErrTimeout
						r.ErrorMessage = "agent did not respond within timeout"
					}); err != nil {
						log.Printf("watchdog: failed to fail run %s: %v", run.ID, err)
					} else {
						if nerr := d.notifier.NotifyPlanFailure(ctx, run.ID); nerr != nil {
							notification.LogFailure(run.ID, nerr)
						}
						d.deleteRunSecrets(ctx, run.ID)
					}
				}
			} else if run.Status == model.RunDispatched {
				// Send cancel and mark failed
				if d.reg.IsConnected(run.AgentID) {
					cancelMsg := &bmcv1.ServerMessage{
						Payload: &bmcv1.ServerMessage_CancelCommand{
							CancelCommand: &bmcv1.CancelCommand{RunId: run.ID},
						},
					}
					_ = d.reg.Send(run.AgentID, cancelMsg)
				}
				if err := d.store.TransitionRun(ctx, run.ID, model.RunDispatched, model.RunFailed, func(r *model.Run) {
					now := time.Now().UTC()
					r.FinishedAt = &now
					r.ErrorCode = model.ErrTimeout
					r.ErrorMessage = "run timed out in dispatched state"
				}); err != nil {
					log.Printf("watchdog: failed to fail run %s: %v", run.ID, err)
				} else {
					if nerr := d.notifier.NotifyPlanFailure(ctx, run.ID); nerr != nil {
						notification.LogFailure(run.ID, nerr)
					}
					d.deleteRunSecrets(ctx, run.ID)
				}
			}
		}
	}
}

// Compile-time check that Dispatcher implements dispatch.Dispatcher
var _ dispatch.Dispatcher = (*Dispatcher)(nil)
