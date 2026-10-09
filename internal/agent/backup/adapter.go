// Package backup defines the contract every plan-kind adapter implements.
// Adapters never assemble shell strings: they receive structured specs, a
// private temp directory, a log sink, and an argv-only command executor.
package backup

import (
	"context"
	"time"

	"backupmanagementcenter/internal/model"
)

// Plan kind constants, mirrored from model for convenience.
const (
	KindFilesystem = "filesystem"
	KindPostgreSQL = "postgresql"
	KindMySQL      = "mysql"
	KindMongoDB    = "mongodb"
	KindSQLite     = "sqlite"
)

// Cmd is one process to run. Args are passed verbatim to exec; no shell.
type Cmd struct {
	Exe       string   // absolute path of the tool
	Args      []string // argv[1:]
	Env       []string // extra KEY=VAL entries appended to the sanitized env
	Dir       string   // optional working directory
	StdinPath string   // optional file connected to stdin
}

// Executor runs commands, streaming stdout/stderr lines to callbacks and
// returning the raw exit code so callers can map restic semantics.
type Executor interface {
	Run(ctx context.Context, c Cmd, onStdout func(line string), onStderr func(line string)) (exitCode int, err error)
}

// SecretBundle carries per-run credentials. Values exist only in memory or in
// 0600 files inside the private temp dir; never logged, never in argv.
type SecretBundle struct {
	ResticPassword string
	DBPassword     string
	RcloneConf     string
}

// RunContext is everything an adapter may touch during Backup/Restore.
type RunContext struct {
	RunID    string
	Task     model.BackupTask
	Secrets  SecretBundle
	TempDir  string // private, wiped after the run
	Exec     Executor
	Logf     func(level, format string, args ...any)
	Progress func(model.Progress)
}

// Manifest describes a database export stored next to the dump files inside
// the restic snapshot. Contains no secrets.
type Manifest struct {
	Adapter      string            `json:"adapter"`
	ToolVersions map[string]string `json:"tool_versions"`
	Databases    []DbExport        `json:"databases"`
	StartedAt    time.Time         `json:"started_at"`
	FinishedAt   time.Time         `json:"finished_at"`
	RestoreHints map[string]string `json:"restore_hints,omitempty"` // non-secret restore parameters
}

type DbExport struct {
	Database string `json:"database"` // logical name; "globals" for pg globals dump
	File     string `json:"file"`     // path relative to staging dir
	Format   string `json:"format"`   // pgdump|sql|archive|sqlite
}

// BackupArtifact tells the pipeline what to hand to restic.
type BackupArtifact struct {
	// LivePaths non-empty => filesystem kind: back up host paths directly.
	LivePaths     []string
	ExcludeFile   string // absolute path to exclude file (temp), optional
	OneFileSystem bool

	// StagingDir non-empty => database kind: back up produced files.
	StagingDir   string   // private temp dir holding artifacts + manifest.json
	StagingFiles []string // relative file names, manifest.json included

	Manifest *Manifest
}

// RestoreSpec is what an adapter needs to import restored data.
type RestoreSpec struct {
	SnapshotID string
	Kind       string
	StagingDir string // snapshot content restored here (database kinds)
	Database   *model.DatabaseRestore
	Secrets    SecretBundle
	Tools      map[string]ToolInfo
	Logf       func(level, format string, args ...any)
	Progress   func(model.Progress)
	Exec       Executor

	// RunID 是本次恢复的 run 标识，用于保护标签与审计。
	RunID string
	// ArtifactFile/ArtifactDatabase/ArtifactFormat 描述本 run 唯一要导入的产物；
	// 多库、globals 与 all 范围已在上游拒绝。
	ArtifactFile     string
	ArtifactDatabase string
	ArtifactFormat   string
	// ArtifactCharset/ArtifactCollation 是产物所属库的默认字符集/排序规则（备份时
	// 记入 manifest，恢复时用于 CREATE DATABASE，避免目标库继承目标服务器默认值）。
	ArtifactCharset   string
	ArtifactCollation string
	// TargetIsNew 为 true 表示目标由本 run 新建，清理时只允许删除该目标。
	TargetIsNew bool
}

// PlanSpec is the validated shape passed to Adapter.Validate.
type PlanSpec struct {
	Kind    string
	Source  model.PlanSource
	AgentID string
}

// ToolInfo mirrors model.ToolInfo to avoid an import cycle in agent code.
type ToolInfo = model.ToolInfo

// Adapter is implemented once per plan kind (filesystem, postgresql, mysql,
// mongodb, sqlite). Database kinds also implement DatabaseRestorer.
type Adapter interface {
	// Validate checks the plan against this host before it can be created or
	// enabled (paths exist/readable/absolute; tools present; flags sane).
	Validate(ctx context.Context, spec PlanSpec) error
	// Backup produces the artifact to snapshot. It must clean up its own
	// intermediate state on error; TempDir is wiped by the runner afterwards.
	Backup(ctx context.Context, rc *RunContext) (*BackupArtifact, error)
}

// DatabaseRestorer is the control plane of a database restore. Implementations
// must be able to tell "target absent" apart from "target unreachable", and must
// refuse destructive work they cannot prove safe.
type DatabaseRestorer interface {
	// TargetExists reports whether the target database/file exists. Permission
	// or connection failures must return an error, never false.
	TargetExists(ctx context.Context, spec *RestoreSpec) (bool, error)
	// Import loads the staged artifact into the target. When TargetIsNew is set
	// it must create the target with conflict-rejecting semantics; otherwise it
	// must fully replace the target's content.
	Import(ctx context.Context, spec *RestoreSpec) error
	// VerifyRestored proves the imported content matches the snapshot. Exit
	// codes or a structural integrity check alone are not sufficient.
	VerifyRestored(ctx context.Context, spec *RestoreSpec) error
	// RemoveTarget deletes the target created by this run. It must never delete
	// a database/file another actor created.
	RemoveTarget(ctx context.Context, spec *RestoreSpec) error
}
