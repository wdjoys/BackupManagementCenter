// Package model defines the shared domain types used by the server, the agent
// and the wire protocol payloads. It is the single source of truth for JSON
// shapes exchanged inside ExecuteCommand.params_json and REST APIs.
package model

import "time"

// Plan kinds.
const (
	KindFilesystem = "filesystem"
	KindPostgreSQL = "postgresql"
	KindMySQL      = "mysql"
	KindMongoDB    = "mongodb"
	KindSQLite     = "sqlite"
)

// Run operations.
const (
	OpBackup        = "backup"
	OpRestore       = "restore"
	OpRestoreDryRun = "restore_dry_run"
	OpCheck         = "check"
	OpForget        = "forget"
	OpSnapshots     = "snapshots"
	OpSnapshotLs    = "snapshot_ls"
	OpVerifyRemote  = "verify_storage_remote"
	OpValidatePaths = "validate_paths"
	OpProbeCaps     = "probe_capabilities"
)

// Run statuses, in state-machine order. Terminal: succeeded|failed|cancelled.
const (
	RunQueued     = "queued"
	RunDispatched = "dispatched"
	RunRunning    = "running"
	RunSucceeded  = "succeeded"
	RunFailed     = "failed"
	RunCancelled  = "cancelled"
)

// Stable error codes surfaced to the UI and audit log.
const (
	ErrServerRestarted          = "server_restarted"
	ErrAgentUnavailable         = "agent_unavailable"
	ErrAgentOnline              = "agent_online"
	ErrAgentRevoked             = "agent_revoked"
	ErrRepositoryMissing        = "repository_missing"
	ErrRepositoryLocked         = "repository_locked"
	ErrPartialSourceRead        = "partial_source_read"
	ErrWrongRepositoryPassword  = "wrong_repository_password"
	ErrCancelled                = "cancelled"
	ErrInsufficientTempSpace    = "insufficient_temp_space"
	ErrMissingTools             = "missing_tools"
	ErrInvalidPlan              = "invalid_plan"
	ErrPathValidation           = "path_validation_failed"
	ErrRestoreTargetNotEmpty    = "restore_target_not_empty"
	ErrRestoreVerification      = "restore_verification_failed"
	ErrStorageRemoteUnreachable = "storage_remote_unreachable"
	ErrTimeout                  = "run_timeout"
	ErrAgentDisconnected        = "agent_disconnected"
	ErrPreRestoreBackupFailed   = "pre_restore_backup_failed"
	ErrRollbackFailed           = "rollback_failed"
	ErrPhysicalBackupRequired   = "physical_backup_required"
	ErrDatabaseRestoreDisabled  = "database_restore_disabled"
)

func nowUTC() time.Time { return time.Now().UTC() }

type Admin struct {
	ID           string     `json:"id"`
	Username     string     `json:"username"`
	PasswordHash string     `json:"-"`
	CreatedAt    time.Time  `json:"created_at"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
}

type Session struct {
	IDHash     string // SHA-256 hex of the bearer token; never the token itself
	AdminID    string
	ExpiresAt  time.Time
	CreatedAt  time.Time
	LastSeenAt time.Time
}

type AgentStatus string

const (
	AgentOnline  AgentStatus = "online"
	AgentOffline AgentStatus = "offline"
)

type Agent struct {
	ID                  string        `json:"id"`
	Name                string        `json:"name"`
	Hostname            string        `json:"hostname"`
	OS                  string        `json:"os"`
	Arch                string        `json:"arch"`
	Version             string        `json:"version"`
	Status              AgentStatus   `json:"status"`
	LastSeenAt          *time.Time    `json:"last_seen_at,omitempty"`
	EnrolledAt          time.Time     `json:"enrolled_at"`
	TokenHash           string        `json:"-"` // SHA-256 hex of the agent secret
	Capabilities        []ToolInfo    `json:"capabilities"`
	SourcePathMappings  []PathMapping `json:"source_path_mappings"`
	RestorePathMappings []PathMapping `json:"restore_path_mappings"`
	CapabilitiesJSON    string        `json:"-"`
	Revoked             bool          `json:"revoked"`
}

// PathMapping 描述宿主机路径到 Agent 运行环境路径的映射。
type PathMapping struct {
	HostPath    string `json:"host_path"`
	RuntimePath string `json:"runtime_path"`
	ReadOnly    bool   `json:"read_only"`
}

// ToolInfo mirrors the proto message for REST exposure.
type ToolInfo struct {
	Name    string `json:"name"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
}
type EnrollmentToken struct {
	ID            string
	TokenHash     string // SHA-256 hex
	ExpiresAt     time.Time
	UsedAt        *time.Time
	TargetAgentID string
}

// StorageTarget is a cloud drive destination accessed via rclone. Config is
// stored AES-256-GCM sealed; never returned in plaintext by any API.
type StorageTarget struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Type            string    `json:"type"` // only "rclone" in phase 1
	RemoteName      string    `json:"remote_name"`
	RemotePath      string    `json:"remote_path"`
	EncryptedConfig []byte    `json:"-"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type Repository struct {
	ID                string     `json:"id"`
	AgentID           string     `json:"agent_id"`
	StorageTargetID   string     `json:"storage_target_id"`
	RepositoryPath    string     `json:"repository_path"` // <remote>:<remote_path>/<instance_id>/<agent_id>
	EncryptedPassword []byte     `json:"-"`
	Status            string     `json:"status"` // pending|ready|error
	LastCheckAt       *time.Time `json:"last_check_at,omitempty"`
	// DetachedAt marks a logical unbind. The row and encrypted repository
	// password are retained so a later bind can safely re-adopt remote data.
	DetachedAt *time.Time `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// Retention maps 1:1 to restic forget flags.
type Retention struct {
	KeepLast    int `json:"keep_last,omitempty"`
	KeepDaily   int `json:"keep_daily,omitempty"`
	KeepWeekly  int `json:"keep_weekly,omitempty"`
	KeepMonthly int `json:"keep_monthly,omitempty"`
}

type Plan struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	AgentID        string     `json:"agent_id"`
	Kind           string     `json:"kind"`     // filesystem|postgresql|mysql|mongodb|sqlite
	Schedule       string     `json:"schedule"` // five-field cron
	Timezone       string     `json:"timezone"` // IANA name
	Enabled        bool       `json:"enabled"`
	Source         PlanSource `json:"source"`
	SourceJSON     string     `json:"-"`
	RepositoryID   string     `json:"repository_id"`
	Retention      Retention  `json:"retention"`
	RetentionJSON  string     `json:"-"`
	TimeoutSeconds int        `json:"timeout_seconds"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// PlanSource is the union of per-kind source specs. Exactly one branch is set,
// selected by Plan.Kind. JSON stored in backup_plans.source_json.
type PlanSource struct {
	// filesystem
	Paths         []string `json:"paths,omitempty"`
	Excludes      []string `json:"excludes,omitempty"`
	OneFileSystem bool     `json:"one_file_system,omitempty"`

	// shared by postgresql/mysql/mongodb
	Host       string   `json:"host,omitempty"`
	Port       int      `json:"port,omitempty"`
	Username   string   `json:"username,omitempty"`
	Database   string   `json:"database,omitempty"`    // single db or literal "all"
	AuthSource string   `json:"auth_source,omitempty"` // mongodb authentication database
	ExtraArgs  []string `json:"extra_args,omitempty"`
	// Estimated dump size in bytes; required for database plans to guard temp
	// space (the agent requires at least 1.3x free space).
	EstimatedDumpBytes int64 `json:"estimated_dump_bytes,omitempty"`

	// mongodb only
	CaptureOplog bool `json:"capture_oplog,omitempty"`

	// sqlite only (absolute path replaces the network fields)
	Path string `json:"path,omitempty"`
}

type Run struct {
	ID string `json:"id"`
	// PlanID empty => system-initiated run not bound to a plan (stored NULL).
	PlanID       string     `json:"plan_id,omitempty"`
	AgentID      string     `json:"agent_id"`
	Operation    string     `json:"operation"`
	Status       string     `json:"status"`
	QueuedAt     time.Time  `json:"queued_at"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	Progress     Progress   `json:"progress"`
	ProgressJSON string     `json:"-"`
	SnapshotID   string     `json:"snapshot_id,omitempty"`
	ErrorCode    string     `json:"error_code,omitempty"`
	ErrorMessage string     `json:"error_message,omitempty"`
	RepositoryID string     `json:"repository_id,omitempty"`
	// Attempt increments whenever a queued run is claimed for delivery. It is
	// persisted so a server restart can distinguish an old lease from a new
	// dispatch attempt.
	Attempt        int        `json:"attempt"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	// ScheduledAt is the cron slot this run fulfils; (plan_id, scheduled_at)
	// is unique to prevent double-queueing. Empty for manual runs.
	ScheduledAt *time.Time `json:"scheduled_at,omitempty"`
}

type Progress struct {
	Phase      string  `json:"phase,omitempty"`
	Percent    float64 `json:"percent,omitempty"`
	BytesDone  int64   `json:"bytes_done,omitempty"`
	BytesTotal int64   `json:"bytes_total,omitempty"`
	FilesDone  int64   `json:"files_done,omitempty"`
	FilesTotal int64   `json:"files_total,omitempty"`
	// Restore dry-run details. These remain in the JSON result even though the
	// streaming protobuf only carries the aggregate files counters.
	FilesAdded   int64    `json:"files_added,omitempty"`
	FilesChanged int64    `json:"files_changed,omitempty"`
	FilesSkipped int64    `json:"files_skipped,omitempty"`
	FilesDeleted int64    `json:"files_deleted,omitempty"`
	Sample       []string `json:"sample,omitempty"`
}

type RunLog struct {
	RunID     string    `json:"run_id"`
	Seq       uint64    `json:"seq"`
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"` // debug|info|warn|error
	Message   string    `json:"message"`
}

// SystemLog 是Server和Agent进程日志的统一API/存储形状。
// ID由Server落库时分配；SourceSeq保留Agent本地序号，便于定位断线与重连。
type SystemLog struct {
	ID        int64     `json:"id"`
	AgentID   string    `json:"agent_id,omitempty"`
	SourceSeq uint64    `json:"source_seq,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`  // system|http|agent|run|scheduler|dispatcher|connection|command|notification
	Level     string    `json:"level"` // debug|info|warn|error
	Message   string    `json:"message"`
}

type RestoreRequest struct {
	ID                 string        `json:"id"`
	RunID              string        `json:"run_id"` // the restore run created for it
	SnapshotID         string        `json:"snapshot_id"`
	RestoreKind        string        `json:"restore_kind"` // filesystem|postgresql|mysql|mongodb|sqlite
	Target             RestoreTarget `json:"target"`
	TargetJSON         string        `json:"-"`
	Overwrite          bool          `json:"overwrite"`
	ConfirmationHash   string        `json:"-"` // SHA-256 of typed db name when overwriting
	PreRestoreRunID    string        `json:"pre_restore_run_id,omitempty"`
	RollbackSnapshotID string        `json:"rollback_snapshot_id,omitempty"`
	Phase              string        `json:"phase,omitempty"`
	CreatedAt          time.Time     `json:"created_at"`
}

type RestoreTarget struct {
	// filesystem
	TargetPath    string   `json:"target_path,omitempty"`
	IncludePaths  []string `json:"include_paths,omitempty"`
	OverwriteMode string   `json:"overwrite_mode,omitempty"` // never|if-changed|always

	// databases
	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"`
	Username   string `json:"username,omitempty"`
	Database   string `json:"database,omitempty"`
	AuthSource string `json:"auth_source,omitempty"`
}

type AuditEvent struct {
	ID           string    `json:"id"`
	OccurredAt   time.Time `json:"occurred_at"`
	ActorType    string    `json:"actor_type"` // admin|system
	ActorID      string    `json:"actor_id"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id"`
	DetailJSON   string    `json:"detail,omitempty"`
}

// TelegramSettings is the web-configured Telegram failure-notification
// target. BotToken is plaintext only in transit/at the edges; at rest it is
// sealed with AAD TelegramTokenAAD. A missing row means notifications are
// disabled.
type TelegramSettings struct {
	BotToken  string    `json:"bot_token"`
	ChatID    string    `json:"chat_id"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Sealing coordinates for the stored bot token (secrets.Sealer AAD).
const (
	TelegramSettingsTable = "telegram_settings"
	TelegramSettingsRow   = "1"
	TelegramTokenColumn   = "encrypted_token"
)

// Snapshot 是 Agent 返回的 Restic 快照读模型，不在 Server 数据库中持久化。
type Snapshot struct {
	ID    string   `json:"id"`
	Time  string   `json:"time"`
	Host  string   `json:"host"`
	Tags  []string `json:"tags"`
	Paths []string `json:"paths"`
}

// SnapshotDeletionSource 标识删除意图来源。
type SnapshotDeletionSource string

const (
	SnapshotDeletionManual SnapshotDeletionSource = "manual"
	SnapshotDeletionOrphan SnapshotDeletionSource = "orphan"
)

// SnapshotDeletionState 标识删除意图状态。
type SnapshotDeletionState string

const (
	SnapshotDeletionCandidate SnapshotDeletionState = "candidate"
	SnapshotDeletionPending   SnapshotDeletionState = "pending"
	SnapshotDeletionRunning   SnapshotDeletionState = "running"
	SnapshotDeletionSucceeded SnapshotDeletionState = "succeeded"
)

// SnapshotDeletion 记录一次快照删除意图与执行状态。
type SnapshotDeletion struct {
	ID             string                 `json:"id"`
	RepositoryID   string                 `json:"repository_id"`
	AgentID        string                 `json:"agent_id"`
	SnapshotID     string                 `json:"snapshot_id"`
	Source         SnapshotDeletionSource `json:"source"`
	State          SnapshotDeletionState  `json:"state"`
	FirstSeenAt    time.Time              `json:"first_seen_at"`
	LastSeenAt     time.Time              `json:"last_seen_at"`
	SeenCount      int                    `json:"seen_count"`
	NextAttemptAt  *time.Time             `json:"next_attempt_at,omitempty"`
	Attempt        int                    `json:"attempt"`
	RunID          string                 `json:"run_id,omitempty"`
	LeaseExpiresAt *time.Time             `json:"lease_expires_at,omitempty"`
	ErrorCode      string                 `json:"error_code,omitempty"`
	ErrorMessage   string                 `json:"error_message,omitempty"`
	RequestedBy    string                 `json:"requested_by,omitempty"`
	CreatedAt      time.Time              `json:"created_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
	CompletedAt    *time.Time             `json:"completed_at,omitempty"`
}

// SnapshotCleanupState 记录每仓库的孤儿扫描进度。
type SnapshotCleanupState struct {
	RepositoryID        string     `json:"repository_id"`
	ScanRunID           string     `json:"scan_run_id,omitempty"`
	LastScanStartedAt   *time.Time `json:"last_scan_started_at,omitempty"`
	LastScanCompletedAt *time.Time `json:"last_scan_completed_at,omitempty"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// ServerInfo is reported to agents during handshake.

// ServerInfo is reported to agents during handshake.
type ServerInfo struct {
	Version       string
	InstanceID    string
	HeartbeatSecs int
}
