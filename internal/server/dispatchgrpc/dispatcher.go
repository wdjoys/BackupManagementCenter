package dispatchgrpc

import (
	"backupmanagementcenter/internal/dispatch"
	"backupmanagementcenter/internal/model"
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	bmcv1 "backupmanagementcenter/api/proto/v1"
	"backupmanagementcenter/internal/server/agentreg"
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
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		WatchdogInterval:  15 * time.Second,
		OfflineRetryDelay: 2 * time.Second,
		NoResponseTimeout: 30 * time.Second,
	}
}

// job represents a run waiting to be dispatched to an agent.
type job struct {
	runID        string
	agentID      string
	repositoryID string
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

	mu           sync.Mutex
	repoQueues   map[string]*repoQueue
	enqueuedRuns map[string]bool // runID -> true (for idempotency)

	// watchdog
	stopWatchdog chan struct{}
	wg           sync.WaitGroup
}

// NewDispatcher creates a new gRPC-based dispatcher. notifier may be nil; a
// no-op is used then.
func NewDispatcher(s store.Store, reg *agentreg.Registry, cfg Config, notifier notification.FailureNotifier) *Dispatcher {
	if notifier == nil {
		notifier = notification.NopNotifier{}
	}
	return &Dispatcher{
		store:        s,
		reg:          reg,
		cfg:          cfg,
		notifier:     notifier,
		repoQueues:   make(map[string]*repoQueue),
		enqueuedRuns: make(map[string]bool),
		stopWatchdog: make(chan struct{}),
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

				d.processJob(j)
			}
		}
	}
}

// processJob handles a single job dispatch.
func (d *Dispatcher) processJob(j *job) {
	ctx := context.Background()

	// Check if agent is connected
	if !d.reg.IsConnected(j.agentID) {
		log.Printf("dispatcher: agent %s not connected; requeue run %s", j.agentID, j.runID)
		d.requeueJob(j)
		return
	}

	// CommandSource resolves repository/target/plan from the run itself.

	// CommandSource resolves repository/target/plan from the run itself.

	// Build ExecuteCommand via the CommandSource (params + decrypted secrets).
	_, cmd, err := d.Src.BuildCommand(ctx, j.runID)
	if err != nil {
		log.Printf("dispatcher: failed to build command for run %s: %v", j.runID, err)
		// Permanent build failure: fail the run instead of spinning the queue.
		if terr := d.store.TransitionRun(ctx, j.runID, model.RunQueued, model.RunFailed, func(r *model.Run) {
			now := time.Now().UTC()
			r.ErrorCode = model.ErrInvalidPlan
			r.ErrorMessage = err.Error()
			r.FinishedAt = &now
		}); terr != nil {
			// Transition failed: no notification; keep the original build error.
			log.Printf("dispatcher: failed to transition run %s to failed: %v", j.runID, terr)
		} else if nerr := d.notifier.NotifyPlanFailure(ctx, j.runID); nerr != nil {
			notification.LogFailure(j.runID, nerr)
		}
		d.removeEnqueued(j.runID)
		return
	}

	// Send command to agent
	msg := &bmcv1.ServerMessage{
		Payload: &bmcv1.ServerMessage_ExecuteCommand{ExecuteCommand: cmd},
	}
	if err := d.reg.Send(j.agentID, msg); err != nil {
		log.Printf("dispatcher: failed to send command to agent %s: %v", j.agentID, err)
		// Revert to queued
		_ = d.store.TransitionRun(ctx, j.runID, model.RunDispatched, model.RunQueued, nil)
		d.requeueJob(j)
		return
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
	d.mu.Unlock()
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

	// If run is queued (not yet dispatched), just mark cancelled
	if run.Status == model.RunQueued {
		return d.store.TransitionRun(ctx, runID, run.Status, model.RunCancelled, func(r *model.Run) {
			now := time.Now().UTC()
			r.FinishedAt = &now
			r.ErrorCode = model.ErrCancelled
		})
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
	return d.store.TransitionRun(ctx, runID, run.Status, model.RunCancelled, func(r *model.Run) {
		now := time.Now().UTC()
		r.FinishedAt = &now
		r.ErrorCode = model.ErrCancelled
	})
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

	// Find dispatched and running runs
	runs, err := d.store.ListRunsByStatus(ctx, []string{model.RunDispatched, model.RunRunning})
	if err != nil {
		log.Printf("watchdog: failed to list runs: %v", err)
		return
	}

	now := time.Now().UTC()
	for _, run := range runs {
		// Use plan timeout if available, otherwise default 300s
		timeoutSeconds := 300
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
					if err := d.store.TransitionRun(ctx, run.ID, model.RunRunning, model.RunFailed, func(r *model.Run) {
						now := time.Now().UTC()
						r.FinishedAt = &now
						r.ErrorCode = model.ErrTimeout
						r.ErrorMessage = "agent did not respond within timeout"
					}); err != nil {
						log.Printf("watchdog: failed to fail run %s: %v", run.ID, err)
					} else if nerr := d.notifier.NotifyPlanFailure(ctx, run.ID); nerr != nil {
						notification.LogFailure(run.ID, nerr)
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
				} else if nerr := d.notifier.NotifyPlanFailure(ctx, run.ID); nerr != nil {
					notification.LogFailure(run.ID, nerr)
				}
			}
		}
	}
}

// Compile-time check that Dispatcher implements dispatch.Dispatcher
var _ dispatch.Dispatcher = (*Dispatcher)(nil)
