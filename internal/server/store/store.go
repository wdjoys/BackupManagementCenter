package store

import (
	"context"
	"errors"
	"time"

	"backupmanagementcenter/internal/model"
)

// Store is the full persistence surface of the server. Implementations must
// be safe for concurrent use and serialize writers appropriately for SQLite
// WAL mode. All times are UTC.
type Store interface {
	Close() error

	// Migrate applies embedded SQL migrations transactionally; safe to call
	// on every boot.
	Migrate(ctx context.Context) error

	// Admins — at most one row ever exists.
	HasAdmin(ctx context.Context) (bool, error)
	CreateAdmin(ctx context.Context, a *model.Admin) error
	GetAdminByUsername(ctx context.Context, username string) (*model.Admin, error)
	GetAdminByID(ctx context.Context, id string) (*model.Admin, error)
	UpdateAdminLastLogin(ctx context.Context, adminID string, at time.Time) error

	// Sessions
	CreateSession(ctx context.Context, s *model.Session) error
	GetSession(ctx context.Context, idHash string) (*model.Session, error)
	TouchSession(ctx context.Context, idHash string, lastSeen time.Time) error
	DeleteSession(ctx context.Context, idHash string) error
	DeleteExpiredSessions(ctx context.Context, now time.Time) error

	// Enrollment tokens (one-time, short-lived)
	CreateEnrollmentToken(ctx context.Context, t *model.EnrollmentToken) error
	ListEnrollmentTokens(ctx context.Context) ([]model.EnrollmentToken, error)
	// ConsumeEnrollmentToken atomically marks an unused, unexpired token used;
	// returns ErrTokenInvalid when unknown/used/expired.
	ConsumeEnrollmentToken(ctx context.Context, tokenHash string, now time.Time) (*model.EnrollmentToken, error)

	// Agents
	UpsertAgentOnConnect(ctx context.Context, a *model.Agent) error // by ID; updates host/os/arch/version/last_seen/status
	SetAgentStatus(ctx context.Context, agentID string, st model.AgentStatus, at time.Time) error
	SaveAgentCapabilities(ctx context.Context, agentID string, tools []model.ToolInfo, at time.Time) error
	GetAgent(ctx context.Context, id string) (*model.Agent, error)
	GetAgentBySecretHash(ctx context.Context, tokenHash string) (*model.Agent, error)
	ListAgents(ctx context.Context) ([]model.Agent, error)
	RevokeAgent(ctx context.Context, id string) error

	// Storage targets
	CreateStorageTarget(ctx context.Context, t *model.StorageTarget) error
	UpdateStorageTarget(ctx context.Context, t *model.StorageTarget) error
	DeleteStorageTarget(ctx context.Context, id string) error
	GetStorageTarget(ctx context.Context, id string) (*model.StorageTarget, error)
	ListStorageTargets(ctx context.Context) ([]model.StorageTarget, error)

	// Repositories
	CreateRepository(ctx context.Context, r *model.Repository) error
	GetRepository(ctx context.Context, id string) (*model.Repository, error)
	GetRepositoryByAgentAndTarget(ctx context.Context, agentID, targetID string) (*model.Repository, error)
	ListRepositories(ctx context.Context) ([]model.Repository, error)
	ListRepositoriesNeedingCheck(ctx context.Context, olderThan time.Time) ([]model.Repository, error)
	UpdateRepositoryStatus(ctx context.Context, id, status string) error
	MarkRepositoryChecked(ctx context.Context, id string, at time.Time) error

	// Plans
	CreatePlan(ctx context.Context, p *model.Plan) error
	UpdatePlan(ctx context.Context, p *model.Plan) error
	DeletePlan(ctx context.Context, id string) error
	GetPlan(ctx context.Context, id string) (*model.Plan, error)
	ListPlans(ctx context.Context, agentID string) ([]model.Plan, error)
	ListEnabledPlans(ctx context.Context) ([]model.Plan, error)

	// Runs. CreateRun enforces unique (plan_id, scheduled_at); returns
	// ErrDuplicateRun when the slot already has a run.
	CreateRun(ctx context.Context, r *model.Run) error
	GetRun(ctx context.Context, id string) (*model.Run, error)
	ListRuns(ctx context.Context, f RunFilter) ([]model.Run, error)
	// TransitionRun moves status forward along the state machine only;
	// returns ErrInvalidTransition otherwise. Terminal states are final.
	TransitionRun(ctx context.Context, id, from, to string, mutate func(*model.Run)) error
	ListRunsByStatus(ctx context.Context, statuses []string) ([]model.Run, error)
	FailStaleRuns(ctx context.Context, statuses []string, errorCode string, at time.Time) (int64, error)

	// Run logs
	AppendRunLogs(ctx context.Context, logs []model.RunLog) error
	ListRunLogs(ctx context.Context, runID string, beforeSeq uint64, limit int) ([]model.RunLog, error)
	MaxRunLogSeq(ctx context.Context, runID string) (uint64, error)

	// Restore requests
	CreateRestoreRequest(ctx context.Context, rr *model.RestoreRequest) error
	GetRestoreRequest(ctx context.Context, id string) (*model.RestoreRequest, error)
	ListRestoreRequests(ctx context.Context, limit int) ([]model.RestoreRequest, error)

	// Audit
	AppendAuditEvent(ctx context.Context, e *model.AuditEvent) error
	ListAuditEvents(ctx context.Context, limit int) ([]model.AuditEvent, error)
}

type RunFilter struct {
	AgentID   string
	PlanID    string
	Statuses  []string
	Operation string
	Limit     int
	Offset    int
}

var (
	ErrNotFound          = errors.New("store: not found")
	ErrDuplicateRun      = errors.New("store: duplicate scheduled run")
	ErrInvalidTransition = errors.New("store: invalid run transition")
	ErrTokenInvalid      = errors.New("store: enrollment token invalid")
	ErrAdminExists       = errors.New("store: admin already exists")
	ErrInUse             = errors.New("store: resource still referenced")
)
