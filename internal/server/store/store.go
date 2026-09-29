package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	posixpath "path"
	"sort"
	"strings"
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
	ResetAdmin(ctx context.Context) error

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
	ReEnrollAgent(ctx context.Context, agentID string, tokenHash string, now time.Time) error

	// Agents
	UpsertAgentOnConnect(ctx context.Context, a *model.Agent) error // by ID; updates host/os/arch/version/last_seen/status
	SetAgentStatus(ctx context.Context, agentID string, st model.AgentStatus, at time.Time) error
	SaveAgentCapabilities(ctx context.Context, agentID string, tools []model.ToolInfo, sourceMappings []model.PathMapping, restoreMappings []model.PathMapping, safeDatabaseRestore bool, at time.Time) error
	GetAgent(ctx context.Context, id string) (*model.Agent, error)
	GetAgentBySecretHash(ctx context.Context, tokenHash string) (*model.Agent, error)
	ListAgents(ctx context.Context) ([]model.Agent, error)
	RevokeAgent(ctx context.Context, id string) error
	// UnrevokeAgent clears the revoked flag and marks the agent offline so it
	// can reconnect with its existing identity/hash; ErrNotFound when unknown.
	UnrevokeAgent(ctx context.Context, id string) error
	RenameAgent(ctx context.Context, id, name string) error
	// TelegramSettings: single-row web-configured failure-notification
	// target. GetTelegramSettings returns ErrNotFound when unset; the token
	// is stored sealed by the caller.
	GetTelegramSettings(ctx context.Context) (*model.TelegramSettings, error)
	SaveTelegramSettings(ctx context.Context, s *model.TelegramSettings) error
	DeleteTelegramSettings(ctx context.Context) error

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
	// DetachRepository hides a binding from normal listings while retaining its
	// encrypted repository password for a later safe re-adoption. It never
	// deletes remote Restic data.
	DetachRepository(ctx context.Context, id string) error
	UpdateRepositoryStatus(ctx context.Context, id, status string) error
	MarkRepositoryChecked(ctx context.Context, id string, at time.Time) error

	// Plans
	CreatePlan(ctx context.Context, p *model.Plan) error
	UpdatePlan(ctx context.Context, p *model.Plan) error
	DeletePlan(ctx context.Context, id string) error
	GetPlan(ctx context.Context, id string) (*model.Plan, error)
	ListPlans(ctx context.Context, agentID string) ([]model.Plan, error)
	ListEnabledPlans(ctx context.Context) ([]model.Plan, error)

	// Runs. CreateRun enforces unique (plan_id, scheduled_at) and unique
	// Run.DedupKey among queued/dispatched/running runs; both violations
	// return ErrDuplicateRun.
	CreateRun(ctx context.Context, r *model.Run) error
	// FindActiveRunByDedupKey returns the newest queued/dispatched/running run
	// carrying dedupKey, or ErrNotFound. Callers use it to join the in-flight
	// task instead of queueing a duplicate.
	FindActiveRunByDedupKey(ctx context.Context, dedupKey string) (*model.Run, error)
	GetRun(ctx context.Context, id string) (*model.Run, error)
	ListRuns(ctx context.Context, f RunFilter) ([]model.Run, error)
	// TransitionRun moves status forward along the state machine only;
	// returns ErrInvalidTransition otherwise. Terminal states are final.
	TransitionRun(ctx context.Context, id, from, to string, mutate func(*model.Run)) error
	ListRunsByStatus(ctx context.Context, statuses []string) ([]model.Run, error)
	// FailStaleRuns force-fails runs in the given non-terminal statuses and
	// returns the IDs actually moved to failed.
	FailStaleRuns(ctx context.Context, statuses []string, errorCode string, at time.Time) ([]string, error)

	// Run logs
	AppendRunLogs(ctx context.Context, logs []model.RunLog) error
	ListRunLogs(ctx context.Context, runID string, beforeID int64, limit int) ([]model.RunLog, error)

	// Restore requests
	CreateRestoreRequest(ctx context.Context, rr *model.RestoreRequest) error
	GetRestoreRequest(ctx context.Context, id string) (*model.RestoreRequest, error)
	// GetRestoreRequestByRunID returns the restore request bound to a restore
	// run, used to reuse an already queued restore instead of queueing a
	// duplicate. Returns ErrNotFound when the run has no request row yet.
	GetRestoreRequestByRunID(ctx context.Context, runID string) (*model.RestoreRequest, error)
	ListRestoreRequests(ctx context.Context, limit int) ([]model.RestoreRequest, error)
	// CreateDatabaseRestoreRun 在同一写事务内校验数据库恢复的全局占用并创建
	// run 与 request 行；占用冲突返回 ErrDatabaseRestoreBusy，等价任务返回
	// ErrDuplicateRun（调用方复用已有 run）。
	CreateDatabaseRestoreRun(ctx context.Context, run *model.Run, rr *model.RestoreRequest) error
	// ActiveDatabaseRestoreRunID 返回当前占用数据库恢复全局互斥的 run ID；
	// 没有占用时返回空字符串。
	ActiveDatabaseRestoreRunID(ctx context.Context) (string, error)
	// RepositoryRestoreBlocked 报告仓库是否被未安全终结的恢复阻塞，
	// exceptRunID 用于放行该 run 自身的首次下发。
	RepositoryRestoreBlocked(ctx context.Context, repositoryID, exceptRunID string) (bool, error)
	// RequestRestoreStop 请求停止一个已下发的恢复：写入 cancelling 阶段并设置取消宽限截止。
	RequestRestoreStop(ctx context.Context, runID string, deadline time.Time) error
	// ProtectedRestoreSnapshotIDs 返回该仓库中未安全终结的恢复所引用的保护快照 ID。
	ProtectedRestoreSnapshotIDs(ctx context.Context, repositoryID string) (map[string]struct{}, error)
	// UpdateRestoreRollbackSnapshot 由 Agent 进度上报同步保护快照 ID；
	// 不覆盖已记录的非空 ID。
	UpdateRestoreRollbackSnapshot(ctx context.Context, runID, snapshotID string) error
	// FinishRestoreRun 在单事务内终结恢复 run 并写入 restore_requests 的
	// phase/rollback_snapshot_id。重复相同终态幂等；已确认的终态与人工
	// 解除结论不会被覆盖。
	FinishRestoreRun(ctx context.Context, in FinishRestoreRunInput) error
	// ResolveRestoreRequest 人工解除阻塞：写入 manual_recovery_resolved 并终结 run。
	ResolveRestoreRequest(ctx context.Context, requestID, runID, actorID, note string, at time.Time) error

	// Audit
	AppendAuditEvent(ctx context.Context, e *model.AuditEvent) error
	ListAuditEvents(ctx context.Context, limit int) ([]model.AuditEvent, error)
}

// LogStore是可选的进程日志持久化接口，单独拆出以保持测试替身和扩展Store兼容。
type LogStore interface {
	AppendServerLogs(ctx context.Context, logs []model.SystemLog) error
	ListServerLogs(ctx context.Context, filter ProcessLogFilter) ([]model.SystemLog, error)
	AppendAgentLogs(ctx context.Context, agentID string, logs []model.SystemLog) error
	ListAgentLogs(ctx context.Context, agentID string, filter ProcessLogFilter) ([]model.SystemLog, error)
}

// SnapshotCacheStore 是可选的持久化快照缓存接口。
type SnapshotCacheStore interface {
	GetSnapshotListCache(ctx context.Context, repositoryID string) (*SnapshotListCache, error)
	GetSnapshotTreeCache(ctx context.Context, repositoryID, snapshotID, cachePath string) (*SnapshotTreeCache, error)
	GetSnapshotListBrowseCache(ctx context.Context, repositoryID string) (*SnapshotListCache, bool, error)
	GetSnapshotTreeBrowseCache(ctx context.Context, repositoryID, snapshotID, cachePath string) (*SnapshotTreeCache, bool, error)
	SnapshotCacheGeneration(ctx context.Context, repositoryID string) (int64, error)
	SaveSnapshotListCache(ctx context.Context, repositoryID string, generation int64, snapshotsJSON, fingerprint string, verifiedAt time.Time) error
	SaveSnapshotTreeCache(ctx context.Context, repositoryID, snapshotID, cachePath string, generation int64, treeJSON string, verifiedAt time.Time) error
	InvalidateSnapshotCache(ctx context.Context, repositoryID string, clearTrees bool) error
}

type SnapshotListCache struct {
	RepositoryID  string
	Generation    int64
	SnapshotsJSON string
	Fingerprint   string
	VerifiedAt    time.Time
}

type SnapshotTreeCache struct {
	RepositoryID string
	SnapshotID   string
	Path         string
	Generation   int64
	TreeJSON     string
	VerifiedAt   time.Time
}

// SnapshotDeletionStore 是窄接口，仅用于快照删除意图与孤儿扫描状态持久化。
// 独立于 Store，避免测试替身和外部实现必须立即实现这些方法。
type SnapshotDeletionStore interface {
	// QueueManualSnapshotDeletion 创建或升级 manual 删除意图。
	// 返回：记录、是否新建/升级、错误。
	QueueManualSnapshotDeletion(ctx context.Context, repositoryID, agentID, snapshotID, actorID string, now time.Time) (*model.SnapshotDeletion, bool, error)
	// HiddenSnapshotIDs 返回当前 repository 中应被前端隐藏的 snapshotID 集合。
	HiddenSnapshotIDs(ctx context.Context, repositoryID string) (map[string]struct{}, error)
	// ListDueSnapshotDeletions 返回到期的 pending 删除意图。
	ListDueSnapshotDeletions(ctx context.Context, now time.Time, limit int) ([]model.SnapshotDeletion, error)
	// ListRunningSnapshotDeletions 返回 state='running' 的删除意图，
	// 供 TickSnapshotCleanup 对照关联 run 终态做完成或 fresh scan 确认。
	ListRunningSnapshotDeletions(ctx context.Context) ([]model.SnapshotDeletion, error)
	// ClaimSnapshotDeletionRun 在同一事务内插入 queued run、写入 ForgetTask、将意图置 running 并绑定 run_id/lease_expires_at。
	ClaimSnapshotDeletionRun(ctx context.Context, deletionID string, run *model.Run, leaseUntil time.Time) error
	// CompleteSnapshotDeletion 将意图置 succeeded 并记录完成时间。
	CompleteSnapshotDeletion(ctx context.Context, deletionID string, now time.Time) error
	// RetrySnapshotDeletion 记录失败并设置下次重试时间。
	RetrySnapshotDeletion(ctx context.Context, deletionID, errorCode, errorMessage string, nextAttemptAt time.Time) error
	// GetSnapshotCleanupState 读取孤儿扫描状态。
	GetSnapshotCleanupState(ctx context.Context, repositoryID string) (*model.SnapshotCleanupState, error)
	// StartSnapshotCleanupScan 以 run_id compare-and-set 开始一次扫描。
	StartSnapshotCleanupScan(ctx context.Context, repositoryID, runID string, startedAt time.Time) error
	// FinishSnapshotCleanupScan 成功扫描后执行孤儿 reconciliation（含 7 天/seen_count 阈值）并写完成时间。
	FinishSnapshotCleanupScan(ctx context.Context, repositoryID, runID string, snapshots []model.Snapshot, completedAt time.Time) error
	// ClearSnapshotCleanupScan 清理活跃扫描（失败时调用），保留候选原状态，不增加 seen_count。
	ClearSnapshotCleanupScan(ctx context.Context, repositoryID, runID string, nextAttemptAt time.Time) error
}

// NormalizeSnapshotPath 统一目录缓存键；空路径和根目录都使用 "/"。
func NormalizeSnapshotPath(raw string) string {
	p := strings.TrimSpace(raw)
	p = strings.ReplaceAll(p, "\\", "/")
	if p == "" || p == "." || p == "/" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = posixpath.Clean(p)
	if p == "." || p == "" {
		return "/"
	}
	return p
}

// SnapshotFingerprint 对快照内容计算稳定摘要，不依赖远程返回顺序。
func SnapshotFingerprint(snaps []model.Snapshot) string {
	normalized := make([]model.Snapshot, len(snaps))
	copy(normalized, snaps)
	for i := range normalized {
		normalized[i].Tags = append([]string(nil), normalized[i].Tags...)
		normalized[i].Paths = append([]string(nil), normalized[i].Paths...)
		sort.Strings(normalized[i].Tags)
		sort.Strings(normalized[i].Paths)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].ID != normalized[j].ID {
			return normalized[i].ID < normalized[j].ID
		}
		if normalized[i].Time != normalized[j].Time {
			return normalized[i].Time < normalized[j].Time
		}
		return normalized[i].Host < normalized[j].Host
	})
	data, _ := json.Marshal(normalized)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ProcessLogFilter定义进程日志分页及筛选条件。
type ProcessLogFilter struct {
	BeforeID int64
	Limit    int
	Levels   []string
	Types    []string
}

type RunFilter struct {
	AgentID      string
	PlanID       string
	RepositoryID string
	Statuses     []string
	Operation    string
	Limit        int
	Offset       int
}

var (
	ErrNotFound               = errors.New("store: not found")
	ErrDuplicateRun           = errors.New("store: duplicate scheduled run")
	ErrInvalidTransition      = errors.New("store: invalid run transition")
	ErrTokenInvalid           = errors.New("store: enrollment token invalid")
	ErrAdminExists            = errors.New("store: admin already exists")
	ErrInUse                  = errors.New("store: resource still referenced")
	ErrPlanHasSnapshots       = errors.New("store: plan still has snapshots")
	ErrCacheGenerationChanged = errors.New("store: snapshot cache generation changed")
	// ErrDatabaseRestoreBusy 表示已有未安全终结的数据库恢复占用全局互斥。
	ErrDatabaseRestoreBusy = errors.New("store: database restore busy")
	// ErrRestoreConflict 表示已有不同的确认结果，不能覆盖。
	ErrRestoreConflict = errors.New("store: restore result conflict")
)

// FinishRestoreRunInput 描述一次恢复 run 的原子终结。
type FinishRestoreRunInput struct {
	RunID        string
	FromStatuses []string
	ToStatus     string
	ErrorCode    string
	ErrorMessage string
	SnapshotID   string
	ResultJSON   string
	// Phase 与 RollbackSnapshotID 写入 restore_requests；phase 为空表示不改动。
	Phase              string
	RollbackSnapshotID string
	// AllowUnsafeTerminal 允许在没有可信结果时以不安全 phase 终结（人工解除）。
	AllowUnsafeTerminal bool
	FinishedAt          time.Time
}
