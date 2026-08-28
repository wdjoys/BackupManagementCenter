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

	// Runs. CreateRun enforces unique (plan_id, scheduled_at); returns
	// ErrDuplicateRun when the slot already has a run.
	CreateRun(ctx context.Context, r *model.Run) error
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
	SnapshotCacheGeneration(ctx context.Context, repositoryID string) (int64, error)
	SaveSnapshotListCache(ctx context.Context, repositoryID string, generation int64, snapshotsJSON, fingerprint string, verifiedAt time.Time) error
	SaveSnapshotTreeCache(ctx context.Context, repositoryID, snapshotID, cachePath string, generation int64, treeJSON string, verifiedAt time.Time) error
	InvalidateSnapshotCache(ctx context.Context, repositoryID string, clearTrees bool) error
}

type SnapshotListCache struct {
	RepositoryID string
	Generation   int64
	SnapshotsJSON string
	Fingerprint  string
	VerifiedAt   time.Time
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
	ErrCacheGenerationChanged = errors.New("store: snapshot cache generation changed")
)
