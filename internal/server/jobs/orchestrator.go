// Package jobs is the server-side orchestrator for run creation, secret
// resolution, command building and synchronous wait. It sits above the
// store / events bus / dispatcher and exposes the business-level surface
// the API layer and scheduler call into.
//
// # System-run result convention
//
// The Run model has no dedicated result_json column. System runs
// (snapshots, snapshot_ls, verify_storage_remote, restore_dry_run) carry
// their structured payload in the Run.ProgressJSON column, so callers
// parse ProgressJSON after WaitRun returns a terminal run. Backup runs
// continue to use ProgressJSON for their incremental progress snapshot.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	bmcv1 "backupmanagementcenter/api/proto/v1"
	"backupmanagementcenter/internal/dispatch"
	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/secrets"
	"backupmanagementcenter/internal/server/events"
	"backupmanagementcenter/internal/server/store"
)

var (
	ErrForbidden     = errors.New("forbidden")
	ErrNotFound      = errors.New("not found")
	ErrWaitTimeout   = errors.New("wait timeout")
	ErrPlanInvalid   = errors.New("invalid plan")
	ErrAgentRevoked  = errors.New("agent revoked")
	ErrAgentOffline  = errors.New("agent offline")
	ErrMissingTools  = errors.New("missing_tools")
	ErrPathInvalid   = errors.New("path_validation_failed")
	ErrRepoPassword  = errors.New("wrong_repository_password")
	ErrRepoLocked    = errors.New("repository_locked")
	ErrRepoMissing   = errors.New("repository_missing")
	ErrStorageRemote = errors.New("storage_remote_unreachable")
)

// MissingToolsError carries the list of missing tool names.
type MissingToolsError struct {
	Tools []string
}

func (e *MissingToolsError) Error() string {
	return fmt.Sprintf("missing_tools: %s", strings.Join(e.Tools, ","))
}

// RestoreInput is the API-supplied restore request.
type RestoreInput struct {
	RepositoryID   string
	SnapshotID     string
	RestoreKind    string // filesystem|postgresql|mysql|mongodb|sqlite
	Target         model.RestoreTarget
	Overwrite      bool
	Confirmation   string
	TargetPassword string // database-only; populated by UI
}

// VerifyResult is the parsed payload of a VERIFY_STORAGE_REMOTE run.
type VerifyResult struct {
	RemoteType string
	Entries    int
}

// DryRunStats is the parsed payload of a RESTORE_DRY_RUN run.
type DryRunStats struct {
	Add     int64    `json:"add"`
	Changed int64    `json:"changed"`
	Skipped int64    `json:"skipped"`
	Delete  int64    `json:"delete"`
	Sample  []string `json:"sample"`
}

// TreeResult is the parsed payload of a SNAPSHOT_LS run.
type TreeResult struct {
	Entries []TreeEntry `json:"entries"`
	Path    string      `json:"path"`
}

type TreeEntry struct {
	Name  string `json:"name"`
	Type  string `json:"type"` // file|dir|symlink
	Path  string `json:"path,omitempty"`
	Size  int64  `json:"size"`
	Mtime string `json:"mtime"`
}

// internalResult is a generic wrapper used to interpret agent-side
// result_json for system runs. Different operations project different
// fields; each method parses it into its typed result.
type internalResult struct {
	Error    string `json:"error,omitempty"`
	ExitCode int    `json:"exit_code"`
}

// Orchestrator is the server-side run coordinator.
type Orchestrator struct {
	Store      store.Store
	Seal       secrets.Sealer
	Disp       dispatch.Dispatcher
	Bus        events.Bus
	InstanceID string

	mu        sync.RWMutex
	confStash map[string]stashEntry
}

// New constructs a new Orchestrator.
func New(st store.Store, seal secrets.Sealer, disp dispatch.Dispatcher, bus events.Bus, instanceID string) *Orchestrator {
	return &Orchestrator{
		Store:      st,
		Seal:       seal,
		Disp:       disp,
		Bus:        bus,
		InstanceID: instanceID,
	}
}

// ---------------------------------------------------------------------------
// Plan / manual runs
// ---------------------------------------------------------------------------

// StartPlanRun creates a backup run for the given plan. scheduledAt is the
// cron slot (unique with planID). Nil scheduledAt means a manual run.
func (o *Orchestrator) StartPlanRun(ctx context.Context, planID string, scheduledAt *time.Time) (*model.Run, error) {
	plan, err := o.Store.GetPlan(ctx, planID)
	if err != nil {
		return nil, err
	}

	agent, err := o.Store.GetAgent(ctx, plan.AgentID)
	if err != nil {
		return nil, err
	}
	if agent.Revoked {
		return nil, ErrAgentRevoked
	}
	if err := o.checkCapabilities(agent, plan.Kind); err != nil {
		return nil, err
	}

	repo, err := o.Store.GetRepository(ctx, plan.RepositoryID)
	if err != nil {
		return nil, err
	}
	if repo.AgentID != plan.AgentID {
		return nil, fmt.Errorf("%s: repository %s belongs to agent %s, plan selects agent %s", model.ErrInvalidPlan, repo.ID, repo.AgentID, plan.AgentID)
	}
	if repo.Status != "ready" {
		return nil, fmt.Errorf("repository not ready: %s", repo.Status)
	}

	target, err := o.Store.GetStorageTarget(ctx, repo.StorageTargetID)
	if err != nil {
		return nil, err
	}

	runID := model.NewUUIDv7()
	now := time.Now().UTC()

	run := &model.Run{
		ID:           runID,
		PlanID:       plan.ID,
		AgentID:      plan.AgentID,
		Operation:    model.OpBackup,
		Status:       model.RunQueued,
		QueuedAt:     now,
		RepositoryID: repo.ID,
		ScheduledAt:  scheduledAt,
		ProgressJSON: "{}",
	}
	if err := o.Store.CreateRun(ctx, run); err != nil {
		if errors.Is(err, store.ErrDuplicateRun) {
			return nil, store.ErrDuplicateRun
		}
		return nil, err
	}

	o.Disp.Enqueue(ctx, run.ID, run.AgentID, run.RepositoryID)

	o.Audit(ctx, "system", "scheduler", "run.start", "run", run.ID,
		map[string]string{"plan_id": plan.ID, "agent_id": plan.AgentID, "kind": plan.Kind})

	// Emit queued state event so WaitRun subscribers see it immediately.
	o.Bus.Publish(run.ID, events.Event{Type: events.State, Run: run})

	// Also stash plan+repo+target lookup info into ProgressJSON so
	// BuildCommand can reconstruct params without additional store reads.
	payload := backupRunLookup{
		PlanID:   plan.ID,
		AgentID:  plan.AgentID,
		Kind:     plan.Kind,
		RepoID:   repo.ID,
		TargetID: target.ID,
	}
	if b, err := json.Marshal(payload); err == nil {
		run.ProgressJSON = string(b)
	}

	return run, nil
}

type backupRunLookup struct {
	PlanID, AgentID, Kind, RepoID, TargetID string
}

// ManualRun is equivalent to StartPlanRun(ctx, planID, nil).
func (o *Orchestrator) ManualRun(ctx context.Context, planID string) (*model.Run, error) {
	return o.StartPlanRun(ctx, planID, nil)
}

// ---------------------------------------------------------------------------
// System runs
// ---------------------------------------------------------------------------

// SystemRun creates a run without a plan, executes the given task, enqueues
// it to the dispatcher and returns the queued run. Result payloads live in
// the run's ProgressJSON column after terminal (see package comment).
func (o *Orchestrator) SystemRun(ctx context.Context, agentID, repositoryID, operation string, params any, timeout time.Duration) (*model.Run, error) {
	return o.systemRun(ctx, agentID, repositoryID, operation, params, timeout, "")
}

// SystemRunWithConf is SystemRun with a per-run rclone config stash consumed
// by BuildCommand for verify-remote runs. The stash is written before the run
// becomes visible to the dispatcher, closing the enqueue race.
func (o *Orchestrator) SystemRunWithConf(ctx context.Context, agentID, repositoryID, operation string, params any, timeout time.Duration, conf string) (*model.Run, error) {
	return o.systemRun(ctx, agentID, repositoryID, operation, params, timeout, conf)
}

func (o *Orchestrator) systemRun(ctx context.Context, agentID, repositoryID, operation string, params any, timeout time.Duration, conf string) (*model.Run, error) {
	agent, err := o.Store.GetAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if agent.Revoked {
		return nil, ErrAgentRevoked
	}

	paramsJSON := "{}"
	if params != nil {
		if b, err := json.Marshal(params); err == nil {
			paramsJSON = string(b)
		}
	}

	now := time.Now().UTC()
	run := &model.Run{
		ID:           model.NewUUIDv7(),
		AgentID:      agentID,
		Operation:    operation,
		Status:       model.RunQueued,
		QueuedAt:     now,
		RepositoryID: repositoryID,
		ProgressJSON: paramsJSON,
	}
	if err := o.Store.CreateRun(ctx, run); err != nil {
		return nil, err
	}

	if conf != "" {
		if rs, ok := o.Store.(interface {
			SaveRunRcloneConfig(context.Context, string, string) error
		}); ok {
			if err := rs.SaveRunRcloneConfig(ctx, run.ID, conf); err != nil {
				_ = o.Store.TransitionRun(ctx, run.ID, model.RunQueued, model.RunFailed, func(r *model.Run) {
					now := time.Now().UTC()
					r.FinishedAt = &now
					r.ErrorCode = "secret_storage_failed"
					r.ErrorMessage = "failed to persist temporary remote configuration"
				})
				return nil, err
			}
		} else {
			o.stashConf(run.ID, conf)
		}
	}

	if repositoryID != "" {
		o.Disp.Enqueue(ctx, run.ID, run.AgentID, repositoryID)
	} else {
		o.Disp.Enqueue(ctx, run.ID, run.AgentID, "")
	}
	o.Bus.Publish(run.ID, events.Event{Type: events.State, Run: run})

	return run, nil
}

// ---------------------------------------------------------------------------
// Cancel
// ---------------------------------------------------------------------------

// CancelRun cancels a run by runID. Queued runs are moved directly to
// cancelled; dispatched/running runs are handed off to the dispatcher.
func (o *Orchestrator) CancelRun(ctx context.Context, runID string) error {
	run, err := o.Store.GetRun(ctx, runID)
	if err != nil {
		return err
	}

	switch run.Status {
	case model.RunQueued:
		return o.Store.TransitionRun(ctx, runID, run.Status, model.RunCancelled,
			func(r *model.Run) {
				now := time.Now().UTC()
				r.FinishedAt = &now
				r.ErrorCode = model.ErrCancelled
			})
	case model.RunDispatched, model.RunRunning:
		_ = o.Disp.Cancel(ctx, runID)
		return nil
	default:
		// already terminal; no-op
		return nil
	}
}

// ---------------------------------------------------------------------------
// Wait
// ---------------------------------------------------------------------------

// WaitRun subscribes to the events bus for runID and polls the store as a
// fallback every 200ms. Returns the final terminal run or ErrWaitTimeout.
func (o *Orchestrator) WaitRun(ctx context.Context, runID string, timeout time.Duration) (*model.Run, error) {
	ch, cancel := o.Bus.Subscribe(runID)
	defer cancel()

	deadline := time.Now().Add(timeout)
	poll := time.NewTicker(200 * time.Millisecond)
	defer poll.Stop()

	// First check current state in case we joined after the fact.
	if run, err := o.Store.GetRun(ctx, runID); err == nil && isTerminal(run.Status) {
		return run, nil
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Until(deadline)):
			return nil, ErrWaitTimeout
		case ev := <-ch:
			if ev.Type == events.State && ev.Run != nil && isTerminal(ev.Run.Status) {
				return o.Store.GetRun(ctx, runID)
			}
		case <-poll.C:
			if run, err := o.Store.GetRun(ctx, runID); err == nil {
				if isTerminal(run.Status) {
					return run, nil
				}
			}
		}
	}
}

// WaitRunResult is like WaitRun but additionally returns the parsed run
// result JSON (ProgressJSON on system runs). Returns nil bytes when the run
// is a backup or has no result.
func (o *Orchestrator) WaitRunResult(ctx context.Context, runID string, timeout time.Duration) (*model.Run, []byte, error) {
	run, err := o.WaitRun(ctx, runID, timeout)
	if err != nil {
		return nil, nil, err
	}
	// Only system runs carry a structured result in ProgressJSON.
	if isSystemRun(run.Operation) && run.ProgressJSON != "" && run.ProgressJSON != "{}" {
		return run, []byte(run.ProgressJSON), nil
	}
	return run, nil, nil
}

func isSystemRun(op string) bool {
	switch op {
	case model.OpSnapshots, model.OpSnapshotLs, model.OpVerifyRemote,
		model.OpRestoreDryRun, model.OpCheck, model.OpValidatePaths,
		model.OpForget, model.OpProbeCaps:
		return true
	}
	return false
}

func isTerminal(status string) bool {
	return status == model.RunSucceeded || status == model.RunFailed || status == model.RunCancelled
}

// ---------------------------------------------------------------------------
// Snapshots / tree / validate / verify / restore / dry-run
// ---------------------------------------------------------------------------

// Snapshots returns the list of snapshots for a repository by executing
// a snapshots run and parsing its result.
func (o *Orchestrator) Snapshots(ctx context.Context, repoID string, agentID string) ([]model.Snapshot, *model.Run, error) {
	repo, err := o.Store.GetRepository(ctx, repoID)
	if err != nil {
		return nil, nil, err
	}

	params := model.SnapshotsTask{Repository: model.RepoAccess{RepositoryPath: repo.RepositoryPath}}
	run, err := o.SystemRun(ctx, agentID, repoID, model.OpSnapshots, params, 0)
	if err != nil {
		return nil, nil, err
	}

	term, resultJSON, err := o.WaitRunResult(ctx, run.ID, 30*time.Second)
	if err != nil {
		return nil, term, err
	}
	if resultJSON == nil {
		return nil, term, nil
	}

	var snaps []model.Snapshot
	if err := json.Unmarshal(resultJSON, &snaps); err != nil {
		return nil, term, err
	}
	return snaps, term, nil
}

// SnapshotTree returns the directory tree inside a snapshot.
func (o *Orchestrator) SnapshotTree(ctx context.Context, repoID, agentID, snapshotID, path string) (*TreeResult, *model.Run, error) {
	repo, err := o.Store.GetRepository(ctx, repoID)
	if err != nil {
		return nil, nil, err
	}

	params := model.SnapshotLsTask{
		Repository: model.RepoAccess{RepositoryPath: repo.RepositoryPath},
		SnapshotID: snapshotID,
		Path:       path,
	}
	run, err := o.SystemRun(ctx, agentID, repoID, model.OpSnapshotLs, params, 0)
	if err != nil {
		return nil, nil, err
	}

	term, resultJSON, err := o.WaitRunResult(ctx, run.ID, 30*time.Second)
	if err != nil {
		return nil, term, err
	}
	if resultJSON == nil {
		return nil, term, nil
	}

	var tr TreeResult
	if err := json.Unmarshal(resultJSON, &tr); err != nil {
		return nil, term, err
	}
	return &tr, term, nil
}

// ValidatePlanSource validates capabilities and, for filesystem plans,
// runs an agent-side path validation.
func (o *Orchestrator) ValidatePlanSource(ctx context.Context, kind string, src model.PlanSource, agentID string) error {
	agent, err := o.Store.GetAgent(ctx, agentID)
	if err != nil {
		return err
	}
	if err := o.checkCapabilities(agent, kind); err != nil {
		return err
	}
	if kind == model.KindFilesystem {
		params := model.ValidatePathsTask{Paths: src.Paths, Excludes: src.Excludes}
		run, err := o.SystemRun(ctx, agentID, "", model.OpValidatePaths, params, 0)
		if err != nil {
			return err
		}
		term, err := o.WaitRun(ctx, run.ID, 30*time.Second)
		if err != nil {
			return err
		}
		if term.Status == model.RunFailed {
			return ErrPathInvalid
		}
	}
	return nil
}

// verifyRemoteWait bounds how long the synchronous validate request waits for
// an agent-side rclone listing. Cold remotes (OAuth token refresh, retries
// against slow endpoints) routinely exceed 30s; rclone's own connection
// timeout alone is 60s. The agent enforces a slightly shorter deadline so the
// real rclone failure surfaces before this wait expires.
const verifyRemoteWait = 120 * time.Second

// ValidateStorageRemote runs a verify remote check on the given agent.
func (o *Orchestrator) ValidateStorageRemote(ctx context.Context, confContent, remoteName, agentID string) (*VerifyResult, error) {
	params := model.VerifyRemoteTask{ConfigProvided: true, RemoteName: remoteName}
	run, err := o.SystemRunWithConf(ctx, agentID, "", model.OpVerifyRemote, params, 0, confContent)
	if err != nil {
		return nil, err
	}

	term, err := o.WaitRun(ctx, run.ID, verifyRemoteWait)
	if err != nil {
		return nil, err
	}
	if term.Status == model.RunFailed {
		if term.ErrorCode == model.ErrStorageRemoteUnreachable {
			// Surface the agent-side reason (rclone stderr) instead of the
			// bare stable code; errorCode() still matches by substring.
			if detail := strings.TrimSpace(term.ErrorMessage); detail != "" {
				return nil, fmt.Errorf("%w: %s", ErrStorageRemote, detail)
			}
			return nil, ErrStorageRemote
		}
		return nil, fmt.Errorf("verify remote failed: %s", term.ErrorMessage)
	}
	var vr VerifyResult
	// Agent result schema: {"remote_type":"...","entries":N}.
	var raw struct {
		RemoteType string `json:"remote_type"`
		Entries    int    `json:"entries"`
	}
	if err := json.Unmarshal([]byte(term.ProgressJSON), &raw); err == nil {
		vr = VerifyResult{RemoteType: raw.RemoteType, Entries: raw.Entries}
	}
	return &vr, nil
}

// CreateStorageTarget persists a new storage target, optionally validating
// the remote via an online agent.
func (o *Orchestrator) CreateStorageTarget(ctx context.Context, actorID, name, conf, remoteName, remotePath string, validate bool) (*model.StorageTarget, error) {
	if validate {
		// require an online agent — use the variant that accepts agentID
		agents := o.Disp.ConnectedAgents()
		if len(agents) == 0 {
			return nil, ErrStorageRemote
		}
		if _, err := o.ValidateStorageRemote(ctx, conf, remoteName, agents[0]); err != nil {
			return nil, err
		}
	}

	now := time.Now().UTC()
	sealed, err := o.Seal.Seal("storage_targets", name, "encrypted_config", conf)
	if err != nil {
		return nil, err
	}

	t := &model.StorageTarget{
		ID:              model.NewUUIDv7(),
		Name:            name,
		Type:            "rclone",
		RemoteName:      remoteName,
		RemotePath:      remotePath,
		EncryptedConfig: sealed,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := o.Store.CreateStorageTarget(ctx, t); err != nil {
		return nil, err
	}

	o.Audit(ctx, "admin", actorID, "storage_target.create", "storage_target", t.ID,
		map[string]string{"name": name, "remote": remoteName})
	return t, nil
}

// CreateStorageTargetWithAgent is like CreateStorageTarget but takes an
// explicit agentID for validation, avoiding the "pick any online agent"
// fallback. When validate is false, agentID is ignored.
func (o *Orchestrator) CreateStorageTargetWithAgent(ctx context.Context, actorID, name, conf, remoteName, remotePath, validateAgentID string, validate bool) (*model.StorageTarget, error) {
	if validate {
		if validateAgentID == "" {
			return nil, ErrStorageRemote
		}
		if _, err := o.ValidateStorageRemote(ctx, conf, remoteName, validateAgentID); err != nil {
			return nil, err
		}
	}

	now := time.Now().UTC()
	targetID := model.NewUUIDv7()
	sealed, err := o.Seal.Seal("storage_targets", targetID, "encrypted_config", conf)
	if err != nil {
		return nil, err
	}

	t := &model.StorageTarget{
		ID:              targetID,
		Name:            name,
		Type:            "rclone",
		RemoteName:      remoteName,
		RemotePath:      remotePath,
		EncryptedConfig: sealed,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := o.Store.CreateStorageTarget(ctx, t); err != nil {
		return nil, err
	}

	o.Audit(ctx, "admin", actorID, "storage_target.create", "storage_target", t.ID,
		map[string]string{"name": name, "remote": remoteName})
	return t, nil
}

// BindRepository creates (or returns existing) repository for the agent+target pair.
func (o *Orchestrator) BindRepository(ctx context.Context, actorID, agentID, storageTargetID string) (*model.Repository, error) {
	target, err := o.Store.GetStorageTarget(ctx, storageTargetID)
	if err != nil {
		return nil, err
	}

	existing, err := o.Store.GetRepositoryByAgentAndTarget(ctx, agentID, storageTargetID)
	if err == nil && existing != nil {
		// A previous bind attempt may have left the row non-ready (agent
		// offline, failed init). Retry adoption/init instead of returning
		// the stale row, otherwise it stays pending forever.
		if existing.Status != "ready" {
			if err := o.EnsureRepository(ctx, existing, agentID); err != nil {
				o.markRepositoryError(existing.ID)
				return nil, err
			}
			existing.Status = "ready"
		}
		o.Audit(ctx, "admin", actorID, "repository.bind", "repository", existing.ID,
			map[string]string{"agent_id": agentID, "target_id": storageTargetID})
		return existing, nil
	}

	// Generate 32-byte random password, hex-encode (64 chars)
	pwBytes := make([]byte, 32)
	if err := randRead(pwBytes); err != nil {
		return nil, err
	}
	repoPw := hexEncode(pwBytes)

	repoPath := buildRepoPath(target, o.InstanceID, agentID)

	repoID := model.NewUUIDv7()
	sealedPw, err := o.Seal.Seal("repositories", repoID, "encrypted_password", repoPw)
	if err != nil {
		return nil, err
	}

	repo := &model.Repository{
		ID:                repoID,
		AgentID:           agentID,
		StorageTargetID:   storageTargetID,
		RepositoryPath:    repoPath,
		EncryptedPassword: sealedPw,
		Status:            "pending",
		CreatedAt:         time.Now().UTC(),
		UpdatedAt:         time.Now().UTC(),
	}
	if err := o.Store.CreateRepository(ctx, repo); err != nil {
		return nil, err
	}
	if err := o.EnsureRepository(ctx, repo, agentID); err != nil {
		o.markRepositoryError(repo.ID)
		return nil, err
	}
	// EnsureRepository updated the DB row; mirror it into the struct so the
	// API response does not report the stale "pending" status.
	repo.Status = "ready"

	return repo, nil
}

// markRepositoryError records a failed bind independently of the HTTP request
// context. Binding waits for agent-side restic operations, so a reverse proxy
// or browser timeout must not leave the repository permanently pending.
func (o *Orchestrator) markRepositoryError(repoID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if repo, err := o.Store.GetRepository(ctx, repoID); err == nil && repo.Status == "ready" {
		return
	}
	_ = o.Store.UpdateRepositoryStatus(ctx, repoID, "error")
}

// EnsureRepository executes the init-or-adopt flow:
//
//  1. Probe via SystemRun(snapshots, SnapshotsTask{Repository}) —
//     success → adopt (status ready);
//     exit_code 10 (repo missing, error_code=="") → step 2;
//     exit_code 12 (wrong password, model.ErrWrongRepositoryPassword) → error;
//     exit_code 11 (repository locked, model.ErrRepositoryLocked) → error.
//
//  2. Init via SystemRun(forget, InitTask{Repository, ResticInit:true}).
//     success → status ready.
//
// Convention note: the agent's RunResult carries the raw exit code in its
// result_json (or error_code for mapped codes). The Orchestrator distinguishes
// exit 10 by matching error_code=="" on a failed run.
func (o *Orchestrator) EnsureRepository(ctx context.Context, repo *model.Repository, agentID string) error {
	repoAccess := model.RepoAccess{RepositoryPath: repo.RepositoryPath}

	// Step 1: probe
	probeRun, err := o.SystemRun(ctx, agentID, repo.ID, model.OpSnapshots,
		model.SnapshotsTask{Repository: repoAccess}, 0)
	if err != nil {
		return err
	}
	term, err := o.WaitRun(ctx, probeRun.ID, 60*time.Second)
	if err != nil {
		return err
	}
	if term.Status == model.RunSucceeded {
		// Existing repo — adopt.
		return o.Store.UpdateRepositoryStatus(ctx, repo.ID, "ready")
	}
	if term.Status == model.RunFailed {
		switch term.ErrorCode {
		case model.ErrRepositoryMissing:
			// restic exit 10 — repository does not exist; fall through to init.
		case model.ErrWrongRepositoryPassword:
			return ErrRepoPassword
		case model.ErrRepositoryLocked:
			return ErrRepoLocked
		default:
			return fmt.Errorf("ensure repository probe failed: %s %s", term.ErrorCode, term.ErrorMessage)
		}
	}

	// Step 2: init via FORGET + InitTask
	initRun, err := o.SystemRun(ctx, agentID, repo.ID, model.OpForget,
		model.InitTask{Repository: repoAccess, ResticInit: true}, 0)
	if err != nil {
		return err
	}
	term, err = o.WaitRun(ctx, initRun.ID, 90*time.Second)
	if err != nil {
		return err
	}
	if term.Status == model.RunSucceeded {
		return o.Store.UpdateRepositoryStatus(ctx, repo.ID, "ready")
	}
	return fmt.Errorf("ensure repository init failed: %s %s", term.ErrorCode, term.ErrorMessage)
}

// StartRetentionRun queues a non-pruning forget operation. Prune is reserved
// for a separately managed maintenance window; this operation only applies
// retention metadata and is serialized by the repository dispatcher queue.
func (o *Orchestrator) StartRetentionRun(ctx context.Context, repositoryID string) error {
	repo, err := o.Store.GetRepository(ctx, repositoryID)
	if err != nil {
		return err
	}
	plans, err := o.Store.ListPlans(ctx, repo.AgentID)
	if err != nil {
		return err
	}
	var retention model.Retention
	var planID, kind string
	for _, plan := range plans {
		if plan.RepositoryID == repositoryID && plan.Enabled {
			retention, planID, kind = plan.Retention, plan.ID, plan.Kind
			break
		}
	}
	if retention.KeepLast+retention.KeepDaily+retention.KeepWeekly+retention.KeepMonthly == 0 {
		return nil
	}
	_, err = o.SystemRun(ctx, repo.AgentID, repositoryID, model.OpForget, model.ForgetTask{
		PlanID: planID, Kind: kind, Repository: model.RepoAccess{RepositoryPath: repo.RepositoryPath}, Retention: retention,
	}, 0)
	return err
}

// StartRestore creates a restore request and a restore run. For filesystem
// restores target_path must be absolute and overwrite_mode ∈ {never,if-changed,always}.
// For database restores with overwrite=true, input.Confirmation must equal
// secrets.HashToken(input.Target.Database).
func (o *Orchestrator) StartRestore(ctx context.Context, actorID string, in RestoreInput) (*model.RestoreRequest, *model.Run, error) {
	repo, err := o.Store.GetRepository(ctx, in.RepositoryID)
	if err != nil {
		return nil, nil, err
	}

	var task model.RestoreTask
	task.Kind = in.RestoreKind
	task.Repository = model.RepoAccess{RepositoryPath: repo.RepositoryPath}

	switch in.RestoreKind {
	case model.KindFilesystem:
		if in.Target.TargetPath == "" || !filepath.IsAbs(in.Target.TargetPath) {
			return nil, nil, fmt.Errorf("%w: target_path must be absolute", ErrPathInvalid)
		}
		om := in.Target.OverwriteMode
		if om != "never" && om != "if-changed" && om != "always" {
			return nil, nil, fmt.Errorf("%w: invalid overwrite_mode", ErrPathInvalid)
		}
		task.Filesystem = &model.FilesystemRestore{
			SnapshotID:    in.SnapshotID,
			IncludePaths:  in.Target.IncludePaths,
			TargetPath:    in.Target.TargetPath,
			OverwriteMode: om,
			DryRun:        false,
		}
	default:
		// database
		// Check the destructive confirmation before validating connection
		// details.  A caller with a stale/incorrect confirmation must receive
		// the same forbidden response regardless of which target fields are
		// omitted or malformed.
		if in.Overwrite {
			expected := secrets.HashToken(in.Target.Database)
			if in.Confirmation != expected {
				return nil, nil, ErrForbidden
			}
		}
		if in.RestoreKind != model.KindSQLite {
			if in.Target.Host == "" || in.Target.Port <= 0 || in.Target.Username == "" {
				return nil, nil, fmt.Errorf("%w: database target host, port and username are required", ErrPathInvalid)
			}
		}
		if in.Target.Database == "" {
			return nil, nil, fmt.Errorf("%w: target database/path is required", ErrPathInvalid)
		}
		if in.RestoreKind == model.KindSQLite && !filepath.IsAbs(in.Target.Database) {
			return nil, nil, fmt.Errorf("%w: sqlite target path must be absolute", ErrPathInvalid)
		}
		task.Database = &model.DatabaseRestore{
			SnapshotID:      in.SnapshotID,
			Kind:            in.RestoreKind,
			TargetHost:      in.Target.Host,
			TargetPort:      in.Target.Port,
			TargetUsername:  in.Target.Username,
			TargetDatabase:  in.Target.Database,
			ReplaceExisting: in.Overwrite,
		}
	}

	// Create restore request row.
	confirmationHash := ""
	if in.Overwrite && in.RestoreKind != model.KindFilesystem {
		confirmationHash = secrets.HashToken(in.Target.Database)
	}

	targetJSON, _ := json.Marshal(in.Target)
	rr := &model.RestoreRequest{
		ID:               model.NewUUIDv7(),
		SnapshotID:       in.SnapshotID,
		RestoreKind:      in.RestoreKind,
		Target:           in.Target,
		TargetJSON:       string(targetJSON),
		Overwrite:        in.Overwrite,
		ConfirmationHash: confirmationHash,
		Phase:            "queued",
		CreatedAt:        time.Now().UTC(),
	}

	// Create the restore run first so we have a runID for the request.
	now := time.Now().UTC()
	run := &model.Run{
		ID:           model.NewUUIDv7(),
		AgentID:      repo.AgentID,
		Operation:    model.OpRestore,
		Status:       model.RunQueued,
		QueuedAt:     now,
		RepositoryID: repo.ID,
		ProgressJSON: "{}",
	}
	if err := o.Store.CreateRun(ctx, run); err != nil {
		return nil, nil, err
	}

	rr.RunID = run.ID
	if err := o.Store.CreateRestoreRequest(ctx, rr); err != nil {
		now := time.Now().UTC()
		_ = o.Store.TransitionRun(ctx, run.ID, model.RunQueued, model.RunFailed, func(r *model.Run) {
			r.FinishedAt = &now
			r.ErrorCode = model.ErrInvalidPlan
			r.ErrorMessage = "failed to persist restore request"
		})
		return nil, nil, err
	}
	if in.TargetPassword != "" {
		if rs, ok := o.Store.(interface {
			SaveRunTargetPassword(context.Context, string, string) error
		}); ok {
			if err := rs.SaveRunTargetPassword(ctx, run.ID, in.TargetPassword); err != nil {
				_ = o.Store.TransitionRun(ctx, run.ID, model.RunQueued, model.RunFailed, func(r *model.Run) {
					now := time.Now().UTC()
					r.FinishedAt = &now
					r.ErrorCode = "secret_storage_failed"
					r.ErrorMessage = "failed to persist restore credential"
				})
				return nil, nil, fmt.Errorf("save restore credential: %w", err)
			}
		}
	}

	o.Disp.Enqueue(ctx, run.ID, run.AgentID, repo.ID)

	o.Audit(ctx, "admin", actorID, "restore.start", "restore_request", rr.ID,
		map[string]string{
			"run_id":       run.ID,
			"snapshot_id":  in.SnapshotID,
			"restore_kind": in.RestoreKind,
			"overwrite":    fmt.Sprintf("%v", in.Overwrite),
			"confirmation": confirmationHash,
		})

	return rr, run, nil
}

// DryRunRestore runs a filesystem restore dry-run.
func (o *Orchestrator) DryRunRestore(ctx context.Context, repoID, snapshotID string, includePaths []string, targetPath, overwriteMode string) (*DryRunStats, *model.Run, error) {
	repo, err := o.Store.GetRepository(ctx, repoID)
	if err != nil {
		return nil, nil, err
	}

	params := model.RestoreTask{
		Kind:       model.KindFilesystem,
		Repository: model.RepoAccess{RepositoryPath: repo.RepositoryPath},
		Filesystem: &model.FilesystemRestore{
			SnapshotID:    snapshotID,
			IncludePaths:  includePaths,
			TargetPath:    targetPath,
			OverwriteMode: overwriteMode,
			DryRun:        true,
		},
	}
	run, err := o.SystemRun(ctx, repo.AgentID, repoID, model.OpRestoreDryRun, params, 0)
	if err != nil {
		return nil, nil, err
	}

	term, resultJSON, err := o.WaitRunResult(ctx, run.ID, 60*time.Second)
	if err != nil {
		return nil, term, err
	}
	if resultJSON == nil {
		return nil, term, nil
	}

	var stats DryRunStats
	if err := json.Unmarshal(resultJSON, &stats); err != nil {
		return nil, term, err
	}
	return &stats, term, nil
}

// ---------------------------------------------------------------------------
// CommandSource interface (consumed by dispatchgrpc)
// ---------------------------------------------------------------------------

// CommandSource builds the gRPC ExecuteCommand for a run from persisted state.
type CommandSource interface {
	BuildCommand(ctx context.Context, runID string) (commandID string, cmd *bmcv1.ExecuteCommand, err error)
}

var _ CommandSource = (*Orchestrator)(nil)

// BuildCommand assembles the gRPC ExecuteCommand for a persisted run. It
// resolves plan → repo → storage target, decrypts secrets, and builds
// params_json from the run's ProgressJSON stash (system runs) or from the
// plan (backup runs).
func (o *Orchestrator) BuildCommand(ctx context.Context, runID string) (string, *bmcv1.ExecuteCommand, error) {
	run, err := o.Store.GetRun(ctx, runID)
	if err != nil {
		return "", nil, err
	}

	op, err := opToProto(run.Operation)
	if err != nil {
		return "", nil, err
	}

	var paramsJSON []byte
	var secrets *bmcv1.SecretSet

	repoID := run.RepositoryID
	if repoID != "" {
		repo, err := o.Store.GetRepository(ctx, repoID)
		if err != nil {
			return "", nil, err
		}
		target, err := o.Store.GetStorageTarget(ctx, repo.StorageTargetID)
		if err != nil {
			return "", nil, err
		}

		rcloneConf, err := o.Seal.Open("storage_targets", target.ID, "encrypted_config", target.EncryptedConfig)
		if err != nil {
			return "", nil, err
		}
		repoPassword, err := o.Seal.Open("repositories", repo.ID, "encrypted_password", repo.EncryptedPassword)
		if err != nil {
			return "", nil, err
		}

		// Database credentials are resolved from encrypted per-plan/per-run
		// secret storage. The legacy JSON fallback exists only for old stores
		// during migration and never enters the API response.
		var dbPassword string
		if run.Operation == model.OpBackup && run.PlanID != "" {
			if ps, ok := o.Store.(interface {
				GetPlanDBPassword(context.Context, string) (string, error)
			}); ok {
				dbPassword, _ = ps.GetPlanDBPassword(ctx, run.PlanID)
			} else if plan, planErr := o.Store.GetPlan(ctx, run.PlanID); planErr == nil {
				dbPassword, _ = o.extractDBPasswordFromSource(plan.SourceJSON)
			}
		}
		if run.Operation == model.OpRestore {
			if rs, ok := o.Store.(interface {
				GetRunTargetPassword(context.Context, string) (string, error)
			}); ok {
				dbPassword, _ = rs.GetRunTargetPassword(ctx, run.ID)
			}
		}

		secrets = &bmcv1.SecretSet{
			RcloneConf:     rcloneConf,
			ResticPassword: repoPassword,
			DbPassword:     dbPassword,
		}
	}

	// System runs without a repo (e.g. verify_storage_remote) may still
	// need rclone config from the per-run stash.
	if repoID == "" {
		stashedConf := ""
		if rs, ok := o.Store.(interface {
			GetRunRcloneConfig(context.Context, string) (string, error)
		}); ok {
			stashedConf, _ = rs.GetRunRcloneConfig(ctx, run.ID)
		}
		if stashedConf == "" {
			stashedConf = o.confFor(run.ID)
		}
		if stashedConf != "" {
			secrets = &bmcv1.SecretSet{RcloneConf: stashedConf}
		}
	}

	switch run.Operation {
	case model.OpBackup:
		paramsJSON, err = o.buildBackupParams(ctx, run)
	case model.OpRestore:
		paramsJSON, err = o.buildRestoreParams(ctx, run)
	default:
		// System runs: params live in run.ProgressJSON.
		paramsJSON = []byte(run.ProgressJSON)
		if len(paramsJSON) == 0 {
			paramsJSON = []byte("{}")
		}
	}
	if err != nil {
		return "", nil, err
	}

	cmd := &bmcv1.ExecuteCommand{
		CommandId:  model.NewUUIDv7(),
		RunId:      run.ID,
		Operation:  op,
		ParamsJson: paramsJSON,
		Secrets:    secrets,
	}
	return cmd.CommandId, cmd, nil
}

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

// Audit records an audit event.
func (o *Orchestrator) Audit(ctx context.Context, actorType, actorID, action, resourceType, resourceID string, detail any) {
	detailJSON := "{}"
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil {
			detailJSON = string(b)
		}
	}
	if err := o.Store.AppendAuditEvent(ctx, &model.AuditEvent{
		ID:           model.NewUUIDv7(),
		OccurredAt:   time.Now().UTC(),
		ActorType:    actorType,
		ActorID:      actorID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		DetailJSON:   detailJSON,
	}); err != nil {
		// audit is best-effort
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func opToProto(op string) (bmcv1.ExecuteCommand_Operation, error) {
	switch op {
	case model.OpBackup:
		return bmcv1.ExecuteCommand_BACKUP, nil
	case model.OpRestore:
		return bmcv1.ExecuteCommand_RESTORE, nil
	case model.OpRestoreDryRun:
		return bmcv1.ExecuteCommand_RESTORE_DRY_RUN, nil
	case model.OpCheck:
		return bmcv1.ExecuteCommand_CHECK, nil
	case model.OpForget:
		return bmcv1.ExecuteCommand_FORGET, nil
	case model.OpSnapshots:
		return bmcv1.ExecuteCommand_SNAPSHOTS, nil
	case model.OpSnapshotLs:
		return bmcv1.ExecuteCommand_SNAPSHOT_LS, nil
	case model.OpVerifyRemote:
		return bmcv1.ExecuteCommand_VERIFY_STORAGE_REMOTE, nil
	case model.OpValidatePaths:
		return bmcv1.ExecuteCommand_VALIDATE_PATHS, nil
	case model.OpProbeCaps:
		return bmcv1.ExecuteCommand_PROBE_CAPABILITIES, nil
	default:
		return 0, fmt.Errorf("unknown operation: %s", op)
	}
}

func (o *Orchestrator) buildBackupParams(ctx context.Context, run *model.Run) ([]byte, error) {
	if run.PlanID == "" {
		return []byte("{}"), nil
	}
	plan, err := o.Store.GetPlan(ctx, run.PlanID)
	if err != nil {
		return nil, err
	}

	repo, err := o.Store.GetRepository(ctx, plan.RepositoryID)
	if err != nil {
		return nil, err
	}

	// Retention tags (already present on plan, we forward as-is)
	tags := []string{"plan:" + plan.ID, "kind:" + plan.Kind, "run:" + run.ID}
	task := model.BackupTask{
		PlanID:         plan.ID,
		Kind:           plan.Kind,
		Repository:     model.RepoAccess{RepositoryPath: repo.RepositoryPath},
		Source:         plan.Source,
		Retention:      plan.Retention,
		Tags:           tags,
		TimeoutSeconds: plan.TimeoutSeconds,
	}
	return json.Marshal(task)
}

func (o *Orchestrator) buildRestoreParams(ctx context.Context, run *model.Run) ([]byte, error) {
	// Look up the restore request by run ID.
	// Since Store has no GetRestoreRequestByRunID, we scan the last few.
	rrs, err := o.Store.ListRestoreRequests(ctx, 100)
	if err != nil {
		return nil, err
	}
	var rr *model.RestoreRequest
	for i := range rrs {
		if rrs[i].RunID == run.ID {
			rr = &rrs[i]
			break
		}
	}
	if rr == nil {
		return nil, fmt.Errorf("restore request not found for run %s", run.ID)
	}

	repo, err := o.Store.GetRepository(ctx, run.RepositoryID)
	if err != nil {
		return nil, err
	}

	task := model.RestoreTask{
		Kind:       rr.RestoreKind,
		Repository: model.RepoAccess{RepositoryPath: repo.RepositoryPath},
	}
	switch rr.RestoreKind {
	case model.KindFilesystem:
		if rr.Target.TargetPath != "" {
			task.Filesystem = &model.FilesystemRestore{
				SnapshotID:    rr.SnapshotID,
				IncludePaths:  rr.Target.IncludePaths,
				TargetPath:    rr.Target.TargetPath,
				OverwriteMode: rr.Target.OverwriteMode,
			}
		}
	default:
		task.Database = &model.DatabaseRestore{
			SnapshotID:       rr.SnapshotID,
			Kind:             rr.RestoreKind,
			TargetHost:       rr.Target.Host,
			TargetPort:       rr.Target.Port,
			TargetUsername:   rr.Target.Username,
			TargetDatabase:   rr.Target.Database,
			TargetAuthSource: rr.Target.AuthSource,
			ReplaceExisting:  rr.Overwrite,
		}
	}
	return json.Marshal(task)
}

// planSourceWithSecret is a legacy compatibility shape used only while
// migrating old plans that embedded a password in source_json.
type planSourceWithSecret struct {
	model.PlanSource
	Password string `json:"password,omitempty"`
}

func (o *Orchestrator) extractDBPasswordFromSource(sourceJSON string) (string, error) {
	var ps planSourceWithSecret
	if err := json.Unmarshal([]byte(sourceJSON), &ps); err != nil {
		return "", err
	}
	if ps.Password == "" {
		return "", nil
	}
	// New plans keep this value in the separate encrypted secret table. This
	// fallback exists only for one-time migration of old rows.
	return ps.Password, nil
}

// checkCapabilities verifies that the agent has all required tools for the
// given plan kind.
func (o *Orchestrator) checkCapabilities(agent *model.Agent, kind string) error {
	required := requiredTools(kind)
	have := make(map[string]bool, len(agent.Capabilities))
	for _, t := range agent.Capabilities {
		have[t.Name] = t.Path != ""
	}
	missing := make([]string, 0, len(required))
	for _, name := range required {
		if !have[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return &MissingToolsError{Tools: missing}
	}
	return nil
}

func requiredTools(kind string) []string {
	switch kind {
	case model.KindFilesystem:
		return []string{"restic"}
	case model.KindPostgreSQL:
		return []string{"restic", "pg_dump", "psql"}
	case model.KindMySQL:
		return []string{"restic", "mysqldump", "mysql"}
	case model.KindMongoDB:
		return []string{"restic", "mongodump", "mongorestore"}
	case model.KindSQLite:
		return []string{"restic", "sqlite3"}
	default:
		return nil
	}
}

// buildRepoPath produces <remote_name>:<remote_path>/<instanceID>/<agentID>
// with remote_path slashes normalised.
func buildRepoPath(target *model.StorageTarget, instanceID, agentID string) string {
	rp := strings.Trim(target.RemotePath, "/")
	return fmt.Sprintf("%s:%s/%s/%s", target.RemoteName, rp, instanceID, agentID)
}

// Stash helpers for per-run rclone config (used by ValidateStorageRemote).
type stashEntry struct {
	conf string
}

func (o *Orchestrator) stashConf(runID, conf string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.confStash == nil {
		o.confStash = make(map[string]stashEntry)
	}
	o.confStash[runID] = stashEntry{conf: conf}
}

func (o *Orchestrator) confFor(runID string) string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.confStash == nil {
		return ""
	}
	if e, ok := o.confStash[runID]; ok {
		return e.conf
	}
	return ""
}

// randRead fills b with crypto/rand bytes.
func randRead(b []byte) error {
	_, err := rand.Read(b)
	return err
}

// hexEncode returns the lowercase hex encoding of b.
func hexEncode(b []byte) string {
	return hex.EncodeToString(b)
}
