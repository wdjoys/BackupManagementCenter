package model

// Task payloads carried in ExecuteCommand.params_json, keyed by operation.
// None of these structures ever contain secrets; SecretSet carries them.

// BackupTask drives OPERATION_BACKUP. Source is the plan's PlanSource;
// RepositoryPath is "<remote>:<path>/<instance>/<agent>".
type BackupTask struct {
	PlanID         string     `json:"plan_id"`
	Kind           string     `json:"kind"`
	Repository     RepoAccess `json:"repository"`
	Source         PlanSource `json:"source"`
	Retention       Retention  `json:"retention,omitempty"` // run after successful backup (best effort)
	Tags           []string   `json:"tags"`                // always contains plan:<id>, kind:<kind>
	TimeoutSeconds int        `json:"timeout_seconds,omitempty"`
}

// RepoAccess identifies how to reach a restic repository.
type RepoAccess struct {
	RepositoryPath string `json:"repository_path"` // e.g. gdrive:bmc/<instance>/<agent>
	CacheDir       string `json:"cache_dir,omitempty"`
}

// CheckTask drives OPERATION_CHECK.
type CheckTask struct {
	Repository RepoAccess `json:"repository"`
}

// ForgetTask drives OPERATION_FORGET.
type ForgetTask struct {
	PlanID     string     `json:"plan_id"`
	Kind       string     `json:"kind"`
	Repository RepoAccess `json:"repository"`
	Retention  Retention  `json:"retention"`
}

// SnapshotsTask drives OPERATION_SNAPSHOTS.
type SnapshotsTask struct {
	Repository RepoAccess `json:"repository"`
}

// SnapshotLsTask drives OPERATION_SNAPSHOT_LS.
type SnapshotLsTask struct {
	Repository RepoAccess `json:"repository"`
	SnapshotID string     `json:"snapshot_id"`
}

// FilesystemRestore is the target branch of RestoreTask for kind filesystem.
type FilesystemRestore struct {
	SnapshotID    string   `json:"snapshot_id"`
	IncludePaths  []string `json:"include_paths,omitempty"`
	TargetPath    string   `json:"target_path"` // absolute; staging dir created by agent
	OverwriteMode string   `json:"overwrite_mode"` // never|if-changed|always
	DryRun        bool     `json:"dry_run"`
}

// DatabaseRestore is the target branch for database kinds.
type DatabaseRestore struct {
	SnapshotID      string `json:"snapshot_id"`
	Kind            string `json:"kind"`
	TargetHost      string `json:"target_host"`
	TargetPort      int    `json:"target_port,omitempty"`
	TargetUsername  string `json:"target_username,omitempty"`
	TargetDatabase  string `json:"target_database"` // may be "all" for pg full-instance artifacts
	ReplaceExisting bool   `json:"replace_existing"`
}

// RestoreTask drives OPERATION_RESTORE and OPERATION_RESTORE_DRY_RUN.
type RestoreTask struct {
	Kind       string            `json:"kind"`
	Repository RepoAccess        `json:"repository"`
	Filesystem *FilesystemRestore `json:"filesystem,omitempty"`
	Database   *DatabaseRestore   `json:"database,omitempty"`
}

// VerifyRemoteTask drives OPERATION_VERIFY_STORAGE_REMOTE: the agent writes
// the conf to a private temp file and runs rclone listremotes + lsd.
type VerifyRemoteTask struct {
	ConfigProvided bool   `json:"config_provided"` // true = use SecretSet.rclone_conf
	RemoteName     string `json:"remote_name"`
}

// ValidatePathsTask drives OPERATION_VALIDATE_PATHS for filesystem plans.
type ValidatePathsTask struct {
	Paths    []string `json:"paths"`
	Excludes []string `json:"excludes,omitempty"`
}

// ProbeCapsTask drives OPERATION_PROBE_CAPABILITIES.
type ProbeCapsTask struct{}

// Restic exit-code to error-code mapping shared by server and agent.
func MapResticExitCode(code int) string {
	switch code {
	case 3:
		return ErrPartialSourceRead
	case 11:
		return ErrRepositoryLocked
	case 12:
		return ErrWrongRepositoryPassword
	case 130:
		return ErrCancelled
	default:
		return ""
	}
}
