package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"backupmanagementcenter/internal/model"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type sqliteStore struct {
	db *sql.DB
	mu sync.Mutex
}

// New opens or creates a SQLite database at path and returns a Store.
// The database is opened with WAL journalling, foreign keys enabled,
// a 5-second busy timeout, and NORMAL synchronous mode.
func New(path string) (Store, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Set pragmas. WAL mode must be set while no other connections exist.
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("set pragma %s: %w", pragma, err)
		}
	}

	// Allow up to 4 connections for concurrent reads; the write mutex
	// serialises writers.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxIdleTime(30 * time.Second)

	s := &sqliteStore{db: db}
	return s, nil
}

func (s *sqliteStore) Close() error {
	return s.db.Close()
}

// ---------------------------------------------------------------------------
// Migrate
// ---------------------------------------------------------------------------

func (s *sqliteStore) Migrate(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Ensure schema_migrations table exists.
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	// Read applied versions.
	applied := map[string]bool{}
	rows, err := s.db.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("query schema_migrations: %w", err)
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("scan schema_migrations: %w", err)
		}
		applied[v] = true
	}
	rows.Close()

	// List migration files, sorted by name.
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	now := nowUTC()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		if applied[e.Name()] {
			continue
		}

		data, err := fs.ReadFile(migrationsFS, path.Join("migrations", e.Name()))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", e.Name(), err)
		}

		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", e.Name(), err)
		}

		if _, err := tx.Exec(string(data)); err != nil {
			tx.Rollback()
			return fmt.Errorf("execute migration %s: %w", e.Name(), err)
		}

		if _, err := tx.Exec(
			"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
			e.Name(), now.Format(time.RFC3339),
		); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %s: %w", e.Name(), err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", e.Name(), err)
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// Admins
// ---------------------------------------------------------------------------

func (s *sqliteStore) HasAdmin(ctx context.Context) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM admins").Scan(&count)
	if err != nil {
		return false, fmt.Errorf("has admin: %w", err)
	}
	return count > 0, nil
}

func (s *sqliteStore) CreateAdmin(ctx context.Context, a *model.Admin) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check no admin exists yet.
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM admins").Scan(&count); err != nil {
		return fmt.Errorf("create admin count: %w", err)
	}
	if count > 0 {
		return ErrAdminExists
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO admins (id, username, password_hash, created_at, last_login_at)
		 VALUES (?, ?, ?, ?, ?)`,
		a.ID, a.Username, a.PasswordHash,
		a.CreatedAt.Format(time.RFC3339),
		nullTime(a.LastLoginAt),
	)
	if err != nil {
		return fmt.Errorf("create admin: %w", err)
	}
	return nil
}

func (s *sqliteStore) GetAdminByUsername(ctx context.Context, username string) (*model.Admin, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT id, username, password_hash, created_at, last_login_at FROM admins WHERE username = ?",
		username,
	)
	return scanAdmin(row)
}

func (s *sqliteStore) GetAdminByID(ctx context.Context, id string) (*model.Admin, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT id, username, password_hash, created_at, last_login_at FROM admins WHERE id = ?",
		id,
	)
	return scanAdmin(row)
}

func (s *sqliteStore) UpdateAdminLastLogin(ctx context.Context, adminID string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE admins SET last_login_at = ? WHERE id = ?",
		at.Format(time.RFC3339), adminID,
	)
	if err != nil {
		return fmt.Errorf("update admin last login: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

func (s *sqliteStore) CreateSession(ctx context.Context, sess *model.Session) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id_hash, admin_id, expires_at, created_at, last_seen_at)
		 VALUES (?, ?, ?, ?, ?)`,
		sess.IDHash, sess.AdminID,
		sess.ExpiresAt.Format(time.RFC3339),
		sess.CreatedAt.Format(time.RFC3339),
		sess.LastSeenAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (s *sqliteStore) GetSession(ctx context.Context, idHash string) (*model.Session, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT id_hash, admin_id, expires_at, created_at, last_seen_at FROM sessions WHERE id_hash = ?",
		idHash,
	)
	return scanSession(row)
}

func (s *sqliteStore) TouchSession(ctx context.Context, idHash string, lastSeen time.Time) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE sessions SET last_seen_at = ? WHERE id_hash = ?",
		lastSeen.Format(time.RFC3339), idHash,
	)
	if err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

func (s *sqliteStore) DeleteSession(ctx context.Context, idHash string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE id_hash = ?", idHash)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

func (s *sqliteStore) DeleteExpiredSessions(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM sessions WHERE expires_at < ?",
		now.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("delete expired sessions: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Enrollment tokens
// ---------------------------------------------------------------------------

func (s *sqliteStore) CreateEnrollmentToken(ctx context.Context, t *model.EnrollmentToken) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO enrollment_tokens (id, token_hash, expires_at, used_at)
		 VALUES (?, ?, ?, ?)`,
		t.ID, t.TokenHash, t.ExpiresAt.Format(time.RFC3339), nullTime(t.UsedAt),
	)
	if err != nil {
		return fmt.Errorf("create enrollment token: %w", err)
	}
	return nil
}

func (s *sqliteStore) ListEnrollmentTokens(ctx context.Context) ([]model.EnrollmentToken, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, token_hash, expires_at, used_at FROM enrollment_tokens ORDER BY expires_at DESC",
	)
	if err != nil {
		return nil, fmt.Errorf("list enrollment tokens: %w", err)
	}
	defer rows.Close()

	var out []model.EnrollmentToken
	for rows.Next() {
		t, err := scanEnrollmentToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *sqliteStore) ConsumeEnrollmentToken(ctx context.Context, tokenHash string, now time.Time) (*model.EnrollmentToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	nowStr := now.Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx,
		`UPDATE enrollment_tokens SET used_at = ?
		 WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?`,
		nowStr, tokenHash, nowStr,
	)
	if err != nil {
		return nil, fmt.Errorf("consume enrollment token: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, ErrTokenInvalid
	}

	row := s.db.QueryRowContext(ctx,
		"SELECT id, token_hash, expires_at, used_at FROM enrollment_tokens WHERE token_hash = ?",
		tokenHash,
	)
	return scanEnrollmentToken(row)
}

// ---------------------------------------------------------------------------
// Agents
// ---------------------------------------------------------------------------

func (s *sqliteStore) UpsertAgentOnConnect(ctx context.Context, a *model.Agent) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agents (id, name, hostname, os, arch, version, status, last_seen_at, enrolled_at, token_hash, capabilities_json, revoked)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
		 ON CONFLICT(id) DO UPDATE SET
		   name=excluded.name, hostname=excluded.hostname, os=excluded.os,
		   arch=excluded.arch, version=excluded.version,
		   status=excluded.status, last_seen_at=excluded.last_seen_at,
		   token_hash=excluded.token_hash`,
		a.ID, a.Name, a.Hostname, a.OS, a.Arch, a.Version,
		string(a.Status), nullTime(a.LastSeenAt),
		a.EnrolledAt.Format(time.RFC3339), a.TokenHash, a.CapabilitiesJSON,
	)
	if err != nil {
		return fmt.Errorf("upsert agent: %w", err)
	}
	return nil
}

func (s *sqliteStore) SetAgentStatus(ctx context.Context, agentID string, st model.AgentStatus, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE agents SET status = ?, last_seen_at = ? WHERE id = ?",
		string(st), at.Format(time.RFC3339), agentID,
	)
	if err != nil {
		return fmt.Errorf("set agent status: %w", err)
	}
	return nil
}

func (s *sqliteStore) SaveAgentCapabilities(ctx context.Context, agentID string, tools []model.ToolInfo, at time.Time) error {
	data, err := json.Marshal(tools)
	if err != nil {
		return fmt.Errorf("marshal capabilities: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		"UPDATE agents SET capabilities_json = ?, last_seen_at = ? WHERE id = ?",
		string(data), at.Format(time.RFC3339), agentID,
	)
	if err != nil {
		return fmt.Errorf("save agent capabilities: %w", err)
	}
	return nil
}

func (s *sqliteStore) GetAgent(ctx context.Context, id string) (*model.Agent, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, hostname, os, arch, version, status, last_seen_at,
		        enrolled_at, token_hash, capabilities_json, revoked
		 FROM agents WHERE id = ?`, id,
	)
	return scanAgent(row)
}

func (s *sqliteStore) GetAgentBySecretHash(ctx context.Context, tokenHash string) (*model.Agent, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, hostname, os, arch, version, status, last_seen_at,
		        enrolled_at, token_hash, capabilities_json, revoked
		 FROM agents WHERE token_hash = ?`, tokenHash,
	)
	return scanAgent(row)
}

func (s *sqliteStore) ListAgents(ctx context.Context) ([]model.Agent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, hostname, os, arch, version, status, last_seen_at,
		        enrolled_at, token_hash, capabilities_json, revoked
		 FROM agents ORDER BY name`,
	)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()

	var out []model.Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (s *sqliteStore) RevokeAgent(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE agents SET revoked = 1 WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("revoke agent: %w", err)
	}
	return nil
}

func (s *sqliteStore) RenameAgent(ctx context.Context, id, name string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE agents SET name = ? WHERE id = ?", name, id)
	if err != nil {
		return fmt.Errorf("rename agent: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// Storage targets
// ---------------------------------------------------------------------------

func (s *sqliteStore) CreateStorageTarget(ctx context.Context, t *model.StorageTarget) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO storage_targets (id, name, type, remote_name, remote_path, encrypted_config, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Name, t.Type, t.RemoteName, t.RemotePath, t.EncryptedConfig,
		t.CreatedAt.Format(time.RFC3339), t.UpdatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("create storage target: %w", err)
	}
	return nil
}

func (s *sqliteStore) UpdateStorageTarget(ctx context.Context, t *model.StorageTarget) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE storage_targets SET name=?, remote_name=?, remote_path=?,
		   encrypted_config=?, updated_at=?
		 WHERE id=?`,
		t.Name, t.RemoteName, t.RemotePath, t.EncryptedConfig,
		t.UpdatedAt.Format(time.RFC3339), t.ID,
	)
	if err != nil {
		return fmt.Errorf("update storage target: %w", err)
	}
	return nil
}

func (s *sqliteStore) DeleteStorageTarget(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check for repository references.
	var count int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM repositories WHERE storage_target_id = ?", id,
	).Scan(&count); err != nil {
		return fmt.Errorf("delete storage target check repos: %w", err)
	}
	if count > 0 {
		return ErrInUse
	}

	_, err := s.db.ExecContext(ctx, "DELETE FROM storage_targets WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete storage target: %w", err)
	}
	return nil
}

func (s *sqliteStore) GetStorageTarget(ctx context.Context, id string) (*model.StorageTarget, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, type, remote_name, remote_path, encrypted_config, created_at, updated_at
		 FROM storage_targets WHERE id = ?`, id,
	)
	return scanStorageTarget(row)
}

func (s *sqliteStore) ListStorageTargets(ctx context.Context) ([]model.StorageTarget, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, type, remote_name, remote_path, encrypted_config, created_at, updated_at
		 FROM storage_targets ORDER BY name`,
	)
	if err != nil {
		return nil, fmt.Errorf("list storage targets: %w", err)
	}
	defer rows.Close()

	var out []model.StorageTarget
	for rows.Next() {
		t, err := scanStorageTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Repositories
// ---------------------------------------------------------------------------

func (s *sqliteStore) CreateRepository(ctx context.Context, r *model.Repository) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO repositories (id, agent_id, storage_target_id, repository_path,
		   encrypted_password, status, last_check_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.AgentID, r.StorageTargetID, r.RepositoryPath, r.EncryptedPassword,
		r.Status, nullTime(r.LastCheckAt),
		r.CreatedAt.Format(time.RFC3339), r.UpdatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("create repository: %w", err)
	}
	return nil
}

func (s *sqliteStore) GetRepository(ctx context.Context, id string) (*model.Repository, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, agent_id, storage_target_id, repository_path,
		        encrypted_password, status, last_check_at, created_at, updated_at
		 FROM repositories WHERE id = ?`, id,
	)
	return scanRepository(row)
}

func (s *sqliteStore) GetRepositoryByAgentAndTarget(ctx context.Context, agentID, targetID string) (*model.Repository, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, agent_id, storage_target_id, repository_path,
		        encrypted_password, status, last_check_at, created_at, updated_at
		 FROM repositories WHERE agent_id = ? AND storage_target_id = ?`,
		agentID, targetID,
	)
	return scanRepository(row)
}

func (s *sqliteStore) ListRepositories(ctx context.Context) ([]model.Repository, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, agent_id, storage_target_id, repository_path,
		        encrypted_password, status, last_check_at, created_at, updated_at
		 FROM repositories ORDER BY repository_path`,
	)
	if err != nil {
		return nil, fmt.Errorf("list repositories: %w", err)
	}
	defer rows.Close()

	var out []model.Repository
	for rows.Next() {
		r, err := scanRepository(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *sqliteStore) ListRepositoriesNeedingCheck(ctx context.Context, olderThan time.Time) ([]model.Repository, error) {
	ot := olderThan.Format(time.RFC3339)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, agent_id, storage_target_id, repository_path,
		        encrypted_password, status, last_check_at, created_at, updated_at
		 FROM repositories
		 WHERE status = 'ready' AND (last_check_at IS NULL OR last_check_at < ?)
		 ORDER BY COALESCE(last_check_at, '1970-01-01T00:00:00Z') ASC`,
		ot,
	)
	if err != nil {
		return nil, fmt.Errorf("list repos needing check: %w", err)
	}
	defer rows.Close()

	var out []model.Repository
	for rows.Next() {
		r, err := scanRepository(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *sqliteStore) UpdateRepositoryStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE repositories SET status = ?, updated_at = ? WHERE id = ?",
		status, nowUTC().Format(time.RFC3339), id,
	)
	if err != nil {
		return fmt.Errorf("update repository status: %w", err)
	}
	return nil
}

func (s *sqliteStore) MarkRepositoryChecked(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE repositories SET last_check_at = ?, updated_at = ? WHERE id = ?",
		at.Format(time.RFC3339), at.Format(time.RFC3339), id,
	)
	if err != nil {
		return fmt.Errorf("mark repository checked: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Plans
// ---------------------------------------------------------------------------

func (s *sqliteStore) CreatePlan(ctx context.Context, p *model.Plan) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO backup_plans (id, name, agent_id, kind, schedule, timezone, enabled,
		   source_json, repository_id, retention_json, timeout_seconds, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.AgentID, p.Kind, p.Schedule, p.Timezone,
		boolInt(p.Enabled), p.SourceJSON, p.RepositoryID, p.RetentionJSON,
		p.TimeoutSeconds,
		p.CreatedAt.Format(time.RFC3339), p.UpdatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("create plan: %w", err)
	}
	return nil
}

func (s *sqliteStore) UpdatePlan(ctx context.Context, p *model.Plan) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE backup_plans SET name=?, agent_id=?, kind=?, schedule=?, timezone=?,
		   enabled=?, source_json=?, repository_id=?, retention_json=?,
		   timeout_seconds=?, updated_at=?
		 WHERE id=?`,
		p.Name, p.AgentID, p.Kind, p.Schedule, p.Timezone,
		boolInt(p.Enabled), p.SourceJSON, p.RepositoryID, p.RetentionJSON,
		p.TimeoutSeconds, p.UpdatedAt.Format(time.RFC3339), p.ID,
	)
	if err != nil {
		return fmt.Errorf("update plan: %w", err)
	}
	return nil
}

func (s *sqliteStore) DeletePlan(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check for runs referencing this plan.
	var count int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM runs WHERE plan_id = ?", id,
	).Scan(&count); err != nil {
		return fmt.Errorf("delete plan check runs: %w", err)
	}
	if count > 0 {
		return ErrInUse
	}

	_, err := s.db.ExecContext(ctx, "DELETE FROM backup_plans WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete plan: %w", err)
	}
	return nil
}

func (s *sqliteStore) GetPlan(ctx context.Context, id string) (*model.Plan, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, agent_id, kind, schedule, timezone, enabled,
		        source_json, repository_id, retention_json, timeout_seconds,
		        created_at, updated_at
		 FROM backup_plans WHERE id = ?`, id,
	)
	return scanPlan(row)
}

func (s *sqliteStore) ListPlans(ctx context.Context, agentID string) ([]model.Plan, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if agentID == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, name, agent_id, kind, schedule, timezone, enabled,
			        source_json, repository_id, retention_json, timeout_seconds,
			        created_at, updated_at
			 FROM backup_plans ORDER BY name`,
		)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, name, agent_id, kind, schedule, timezone, enabled,
			        source_json, repository_id, retention_json, timeout_seconds,
			        created_at, updated_at
			 FROM backup_plans WHERE agent_id = ? ORDER BY name`,
			agentID,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("list plans: %w", err)
	}
	defer rows.Close()

	var out []model.Plan
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *sqliteStore) ListEnabledPlans(ctx context.Context) ([]model.Plan, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, agent_id, kind, schedule, timezone, enabled,
		        source_json, repository_id, retention_json, timeout_seconds,
		        created_at, updated_at
		 FROM backup_plans WHERE enabled = 1 ORDER BY name`,
	)
	if err != nil {
		return nil, fmt.Errorf("list enabled plans: %w", err)
	}
	defer rows.Close()

	var out []model.Plan
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Runs
// ---------------------------------------------------------------------------

func (s *sqliteStore) CreateRun(ctx context.Context, r *model.Run) error {
	planID := nullString(r.PlanID)
	repoID := nullString(r.RepositoryID)

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO runs (id, plan_id, agent_id, operation, status,
		   queued_at, started_at, finished_at, progress_json, snapshot_id,
		   error_code, error_message, repository_id, scheduled_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, planID, r.AgentID, r.Operation, r.Status,
		r.QueuedAt.Format(time.RFC3339),
		nullTime(r.StartedAt),
		nullTime(r.FinishedAt),
		r.ProgressJSON, r.SnapshotID,
		r.ErrorCode, r.ErrorMessage,
		repoID,
		nullTime(r.ScheduledAt),
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return ErrDuplicateRun
		}
		return fmt.Errorf("create run: %w", err)
	}
	return nil
}

func (s *sqliteStore) GetRun(ctx context.Context, id string) (*model.Run, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, plan_id, agent_id, operation, status,
		        queued_at, started_at, finished_at, progress_json, snapshot_id,
		        error_code, error_message, repository_id, scheduled_at
		 FROM runs WHERE id = ?`, id,
	)
	return scanRun(row)
}

func (s *sqliteStore) ListRuns(ctx context.Context, f RunFilter) ([]model.Run, error) {
	where := []string{"1=1"}
	args := []any{}

	if f.AgentID != "" {
		where = append(where, "agent_id = ?")
		args = append(args, f.AgentID)
	}
	if f.PlanID != "" {
		where = append(where, "plan_id = ?")
		args = append(args, f.PlanID)
	}
	if f.Operation != "" {
		where = append(where, "operation = ?")
		args = append(args, f.Operation)
	}
	if len(f.Statuses) > 0 {
		placeholders := make([]string, len(f.Statuses))
		for i, st := range f.Statuses {
			placeholders[i] = "?"
			args = append(args, st)
		}
		where = append(where, fmt.Sprintf("status IN (%s)", strings.Join(placeholders, ",")))
	}

	if f.Limit <= 0 {
		f.Limit = 100
	}

	query := fmt.Sprintf(
		`SELECT id, plan_id, agent_id, operation, status,
		        queued_at, started_at, finished_at, progress_json, snapshot_id,
		        error_code, error_message, repository_id, scheduled_at
		 FROM runs WHERE %s ORDER BY queued_at DESC LIMIT ? OFFSET ?`,
		strings.Join(where, " AND "),
	)
	args = append(args, f.Limit, f.Offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()

	var out []model.Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// Early failure exits are legal: the dispatcher fails permanently
// un-buildable jobs straight from queued, fast-finished runs may report a
// terminal state while still dispatched, and both watchdogs force-fail from
// dispatched/running. Terminal states remain final.
var validTransitions = map[string]map[string]bool{
	model.RunQueued:     {model.RunDispatched: true, model.RunFailed: true},
	model.RunDispatched: {model.RunRunning: true, model.RunSucceeded: true, model.RunFailed: true, model.RunCancelled: true},
	model.RunRunning:    {model.RunSucceeded: true, model.RunFailed: true, model.RunCancelled: true},
}

func isTerminal(status string) bool {
	return status == model.RunSucceeded || status == model.RunFailed || status == model.RunCancelled
}

func (s *sqliteStore) TransitionRun(ctx context.Context, id, from, to string, mutate func(*model.Run)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("transition run begin tx: %w", err)
	}
	defer tx.Rollback()

	// Read current run with lock.
	row := tx.QueryRowContext(ctx,
		`SELECT id, plan_id, agent_id, operation, status,
		        queued_at, started_at, finished_at, progress_json, snapshot_id,
		        error_code, error_message, repository_id, scheduled_at
		 FROM runs WHERE id = ?`, id,
	)
	run, err := scanRun(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("transition run scan: %w", err)
	}

	if run.Status != from {
		return ErrInvalidTransition
	}
	if isTerminal(run.Status) {
		return ErrInvalidTransition
	}
	if !validTransitions[from][to] {
		return ErrInvalidTransition
	}

	// Apply mutate.
	run.Status = to
	if mutate != nil {
		mutate(run)
	}

	// Write back.
	_, err = tx.ExecContext(ctx,
		`UPDATE runs SET status=?, started_at=?, finished_at=?, progress_json=?,
		   snapshot_id=?, error_code=?, error_message=?
		 WHERE id=?`,
		run.Status,
		nullTime(run.StartedAt),
		nullTime(run.FinishedAt),
		run.ProgressJSON, run.SnapshotID,
		run.ErrorCode, run.ErrorMessage,
		run.ID,
	)
	if err != nil {
		return fmt.Errorf("transition run update: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("transition run commit: %w", err)
	}
	return nil
}

func (s *sqliteStore) ListRunsByStatus(ctx context.Context, statuses []string) ([]model.Run, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(statuses))
	args := make([]any, len(statuses))
	for i, st := range statuses {
		placeholders[i] = "?"
		args[i] = st
	}

	query := fmt.Sprintf(
		`SELECT id, plan_id, agent_id, operation, status,
		        queued_at, started_at, finished_at, progress_json, snapshot_id,
		        error_code, error_message, repository_id, scheduled_at
		 FROM runs WHERE status IN (%s) ORDER BY queued_at DESC`,
		strings.Join(placeholders, ","),
	)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list runs by status: %w", err)
	}
	defer rows.Close()

	var out []model.Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// FailStaleRuns force-fails runs stuck in the given non-terminal statuses
// and returns the IDs actually moved to failed, enabling per-run failure
// notifications without a read-then-write race.
func (s *sqliteStore) FailStaleRuns(ctx context.Context, statuses []string, errorCode string, at time.Time) ([]string, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	placeholders := make([]string, 0, len(statuses))
	for range statuses {
		placeholders = append(placeholders, "?")
	}

	query := fmt.Sprintf(
		`UPDATE runs SET status='failed', error_code=?, finished_at=?
		 WHERE status IN (%s) RETURNING id`,
		strings.Join(placeholders, ","),
	)

	args := make([]any, 0, len(statuses)+2)
	args = append(args, errorCode, at.Format(time.RFC3339))
	for _, st := range statuses {
		args = append(args, st)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("fail stale runs: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("fail stale runs: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ---------------------------------------------------------------------------
// Run logs
// ---------------------------------------------------------------------------

func (s *sqliteStore) AppendRunLogs(ctx context.Context, logs []model.RunLog) error {
	if len(logs) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("append run logs begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO run_logs (run_id, seq, timestamp, level, message) VALUES (?, ?, ?, ?, ?)`,
	)
	if err != nil {
		return fmt.Errorf("append run logs prepare: %w", err)
	}
	defer stmt.Close()

	for _, l := range logs {
		if _, err := stmt.ExecContext(ctx, l.RunID, l.Seq, l.Timestamp.Format(time.RFC3339), l.Level, l.Message); err != nil {
			return fmt.Errorf("append run log: %w", err)
		}
	}

	return tx.Commit()
}

func (s *sqliteStore) ListRunLogs(ctx context.Context, runID string, beforeSeq uint64, limit int) ([]model.RunLog, error) {
	if limit <= 0 {
		limit = 100
	}

	var (
		rows *sql.Rows
		err  error
	)
	if beforeSeq > 0 {
		rows, err = s.db.QueryContext(ctx,
			`SELECT run_id, seq, timestamp, level, message
			 FROM run_logs WHERE run_id = ? AND seq < ?
			 ORDER BY seq DESC LIMIT ?`,
			runID, beforeSeq, limit,
		)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT run_id, seq, timestamp, level, message
			 FROM run_logs WHERE run_id = ?
			 ORDER BY seq DESC LIMIT ?`,
			runID, limit,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("list run logs: %w", err)
	}
	defer rows.Close()

	var out []model.RunLog
	for rows.Next() {
		l, err := scanRunLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

func (s *sqliteStore) MaxRunLogSeq(ctx context.Context, runID string) (uint64, error) {
	var seq sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		"SELECT MAX(seq) FROM run_logs WHERE run_id = ?", runID,
	).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("max run log seq: %w", err)
	}
	if seq.Valid {
		return uint64(seq.Int64), nil
	}
	return 0, nil
}

// ---------------------------------------------------------------------------
// Restore requests
// ---------------------------------------------------------------------------

func (s *sqliteStore) CreateRestoreRequest(ctx context.Context, rr *model.RestoreRequest) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO restore_requests (id, run_id, snapshot_id, restore_kind,
		   target_json, overwrite, confirmation_hash, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		rr.ID, rr.RunID, rr.SnapshotID, rr.RestoreKind,
		rr.TargetJSON, boolInt(rr.Overwrite), rr.ConfirmationHash,
		rr.CreatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("create restore request: %w", err)
	}
	return nil
}

func (s *sqliteStore) GetRestoreRequest(ctx context.Context, id string) (*model.RestoreRequest, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, run_id, snapshot_id, restore_kind,
		        target_json, overwrite, confirmation_hash, created_at
		 FROM restore_requests WHERE id = ?`, id,
	)
	return scanRestoreRequest(row)
}

func (s *sqliteStore) ListRestoreRequests(ctx context.Context, limit int) ([]model.RestoreRequest, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, run_id, snapshot_id, restore_kind,
		        target_json, overwrite, confirmation_hash, created_at
		 FROM restore_requests ORDER BY created_at DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list restore requests: %w", err)
	}
	defer rows.Close()

	var out []model.RestoreRequest
	for rows.Next() {
		rr, err := scanRestoreRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rr)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

func (s *sqliteStore) AppendAuditEvent(ctx context.Context, e *model.AuditEvent) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_events (id, occurred_at, actor_type, actor_id, action, resource_type, resource_id, detail_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.OccurredAt.Format(time.RFC3339),
		e.ActorType, e.ActorID, e.Action,
		e.ResourceType, e.ResourceID, e.DetailJSON,
	)
	if err != nil {
		return fmt.Errorf("append audit event: %w", err)
	}
	return nil
}

func (s *sqliteStore) ListAuditEvents(ctx context.Context, limit int) ([]model.AuditEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, occurred_at, actor_type, actor_id, action, resource_type, resource_id, detail_json
		 FROM audit_events ORDER BY occurred_at DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()

	var out []model.AuditEvent
	for rows.Next() {
		e, err := scanAuditEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Scan helpers
// ---------------------------------------------------------------------------

func scanAdmin(row interface{ Scan(dest ...any) error }) (*model.Admin, error) {
	var (
		id, username, passwordHash, createdAt string
		lastLoginAt                           sql.NullString
	)
	if err := row.Scan(&id, &username, &passwordHash, &createdAt, &lastLoginAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan admin: %w", err)
	}
	return &model.Admin{
		ID:           id,
		Username:     username,
		PasswordHash: passwordHash,
		CreatedAt:    parseTime(createdAt),
		LastLoginAt:  parseTimePtr(lastLoginAt),
	}, nil
}

func scanSession(row interface{ Scan(dest ...any) error }) (*model.Session, error) {
	var idHash, adminID, expiresAt, createdAt, lastSeenAt string
	if err := row.Scan(&idHash, &adminID, &expiresAt, &createdAt, &lastSeenAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan session: %w", err)
	}
	return &model.Session{
		IDHash:     idHash,
		AdminID:    adminID,
		ExpiresAt:  parseTime(expiresAt),
		CreatedAt:  parseTime(createdAt),
		LastSeenAt: parseTime(lastSeenAt),
	}, nil
}

func scanEnrollmentToken(row interface{ Scan(dest ...any) error }) (*model.EnrollmentToken, error) {
	var id, tokenHash, expiresAt string
	var usedAt sql.NullString
	if err := row.Scan(&id, &tokenHash, &expiresAt, &usedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan enrollment token: %w", err)
	}
	return &model.EnrollmentToken{
		ID:        id,
		TokenHash: tokenHash,
		ExpiresAt: parseTime(expiresAt),
		UsedAt:    parseTimePtr(usedAt),
	}, nil
}

func scanAgent(row interface{ Scan(dest ...any) error }) (*model.Agent, error) {
	var (
		id, name, hostname, os, arch, version, status string
		enrolledAt, tokenHash, capsJSON               string
		revoked                                       int
		lastSeenNull                                  sql.NullString
	)
	if err := row.Scan(&id, &name, &hostname, &os, &arch, &version, &status, &lastSeenNull, &enrolledAt, &tokenHash, &capsJSON, &revoked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan agent: %w", err)
	}

	var tools []model.ToolInfo
	if capsJSON != "" && capsJSON != "[]" {
		_ = json.Unmarshal([]byte(capsJSON), &tools)
	}
	if tools == nil {
		tools = []model.ToolInfo{}
	}

	return &model.Agent{
		ID:               id,
		Name:             name,
		Hostname:         hostname,
		OS:               os,
		Arch:             arch,
		Version:          version,
		Status:           model.AgentStatus(status),
		LastSeenAt:       parseTimePtr(lastSeenNull),
		EnrolledAt:       parseTime(enrolledAt),
		TokenHash:        tokenHash,
		Capabilities:     tools,
		CapabilitiesJSON: capsJSON,
		Revoked:          revoked != 0,
	}, nil
}

func scanStorageTarget(row interface{ Scan(dest ...any) error }) (*model.StorageTarget, error) {
	var (
		id, name, typ, remoteName, remotePath string
		encryptedConfig                       []byte
		createdAt, updatedAt                  string
	)
	if err := row.Scan(&id, &name, &typ, &remoteName, &remotePath, &encryptedConfig, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan storage target: %w", err)
	}
	return &model.StorageTarget{
		ID:              id,
		Name:            name,
		Type:            typ,
		RemoteName:      remoteName,
		RemotePath:      remotePath,
		EncryptedConfig: encryptedConfig,
		CreatedAt:       parseTime(createdAt),
		UpdatedAt:       parseTime(updatedAt),
	}, nil
}

func scanRepository(row interface{ Scan(dest ...any) error }) (*model.Repository, error) {
	var (
		id, agentID, targetID, repoPath string
		encPass                         []byte
		status                          string
		lastCheckAt                     sql.NullString
		createdAt, updatedAt            string
	)
	if err := row.Scan(&id, &agentID, &targetID, &repoPath, &encPass, &status, &lastCheckAt, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan repository: %w", err)
	}
	return &model.Repository{
		ID:                id,
		AgentID:           agentID,
		StorageTargetID:   targetID,
		RepositoryPath:    repoPath,
		EncryptedPassword: encPass,
		Status:            status,
		LastCheckAt:       parseTimePtr(lastCheckAt),
		CreatedAt:         parseTime(createdAt),
		UpdatedAt:         parseTime(updatedAt),
	}, nil
}

func scanPlan(row interface{ Scan(dest ...any) error }) (*model.Plan, error) {
	var (
		id, name, agentID, kind, schedule, timezone string
		enabled                                     int
		sourceJSON, repoID, retentionJSON           string
		timeoutSec                                  int
		createdAt, updatedAt                        string
	)
	if err := row.Scan(&id, &name, &agentID, &kind, &schedule, &timezone, &enabled, &sourceJSON, &repoID, &retentionJSON, &timeoutSec, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan plan: %w", err)
	}

	var src model.PlanSource
	_ = json.Unmarshal([]byte(sourceJSON), &src)

	var ret model.Retention
	_ = json.Unmarshal([]byte(retentionJSON), &ret)

	return &model.Plan{
		ID:             id,
		Name:           name,
		AgentID:        agentID,
		Kind:           kind,
		Schedule:       schedule,
		Timezone:       timezone,
		Enabled:        enabled != 0,
		Source:         src,
		SourceJSON:     sourceJSON,
		RepositoryID:   repoID,
		Retention:      ret,
		RetentionJSON:  retentionJSON,
		TimeoutSeconds: timeoutSec,
		CreatedAt:      parseTime(createdAt),
		UpdatedAt:      parseTime(updatedAt),
	}, nil
}

func scanRun(row interface{ Scan(dest ...any) error }) (*model.Run, error) {
	var (
		id, agentID, operation, status string
		queuedAt                       string
		startedAt, finishedAt          sql.NullString
		progressJSON, snapshotID       string
		errorCode, errorMessage        sql.NullString
		planID, repositoryID           sql.NullString
		scheduledAt                    sql.NullString
	)
	if err := row.Scan(&id, &planID, &agentID, &operation, &status, &queuedAt, &startedAt, &finishedAt, &progressJSON, &snapshotID, &errorCode, &errorMessage, &repositoryID, &scheduledAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan run: %w", err)
	}

	var prog model.Progress
	_ = json.Unmarshal([]byte(progressJSON), &prog)

	return &model.Run{
		ID:           id,
		PlanID:       planID.String,
		AgentID:      agentID,
		Operation:    operation,
		Status:       status,
		QueuedAt:     parseTime(queuedAt),
		StartedAt:    parseTimePtr(startedAt),
		FinishedAt:   parseTimePtr(finishedAt),
		Progress:     prog,
		ProgressJSON: progressJSON,
		SnapshotID:   snapshotID,
		ErrorCode:    errorCode.String,
		ErrorMessage: errorMessage.String,
		RepositoryID: repositoryID.String,
		ScheduledAt:  parseTimePtr(scheduledAt),
	}, nil
}

func scanRunLog(row interface{ Scan(dest ...any) error }) (*model.RunLog, error) {
	var runID, timestamp, level, message string
	var seq int64
	if err := row.Scan(&runID, &seq, &timestamp, &level, &message); err != nil {
		return nil, fmt.Errorf("scan run log: %w", err)
	}
	return &model.RunLog{
		RunID:     runID,
		Seq:       uint64(seq),
		Timestamp: parseTime(timestamp),
		Level:     level,
		Message:   message,
	}, nil
}

func scanRestoreRequest(row interface{ Scan(dest ...any) error }) (*model.RestoreRequest, error) {
	var (
		id, runID, snapshotID, restoreKind string
		targetJSON                         string
		overwrite                          int
		confirmationHash                   sql.NullString
		createdAt                          string
	)
	if err := row.Scan(&id, &runID, &snapshotID, &restoreKind, &targetJSON, &overwrite, &confirmationHash, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan restore request: %w", err)
	}

	var target model.RestoreTarget
	_ = json.Unmarshal([]byte(targetJSON), &target)

	return &model.RestoreRequest{
		ID:               id,
		RunID:            runID,
		SnapshotID:       snapshotID,
		RestoreKind:      restoreKind,
		Target:           target,
		TargetJSON:       targetJSON,
		Overwrite:        overwrite != 0,
		ConfirmationHash: confirmationHash.String,
		CreatedAt:        parseTime(createdAt),
	}, nil
}

func scanAuditEvent(row interface{ Scan(dest ...any) error }) (*model.AuditEvent, error) {
	var id, occurredAt, actorType, actorID, action, resourceType, resourceID, detailJSON string
	if err := row.Scan(&id, &occurredAt, &actorType, &actorID, &action, &resourceType, &resourceID, &detailJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan audit event: %w", err)
	}
	return &model.AuditEvent{
		ID:           id,
		OccurredAt:   parseTime(occurredAt),
		ActorType:    actorType,
		ActorID:      actorID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		DetailJSON:   detailJSON,
	}, nil
}

// ---------------------------------------------------------------------------
// Utility helpers
// ---------------------------------------------------------------------------

func nowUTC() time.Time {
	return time.Now().UTC()
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func parseTimePtr(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, ns.String)
	if err != nil {
		return nil
	}
	return &t
}

func nullTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(time.RFC3339)
	return &s
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func isUniqueConstraint(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
