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
	OpBackup         = "backup"
	OpRestore        = "restore"
	OpRestoreDryRun  = "restore_dry_run"
	OpCheck          = "check"
	OpForget         = "forget"
	OpSnapshots      = "snapshots"
	OpSnapshotLs     = "snapshot_ls"
	OpVerifyRemote   = "verify_storage_remote"
	OpValidatePaths  = "validate_paths"
	OpProbeCaps      = "probe_capabilities"
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
	ErrRepositoryLocked         = "repository_locked"
	ErrRepositoryMissing        = "repository_missing"
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
	IDHash     string    // SHA-256 hex of the bearer token; never the token itself
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
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Hostname         string            `json:"hostname"`
	OS               string            `json:"os"`
	Arch             string            `json:"arch"`
	Version          string            `json:"version"`
	Status           AgentStatus       `json:"status"`
	LastSeenAt       *time.Time        `json:"last_seen_at,omitempty"`
	EnrolledAt       time.Time         `json:"enrolled_at"`
	TokenHash        string            `json:"-"` // SHA-256 hex of the agent secret
	Capabilities     []ToolInfo        `json:"capabilities"`
	CapabilitiesJSON string            `json:"-"`
	Revoked          bool              `json:"revoked"`
}

// ToolInfo mirrors the proto message for REST exposure.
type ToolInfo struct {
	Name    string `json:"name"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
}

type EnrollmentToken struct {
	ID        string
	TokenHash string // SHA-256 hex
	ExpiresAt time.Time
	UsedAt    *time.Time
}

// StorageTarget is a cloud drive destination accessed via rclone. Config is
// stored AES-256-GCM sealed; never returned in plaintext by any API.
type StorageTarget struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Type             string    `json:"type"` // only "rclone" in phase 1
	RemoteName       string    `json:"remote_name"`
	RemotePath       string    `json:"remote_path"`
	EncryptedConfig  []byte    `json:"-"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type Repository struct {
	ID                string     `json:"id"`
	AgentID           string     `json:"agent_id"`
	StorageTargetID   string     `json:"storage_target_id"`
	RepositoryPath    string     `json:"repository_path"` // <remote>:<remote_path>/<instance_id>/<agent_id>
	EncryptedPassword []byte     `json:"-"`
	Status            string     `json:"status"` // pending|ready|error
	LastCheckAt       *time.Time `json:"last_check_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
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
	Kind           string     `json:"kind"` // filesystem|postgresql|mysql|mongodb|sqlite
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
	Database   string   `json:"database,omitempty"` // single db or literal "all"
	ExtraArgs  []string `json:"extra_args,omitempty"`
	// Estimated dump size in bytes; required for database plans to guard temp
	// space (run fails insufficient_temp_space when free < 1.2x).
	EstimatedDumpBytes int64 `json:"estimated_dump_bytes,omitempty"`

	// mongodb only
	CaptureOplog bool `json:"capture_oplog,omitempty"`

	// sqlite only (absolute path replaces the network fields)
	Path string `json:"path,omitempty"`
}

type Run struct {
	ID           string     `json:"id"`
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
	// ScheduledAt is the cron slot this run fulfils; (plan_id, scheduled_at)
	// is unique to prevent double-queueing. Empty for manual runs.
	ScheduledAt *time.Time `json:"scheduled_at,omitempty"`
}

type Progress struct {
	Phase      string `json:"phase,omitempty"`
	Percent    float64 `json:"percent,omitempty"`
	BytesDone  int64  `json:"bytes_done,omitempty"`
	BytesTotal int64  `json:"bytes_total,omitempty"`
	FilesDone  int64  `json:"files_done,omitempty"`
	FilesTotal int64  `json:"files_total,omitempty"`
}

type RunLog struct {
	RunID     string    `json:"run_id"`
	Seq       uint64    `json:"seq"`
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"` // debug|info|warn|error
	Message   string    `json:"message"`
}

type RestoreRequest struct {
	ID                string     `json:"id"`
	RunID             string     `json:"run_id"` // the restore run created for it
	SnapshotID        string     `json:"snapshot_id"`
	RestoreKind       string     `json:"restore_kind"` // filesystem|postgresql|mysql|mongodb|sqlite
	Target            RestoreTarget `json:"target"`
	TargetJSON        string     `json:"-"`
	Overwrite         bool       `json:"overwrite"`
	ConfirmationHash  string     `json:"-"` // SHA-256 of typed db name when overwriting
	CreatedAt         time.Time  `json:"created_at"`
}

type RestoreTarget struct {
	// filesystem
	TargetPath    string   `json:"target_path,omitempty"`
	IncludePaths  []string `json:"include_paths,omitempty"`
	OverwriteMode string   `json:"overwrite_mode,omitempty"` // never|if-changed|always

	// databases
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Username string `json:"username,omitempty"`
	Database string `json:"database,omitempty"`
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

// Snapshot is a read model assembled from `restic snapshots --json` on the
// agent; not persisted server-side.
type Snapshot struct {
	ID       string   `json:"id"`
	Time     string   `json:"time"`
	Host     string   `json:"host"`
	Tags     []string `json:"tags,omitempty"`
	Paths    []string `json:"paths,omitempty"`
	PlanID   string   `json:"-"`
}

// ServerInfo is reported to agents during handshake.
type ServerInfo struct {
	Version        string
	InstanceID     string
	HeartbeatSecs  int
}
