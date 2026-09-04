package jobs

import (
	"context"
	"encoding/json"
	"errors"
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
		seenSlots:       make(map[string]bool),
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
	s.runs[r.ID] = r
	return nil
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
func (s *fakeStore) SaveAgentCapabilities(ctx context.Context, agentID string, tools []model.ToolInfo, sourceMappings []model.PathMapping, restoreMappings []model.PathMapping, at time.Time) error {
	return nil
}
func (s *fakeStore) GetAgentBySecretHash(ctx context.Context, h string) (*model.Agent, error) {
	return nil, store.ErrNotFound
}
func (s *fakeStore) ListAgents(ctx context.Context) ([]model.Agent, error)  { return nil, nil }
func (s *fakeStore) RevokeAgent(ctx context.Context, id string) error       { return nil }
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
	return nil, nil
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
func (s *fakeStore) ListRunLogs(ctx context.Context, runID string, beforeSeq uint64, limit int) ([]model.RunLog, error) {
	return nil, nil
}
func (s *fakeStore) MaxRunLogSeq(ctx context.Context, runID string) (uint64, error) { return 0, nil }
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

func TestBuildRepoPath(t *testing.T) {
	target := &model.StorageTarget{
		RemoteName: "gdrive",
		RemotePath: "/bmc/backup/",
	}
	got := buildRepoPath(target, "inst-1", "agent-1")
	want := "gdrive:bmc/backup/inst-1/agent-1"
	if got != want {
		t.Fatalf("buildRepoPath = %q, want %q", got, want)
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
