package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	bmcv1 "backupmanagementcenter/api/proto/v1"
	"backupmanagementcenter/internal/dispatch"
	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/secrets"
	"backupmanagementcenter/internal/server/events"
	"backupmanagementcenter/internal/server/store"
)

// ---------------------------------------------------------------------------
// fakeStore — implements just the subset of store.Store the orchestrator uses.
// ---------------------------------------------------------------------------

type fakeStore struct {
	mu              sync.Mutex
	plans           map[string]*model.Plan
	agents          map[string]*model.Agent
	repos           map[string]*model.Repository
	targets         map[string]*model.StorageTarget
	targetsByName   map[string]*model.StorageTarget
	runs            map[string]*model.Run
	restoreRequests map[string]*model.RestoreRequest
	auditEvents     []model.AuditEvent

	// snapshot cleanup scan state（孤儿扫描退避）
	cleanupStates map[string]*model.SnapshotCleanupState

	// snapshot cache + hidden state used by restore authorization
	snapshotCache map[string]*store.SnapshotListCache
	treeCache     map[string]*store.SnapshotTreeCache
	hidden        map[string]map[string]struct{}
	runSecrets    map[string]string

	// simulate duplicate (plan_id, scheduled_at) slot
	seenSlots map[string]bool // "planID:scheduledAt"
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		plans:           make(map[string]*model.Plan),
		agents:          make(map[string]*model.Agent),
		repos:           make(map[string]*model.Repository),
		targets:         make(map[string]*model.StorageTarget),
		targetsByName:   make(map[string]*model.StorageTarget),
		runs:            make(map[string]*model.Run),
		restoreRequests: make(map[string]*model.RestoreRequest),
		cleanupStates:   make(map[string]*model.SnapshotCleanupState),
		seenSlots:       make(map[string]bool),
		snapshotCache:   make(map[string]*store.SnapshotListCache),
		treeCache:       make(map[string]*store.SnapshotTreeCache),
		hidden:          make(map[string]map[string]struct{}),
		runSecrets:      make(map[string]string),
	}
}

func (s *fakeStore) getSlotKey(planID string, scheduledAt *time.Time) string {
	if scheduledAt == nil {
		return ""
	}
	return planID + ":" + scheduledAt.Format(time.RFC3339)
}

func (s *fakeStore) CreateRun(ctx context.Context, r *model.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := s.getSlotKey(r.PlanID, r.ScheduledAt)
	if key != "" && s.seenSlots[key] {
		return store.ErrDuplicateRun
	}
	if key != "" {
		s.seenSlots[key] = true
	}
	// 队列去重：相同 DedupKey 的未终结 run 只允许一个（真实 store 由部分唯一索引保证）。
	if r.DedupKey != "" {
		for _, existing := range s.runs {
			if existing.DedupKey == r.DedupKey && !isTerminal(existing.Status) {
				return store.ErrDuplicateRun
			}
		}
	}
	s.runs[r.ID] = r
	return nil
}

func (s *fakeStore) FindActiveRunByDedupKey(ctx context.Context, dedupKey string) (*model.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if dedupKey == "" {
		return nil, store.ErrNotFound
	}
	for _, r := range s.runs {
		if r.DedupKey == dedupKey && !isTerminal(r.Status) {
			return r, nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *fakeStore) GetRun(ctx context.Context, id string) (*model.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return r, nil
}

func (s *fakeStore) TransitionRun(ctx context.Context, id, from, to string, mutate func(*model.Run)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return store.ErrNotFound
	}
	if r.Status != from {
		return store.ErrInvalidTransition
	}
	r.Status = to
	if mutate != nil {
		mutate(r)
	}
	return nil
}

func (s *fakeStore) GetPlan(ctx context.Context, id string) (*model.Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.plans[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return p, nil
}

func (s *fakeStore) GetAgent(ctx context.Context, id string) (*model.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return a, nil
}

func (s *fakeStore) GetRepository(ctx context.Context, id string) (*model.Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.repos[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return r, nil
}

func (s *fakeStore) GetRepositoryByAgentAndTarget(ctx context.Context, agentID, targetID string) (*model.Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.repos {
		if r.AgentID == agentID && r.StorageTargetID == targetID {
			return r, nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *fakeStore) GetStorageTarget(ctx context.Context, id string) (*model.StorageTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.targets[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return t, nil
}

func (s *fakeStore) CreateStorageTarget(ctx context.Context, t *model.StorageTarget) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.targets[t.ID] = t
	s.targetsByName[t.Name] = t
	return nil
}

func (s *fakeStore) CreateRepository(ctx context.Context, r *model.Repository) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repos[r.ID] = r
	return nil
}

func (s *fakeStore) UpdateRepositoryStatus(ctx context.Context, id, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.repos[id]
	if !ok {
		return store.ErrNotFound
	}
	r.Status = status
	if status == "ready" {
		r.DetachedAt = nil
	}
	return nil
}

func (s *fakeStore) CreateRestoreRequest(ctx context.Context, rr *model.RestoreRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.restoreRequests[rr.ID] = rr
	return nil
}

func (s *fakeStore) ListRestoreRequests(ctx context.Context, limit int) ([]model.RestoreRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.RestoreRequest, 0, len(s.restoreRequests))
	for _, rr := range s.restoreRequests {
		out = append(out, *rr)
	}
	return out, nil
}

func (s *fakeStore) GetRestoreRequestByRunID(ctx context.Context, runID string) (*model.RestoreRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rr := range s.restoreRequests {
		if rr.RunID == runID {
			return rr, nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *fakeStore) UpdateRestorePhase(ctx context.Context, runID, phase string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rr := range s.restoreRequests {
		if rr.RunID == runID && !model.RestorePhaseIsTerminal(rr.Phase) {
			rr.Phase = phase
		}
	}
	return nil
}

func (s *fakeStore) UpdateRestoreRollbackSnapshot(ctx context.Context, runID, snapshotID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rr := range s.restoreRequests {
		if rr.RunID == runID && rr.RollbackSnapshotID == "" {
			rr.RollbackSnapshotID = snapshotID
		}
	}
	return nil
}

func (s *fakeStore) CreateDatabaseRestoreRun(ctx context.Context, run *model.Run, rr *model.RestoreRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kinds := map[string]bool{
		model.KindPostgreSQL: true, model.KindMySQL: true, model.KindMongoDB: true, model.KindSQLite: true,
	}
	for _, other := range s.restoreRequests {
		if !kinds[other.RestoreKind] || model.RestorePhaseReleasesOccupancy(other.Phase) {
			continue
		}
		if existing, ok := s.runs[other.RunID]; ok && existing.DedupKey == run.DedupKey {
			return store.ErrDuplicateRun
		}
		return store.ErrDatabaseRestoreBusy
	}
	for _, existing := range s.runs {
		if existing.DedupKey != "" && existing.DedupKey == run.DedupKey {
			if existing.Status == model.RunQueued || existing.Status == model.RunDispatched || existing.Status == model.RunRunning {
				return store.ErrDuplicateRun
			}
		}
	}
	s.runs[run.ID] = run
	rr.RunID = run.ID
	s.restoreRequests[rr.ID] = rr
	return nil
}

func (s *fakeStore) ActiveDatabaseRestoreRunID(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kinds := map[string]bool{
		model.KindPostgreSQL: true, model.KindMySQL: true, model.KindMongoDB: true, model.KindSQLite: true,
	}
	for _, rr := range s.restoreRequests {
		if kinds[rr.RestoreKind] && !model.RestorePhaseReleasesOccupancy(rr.Phase) {
			return rr.RunID, nil
		}
	}
	return "", nil
}

func (s *fakeStore) RepositoryRestoreBlocked(ctx context.Context, repositoryID, exceptRunID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rr := range s.restoreRequests {
		if rr.RunID == exceptRunID || model.RestorePhaseReleasesOccupancy(rr.Phase) {
			continue
		}
		if run, ok := s.runs[rr.RunID]; ok && run.RepositoryID == repositoryID {
			return true, nil
		}
	}
	return false, nil
}

func (s *fakeStore) ProtectedRestoreSnapshotIDs(ctx context.Context, repositoryID string) (map[string]struct{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]struct{}{}
	for _, rr := range s.restoreRequests {
		if model.RestorePhaseReleasesOccupancy(rr.Phase) || rr.RollbackSnapshotID == "" {
			continue
		}
		if run, ok := s.runs[rr.RunID]; ok && run.RepositoryID == repositoryID {
			out[rr.RollbackSnapshotID] = struct{}{}
		}
	}
	return out, nil
}

func (s *fakeStore) RequestRestoreStop(ctx context.Context, runID string, deadline time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rr := range s.restoreRequests {
		if rr.RunID == runID && !model.RestorePhaseIsTerminal(rr.Phase) {
			rr.Phase = model.RestorePhaseCancelling
		}
	}
	if run, ok := s.runs[runID]; ok {
		d := deadline
		run.LeaseExpiresAt = &d
	}
	return nil
}

func (s *fakeStore) FinishRestoreRun(ctx context.Context, in store.FinishRestoreRunInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[in.RunID]
	if !ok {
		return store.ErrNotFound
	}
	terminal := run.Status == model.RunSucceeded || run.Status == model.RunFailed || run.Status == model.RunCancelled
	if !terminal {
		run.Status = in.ToStatus
		finished := in.FinishedAt
		run.FinishedAt = &finished
		run.ErrorCode = in.ErrorCode
		run.ErrorMessage = in.ErrorMessage
		if in.ResultJSON != "" {
			run.ProgressJSON = in.ResultJSON
		}
	}
	for _, rr := range s.restoreRequests {
		if rr.RunID != in.RunID {
			continue
		}
		if rr.RollbackSnapshotID == "" {
			rr.RollbackSnapshotID = in.RollbackSnapshotID
		}
		switch {
		case in.Phase == "":
		case rr.Phase == model.RestorePhaseManualRecoveryDone:
		case model.RestorePhaseReleasesOccupancy(rr.Phase) && rr.Phase != in.Phase:
		default:
			rr.Phase = in.Phase
		}
	}
	return nil
}

func (s *fakeStore) ResolveRestoreRequest(ctx context.Context, requestID, runID, actorID, note string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rr, ok := s.restoreRequests[requestID]
	if !ok {
		return store.ErrNotFound
	}
	if runID != "" && rr.RunID != runID {
		return store.ErrRestoreConflict
	}
	if rr.Phase != model.RestorePhaseManualRecoveryNeeded && rr.Phase != model.RestorePhaseRollbackFailed {
		return store.ErrRestoreConflict
	}
	rr.Phase = model.RestorePhaseManualRecoveryDone
	if run, ok := s.runs[rr.RunID]; ok && !model.RestorePhaseIsTerminal(run.Status) {
		run.Status = model.RunFailed
		finished := at
		run.FinishedAt = &finished
		run.ErrorCode = model.ErrRollbackFailed
	}
	return nil
}

func (s *fakeStore) GetSnapshotListCache(ctx context.Context, repositoryID string) (*store.SnapshotListCache, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.snapshotCache[repositoryID]; ok {
		return c, nil
	}
	return nil, store.ErrNotFound
}

func (s *fakeStore) GetSnapshotTreeCache(ctx context.Context, repositoryID, snapshotID, cachePath string) (*store.SnapshotTreeCache, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := repositoryID + "/" + snapshotID + "/" + store.NormalizeSnapshotPath(cachePath)
	if c, ok := s.treeCache[key]; ok {
		return c, nil
	}
	return nil, store.ErrNotFound
}

func (s *fakeStore) HiddenSnapshotIDs(ctx context.Context, repositoryID string) (map[string]struct{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]struct{}, len(s.hidden[repositoryID]))
	for id := range s.hidden[repositoryID] {
		out[id] = struct{}{}
	}
	return out, nil
}

func (s *fakeStore) SaveRunTargetPassword(ctx context.Context, runID, password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runSecrets[runID] = password
	return nil
}

func (s *fakeStore) GetRunTargetPassword(ctx context.Context, runID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pw, ok := s.runSecrets[runID]
	if !ok {
		return "", store.ErrNotFound
	}
	return pw, nil
}

func (s *fakeStore) DeleteRunSecrets(ctx context.Context, runID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.runSecrets, runID)
	return nil
}

// seedVerifiedSnapshotCache 写入一份“已验证”的快照列表缓存，供恢复授权测试使用。
func (s *fakeStore) seedVerifiedSnapshotCache(repositoryID string, snaps []model.Snapshot) {
	raw, _ := json.Marshal(snaps)
	s.snapshotCache[repositoryID] = &store.SnapshotListCache{
		RepositoryID:  repositoryID,
		Generation:    1,
		SnapshotsJSON: string(raw),
		Fingerprint:   store.SnapshotFingerprint(snaps),
		VerifiedAt:    time.Now().UTC(),
	}
}

// seedVerifiedTreeCache 写入一份已验证的目录缓存。
func (s *fakeStore) seedVerifiedTreeCache(repositoryID, snapshotID, path string, entries []TreeEntry) {
	raw, _ := json.Marshal(TreeResult{Entries: entries, Path: path})
	s.treeCache[repositoryID+"/"+snapshotID+"/"+store.NormalizeSnapshotPath(path)] = &store.SnapshotTreeCache{
		RepositoryID: repositoryID,
		SnapshotID:   snapshotID,
		Path:         store.NormalizeSnapshotPath(path),
		Generation:   1,
		TreeJSON:     string(raw),
		VerifiedAt:   time.Now().UTC(),
	}
}

func (s *fakeStore) ListDueSnapshotDeletions(ctx context.Context, now time.Time, limit int) ([]model.SnapshotDeletion, error) {
	return nil, nil
}
func (s *fakeStore) ListRunningSnapshotDeletions(ctx context.Context) ([]model.SnapshotDeletion, error) {
	return nil, nil
}
func (s *fakeStore) ClaimSnapshotDeletionRun(ctx context.Context, deletionID string, run *model.Run, leaseUntil time.Time) error {
	return nil
}
func (s *fakeStore) CompleteSnapshotDeletion(ctx context.Context, deletionID string, now time.Time) error {
	return nil
}
func (s *fakeStore) RetrySnapshotDeletion(ctx context.Context, deletionID, errorCode, errorMessage string, nextAttemptAt time.Time) error {
	return nil
}
func (s *fakeStore) GetSnapshotCleanupState(ctx context.Context, repositoryID string) (*model.SnapshotCleanupState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.cleanupStates[repositoryID]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *st
	return &cp, nil
}
func (s *fakeStore) StartSnapshotCleanupScan(ctx context.Context, repositoryID, runID string, startedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.cleanupStates[repositoryID]
	if st == nil {
		st = &model.SnapshotCleanupState{RepositoryID: repositoryID}
		s.cleanupStates[repositoryID] = st
	}
	st.ScanRunID = runID
	t := startedAt
	st.LastScanStartedAt = &t
	st.NextAttemptAt = nil
	return nil
}
func (s *fakeStore) FinishSnapshotCleanupScan(ctx context.Context, repositoryID, runID string, snapshots []model.Snapshot, completedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.cleanupStates[repositoryID]
	if st == nil {
		st = &model.SnapshotCleanupState{RepositoryID: repositoryID}
		s.cleanupStates[repositoryID] = st
	}
	st.ScanRunID = ""
	t := completedAt
	st.LastScanCompletedAt = &t
	st.NextAttemptAt = nil
	return nil
}
func (s *fakeStore) ClearSnapshotCleanupScan(ctx context.Context, repositoryID, runID string, nextAttemptAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.cleanupStates[repositoryID]
	if st == nil {
		return store.ErrNotFound
	}
	st.ScanRunID = ""
	t := nextAttemptAt
	st.NextAttemptAt = &t
	return nil
}

func (s *fakeStore) QueueManualSnapshotDeletion(ctx context.Context, repositoryID, agentID, snapshotID, actorID string, now time.Time) (*model.SnapshotDeletion, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hidden[repositoryID] == nil {
		s.hidden[repositoryID] = map[string]struct{}{}
	}
	s.hidden[repositoryID][snapshotID] = struct{}{}
	return &model.SnapshotDeletion{
		ID: "del-" + snapshotID, RepositoryID: repositoryID, AgentID: agentID, SnapshotID: snapshotID,
		Source: model.SnapshotDeletionManual, State: model.SnapshotDeletionPending, CreatedAt: now, UpdatedAt: now,
	}, true, nil
}

func (s *fakeStore) AppendAuditEvent(ctx context.Context, e *model.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auditEvents = append(s.auditEvents, *e)
	return nil
}

// No-op stubs to satisfy Store interface.
func (s *fakeStore) Close() error                                          { return nil }
func (s *fakeStore) Migrate(ctx context.Context) error                     { return nil }
func (s *fakeStore) HasAdmin(ctx context.Context) (bool, error)            { return false, nil }
func (s *fakeStore) CreateAdmin(ctx context.Context, a *model.Admin) error { return nil }
func (s *fakeStore) GetAdminByUsername(ctx context.Context, u string) (*model.Admin, error) {
	return nil, store.ErrNotFound
}
func (s *fakeStore) GetAdminByID(ctx context.Context, id string) (*model.Admin, error) {
	return nil, store.ErrNotFound
}
func (s *fakeStore) UpdateAdminLastLogin(ctx context.Context, id string, at time.Time) error {
	return nil
}
func (s *fakeStore) ResetAdmin(ctx context.Context) error                       { return nil }
func (s *fakeStore) CreateSession(ctx context.Context, s1 *model.Session) error { return nil }
func (s *fakeStore) GetSession(ctx context.Context, idHash string) (*model.Session, error) {
	return nil, store.ErrNotFound
}
func (s *fakeStore) TouchSession(ctx context.Context, idHash string, lastSeen time.Time) error {
	return nil
}
func (s *fakeStore) DeleteSession(ctx context.Context, idHash string) error         { return nil }
func (s *fakeStore) DeleteExpiredSessions(ctx context.Context, now time.Time) error { return nil }
func (s *fakeStore) CreateEnrollmentToken(ctx context.Context, t *model.EnrollmentToken) error {
	return nil
}
func (s *fakeStore) ListEnrollmentTokens(ctx context.Context) ([]model.EnrollmentToken, error) {
	return nil, nil
}
func (s *fakeStore) ConsumeEnrollmentToken(ctx context.Context, h string, now time.Time) (*model.EnrollmentToken, error) {
	return nil, store.ErrTokenInvalid
}
func (s *fakeStore) ReEnrollAgent(context.Context, string, string, time.Time) error { return nil }
func (s *fakeStore) UpsertAgentOnConnect(ctx context.Context, a *model.Agent) error { return nil }
func (s *fakeStore) SetAgentStatus(ctx context.Context, agentID string, st model.AgentStatus, at time.Time) error {
	return nil
}
func (s *fakeStore) SaveAgentCapabilities(ctx context.Context, agentID string, tools []model.ToolInfo, sourceMappings []model.PathMapping, restoreMappings []model.PathMapping, safeDatabaseRestore bool, at time.Time) error {
	return nil
}
func (s *fakeStore) GetAgentBySecretHash(ctx context.Context, h string) (*model.Agent, error) {
	return nil, store.ErrNotFound
}
func (s *fakeStore) ListAgents(ctx context.Context) ([]model.Agent, error)  { return nil, nil }
func (s *fakeStore) RevokeAgent(ctx context.Context, id string) error       { return nil }
func (s *fakeStore) UnrevokeAgent(ctx context.Context, id string) error     { return nil }
func (s *fakeStore) RenameAgent(ctx context.Context, id, name string) error { return nil }
func (s *fakeStore) GetTelegramSettings(ctx context.Context) (*model.TelegramSettings, error) {
	return nil, store.ErrNotFound
}
func (s *fakeStore) SaveTelegramSettings(ctx context.Context, ts *model.TelegramSettings) error {
	return nil
}
func (s *fakeStore) DeleteTelegramSettings(ctx context.Context) error { return nil }
func (s *fakeStore) UpdateStorageTarget(ctx context.Context, t *model.StorageTarget) error {
	return nil
}
func (s *fakeStore) DeleteStorageTarget(ctx context.Context, id string) error { return nil }
func (s *fakeStore) ListStorageTargets(ctx context.Context) ([]model.StorageTarget, error) {
	return nil, nil
}
func (s *fakeStore) ListRepositories(ctx context.Context) ([]model.Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.Repository, 0, len(s.repos))
	for _, r := range s.repos {
		out = append(out, *r)
	}
	return out, nil
}
func (s *fakeStore) ListRepositoriesNeedingCheck(ctx context.Context, olderThan time.Time) ([]model.Repository, error) {
	return nil, nil
}
func (s *fakeStore) DetachRepository(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.repos[id]
	if !ok {
		return store.ErrNotFound
	}
	now := time.Now().UTC()
	r.DetachedAt = &now
	return nil
}
func (s *fakeStore) MarkRepositoryChecked(ctx context.Context, id string, at time.Time) error {
	return nil
}
func (s *fakeStore) CreatePlan(ctx context.Context, p *model.Plan) error { return nil }
func (s *fakeStore) UpdatePlan(ctx context.Context, p *model.Plan) error { return nil }
func (s *fakeStore) DeletePlan(ctx context.Context, id string) error     { return nil }
func (s *fakeStore) ListPlans(ctx context.Context, agentID string) ([]model.Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.Plan, 0, len(s.plans))
	for _, p := range s.plans {
		if agentID == "" || p.AgentID == agentID {
			out = append(out, *p)
		}
	}
	return out, nil
}
func (s *fakeStore) ListEnabledPlans(ctx context.Context) ([]model.Plan, error) { return nil, nil }
func (s *fakeStore) ListRuns(ctx context.Context, f store.RunFilter) ([]model.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	statusSet := make(map[string]bool, len(f.Statuses))
	for _, status := range f.Statuses {
		statusSet[status] = true
	}
	out := make([]model.Run, 0)
	for _, run := range s.runs {
		if f.RepositoryID != "" && run.RepositoryID != f.RepositoryID {
			continue
		}
		if len(statusSet) > 0 && !statusSet[run.Status] {
			continue
		}
		out = append(out, *run)
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}
	return out, nil
}
func (s *fakeStore) ListRunsByStatus(ctx context.Context, statuses []string) ([]model.Run, error) {
	return nil, nil
}
func (s *fakeStore) FailStaleRuns(ctx context.Context, statuses []string, code string, at time.Time) ([]string, error) {
	return nil, nil
}
func (s *fakeStore) AppendRunLogs(ctx context.Context, logs []model.RunLog) error { return nil }
func (s *fakeStore) ListRunLogs(ctx context.Context, runID string, beforeID int64, limit int) ([]model.RunLog, error) {
	return nil, nil
}
func (s *fakeStore) GetRestoreRequest(ctx context.Context, id string) (*model.RestoreRequest, error) {
	return nil, store.ErrNotFound
}
func (s *fakeStore) ListAuditEvents(ctx context.Context, limit int) ([]model.AuditEvent, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// fakeDispatcher
// ---------------------------------------------------------------------------

type fakeDispatcher struct {
	mu        sync.Mutex
	enqueued  []string
	cancelled []string
	connected map[string]bool
}

func newFakeDispatcher() *fakeDispatcher {
	return &fakeDispatcher{connected: make(map[string]bool)}
}

func (d *fakeDispatcher) Enqueue(ctx context.Context, runID, agentID, repositoryID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.enqueued = append(d.enqueued, runID)
}

func (d *fakeDispatcher) Cancel(ctx context.Context, runID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cancelled = append(d.cancelled, runID)
	return nil
}

func (d *fakeDispatcher) ConnectedAgents() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for id := range d.connected {
		out = append(out, id)
	}
	return out
}

func (d *fakeDispatcher) IsConnected(agentID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.connected[agentID]
}

func (d *fakeDispatcher) Enqueued() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.enqueued...)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func fakeKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i)
	}
	return k
}

// staticCaps 是固定的连接能力来源，供恢复前置校验测试使用。
type staticCaps struct {
	ready     bool
	safe      bool
	connected bool
}

func (c staticCaps) ConnectionCapabilities(string) (bool, bool, bool) {
	return c.ready, c.safe, c.connected
}

// allowRestore 让测试 store/dispatcher/orchestrator 满足恢复前置条件。
func allowRestore(st *fakeStore, disp *fakeDispatcher, o *Orchestrator, repo *model.Repository, snaps ...model.Snapshot) {
	disp.mu.Lock()
	disp.connected[repo.AgentID] = true
	disp.mu.Unlock()
	o.AgentCaps = staticCaps{ready: true, safe: true, connected: true}
	st.seedVerifiedSnapshotCache(repo.ID, snaps)
}

// restoreTestPath 返回当前平台的绝对路径（Windows 开发机上 /tmp 不是绝对路径）。
func restoreTestPath() string {
	if filepath.IsAbs("/tmp/bmc-restore-out") {
		return "/tmp/bmc-restore-out"
	}
	return filepath.Join(os.TempDir(), "bmc-restore-out")
}

func restoreSnapshotList(id, kind string) []model.Snapshot {
	return []model.Snapshot{restoreSnapshot(id, kind)}
}

func restoreSnapshot(id, kind string) model.Snapshot {
	return model.Snapshot{ID: id, Time: time.Now().UTC().Format(time.RFC3339), Tags: []string{"kind:" + kind, "plan:plan-1", "run:run-1"}}
}

func testAgent() *model.Agent {
	return &model.Agent{
		ID:         "agent-1",
		Name:       "srv-1",
		Hostname:   "srv-1",
		OS:         "linux",
		Arch:       "amd64",
		Version:    "0.1.0",
		Status:     model.AgentOnline,
		EnrolledAt: time.Now().UTC(),
		Capabilities: []model.ToolInfo{
			{Name: "restic", Path: "/usr/bin/restic"},
			{Name: "pg_dump", Path: "/usr/bin/pg_dump"},
			{Name: "pg_restore", Path: "/usr/bin/pg_restore"},
			{Name: "psql", Path: "/usr/bin/psql"},
			{Name: "mysqldump", Path: "/usr/bin/mysqldump"},
			{Name: "mysql", Path: "/usr/bin/mysql"},
			{Name: "mongodump", Path: "/usr/bin/mongodump"},
			{Name: "mongorestore", Path: "/usr/bin/mongorestore"},
			{Name: "sqlite3", Path: "/usr/bin/sqlite3"},
			{Name: "rclone", Path: "/usr/bin/rclone"},
		},
		CapabilitiesJSON: "[]",
		Revoked:          false,
	}
}

func testTarget(seal secrets.Sealer) *model.StorageTarget {
	sealed, _ := seal.Seal("storage_targets", "target-1", "encrypted_config", "dummy-conf")
	now := time.Now().UTC()
	return &model.StorageTarget{
		ID:              "target-1",
		Name:            "target-1",
		Type:            "rclone",
		RemoteName:      "gdrive",
		RemotePath:      "/bmc",
		EncryptedConfig: sealed,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func testRepo(seal secrets.Sealer, store *fakeStore, target *model.StorageTarget) *model.Repository {
	sealed, _ := seal.Seal("repositories", "repo-1", "encrypted_password", "repo-secret-123")
	now := time.Now().UTC()
	repo := &model.Repository{
		ID:                "repo-1",
		AgentID:           "agent-1",
		StorageTargetID:   target.ID,
		RepositoryPath:    "gdrive:/bmc/inst-1/agent-1",
		EncryptedPassword: sealed,
		Status:            "ready",
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	store.CreateRepository(context.Background(), repo)
	return repo
}

func testPlan(store *fakeStore, agent *model.Agent, repo *model.Repository) *model.Plan {
	now := time.Now().UTC()
	srcJSON, _ := json.Marshal(model.PlanSource{
		Paths: []string{"/etc", "/srv/app"},
	})
	retJSON, _ := json.Marshal(model.Retention{KeepLast: 5})
	plan := &model.Plan{
		ID:             "plan-1",
		Name:           "etc backup",
		AgentID:        agent.ID,
		Kind:           model.KindFilesystem,
		Schedule:       "0 * * * *",
		Timezone:       "UTC",
		Enabled:        true,
		Source:         model.PlanSource{Paths: []string{"/etc", "/srv/app"}},
		SourceJSON:     string(srcJSON),
		RepositoryID:   repo.ID,
		Retention:      model.Retention{KeepLast: 5},
		RetentionJSON:  string(retJSON),
		TimeoutSeconds: 3600,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	store.plans[plan.ID] = plan
	return plan
}

func newTestOrchestrator(st *fakeStore, disp *fakeDispatcher) (*Orchestrator, secrets.Sealer) {
	seal, _ := secrets.NewSealer(fakeKey())
	bus := events.New()
	o := New(st, seal, disp, bus, "inst-1")
	return o, seal
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestRenameStorageTargetKeepsConnectionFields(t *testing.T) {
	st := newFakeStore()
	o, seal := newTestOrchestrator(st, newFakeDispatcher())
	target := testTarget(seal)
	if err := st.CreateStorageTarget(context.Background(), target); err != nil {
		t.Fatal(err)
	}

	got, err := o.RenameStorageTarget(context.Background(), "admin", target.ID, "  production drive  ")
	if err != nil {
		t.Fatalf("RenameStorageTarget: %v", err)
	}
	if got.Name != "production drive" || got.RemoteName != target.RemoteName || got.RemotePath != target.RemotePath {
		t.Fatalf("unexpected target after rename: %+v", got)
	}
}

func TestUnbindRepositoryProtectsReferences(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	o, seal := newTestOrchestrator(st, newFakeDispatcher())
	target := testTarget(seal)
	_ = st.CreateStorageTarget(ctx, target)
	repo := testRepo(seal, st, target)

	if err := o.UnbindRepository(ctx, "admin", repo.ID); err != nil {
		t.Fatalf("unbound repository should succeed: %v", err)
	}
	if got, err := st.GetRepository(ctx, repo.ID); err != nil || got.DetachedAt == nil {
		t.Fatalf("expected repository to be detached and retained, got=%+v err=%v", got, err)
	}

	// A plan reference blocks unbind before the row can be removed.
	repo = testRepo(seal, st, target)
	_ = testPlan(st, testAgent(), repo)
	if err := o.UnbindRepository(ctx, "admin", repo.ID); !errors.Is(err, store.ErrInUse) {
		t.Fatalf("expected plan reference conflict, got %v", err)
	}
	if _, err := st.GetRepository(ctx, repo.ID); err != nil {
		t.Fatalf("repository should remain after blocked unbind: %v", err)
	}
}

func TestUnbindRepositoryProtectsActiveRuns(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	o, seal := newTestOrchestrator(st, newFakeDispatcher())
	target := testTarget(seal)
	_ = st.CreateStorageTarget(ctx, target)
	repo := testRepo(seal, st, target)
	st.runs["run-1"] = &model.Run{ID: "run-1", RepositoryID: repo.ID, Status: model.RunRunning}

	if err := o.UnbindRepository(ctx, "admin", repo.ID); !errors.Is(err, store.ErrInUse) {
		t.Fatalf("expected active run conflict, got %v", err)
	}
}

func TestStartPlanRunHappyPath(t *testing.T) {
	st := newFakeStore()
	disp := newFakeDispatcher()
	seal, _ := secrets.NewSealer(fakeKey())
	bus := events.New()
	o := New(st, seal, disp, bus, "inst-1")

	agent := testAgent()
	st.agents[agent.ID] = agent
	target := testTarget(seal)
	st.CreateStorageTarget(context.Background(), target)
	repo := testRepo(seal, st, target)
	_ = testPlan(st, agent, repo)

	ctx := context.Background()
	run, err := o.StartPlanRun(ctx, "plan-1", nil)
	if err != nil {
		t.Fatalf("StartPlanRun error: %v", err)
	}
	if run.Status != model.RunQueued {
		t.Fatalf("expected queued, got %s", run.Status)
	}
	if run.Operation != model.OpBackup {
		t.Fatalf("expected backup op, got %s", run.Operation)
	}
	if run.RepositoryID != "repo-1" {
		t.Fatalf("expected repo-1, got %s", run.RepositoryID)
	}

	enq := disp.Enqueued()
	if len(enq) != 1 || enq[0] != run.ID {
		t.Fatalf("expected enqueued %s, got %v", run.ID, enq)
	}
}

func TestStartPlanRunCapabilityGate(t *testing.T) {
	st := newFakeStore()
	disp := newFakeDispatcher()
	seal, _ := secrets.NewSealer(fakeKey())
	bus := events.New()
	o := New(st, seal, disp, bus, "inst-1")

	agent := &model.Agent{
		ID:           "agent-1",
		Revoked:      false,
		Capabilities: []model.ToolInfo{{Name: "restic", Path: "/usr/bin/restic"}},
		// missing pg_dump, psql
	}
	st.agents[agent.ID] = agent
	target := testTarget(seal)
	st.CreateStorageTarget(context.Background(), target)
	repo := testRepo(seal, st, target)

	now := time.Now().UTC()
	plan := &model.Plan{
		ID:             "plan-1",
		AgentID:        agent.ID,
		Kind:           model.KindPostgreSQL,
		RepositoryID:   repo.ID,
		TimeoutSeconds: 3600,
		CreatedAt:      now, UpdatedAt: now,
	}
	st.plans[plan.ID] = plan

	ctx := context.Background()
	_, err := o.StartPlanRun(ctx, "plan-1", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var mte *MissingToolsError
	if !errors.As(err, &mte) {
		t.Fatalf("expected MissingToolsError, got %T: %v", err, err)
	}
	if len(mte.Tools) != 2 {
		t.Fatalf("expected 2 missing tools, got %v", mte.Tools)
	}
}

func TestStartPlanRunDuplicateSlot(t *testing.T) {
	st := newFakeStore()
	disp := newFakeDispatcher()
	seal, _ := secrets.NewSealer(fakeKey())
	bus := events.New()
	o := New(st, seal, disp, bus, "inst-1")

	agent := testAgent()
	st.agents[agent.ID] = agent
	target := testTarget(seal)
	st.CreateStorageTarget(context.Background(), target)
	repo := testRepo(seal, st, target)
	_ = testPlan(st, agent, repo)

	ctx := context.Background()
	slot := time.Now().UTC().Truncate(time.Second)
	_, err := o.StartPlanRun(ctx, "plan-1", &slot)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	_, err = o.StartPlanRun(ctx, "plan-1", &slot)
	if !errors.Is(err, store.ErrDuplicateRun) {
		t.Fatalf("expected ErrDuplicateRun, got %v", err)
	}
}

func startPlanRunFixture(t *testing.T) (*Orchestrator, *fakeStore, *fakeDispatcher, *model.Plan) {
	t.Helper()
	st := newFakeStore()
	disp := newFakeDispatcher()
	o, seal := newTestOrchestrator(st, disp)
	agent := testAgent()
	st.agents[agent.ID] = agent
	target := testTarget(seal)
	if err := st.CreateStorageTarget(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	repo := testRepo(seal, st, target)
	return o, st, disp, testPlan(st, agent, repo)
}

// 相同计划的手动运行在队列期间只创建一个 run：重复请求复用已有 run 且不重复入队。
func TestManualRunReusesQueuedRun(t *testing.T) {
	o, st, disp, plan := startPlanRunFixture(t)
	ctx := context.Background()

	first, err := o.ManualRun(ctx, plan.ID)
	if err != nil {
		t.Fatalf("first ManualRun: %v", err)
	}
	second, err := o.ManualRun(ctx, plan.ID)
	if err != nil {
		t.Fatalf("second ManualRun: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected reuse of run %s, got %s", first.ID, second.ID)
	}
	if got := len(st.runs); got != 1 {
		t.Fatalf("expected 1 queued run, got %d", got)
	}
	if enq := disp.Enqueued(); len(enq) != 1 || enq[0] != first.ID {
		t.Fatalf("expected single enqueue of %s, got %v", first.ID, enq)
	}

	// 不同参数（另一个计划）可以并存。
	other := *plan
	other.ID = "plan-2"
	st.plans[other.ID] = &other
	third, err := o.ManualRun(ctx, other.ID)
	if err != nil {
		t.Fatalf("manual run of second plan: %v", err)
	}
	if third.ID == first.ID {
		t.Fatal("different plan must not reuse the queued run")
	}
	if got := len(st.runs); got != 2 {
		t.Fatalf("expected 2 queued runs, got %d", got)
	}
	if enq := disp.Enqueued(); len(enq) != 2 {
		t.Fatalf("expected 2 enqueues, got %v", enq)
	}
}

// 取消或失败之后，同样的手动运行必须重新入队（终态释放队列位）。
func TestManualRunAfterTerminalStartsNewRun(t *testing.T) {
	o, st, disp, plan := startPlanRunFixture(t)
	ctx := context.Background()

	cancelled, err := o.ManualRun(ctx, plan.ID)
	if err != nil {
		t.Fatalf("ManualRun: %v", err)
	}
	if err := o.CancelRun(ctx, cancelled.ID); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if st.runs[cancelled.ID].Status != model.RunCancelled {
		t.Fatalf("expected cancelled, got %s", st.runs[cancelled.ID].Status)
	}

	afterCancel, err := o.ManualRun(ctx, plan.ID)
	if err != nil {
		t.Fatalf("ManualRun after cancel: %v", err)
	}
	if afterCancel.ID == cancelled.ID {
		t.Fatal("cancelled run must not be reused")
	}

	if err := st.TransitionRun(ctx, afterCancel.ID, model.RunQueued, model.RunFailed, nil); err != nil {
		t.Fatalf("fail run: %v", err)
	}
	afterFailure, err := o.ManualRun(ctx, plan.ID)
	if err != nil {
		t.Fatalf("ManualRun after failure: %v", err)
	}
	if afterFailure.ID == afterCancel.ID {
		t.Fatal("failed run must not be reused")
	}
	if enq := disp.Enqueued(); len(enq) != 3 {
		t.Fatalf("expected 3 enqueues, got %v", enq)
	}
}

// 系统运行按 agent/操作/仓库/参数去重：相同参数复用队列中的 run，不同参数并存。
func TestSystemRunReusesQueuedRun(t *testing.T) {
	st := newFakeStore()
	disp := newFakeDispatcher()
	o, seal := newTestOrchestrator(st, disp)
	agent := testAgent()
	st.agents[agent.ID] = agent
	target := testTarget(seal)
	if err := st.CreateStorageTarget(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	repo := testRepo(seal, st, target)

	ctx := context.Background()
	params := model.SnapshotsTask{Repository: model.RepoAccess{RepositoryPath: repo.RepositoryPath}}

	first, err := o.SystemRun(ctx, agent.ID, repo.ID, model.OpSnapshots, params, 0)
	if err != nil {
		t.Fatalf("first SystemRun: %v", err)
	}
	second, err := o.SystemRun(ctx, agent.ID, repo.ID, model.OpSnapshots, params, 0)
	if err != nil {
		t.Fatalf("second SystemRun: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected reuse of run %s, got %s", first.ID, second.ID)
	}

	// 不同参数（不同仓库路径）可以并存。
	other, err := o.SystemRun(ctx, agent.ID, repo.ID, model.OpSnapshots,
		model.SnapshotsTask{Repository: model.RepoAccess{RepositoryPath: "/another/path"}}, 0)
	if err != nil {
		t.Fatalf("SystemRun with different params: %v", err)
	}
	if other.ID == first.ID {
		t.Fatal("different params must not reuse the queued run")
	}
	if enq := disp.Enqueued(); len(enq) != 2 {
		t.Fatalf("expected 2 enqueues, got %v", enq)
	}

	// 终态后同样的系统运行重新入队。
	if err := st.TransitionRun(ctx, first.ID, model.RunQueued, model.RunSucceeded, nil); err != nil {
		t.Fatal(err)
	}
	third, err := o.SystemRun(ctx, agent.ID, repo.ID, model.OpSnapshots, params, 0)
	if err != nil {
		t.Fatalf("SystemRun after success: %v", err)
	}
	if third.ID == first.ID {
		t.Fatal("succeeded run must not be reused")
	}
	if enq := disp.Enqueued(); len(enq) != 3 {
		t.Fatalf("expected 3 enqueues, got %v", enq)
	}
}

// verify-remote 的临时 rclone 配置以密钥化指纹参与去重：同配置复用，换配置不复用
// （否则会用错误凭据执行）。
func TestSystemRunWithConfDeduplicatesByCredential(t *testing.T) {
	st := newFakeStore()
	disp := newFakeDispatcher()
	o, _ := newTestOrchestrator(st, disp)
	agent := testAgent()
	st.agents[agent.ID] = agent

	ctx := context.Background()
	params := model.VerifyRemoteTask{ConfigProvided: true, RemoteName: "gdrive"}

	first, err := o.SystemRunWithConf(ctx, agent.ID, "", model.OpVerifyRemote, params, 0, "conf-a")
	if err != nil {
		t.Fatalf("first SystemRunWithConf: %v", err)
	}
	again, err := o.SystemRunWithConf(ctx, agent.ID, "", model.OpVerifyRemote, params, 0, "conf-a")
	if err != nil {
		t.Fatalf("second SystemRunWithConf: %v", err)
	}
	if again.ID != first.ID {
		t.Fatalf("same conf must reuse run %s, got %s", first.ID, again.ID)
	}
	other, err := o.SystemRunWithConf(ctx, agent.ID, "", model.OpVerifyRemote, params, 0, "conf-b")
	if err != nil {
		t.Fatalf("SystemRunWithConf with other conf: %v", err)
	}
	if other.ID == first.ID {
		t.Fatal("different conf must not reuse the queued run")
	}
	if enq := disp.Enqueued(); len(enq) != 2 {
		t.Fatalf("expected 2 enqueues, got %v", enq)
	}
}

func TestStartPlanRunAgentRevoked(t *testing.T) {
	st := newFakeStore()
	disp := newFakeDispatcher()
	seal, _ := secrets.NewSealer(fakeKey())
	bus := events.New()
	o := New(st, seal, disp, bus, "inst-1")

	agent := testAgent()
	agent.Revoked = true
	st.agents[agent.ID] = agent
	target := testTarget(seal)
	st.CreateStorageTarget(context.Background(), target)
	repo := testRepo(seal, st, target)
	_ = testPlan(st, agent, repo)

	_, err := o.StartPlanRun(context.Background(), "plan-1", nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBuildCommandBackupSecretsAndParams(t *testing.T) {
	st := newFakeStore()
	disp := newFakeDispatcher()
	seal, _ := secrets.NewSealer(fakeKey())
	bus := events.New()
	o := New(st, seal, disp, bus, "inst-1")

	agent := testAgent()
	st.agents[agent.ID] = agent
	target := testTarget(seal)
	st.CreateStorageTarget(context.Background(), target)
	repo := testRepo(seal, st, target)
	_ = testPlan(st, agent, repo)

	ctx := context.Background()
	run, err := o.StartPlanRun(ctx, "plan-1", nil)
	if err != nil {
		t.Fatalf("StartPlanRun: %v", err)
	}

	cmdID, cmd, err := o.BuildCommand(ctx, run.ID)
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	if cmdID == "" {
		t.Fatal("empty commandID")
	}
	if cmd.RunId != run.ID {
		t.Fatalf("expected RunId %s, got %s", run.ID, cmd.RunId)
	}
	if cmd.Operation != bmcv1.ExecuteCommand_BACKUP {
		t.Fatalf("expected BACKUP, got %v", cmd.Operation)
	}

	// Secrets
	if cmd.Secrets == nil {
		t.Fatal("expected secrets")
	}
	if cmd.Secrets.RcloneConf != "dummy-conf" {
		t.Fatalf("expected dummy-conf, got %q", cmd.Secrets.RcloneConf)
	}
	if cmd.Secrets.ResticPassword != "repo-secret-123" {
		t.Fatalf("expected repo-secret-123, got %q", cmd.Secrets.ResticPassword)
	}

	// Params
	var task model.BackupTask
	if err := json.Unmarshal(cmd.ParamsJson, &task); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if task.PlanID != "plan-1" {
		t.Fatalf("expected plan-1, got %s", task.PlanID)
	}
	if task.Kind != model.KindFilesystem {
		t.Fatalf("expected filesystem, got %s", task.Kind)
	}
	if len(task.Tags) != 3 {
		t.Fatalf("expected plan, kind and run tags, got %v", task.Tags)
	}
	if !containsString(task.Tags, "run:"+run.ID) {
		t.Fatalf("expected run tag, got %v", task.Tags)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestBuildCommandSystemRun(t *testing.T) {
	st := newFakeStore()
	disp := newFakeDispatcher()
	seal, _ := secrets.NewSealer(fakeKey())
	bus := events.New()
	o := New(st, seal, disp, bus, "inst-1")

	agent := testAgent()
	st.agents[agent.ID] = agent
	target := testTarget(seal)
	st.CreateStorageTarget(context.Background(), target)
	repo := testRepo(seal, st, target)

	ctx := context.Background()
	params := model.SnapshotsTask{Repository: model.RepoAccess{RepositoryPath: repo.RepositoryPath}}
	run, err := o.SystemRun(ctx, agent.ID, repo.ID, model.OpSnapshots, params, 0)
	if err != nil {
		t.Fatalf("SystemRun: %v", err)
	}

	cmdID, cmd, err := o.BuildCommand(ctx, run.ID)
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	if cmdID == "" {
		t.Fatal("empty commandID")
	}
	if cmd.Operation != bmcv1.ExecuteCommand_SNAPSHOTS {
		t.Fatalf("expected SNAPSHOTS, got %v", cmd.Operation)
	}
	if cmd.Secrets == nil {
		t.Fatal("expected secrets")
	}
	if cmd.Secrets.RcloneConf != "dummy-conf" {
		t.Fatalf("expected dummy-conf, got %q", cmd.Secrets.RcloneConf)
	}
	if cmd.Secrets.ResticPassword != "repo-secret-123" {
		t.Fatalf("expected repo-secret-123, got %q", cmd.Secrets.ResticPassword)
	}

	var task model.SnapshotsTask
	if err := json.Unmarshal(cmd.ParamsJson, &task); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if task.Repository.RepositoryPath != repo.RepositoryPath {
		t.Fatalf("expected repo path, got %q", task.Repository.RepositoryPath)
	}
}

func TestBuildCommandVerifyRemoteStashedConf(t *testing.T) {
	st := newFakeStore()
	disp := newFakeDispatcher()
	seal, _ := secrets.NewSealer(fakeKey())
	bus := events.New()
	o := New(st, seal, disp, bus, "inst-1")

	agent := testAgent()
	st.agents[agent.ID] = agent

	ctx := context.Background()
	params := model.VerifyRemoteTask{ConfigProvided: true, RemoteName: "gdrive"}
	run, err := o.SystemRun(ctx, agent.ID, "", model.OpVerifyRemote, params, 0)
	if err != nil {
		t.Fatalf("SystemRun: %v", err)
	}
	o.stashConf(run.ID, "stashed-rclone-conf")

	_, cmd, err := o.BuildCommand(ctx, run.ID)
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	if cmd.Operation != bmcv1.ExecuteCommand_VERIFY_STORAGE_REMOTE {
		t.Fatalf("expected VERIFY_STORAGE_REMOTE, got %v", cmd.Operation)
	}
	if cmd.Secrets == nil {
		t.Fatal("expected secrets")
	}
	if cmd.Secrets.RcloneConf != "stashed-rclone-conf" {
		t.Fatalf("expected stashed conf, got %q", cmd.Secrets.RcloneConf)
	}
}

// 相同参数的恢复提交只创建一个 run 与一条 request：重复请求复用二者，不重复入队。
func TestStartRestoreReusesQueuedRun(t *testing.T) {
	st := newFakeStore()
	disp := newFakeDispatcher()
	seal, _ := secrets.NewSealer(fakeKey())
	o := New(st, seal, disp, events.New(), "inst-1")

	agent := testAgent()
	st.agents[agent.ID] = agent
	target := testTarget(seal)
	_ = st.CreateStorageTarget(context.Background(), target)
	repo := testRepo(seal, st, target)
	allowRestore(st, disp, o, repo, restoreSnapshot("snap-1", model.KindPostgreSQL))

	ctx := context.Background()
	dbName := "mydb"
	in := RestoreInput{
		RepositoryID: repo.ID,
		SnapshotID:   "snap-1",
		RestoreKind:  model.KindPostgreSQL,
		Target:       model.RestoreTarget{Host: "localhost", Port: 5432, Username: "pg", Database: dbName},
		Overwrite:    true,
		Confirmation: secrets.HashToken(dbName),
	}

	firstRR, firstRun, err := o.StartRestore(ctx, "admin-1", in)
	if err != nil {
		t.Fatalf("first StartRestore: %v", err)
	}
	secondRR, secondRun, err := o.StartRestore(ctx, "admin-1", in)
	if err != nil {
		t.Fatalf("second StartRestore: %v", err)
	}
	if secondRun.ID != firstRun.ID {
		t.Fatalf("expected reuse of run %s, got %s", firstRun.ID, secondRun.ID)
	}
	if secondRR.ID != firstRR.ID {
		t.Fatalf("expected reuse of request %s, got %s", firstRR.ID, secondRR.ID)
	}
	if len(st.runs) != 1 || len(st.restoreRequests) != 1 {
		t.Fatalf("expected 1 run and 1 request, got %d and %d", len(st.runs), len(st.restoreRequests))
	}
	if enq := disp.Enqueued(); len(enq) != 1 || enq[0] != firstRun.ID {
		t.Fatalf("expected single enqueue of %s, got %v", firstRun.ID, enq)
	}

	// 数据库恢复采用全局单任务占用：不同口令、不同目标都不能并行。
	withPassword := in
	withPassword.TargetPassword = "secret-1"
	if _, _, err := o.StartRestore(ctx, "admin-1", withPassword); !errors.Is(err, ErrRestoreBusy) {
		t.Fatalf("expected ErrRestoreBusy while a database restore is in flight, got %v", err)
	}
	if err := st.FinishRestoreRun(ctx, store.FinishRestoreRunInput{
		RunID: firstRun.ID, ToStatus: model.RunSucceeded, Phase: model.RestorePhaseSucceeded,
		FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	// 终态（安全）后占用释放：同参数恢复重新入队而非复用旧 run。
	retryRR, retryRun, err := o.StartRestore(ctx, "admin-1", in)
	if err != nil {
		t.Fatalf("StartRestore after success: %v", err)
	}
	if retryRun.ID == firstRun.ID || retryRR.ID == firstRR.ID {
		t.Fatal("succeeded restore must not be reused")
	}
	// 释放占用后再提交口令变体：口令是任务参数，更换口令必须视为另一任务。
	if err := st.FinishRestoreRun(ctx, store.FinishRestoreRunInput{
		RunID: retryRun.ID, ToStatus: model.RunSucceeded, Phase: model.RestorePhaseSucceeded,
		FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	pwRR, pwRun, err := o.StartRestore(ctx, "admin-1", withPassword)
	if err != nil {
		t.Fatalf("StartRestore with credential: %v", err)
	}
	if pwRun.ID == retryRun.ID || pwRR.ID == retryRR.ID {
		t.Fatal("different credential must not reuse a queued run")
	}
	if _, again, err := o.StartRestore(ctx, "admin-1", withPassword); err != nil || again.ID != pwRun.ID {
		t.Fatalf("expected reuse with identical credential, got run=%v err=%v", again, err)
	}
	// first + retry + withPassword；相同口令的重复提交不重复入队。
	if enq := disp.Enqueued(); len(enq) != 3 {
		t.Fatalf("expected 3 enqueues, got %v", enq)
	}
	if len(st.runs) != 3 {
		t.Fatalf("expected 3 runs, got %d", len(st.runs))
	}
}

func TestStartRestoreConfirmationHash(t *testing.T) {
	st := newFakeStore()
	disp := newFakeDispatcher()
	seal, _ := secrets.NewSealer(fakeKey())
	bus := events.New()
	o := New(st, seal, disp, bus, "inst-1")

	agent := testAgent()
	st.agents[agent.ID] = agent
	target := testTarget(seal)
	st.CreateStorageTarget(context.Background(), target)
	repo := testRepo(seal, st, target)
	allowRestore(st, disp, o, repo, restoreSnapshot("snap-1", model.KindPostgreSQL))

	ctx := context.Background()
	dbName := "mydb"
	expectedHash := secrets.HashToken(dbName)

	// correct confirmation
	rr, run, err := o.StartRestore(ctx, "admin-1", RestoreInput{
		RepositoryID: repo.ID,
		SnapshotID:   "snap-1",
		RestoreKind:  model.KindPostgreSQL,
		Target:       model.RestoreTarget{Host: "localhost", Port: 5432, Username: "pg", Database: dbName},
		Overwrite:    true,
		Confirmation: expectedHash,
	})
	if err != nil {
		t.Fatalf("StartRestore: %v", err)
	}
	if rr.ConfirmationHash != expectedHash {
		t.Fatalf("expected hash %s, got %s", expectedHash, rr.ConfirmationHash)
	}
	if run.Operation != model.OpRestore {
		t.Fatalf("expected restore op, got %s", run.Operation)
	}

	// wrong confirmation → forbidden
	_, _, err = o.StartRestore(ctx, "admin-1", RestoreInput{
		RepositoryID: repo.ID,
		SnapshotID:   "snap-1",
		RestoreKind:  model.KindPostgreSQL,
		Target:       model.RestoreTarget{Database: dbName},
		Overwrite:    true,
		Confirmation: "wrong-hash",
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestStartRestoreFilesystemValidation(t *testing.T) {
	st := newFakeStore()
	disp := newFakeDispatcher()
	seal, _ := secrets.NewSealer(fakeKey())
	bus := events.New()
	o := New(st, seal, disp, bus, "inst-1")

	agent := testAgent()
	st.agents[agent.ID] = agent
	target := testTarget(seal)
	st.CreateStorageTarget(context.Background(), target)
	repo := testRepo(seal, st, target)
	allowRestore(st, disp, o, repo, restoreSnapshot("snap-1", model.KindFilesystem))

	ctx := context.Background()

	// relative path → error
	_, _, err := o.StartRestore(ctx, "admin-1", RestoreInput{
		RepositoryID: repo.ID, SnapshotID: "snap-1", RestoreKind: model.KindFilesystem,
		Target: model.RestoreTarget{TargetPath: "relative/path", OverwriteMode: "never"},
	})
	if err == nil {
		t.Fatal("expected error for relative path")
	}

	// invalid overwrite_mode
	_, _, err = o.StartRestore(ctx, "admin-1", RestoreInput{
		RepositoryID: repo.ID, SnapshotID: "snap-1", RestoreKind: model.KindFilesystem,
		Target: model.RestoreTarget{TargetPath: "/abs/path", OverwriteMode: "bogus"},
	})
	if err == nil {
		t.Fatal("expected error for bogus overwrite_mode")
	}
}

func TestWaitRunTimeout(t *testing.T) {
	st := newFakeStore()
	disp := newFakeDispatcher()
	seal, _ := secrets.NewSealer(fakeKey())
	bus := events.New()
	o := New(st, seal, disp, bus, "inst-1")

	agent := testAgent()
	st.agents[agent.ID] = agent

	ctx := context.Background()
	run, err := o.SystemRun(ctx, agent.ID, "", model.OpCheck, model.CheckTask{}, 0)
	if err != nil {
		t.Fatalf("SystemRun: %v", err)
	}

	// Wait with very short timeout; the run never transitions → timeout.
	_, err = o.WaitRun(ctx, run.ID, 50*time.Millisecond)
	if !errors.Is(err, ErrWaitTimeout) {
		t.Fatalf("expected ErrWaitTimeout, got %v", err)
	}
}

func TestWaitRunImmediateTerminal(t *testing.T) {
	st := newFakeStore()
	disp := newFakeDispatcher()
	seal, _ := secrets.NewSealer(fakeKey())
	bus := events.New()
	o := New(st, seal, disp, bus, "inst-1")

	agent := testAgent()
	st.agents[agent.ID] = agent

	ctx := context.Background()
	run, err := o.SystemRun(ctx, agent.ID, "", model.OpCheck, model.CheckTask{}, 0)
	if err != nil {
		t.Fatalf("SystemRun: %v", err)
	}

	// Manually transition to succeeded.
	now := time.Now().UTC()
	st.runs[run.ID].Status = model.RunSucceeded
	st.runs[run.ID].FinishedAt = &now

	// Wait should return immediately.
	got, err := o.WaitRun(ctx, run.ID, 2*time.Second)
	if err != nil {
		t.Fatalf("WaitRun: %v", err)
	}
	if got.Status != model.RunSucceeded {
		t.Fatalf("expected succeeded, got %s", got.Status)
	}
}

func TestWaitRunViaBusEvent(t *testing.T) {
	st := newFakeStore()
	disp := newFakeDispatcher()
	seal, _ := secrets.NewSealer(fakeKey())
	bus := events.New()
	o := New(st, seal, disp, bus, "inst-1")

	agent := testAgent()
	st.agents[agent.ID] = agent

	ctx := context.Background()
	run, err := o.SystemRun(ctx, agent.ID, "", model.OpCheck, model.CheckTask{}, 0)
	if err != nil {
		t.Fatalf("SystemRun: %v", err)
	}

	// Transition the run in store and publish a State event.
	now := time.Now().UTC()
	st.mu.Lock()
	st.runs[run.ID].Status = model.RunSucceeded
	st.runs[run.ID].FinishedAt = &now
	st.mu.Unlock()

	bus.Publish(run.ID, events.Event{Type: events.State, Run: st.runs[run.ID]})

	got, err := o.WaitRun(ctx, run.ID, 5*time.Second)
	if err != nil {
		t.Fatalf("WaitRun: %v", err)
	}
	if got.Status != model.RunSucceeded {
		t.Fatalf("expected succeeded, got %s", got.Status)
	}
}

func TestRedact(t *testing.T) {
	cases := []struct {
		in, out string
	}{
		{"postgres://user:secret@host/db", "postgres://***@host/db"},
		{"mysql://root:hunter2@localhost/mydb", "mysql://***@localhost/mydb"},
		{"?password=supersecret&host=localhost", "?password=***&host=localhost"},
		{"?password=***&host=localhost", "?password=***&host=localhost"},
		{"/tmp/foo.bmc-secret.txt", "***"},
		{"normal text", "normal text"},
		{"https://admin:pass123@example.com/api", "https://***@example.com/api"},
	}
	for _, tc := range cases {
		got := Redact(tc.in)
		if got != tc.out {
			t.Errorf("Redact(%q) = %q, want %q", tc.in, got, tc.out)
		}
	}
}

func TestRequiredTools(t *testing.T) {
	cases := map[string][]string{
		model.KindFilesystem: {"restic"},
		model.KindPostgreSQL: {"restic", "pg_dump", "psql"},
		model.KindMySQL:      {"restic", "mysqldump", "mysql"},
		model.KindMongoDB:    {"restic", "mongodump", "mongorestore"},
		model.KindSQLite:     {"restic", "sqlite3"},
	}
	for kind, want := range cases {
		got := requiredTools(kind)
		if len(got) != len(want) {
			t.Errorf("requiredTools(%s) = %v, want %v", kind, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("requiredTools(%s)[%d] = %s, want %s", kind, i, got[i], want[i])
			}
		}
	}
}

func TestRestoreRequiredTools(t *testing.T) {
	cases := map[string][]string{
		model.KindFilesystem: {"restic", "rclone"},
		model.KindPostgreSQL: {"restic", "rclone", "pg_dump", "pg_restore", "psql"},
		model.KindMySQL:      {"restic", "rclone", "mysqldump", "mysql"},
		// mongosh 缺失时 mongodb 适配器直接拒绝恢复，声明必须与之同步。
		model.KindMongoDB: {"restic", "rclone", "mongodump", "mongorestore", "mongosh"},
		model.KindSQLite:  {"restic", "rclone"},
	}
	for kind, want := range cases {
		got := restoreRequiredTools(kind)
		if len(got) != len(want) {
			t.Errorf("restoreRequiredTools(%s) = %v, want %v", kind, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("restoreRequiredTools(%s)[%d] = %s, want %s", kind, i, got[i], want[i])
			}
		}
	}
}

func TestBuildRepoPath(t *testing.T) {
	cases := []struct {
		name       string
		remotePath string
		want       string
	}{
		// 绝对路径必须保留前导 "/"：rclone 的 local 后端按进程工作目录解析
		// 相对路径，丢失前导斜杠会让仓库落到 agent 的临时目录下。
		{"absolute", "/bmc/backup/", "gdrive:/bmc/backup/inst-1/agent-1"},
		{"absolute no trailing", "/bmc/backup", "gdrive:/bmc/backup/inst-1/agent-1"},
		// 对象存储桶路径没有前导斜杠，保持原有相对语义。
		{"relative bucket", "bmc/backup", "gdrive:bmc/backup/inst-1/agent-1"},
		{"empty", "", "gdrive:inst-1/agent-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := &model.StorageTarget{RemoteName: "gdrive", RemotePath: tc.remotePath}
			if got := buildRepoPath(target, "inst-1", "agent-1"); got != tc.want {
				t.Fatalf("buildRepoPath(%q) = %q, want %q", tc.remotePath, got, tc.want)
			}
		})
	}
}

func TestCancelRunQueued(t *testing.T) {
	st := newFakeStore()
	disp := newFakeDispatcher()
	seal, _ := secrets.NewSealer(fakeKey())
	bus := events.New()
	o := New(st, seal, disp, bus, "inst-1")

	agent := testAgent()
	st.agents[agent.ID] = agent

	ctx := context.Background()
	run, err := o.SystemRun(ctx, agent.ID, "", model.OpCheck, model.CheckTask{}, 0)
	if err != nil {
		t.Fatalf("SystemRun: %v", err)
	}

	if err := o.CancelRun(ctx, run.ID); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}

	got, err := st.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != model.RunCancelled {
		t.Fatalf("expected cancelled, got %s", got.Status)
	}
	if got.ErrorCode != model.ErrCancelled {
		t.Fatalf("expected ErrCancelled, got %s", got.ErrorCode)
	}
}

func TestManualRunIsAlias(t *testing.T) {
	st := newFakeStore()
	disp := newFakeDispatcher()
	seal, _ := secrets.NewSealer(fakeKey())
	bus := events.New()
	o := New(st, seal, disp, bus, "inst-1")

	agent := testAgent()
	st.agents[agent.ID] = agent
	target := testTarget(seal)
	st.CreateStorageTarget(context.Background(), target)
	repo := testRepo(seal, st, target)
	_ = testPlan(st, agent, repo)

	ctx := context.Background()
	run, err := o.ManualRun(ctx, "plan-1")
	if err != nil {
		t.Fatalf("ManualRun: %v", err)
	}
	if run.ScheduledAt != nil {
		t.Fatalf("ManualRun should have nil ScheduledAt, got %v", run.ScheduledAt)
	}
}

// Compile-time assertions.
var _ dispatch.Dispatcher = (*fakeDispatcher)(nil)

// 扫描失败的退避必须被遵守：否则持续失败的仓库会在每个 scheduler tick
// （15s）重发一次孤儿扫描，造成 run/日志无上限增长与进程耗尽。
func TestMaybeStartCleanupScanHonorsBackoff(t *testing.T) {
	ctx := context.Background()
	t0 := time.Now().UTC()
	st := newFakeStore()
	o, seal := newTestOrchestrator(st, newFakeDispatcher())

	agent := &model.Agent{
		ID: "agent-1", Name: "a", Hostname: "h", Status: model.AgentOnline,
		EnrolledAt: t0, LastSeenAt: &t0,
	}
	st.agents[agent.ID] = agent
	st.repos["repo-1"] = &model.Repository{
		ID: "repo-1", AgentID: agent.ID, RepositoryPath: "r:/x", Status: "ready",
		EncryptedPassword: []byte("pw"), CreatedAt: t0, UpdatedAt: t0,
	}
	_ = seal

	runCount := func() int {
		st.mu.Lock()
		defer st.mu.Unlock()
		n := 0
		for _, r := range st.runs {
			if r.Operation == model.OpSnapshots {
				n++
			}
		}
		return n
	}

	// 退避未到期：不得发起新扫描。
	future := t0.Add(time.Hour)
	st.cleanupStates["repo-1"] = &model.SnapshotCleanupState{
		RepositoryID:  "repo-1",
		NextAttemptAt: &future,
	}
	if err := o.TickSnapshotCleanup(ctx, t0); err != nil {
		t.Fatalf("TickSnapshotCleanup: %v", err)
	}
	if n := runCount(); n != 0 {
		t.Fatalf("backoff must suppress scan, got %d snapshots runs", n)
	}

	// 退避已过期：应当发起扫描。
	past := t0.Add(-time.Minute)
	st.cleanupStates["repo-1"] = &model.SnapshotCleanupState{
		RepositoryID:  "repo-1",
		NextAttemptAt: &past,
	}
	if err := o.TickSnapshotCleanup(ctx, t0); err != nil {
		t.Fatalf("TickSnapshotCleanup: %v", err)
	}
	if n := runCount(); n != 1 {
		t.Fatalf("expired backoff must allow one scan, got %d", n)
	}

	// 失败 run 尚未被 consume：同一 tick 内不得再发第二次扫描。
	st.cleanupStates["repo-1"] = &model.SnapshotCleanupState{
		RepositoryID: "repo-1", ScanRunID: "stale-run",
	}
	st.runs["stale-run"] = &model.Run{
		ID: "stale-run", AgentID: agent.ID, Operation: model.OpSnapshots,
		Status: model.RunFailed, QueuedAt: t0, ProgressJSON: "{}",
	}
	before := runCount()
	if err := o.TickSnapshotCleanup(ctx, t0); err != nil {
		t.Fatalf("TickSnapshotCleanup: %v", err)
	}
	if n := runCount(); n != before {
		t.Fatalf("failed scan must wait for consume, got %d new runs", n-before)
	}
}
