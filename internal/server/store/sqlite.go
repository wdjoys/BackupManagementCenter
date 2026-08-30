package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/base64"
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

	"backupmanagementcenter/internal/secrets"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type sqliteStore struct {
	db   *sql.DB
	mu   sync.Mutex
	seal secrets.Sealer
}

// BackupSQLite creates a consistent point-in-time copy using SQLite's online
// backup primitive. It is used before migrations or secret-format upgrades so
// an operator always has a recoverable copy of the control database.
func BackupSQLite(ctx context.Context, dbPath, backupPath string) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", backupPath); err != nil {
		return fmt.Errorf("sqlite online backup: %w", err)
	}
	return nil
}

// New opens or creates a SQLite database at path with a no-op sealer (dev).
func New(path string) (Store, error) {
	return NewWithSealer(path, secrets.NewNoopSealer())
}

// NewWithSealer opens or creates a SQLite database at path. The sealer
// encrypts secret columns (currently the Telegram bot token).
func NewWithSealer(path string, seal secrets.Sealer) (Store, error) {
	if seal == nil {
		return nil, fmt.Errorf("nil sealer")
	}

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

	s := &sqliteStore{db: db, seal: seal}
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

func (s *sqliteStore) SaveAgentCapabilities(ctx context.Context, agentID string, tools []model.ToolInfo, mappings []model.PathMapping, at time.Time) error {
	data, err := json.Marshal(struct {
		Tools              []model.ToolInfo    `json:"tools"`
		SourcePathMappings []model.PathMapping `json:"source_path_mappings"`
	}{Tools: tools, SourcePathMappings: mappings})
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
// Telegram settings (single row, web-configured)
// ---------------------------------------------------------------------------

func (s *sqliteStore) GetTelegramSettings(ctx context.Context) (*model.TelegramSettings, error) {
	var (
		encToken []byte
		chatID   string
		updated  string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT encrypted_token, chat_id, updated_at FROM telegram_settings WHERE id = 1`,
	).Scan(&encToken, &chatID, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get telegram settings: %w", err)
	}
	token, err := s.seal.Open(model.TelegramSettingsTable, model.TelegramSettingsRow, model.TelegramTokenColumn, encToken)
	if err != nil {
		// Installations created before the master-key boundary used the
		// development NoopSealer. Migrate that legacy payload on first read so
		// it is immediately rewritten with the configured AES-GCM sealer.
		if legacy, ok := decodeLegacyNoop(encToken); ok {
			migrated := &model.TelegramSettings{BotToken: legacy, ChatID: chatID, UpdatedAt: parseTime(updated)}
			if saveErr := s.SaveTelegramSettings(ctx, migrated); saveErr != nil {
				return nil, fmt.Errorf("migrate telegram token: %w", saveErr)
			}
			return migrated, nil
		}
		return nil, fmt.Errorf("unseal telegram token: %w", err)
	}
	return &model.TelegramSettings{BotToken: token, ChatID: chatID, UpdatedAt: parseTime(updated)}, nil
}

func decodeLegacyNoop(data []byte) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	raw, err := base64.StdEncoding.DecodeString(string(data))
	const prefix = "noop:"
	if err != nil || len(raw) < len(prefix) || string(raw[:len(prefix)]) != prefix {
		return "", false
	}
	return string(raw[len(prefix):]), true
}

func (s *sqliteStore) SaveTelegramSettings(ctx context.Context, ts *model.TelegramSettings) error {
	encToken, err := s.seal.Seal(model.TelegramSettingsTable, model.TelegramSettingsRow, model.TelegramTokenColumn, ts.BotToken)
	if err != nil {
		return fmt.Errorf("seal telegram token: %w", err)
	}
	now := nowUTC()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO telegram_settings (id, encrypted_token, chat_id, updated_at)
		 VALUES (1, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET encrypted_token = excluded.encrypted_token,
		   chat_id = excluded.chat_id, updated_at = excluded.updated_at`,
		encToken, ts.ChatID, now.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("save telegram settings: %w", err)
	}
	ts.UpdatedAt = now
	return nil
}

func (s *sqliteStore) DeleteTelegramSettings(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM telegram_settings WHERE id = 1`)
	if err != nil {
		return fmt.Errorf("delete telegram settings: %w", err)
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
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("update storage target begin tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`UPDATE storage_targets SET name=?, remote_name=?, remote_path=?,
		   encrypted_config=?, updated_at=? WHERE id=?`,
		t.Name, t.RemoteName, t.RemotePath, t.EncryptedConfig,
		t.UpdatedAt.Format(time.RFC3339), t.ID,
	); err != nil {
		return fmt.Errorf("update storage target: %w", err)
	}
	if err := invalidateTargetCaches(ctx, tx, t.ID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("update storage target commit: %w", err)
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
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("create repository begin tx: %w", err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx,
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
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO repository_cache_state (repository_id, generation, updated_at)
		 VALUES (?, 0, ?)`, r.ID, nowUTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("create repository cache state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("create repository commit: %w", err)
	}
	return nil
}

func (s *sqliteStore) GetRepository(ctx context.Context, id string) (*model.Repository, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, agent_id, storage_target_id, repository_path,
		        encrypted_password, status, last_check_at, detached_at, created_at, updated_at
		 FROM repositories WHERE id = ?`, id,
	)
	return scanRepository(row)
}

func (s *sqliteStore) GetRepositoryByAgentAndTarget(ctx context.Context, agentID, targetID string) (*model.Repository, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, agent_id, storage_target_id, repository_path,
		        encrypted_password, status, last_check_at, detached_at, created_at, updated_at
		 FROM repositories WHERE agent_id = ? AND storage_target_id = ?`,
		agentID, targetID,
	)
	return scanRepository(row)
}

func (s *sqliteStore) ListRepositories(ctx context.Context) ([]model.Repository, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, agent_id, storage_target_id, repository_path,
		        encrypted_password, status, last_check_at, detached_at, created_at, updated_at
		 FROM repositories WHERE detached_at IS NULL ORDER BY repository_path`,
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
		        encrypted_password, status, last_check_at, detached_at, created_at, updated_at
		 FROM repositories
		 WHERE detached_at IS NULL AND status = 'ready' AND (last_check_at IS NULL OR last_check_at < ?)
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

// DetachRepository removes the binding from normal server listings while
// retaining its encrypted password. Restic data in the remote remains
// untouched and can be adopted again by a later bind.
func (s *sqliteStore) DetachRepository(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("detach repository begin tx: %w", err)
	}
	defer tx.Rollback()
	at := nowUTC().Format(time.RFC3339)
	result, err := tx.ExecContext(ctx,
		"UPDATE repositories SET detached_at = ?, updated_at = ? WHERE id = ?",
		at, at, id)
	if err != nil {
		return fmt.Errorf("detach repository: %w", err)
	}
	if n, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("detach repository rows affected: %w", err)
	} else if n == 0 {
		return ErrNotFound
	}
	if err := invalidateSnapshotCacheTx(ctx, tx, id, true); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("detach repository commit: %w", err)
	}
	return nil
}

func (s *sqliteStore) UpdateRepositoryStatus(ctx context.Context, id, status string) error {
	var (
		err error
		at  = nowUTC().Format(time.RFC3339)
	)
	if status == "ready" {
		_, err = s.db.ExecContext(ctx,
			"UPDATE repositories SET status = ?, detached_at = NULL, updated_at = ? WHERE id = ?",
			status, at, id,
		)
	} else {
		_, err = s.db.ExecContext(ctx,
			"UPDATE repositories SET status = ?, updated_at = ? WHERE id = ?",
			status, at, id,
		)
	}
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

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete plan begin tx: %w", err)
	}
	defer tx.Rollback()

	// 活跃运行和已有快照都依赖计划配置，删除前必须先处理完毕；历史运行则可解绑计划后保留。
	var count int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runs
     WHERE plan_id = ? AND status IN (?, ?, ?)`,
		id, model.RunQueued, model.RunDispatched, model.RunRunning,
	).Scan(&count); err != nil {
		return fmt.Errorf("delete plan check active runs: %w", err)
	}
	if count > 0 {
		return ErrInUse
	}

	if _, err := tx.ExecContext(ctx, "UPDATE runs SET plan_id = NULL WHERE plan_id = ?", id); err != nil {
		return fmt.Errorf("delete plan detach runs: %w", err)
	}

	result, err := tx.ExecContext(ctx, "DELETE FROM backup_plans WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete plan: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("delete plan rows affected: %w", err)
	} else if affected == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete plan commit: %w", err)
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

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("create run begin tx: %w", err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx,
		`INSERT INTO runs (id, plan_id, agent_id, operation, status,
		   queued_at, started_at, finished_at, progress_json, snapshot_id,
		   error_code, error_message, repository_id, scheduled_at, attempt, lease_expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, planID, r.AgentID, r.Operation, r.Status,
		r.QueuedAt.Format(time.RFC3339),
		nullTime(r.StartedAt),
		nullTime(r.FinishedAt),
		r.ProgressJSON, r.SnapshotID,
		r.ErrorCode, r.ErrorMessage,
		repoID,
		nullTime(r.ScheduledAt),
		r.Attempt,
		nullTime(r.LeaseExpiresAt),
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return ErrDuplicateRun
		}
		return fmt.Errorf("create run: %w", err)
	}
	if r.RepositoryID != "" && invalidatesSnapshotCache(r.Operation) {
		clearTrees := r.Operation == model.OpForget
		if err := invalidateSnapshotCacheTx(ctx, tx, r.RepositoryID, clearTrees); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("create run commit: %w", err)
	}
	return nil
}

func (s *sqliteStore) GetRun(ctx context.Context, id string) (*model.Run, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, plan_id, agent_id, operation, status,
		        queued_at, started_at, finished_at, progress_json, snapshot_id,
		        error_code, error_message, repository_id, scheduled_at,
		        attempt, lease_expires_at
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
	if f.RepositoryID != "" {
		where = append(where, "repository_id = ?")
		args = append(args, f.RepositoryID)
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
		        error_code, error_message, repository_id, scheduled_at,
		        attempt, lease_expires_at
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
	model.RunDispatched: {model.RunQueued: true, model.RunRunning: true, model.RunSucceeded: true, model.RunFailed: true, model.RunCancelled: true},
	model.RunRunning:    {model.RunQueued: true, model.RunSucceeded: true, model.RunFailed: true, model.RunCancelled: true},
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
		        error_code, error_message, repository_id, scheduled_at,
		        attempt, lease_expires_at
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
		   snapshot_id=?, error_code=?, error_message=?, attempt=?, lease_expires_at=?
		 WHERE id=?`,
		run.Status,
		nullTime(run.StartedAt),
		nullTime(run.FinishedAt),
		run.ProgressJSON, run.SnapshotID,
		run.ErrorCode, run.ErrorMessage, run.Attempt, nullTime(run.LeaseExpiresAt),
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
		        error_code, error_message, repository_id, scheduled_at,
		        attempt, lease_expires_at
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

// UpdateRunProgress persists a progress snapshot without changing the run
// state. Progress messages are high frequency and must not go through the
// terminal-state transition function.
func (s *sqliteStore) UpdateRunProgress(ctx context.Context, id, progressJSON string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE runs SET progress_json = ?
		 WHERE id = ? AND status IN ('queued','dispatched','running')`,
		progressJSON, id)
	if err != nil {
		return fmt.Errorf("update run progress: %w", err)
	}
	return nil
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
		`UPDATE runs SET status='failed', error_code=?, finished_at=?, lease_expires_at=NULL
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
// 进程日志
// ---------------------------------------------------------------------------

const processLogRetention = 20000

func (s *sqliteStore) AppendServerLogs(ctx context.Context, logs []model.SystemLog) error {
	if len(logs) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("append server logs begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO server_logs (timestamp, type, level, message) VALUES (?, ?, ?, ?)`,
	)
	if err != nil {
		return fmt.Errorf("append server logs prepare: %w", err)
	}
	defer stmt.Close()

	for _, entry := range logs {
		logType := entry.Type
		if logType == "" {
			logType = "system"
		}
		if _, err := stmt.ExecContext(ctx, entry.Timestamp.UTC().Format(time.RFC3339Nano), logType, entry.Level, entry.Message); err != nil {
			return fmt.Errorf("append server log: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM server_logs
		 WHERE id <= (SELECT COALESCE(MAX(id), 0) - ? FROM server_logs)`,
		processLogRetention,
	); err != nil {
		return fmt.Errorf("prune server logs: %w", err)
	}
	return tx.Commit()
}

func (s *sqliteStore) ListServerLogs(ctx context.Context, filter ProcessLogFilter) ([]model.SystemLog, error) {
	filter.Limit = normalizeProcessLogLimit(filter.Limit)
	where, args := processLogWhere(filter, "")
	query := `SELECT id, type, timestamp, level, message
		FROM server_logs WHERE ` + where + ` ORDER BY id DESC LIMIT ?`
	args = append(args, filter.Limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list server logs: %w", err)
	}
	defer rows.Close()

	out := make([]model.SystemLog, 0, filter.Limit)
	for rows.Next() {
		entry, err := scanSystemLog(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, *entry)
	}
	return out, rows.Err()
}

func (s *sqliteStore) AppendAgentLogs(ctx context.Context, agentID string, logs []model.SystemLog) error {
	if len(logs) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("append agent logs begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO agent_logs (agent_id, source_seq, timestamp, type, level, message)
		 VALUES (?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		return fmt.Errorf("append agent logs prepare: %w", err)
	}
	defer stmt.Close()

	for _, entry := range logs {
		logType := entry.Type
		if logType == "" {
			logType = "system"
		}
		if _, err := stmt.ExecContext(ctx, agentID, int64(entry.SourceSeq), entry.Timestamp.UTC().Format(time.RFC3339Nano), logType, entry.Level, entry.Message); err != nil {
			return fmt.Errorf("append agent log: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM agent_logs
		 WHERE agent_id = ?
		   AND id NOT IN (
		     SELECT id FROM agent_logs
		     WHERE agent_id = ?
		     ORDER BY id DESC LIMIT ?
		   )`,
		agentID, agentID, processLogRetention,
	); err != nil {
		return fmt.Errorf("prune agent logs: %w", err)
	}
	return tx.Commit()
}

func (s *sqliteStore) ListAgentLogs(ctx context.Context, agentID string, filter ProcessLogFilter) ([]model.SystemLog, error) {
	filter.Limit = normalizeProcessLogLimit(filter.Limit)
	where, args := processLogWhere(filter, agentID)
	query := `SELECT id, agent_id, source_seq, type, timestamp, level, message
		FROM agent_logs WHERE ` + where + ` ORDER BY id DESC LIMIT ?`
	args = append(args, filter.Limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list agent logs: %w", err)
	}
	defer rows.Close()

	out := make([]model.SystemLog, 0, filter.Limit)
	for rows.Next() {
		entry, err := scanSystemLog(rows, true)
		if err != nil {
			return nil, err
		}
		out = append(out, *entry)
	}
	return out, rows.Err()
}

func processLogWhere(filter ProcessLogFilter, agentID string) (string, []any) {
	conditions := []string{"1=1"}
	args := make([]any, 0, 1+len(filter.Levels)+len(filter.Types))
	if agentID != "" {
		conditions = append(conditions, "agent_id = ?")
		args = append(args, agentID)
	}
	if filter.BeforeID > 0 {
		conditions = append(conditions, "id < ?")
		args = append(args, filter.BeforeID)
	}
	appendProcessLogInFilter(&conditions, &args, "level", filter.Levels)
	appendProcessLogInFilter(&conditions, &args, "type", filter.Types)
	return strings.Join(conditions, " AND "), args
}

func appendProcessLogInFilter(conditions *[]string, args *[]any, column string, values []string) {
	if len(values) == 0 {
		return
	}
	placeholders := make([]string, len(values))
	for i, value := range values {
		placeholders[i] = "?"
		*args = append(*args, value)
	}
	*conditions = append(*conditions, column+" IN ("+strings.Join(placeholders, ",")+")")
}

func normalizeProcessLogLimit(limit int) int {
	if limit <= 0 || limit > 500 {
		return 200
	}
	return limit
}

// ---------------------------------------------------------------------------
// Restore requests
// ---------------------------------------------------------------------------

func (s *sqliteStore) CreateRestoreRequest(ctx context.Context, rr *model.RestoreRequest) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO restore_requests (id, run_id, snapshot_id, restore_kind,
		       target_json, overwrite, confirmation_hash, pre_restore_run_id,
		       rollback_snapshot_id, phase, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rr.ID, rr.RunID, rr.SnapshotID, rr.RestoreKind,
		rr.TargetJSON, boolInt(rr.Overwrite), rr.ConfirmationHash, rr.PreRestoreRunID,
		rr.RollbackSnapshotID, rr.Phase,
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
		        target_json, overwrite, confirmation_hash, pre_restore_run_id,
		        rollback_snapshot_id, phase, created_at
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
		        target_json, overwrite, confirmation_hash, pre_restore_run_id,
		        rollback_snapshot_id, phase, created_at
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

func (s *sqliteStore) UpdateRestorePhase(ctx context.Context, runID, phase string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE restore_requests SET phase=? WHERE run_id=?`, phase, runID)
	if err != nil {
		return fmt.Errorf("update restore phase: %w", err)
	}
	return nil
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
	var mappings []model.PathMapping
	if capsJSON != "" && capsJSON != "[]" {
		if strings.HasPrefix(strings.TrimSpace(capsJSON), "[") {
			_ = json.Unmarshal([]byte(capsJSON), &tools)
		} else {
			var caps struct {
				Tools              []model.ToolInfo    `json:"tools"`
				SourcePathMappings []model.PathMapping `json:"source_path_mappings"`
			}
			_ = json.Unmarshal([]byte(capsJSON), &caps)
			tools, mappings = caps.Tools, caps.SourcePathMappings
		}
	}
	if tools == nil {
		tools = []model.ToolInfo{}
	}
	if mappings == nil {
		mappings = []model.PathMapping{}
	}

	return &model.Agent{
		ID:                 id,
		Name:               name,
		Hostname:           hostname,
		OS:                 os,
		Arch:               arch,
		Version:            version,
		Status:             model.AgentStatus(status),
		LastSeenAt:         parseTimePtr(lastSeenNull),
		TokenHash:          tokenHash,
		Capabilities:       tools,
		SourcePathMappings: mappings,
		CapabilitiesJSON:   capsJSON,
		Revoked:            revoked != 0,
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
		lastCheckAt, detachedAt         sql.NullString
		createdAt, updatedAt            string
	)
	if err := row.Scan(&id, &agentID, &targetID, &repoPath, &encPass, &status, &lastCheckAt, &detachedAt, &createdAt, &updatedAt); err != nil {
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
		DetachedAt:        parseTimePtr(detachedAt),
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
		attempt                        int
		leaseExpiresAt                 sql.NullString
	)
	if err := row.Scan(&id, &planID, &agentID, &operation, &status, &queuedAt, &startedAt, &finishedAt, &progressJSON, &snapshotID, &errorCode, &errorMessage, &repositoryID, &scheduledAt, &attempt, &leaseExpiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan run: %w", err)
	}

	var prog model.Progress
	_ = json.Unmarshal([]byte(progressJSON), &prog)

	return &model.Run{
		ID:             id,
		PlanID:         planID.String,
		AgentID:        agentID,
		Operation:      operation,
		Status:         status,
		QueuedAt:       parseTime(queuedAt),
		StartedAt:      parseTimePtr(startedAt),
		FinishedAt:     parseTimePtr(finishedAt),
		Progress:       prog,
		ProgressJSON:   progressJSON,
		SnapshotID:     snapshotID,
		ErrorCode:      errorCode.String,
		ErrorMessage:   errorMessage.String,
		RepositoryID:   repositoryID.String,
		Attempt:        attempt,
		LeaseExpiresAt: parseTimePtr(leaseExpiresAt),
		ScheduledAt:    parseTimePtr(scheduledAt),
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
func scanSystemLog(row interface{ Scan(dest ...any) error }, withAgent bool) (*model.SystemLog, error) {
	var (
		id        int64
		agentID   string
		sourceSeq int64
		logType   string
		timestamp string
		level     string
		message   string
	)
	if withAgent {
		if err := row.Scan(&id, &agentID, &sourceSeq, &logType, &timestamp, &level, &message); err != nil {
			return nil, fmt.Errorf("scan agent log: %w", err)
		}
	} else {
		if err := row.Scan(&id, &logType, &timestamp, &level, &message); err != nil {
			return nil, fmt.Errorf("scan server log: %w", err)
		}
	}
	var seq uint64
	if sourceSeq > 0 {
		seq = uint64(sourceSeq)
	}
	return &model.SystemLog{
		ID:        id,
		AgentID:   agentID,
		SourceSeq: seq,
		Timestamp: parseTime(timestamp),
		Type:      logType,
		Level:     level,
		Message:   message,
	}, nil
}

func scanRestoreRequest(row interface{ Scan(dest ...any) error }) (*model.RestoreRequest, error) {
	var (
		id, runID, snapshotID, restoreKind         string
		targetJSON                                 string
		overwrite                                  int
		confirmationHash                           sql.NullString
		preRestoreRunID, rollbackSnapshotID, phase sql.NullString
		createdAt                                  string
	)
	if err := row.Scan(&id, &runID, &snapshotID, &restoreKind, &targetJSON, &overwrite, &confirmationHash, &preRestoreRunID, &rollbackSnapshotID, &phase, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan restore request: %w", err)
	}

	var target model.RestoreTarget
	_ = json.Unmarshal([]byte(targetJSON), &target)

	return &model.RestoreRequest{
		ID:                 id,
		RunID:              runID,
		SnapshotID:         snapshotID,
		RestoreKind:        restoreKind,
		Target:             target,
		TargetJSON:         targetJSON,
		Overwrite:          overwrite != 0,
		ConfirmationHash:   confirmationHash.String,
		PreRestoreRunID:    preRestoreRunID.String,
		RollbackSnapshotID: rollbackSnapshotID.String,
		Phase:              phase.String,
		CreatedAt:          parseTime(createdAt),
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
// 快照缓存
// ---------------------------------------------------------------------------

func (s *sqliteStore) GetSnapshotListCache(ctx context.Context, repositoryID string) (*SnapshotListCache, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT l.repository_id, l.generation, l.snapshots_json, l.fingerprint, l.verified_at
		FROM snapshot_list_cache l
		JOIN repository_cache_state c ON c.repository_id = l.repository_id
		WHERE l.repository_id = ? AND c.generation = l.generation
		  AND c.list_verified_at = l.verified_at`, repositoryID)
	var out SnapshotListCache
	var verified string
	if err := row.Scan(&out.RepositoryID, &out.Generation, &out.SnapshotsJSON, &out.Fingerprint, &verified); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get snapshot list cache: %w", err)
	}
	out.VerifiedAt = parseTime(verified)
	return &out, nil
}

func (s *sqliteStore) GetSnapshotTreeCache(ctx context.Context, repositoryID, snapshotID, cachePath string) (*SnapshotTreeCache, error) {
	cachePath = NormalizeSnapshotPath(cachePath)
	row := s.db.QueryRowContext(ctx, `
		SELECT t.repository_id, t.snapshot_id, t.path, t.generation, t.tree_json, t.verified_at
		FROM snapshot_tree_cache t
		JOIN repository_cache_state c ON c.repository_id = t.repository_id
		WHERE t.repository_id = ? AND t.snapshot_id = ? AND t.path = ?
		  AND c.generation = t.generation AND c.list_verified_at IS NOT NULL`, repositoryID, snapshotID, cachePath)
	var out SnapshotTreeCache
	var verified string
	if err := row.Scan(&out.RepositoryID, &out.SnapshotID, &out.Path, &out.Generation, &out.TreeJSON, &verified); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get snapshot tree cache: %w", err)
	}
	out.VerifiedAt = parseTime(verified)
	return &out, nil
}

func (s *sqliteStore) SnapshotCacheGeneration(ctx context.Context, repositoryID string) (int64, error) {
	var generation int64
	err := s.db.QueryRowContext(ctx,
		"SELECT generation FROM repository_cache_state WHERE repository_id = ?", repositoryID,
	).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("get snapshot cache generation: %w", err)
	}
	return generation, nil
}

func (s *sqliteStore) SaveSnapshotListCache(ctx context.Context, repositoryID string, generation int64, snapshotsJSON, fingerprint string, verifiedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save snapshot list cache begin tx: %w", err)
	}
	defer tx.Rollback()
	if err := ensureCacheGeneration(ctx, tx, repositoryID, generation); err != nil {
		return err
	}
	verified := verifiedAt.UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO snapshot_list_cache (repository_id, generation, snapshots_json, fingerprint, verified_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(repository_id) DO UPDATE SET generation=excluded.generation,
		  snapshots_json=excluded.snapshots_json, fingerprint=excluded.fingerprint,
		  verified_at=excluded.verified_at`,
		repositoryID, generation, snapshotsJSON, fingerprint, verified); err != nil {
		return fmt.Errorf("save snapshot list cache: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE repository_cache_state SET list_verified_at=?, list_fingerprint=?, updated_at=?
		WHERE repository_id=? AND generation=?`,
		verified, fingerprint, verified, repositoryID, generation); err != nil {
		return fmt.Errorf("mark snapshot list cache verified: %w", err)
	}
	var snaps []model.Snapshot
	if err := json.Unmarshal([]byte(snapshotsJSON), &snaps); err != nil {
		return fmt.Errorf("parse snapshot list cache: %w", err)
	}
	if len(snaps) == 0 {
		if _, err := tx.ExecContext(ctx, "DELETE FROM snapshot_tree_cache WHERE repository_id = ?", repositoryID); err != nil {
			return fmt.Errorf("reconcile snapshot tree cache: %w", err)
		}
	} else {
		placeholders := make([]string, len(snaps))
		ids := make([]any, len(snaps))
		for i, snap := range snaps {
			placeholders[i] = "?"
			ids[i] = snap.ID
		}
		markQuery := "UPDATE snapshot_tree_cache SET generation=? WHERE repository_id=? AND snapshot_id IN (" + strings.Join(placeholders, ",") + ")"
		markArgs := []any{generation, repositoryID}
		markArgs = append(markArgs, ids...)
		if _, err := tx.ExecContext(ctx, markQuery, markArgs...); err != nil {
			return fmt.Errorf("promote snapshot tree cache: %w", err)
		}
		deleteQuery := "DELETE FROM snapshot_tree_cache WHERE repository_id=? AND snapshot_id NOT IN (" + strings.Join(placeholders, ",") + ")"
		deleteArgs := []any{repositoryID}
		deleteArgs = append(deleteArgs, ids...)
		if _, err := tx.ExecContext(ctx, deleteQuery, deleteArgs...); err != nil {
			return fmt.Errorf("reconcile snapshot tree cache: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("save snapshot list cache commit: %w", err)
	}
	return nil
}

func (s *sqliteStore) SaveSnapshotTreeCache(ctx context.Context, repositoryID, snapshotID, cachePath string, generation int64, treeJSON string, verifiedAt time.Time) error {
	cachePath = NormalizeSnapshotPath(cachePath)
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save snapshot tree cache begin tx: %w", err)
	}
	defer tx.Rollback()
	if err := ensureCacheGeneration(ctx, tx, repositoryID, generation); err != nil {
		return err
	}
	var verifiedList string
	if err := tx.QueryRowContext(ctx,
		"SELECT list_verified_at FROM repository_cache_state WHERE repository_id = ?", repositoryID,
	).Scan(&verifiedList); err != nil {
		return fmt.Errorf("check snapshot list cache: %w", err)
	}
	if verifiedList == "" {
		return ErrCacheGenerationChanged
	}
	verified := verifiedAt.UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO snapshot_tree_cache (repository_id, snapshot_id, path, generation, tree_json, verified_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(repository_id, snapshot_id, path) DO UPDATE SET generation=excluded.generation,
		  tree_json=excluded.tree_json, verified_at=excluded.verified_at`,
		repositoryID, snapshotID, cachePath, generation, treeJSON, verified); err != nil {
		return fmt.Errorf("save snapshot tree cache: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("save snapshot tree cache commit: %w", err)
	}
	return nil
}

func (s *sqliteStore) InvalidateSnapshotCache(ctx context.Context, repositoryID string, clearTrees bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("invalidate snapshot cache begin tx: %w", err)
	}
	defer tx.Rollback()
	if err := invalidateSnapshotCacheTx(ctx, tx, repositoryID, clearTrees); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("invalidate snapshot cache commit: %w", err)
	}
	return nil
}

func ensureCacheGeneration(ctx context.Context, tx *sql.Tx, repositoryID string, expected int64) error {
	var actual int64
	err := tx.QueryRowContext(ctx,
		"SELECT generation FROM repository_cache_state WHERE repository_id = ?", repositoryID,
	).Scan(&actual)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read snapshot cache generation: %w", err)
	}
	if actual != expected {
		return ErrCacheGenerationChanged
	}
	return nil
}

func invalidateSnapshotCacheTx(ctx context.Context, tx *sql.Tx, repositoryID string, clearTrees bool) error {
	now := nowUTC().Format(time.RFC3339)
	result, err := tx.ExecContext(ctx, `
		UPDATE repository_cache_state
		SET generation=generation+1, list_verified_at=NULL, updated_at=?
		WHERE repository_id=?`, now, repositoryID)
	if err != nil {
		return fmt.Errorf("invalidate snapshot cache state: %w", err)
	}
	if n, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("invalidate snapshot cache rows affected: %w", err)
	} else if n == 0 {
		return ErrNotFound
	}
	if clearTrees {
		if _, err := tx.ExecContext(ctx, "DELETE FROM snapshot_tree_cache WHERE repository_id = ?", repositoryID); err != nil {
			return fmt.Errorf("clear snapshot tree cache: %w", err)
		}
	}
	return nil
}

func invalidateTargetCaches(ctx context.Context, tx *sql.Tx, targetID string) error {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM repositories WHERE storage_target_id = ?", targetID)
	if err != nil {
		return fmt.Errorf("list target repositories for cache invalidation: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scan target repository: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("list target repositories: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close target repositories: %w", err)
	}
	for _, id := range ids {
		if err := invalidateSnapshotCacheTx(ctx, tx, id, true); err != nil {
			return err
		}
	}
	return nil
}

func invalidatesSnapshotCache(operation string) bool {
	return operation == model.OpBackup || operation == model.OpForget
}

// ---------------------------------------------------------------------------
// 快照删除意图与孤儿扫描
// ---------------------------------------------------------------------------

func scanSnapshotDeletion(row interface{ Scan(dest ...any) error }) (*model.SnapshotDeletion, error) {
	var (
		id, repositoryID, agentID, snapshotID, source, state string
		firstSeenAt, lastSeenAt, createdAt, updatedAt        string
		nextAttemptAt, leaseExpiresAt, completedAt           sql.NullString
		runID, errorCode, errorMessage, requestedBy          sql.NullString
		seenCount, attempt                                   int
	)
	if err := row.Scan(&id, &repositoryID, &agentID, &snapshotID, &source, &state,
		&firstSeenAt, &lastSeenAt, &seenCount, &nextAttemptAt, &attempt,
		&runID, &leaseExpiresAt, &errorCode, &errorMessage, &requestedBy,
		&createdAt, &updatedAt, &completedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan snapshot deletion: %w", err)
	}
	return &model.SnapshotDeletion{
		ID:             id,
		RepositoryID:   repositoryID,
		AgentID:        agentID,
		SnapshotID:     snapshotID,
		Source:         model.SnapshotDeletionSource(source),
		State:          model.SnapshotDeletionState(state),
		FirstSeenAt:    parseTime(firstSeenAt),
		LastSeenAt:     parseTime(lastSeenAt),
		SeenCount:      seenCount,
		NextAttemptAt:  parseTimePtr(nextAttemptAt),
		Attempt:        attempt,
		RunID:          runID.String,
		LeaseExpiresAt: parseTimePtr(leaseExpiresAt),
		ErrorCode:      errorCode.String,
		ErrorMessage:   errorMessage.String,
		RequestedBy:    requestedBy.String,
		CreatedAt:      parseTime(createdAt),
		UpdatedAt:      parseTime(updatedAt),
		CompletedAt:    parseTimePtr(completedAt),
	}, nil
}

func (s *sqliteStore) QueueManualSnapshotDeletion(ctx context.Context, repositoryID, agentID, snapshotID, actorID string, now time.Time) (*model.SnapshotDeletion, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("queue manual deletion begin tx: %w", err)
	}
	defer tx.Rollback()

	// 读取既有意图。
	var existing *model.SnapshotDeletion
	row := tx.QueryRowContext(ctx,
		`SELECT id, repository_id, agent_id, snapshot_id, source, state,
		        first_seen_at, last_seen_at, seen_count, next_attempt_at, attempt,
		        run_id, lease_expires_at, error_code, error_message, requested_by,
		        created_at, updated_at, completed_at
		 FROM snapshot_deletions WHERE repository_id = ? AND snapshot_id = ?`,
		repositoryID, snapshotID,
	)
	existing, err = scanSnapshotDeletion(row)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, false, fmt.Errorf("queue manual deletion read: %w", err)
	}

	nowStr := now.UTC().Format(time.RFC3339)

	// 已进入 pending|running|succeeded 的意图直接返回原记录，不重复创建。
	if existing != nil {
		switch existing.State {
		case model.SnapshotDeletionPending, model.SnapshotDeletionRunning, model.SnapshotDeletionSucceeded:
			return existing, false, nil
		}
		// candidate 原子升级为 manual/pending。
		if _, err := tx.ExecContext(ctx,
			`UPDATE snapshot_deletions
			 SET source='manual', state='pending', agent_id=?, requested_by=?,
			     next_attempt_at=NULL, error_code=NULL, error_message=NULL,
			     updated_at=?
			 WHERE id=?`,
			agentID, actorID, nowStr, existing.ID); err != nil {
			return nil, false, fmt.Errorf("upgrade manual deletion: %w", err)
		}
		existing.Source = model.SnapshotDeletionManual
		existing.State = model.SnapshotDeletionPending
		existing.AgentID = agentID
		existing.RequestedBy = actorID
		existing.NextAttemptAt = nil
		existing.ErrorCode = ""
		existing.ErrorMessage = ""
		existing.UpdatedAt = now.UTC()
		if err := tx.Commit(); err != nil {
			return nil, false, fmt.Errorf("upgrade manual deletion commit: %w", err)
		}
		return existing, true, nil
	}

	// 新建 manual/pending 意图。
	id := model.NewUUIDv7()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO snapshot_deletions
		   (id, repository_id, agent_id, snapshot_id, source, state,
		    first_seen_at, last_seen_at, seen_count, next_attempt_at, attempt,
		    run_id, lease_expires_at, error_code, error_message, requested_by,
		    created_at, updated_at, completed_at)
		 VALUES (?, ?, ?, ?, 'manual', 'pending', ?, ?, 1, NULL, 0,
		         NULL, NULL, NULL, NULL, ?, ?, ?, NULL)`,
		id, repositoryID, agentID, snapshotID, nowStr, nowStr, actorID, nowStr, nowStr); err != nil {
		return nil, false, fmt.Errorf("insert manual deletion: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("insert manual deletion commit: %w", err)
	}
	return &model.SnapshotDeletion{
		ID:           id,
		RepositoryID: repositoryID,
		AgentID:      agentID,
		SnapshotID:   snapshotID,
		Source:       model.SnapshotDeletionManual,
		State:        model.SnapshotDeletionPending,
		FirstSeenAt:  now.UTC(),
		LastSeenAt:   now.UTC(),
		SeenCount:    1,
		RequestedBy:  actorID,
		CreatedAt:    now.UTC(),
		UpdatedAt:    now.UTC(),
	}, true, nil
}

func (s *sqliteStore) HiddenSnapshotIDs(ctx context.Context, repositoryID string) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT snapshot_id FROM snapshot_deletions
		 WHERE repository_id = ?
		   AND (
		     (source = 'manual' AND state IN ('pending', 'running', 'succeeded'))
		     OR (source = 'orphan' AND state IN ('pending', 'running', 'succeeded'))
		   )`,
		repositoryID,
	)
	if err != nil {
		return nil, fmt.Errorf("hidden snapshot ids: %w", err)
	}
	defer rows.Close()

	out := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan hidden snapshot id: %w", err)
		}
		out[id] = struct{}{}
	}
	return out, rows.Err()
}

func (s *sqliteStore) ListRunningSnapshotDeletions(ctx context.Context) ([]model.SnapshotDeletion, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, repository_id, agent_id, snapshot_id, source, state,
		        first_seen_at, last_seen_at, seen_count, next_attempt_at, attempt,
		        run_id, lease_expires_at, error_code, error_message, requested_by,
		        created_at, updated_at, completed_at
		 FROM snapshot_deletions
		 WHERE state = 'running'
		 ORDER BY updated_at ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list running snapshot deletions: %w", err)
	}
	defer rows.Close()

	var out []model.SnapshotDeletion
	for rows.Next() {
		d, err := scanSnapshotDeletion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (s *sqliteStore) ListDueSnapshotDeletions(ctx context.Context, now time.Time, limit int) ([]model.SnapshotDeletion, error) {
	if limit <= 0 {
		limit = 100
	}
	nowStr := now.UTC().Format(time.RFC3339)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, repository_id, agent_id, snapshot_id, source, state,
		        first_seen_at, last_seen_at, seen_count, next_attempt_at, attempt,
		        run_id, lease_expires_at, error_code, error_message, requested_by,
		        created_at, updated_at, completed_at
		 FROM snapshot_deletions
		 WHERE state = 'pending' AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
		 ORDER BY next_attempt_at ASC NULLS FIRST, updated_at ASC
		 LIMIT ?`,
		nowStr, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list due snapshot deletions: %w", err)
	}
	defer rows.Close()

	var out []model.SnapshotDeletion
	for rows.Next() {
		d, err := scanSnapshotDeletion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (s *sqliteStore) ClaimSnapshotDeletionRun(ctx context.Context, deletionID string, run *model.Run, leaseUntil time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("claim deletion run begin tx: %w", err)
	}
	defer tx.Rollback()

	// 读取意图，确认仍为 pending。
	var state string
	if err := tx.QueryRowContext(ctx,
		"SELECT state FROM snapshot_deletions WHERE id = ?", deletionID,
	).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("claim deletion read state: %w", err)
	}
	if state != string(model.SnapshotDeletionPending) {
		return ErrInvalidTransition
	}

	// 插入 queued run。
	planID := nullString(run.PlanID)
	repoID := nullString(run.RepositoryID)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO runs (id, plan_id, agent_id, operation, status,
		   queued_at, started_at, finished_at, progress_json, snapshot_id,
		   error_code, error_message, repository_id, scheduled_at, attempt, lease_expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, planID, run.AgentID, run.Operation, run.Status,
		run.QueuedAt.Format(time.RFC3339),
		nullTime(run.StartedAt),
		nullTime(run.FinishedAt),
		run.ProgressJSON, run.SnapshotID,
		run.ErrorCode, run.ErrorMessage,
		repoID,
		nullTime(run.ScheduledAt),
		run.Attempt,
		nullTime(run.LeaseExpiresAt),
	); err != nil {
		return fmt.Errorf("claim deletion insert run: %w", err)
	}

	// 意图置 running 并绑定 run_id/lease_expires_at。
	leaseStr := leaseUntil.UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx,
		`UPDATE snapshot_deletions
		 SET state='running', run_id=?, lease_expires_at=?, attempt=attempt+1,
		     error_code=NULL, error_message=NULL, updated_at=?
		 WHERE id=?`,
		run.ID, leaseStr, nowUTC().Format(time.RFC3339), deletionID); err != nil {
		return fmt.Errorf("claim deletion set running: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("claim deletion run commit: %w", err)
	}
	return nil
}

func (s *sqliteStore) CompleteSnapshotDeletion(ctx context.Context, deletionID string, now time.Time) error {
	nowStr := now.UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx,
		`UPDATE snapshot_deletions
		 SET state='succeeded', completed_at=?, error_code=NULL, error_message=NULL,
		     next_attempt_at=NULL, updated_at=?
		 WHERE id=?`,
		nowStr, nowStr, deletionID)
	if err != nil {
		return fmt.Errorf("complete snapshot deletion: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *sqliteStore) RetrySnapshotDeletion(ctx context.Context, deletionID, errorCode, errorMessage string, nextAttemptAt time.Time) error {
	nextStr := nextAttemptAt.UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx,
		`UPDATE snapshot_deletions
		 SET state='pending', run_id=NULL, lease_expires_at=NULL,
		     error_code=?, error_message=?, next_attempt_at=?, updated_at=?
		 WHERE id=?`,
		errorCode, errorMessage, nextStr, nowUTC().Format(time.RFC3339), deletionID)
	if err != nil {
		return fmt.Errorf("retry snapshot deletion: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *sqliteStore) GetSnapshotCleanupState(ctx context.Context, repositoryID string) (*model.SnapshotCleanupState, error) {
	var (
		scanRunID, lastStarted, lastCompleted sql.NullString
		updated                               string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT repository_id, scan_run_id, last_scan_started_at, last_scan_completed_at, updated_at
		 FROM snapshot_cleanup_state WHERE repository_id = ?`, repositoryID,
	).Scan(&repositoryID, &scanRunID, &lastStarted, &lastCompleted, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get snapshot cleanup state: %w", err)
	}
	return &model.SnapshotCleanupState{
		RepositoryID:        repositoryID,
		ScanRunID:           scanRunID.String,
		LastScanStartedAt:   parseTimePtr(lastStarted),
		LastScanCompletedAt: parseTimePtr(lastCompleted),
		UpdatedAt:           parseTime(updated),
	}, nil
}

func (s *sqliteStore) FinishSnapshotCleanupScan(ctx context.Context, repositoryID, runID string, snapshots []model.Snapshot, completedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("finish cleanup scan begin tx: %w", err)
	}
	defer tx.Rollback()

	// 空 runID 仅用于存储层直接 reconciliation；后台路径始终传入真实 run ID。
	var currentRunID string
	if err := tx.QueryRowContext(ctx,
		"SELECT scan_run_id FROM snapshot_cleanup_state WHERE repository_id = ?", repositoryID,
	).Scan(&currentRunID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if runID != "" {
				return ErrNotFound
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO snapshot_cleanup_state (repository_id, updated_at) VALUES (?, ?)`,
				repositoryID, completedAt.UTC().Format(time.RFC3339)); err != nil {
				return fmt.Errorf("create cleanup scan state: %w", err)
			}
			currentRunID = ""
		} else {
			return fmt.Errorf("finish cleanup scan read run: %w", err)
		}
	}
	if runID != "" && currentRunID != runID {
		return ErrInvalidTransition
	}

	completedStr := completedAt.UTC().Format(time.RFC3339)

	// 孤儿 reconciliation：对每个远端快照，若标签完整且无权威匹配 run，则写/递增 candidate。
	for _, snap := range snapshots {
		planID, kind, runTag, ok := parseSnapshotTags(snap.Tags)
		if !ok {
			continue // 标签不完整或异常，跳过
		}
		_ = planID
		_ = kind
		_ = runTag
		// 权威匹配：runs.id = run: 标签、operation='backup'、status='succeeded'、
		// repository_id 一致、snapshot_id 一致。
		var matched int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM runs
			 WHERE id = ? AND operation = 'backup' AND status = 'succeeded'
			   AND repository_id = ? AND snapshot_id = ?`,
			runTag, repositoryID, snap.ID,
		).Scan(&matched); err != nil {
			return fmt.Errorf("finish cleanup scan match run: %w", err)
		}
		if matched > 0 {
			// 有权威匹配：删除候选记录（若存在）。
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM snapshot_deletions
				 WHERE repository_id = ? AND snapshot_id = ? AND source = 'orphan' AND state = 'candidate'`,
				repositoryID, snap.ID); err != nil {
				return fmt.Errorf("finish cleanup scan clear candidate: %w", err)
			}
			continue
		}
		// 无权威匹配：写 candidate 或递增 seen_count。
		if err := upsertOrphanCandidateTx(ctx, tx, repositoryID, snap.ID, completedAt); err != nil {
			return err
		}
	}

	// 已从远端消失的候选记录删除。
	if err := deleteAbsentCandidatesTx(ctx, tx, repositoryID, snapshots); err != nil {
		return err
	}

	// 写完成时间并清活跃扫描。
	if _, err := tx.ExecContext(ctx,
		`UPDATE snapshot_cleanup_state
		 SET scan_run_id=NULL, last_scan_completed_at=?, updated_at=?
		 WHERE repository_id=?`,
		completedStr, completedStr, repositoryID); err != nil {
		return fmt.Errorf("finish cleanup scan complete: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("finish cleanup scan commit: %w", err)
	}
	return nil
}

func (s *sqliteStore) StartSnapshotCleanupScan(ctx context.Context, repositoryID, runID string, startedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("start cleanup scan begin tx: %w", err)
	}
	defer tx.Rollback()

	if runID == "" {
		return ErrInvalidTransition
	}
	startedStr := startedAt.UTC().Format(time.RFC3339)
	// compare-and-set：首次无状态行时插入；仅当无活跃扫描或 run_id 相同（重启恢复）时更新。
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO snapshot_cleanup_state (repository_id, scan_run_id, last_scan_started_at, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(repository_id) DO UPDATE SET
		   scan_run_id = excluded.scan_run_id,
		   last_scan_started_at = excluded.last_scan_started_at,
		   updated_at = excluded.updated_at
		 WHERE snapshot_cleanup_state.scan_run_id IS NULL
		    OR snapshot_cleanup_state.scan_run_id = excluded.scan_run_id`,
		repositoryID, runID, startedStr, startedStr); err != nil {
		return fmt.Errorf("start cleanup scan upsert: %w", err)
	}
	// 校验：若被另一 run 抢占（活跃扫描且 run_id 不同），返回冲突。
	var finalRunID sql.NullString
	if err := tx.QueryRowContext(ctx,
		"SELECT scan_run_id FROM snapshot_cleanup_state WHERE repository_id = ?", repositoryID,
	).Scan(&finalRunID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("start cleanup scan verify: %w", err)
	}
	if finalRunID.Valid && finalRunID.String != runID {
		return ErrInvalidTransition
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("start cleanup scan commit: %w", err)
	}
	return nil
}

func (s *sqliteStore) ClearSnapshotCleanupScan(ctx context.Context, repositoryID, runID string, nextAttemptAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("clear cleanup scan begin tx: %w", err)
	}
	defer tx.Rollback()

	var currentRunID string
	if err := tx.QueryRowContext(ctx,
		"SELECT scan_run_id FROM snapshot_cleanup_state WHERE repository_id = ?", repositoryID,
	).Scan(&currentRunID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("clear cleanup scan read run: %w", err)
	}
	if currentRunID != runID {
		return ErrInvalidTransition
	}

	// 失败只清活跃扫描，保留候选原状态，不增加 seen_count。
	if _, err := tx.ExecContext(ctx,
		`UPDATE snapshot_cleanup_state
		 SET scan_run_id=NULL, updated_at=?
		 WHERE repository_id=?`,
		nowUTC().Format(time.RFC3339), repositoryID); err != nil {
		return fmt.Errorf("clear cleanup scan update: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("clear cleanup scan commit: %w", err)
	}
	return nil
}

// parseSnapshotTags 校验快照标签：必须同时且各自恰好包含一个 plan:<id>、kind:<kind>、run:<id>。
// 返回 (planID, kind, runID, ok)。重复同前缀标签、多个计划标签、缺失任一标签均视为不完整。
func parseSnapshotTags(tags []string) (planID, kind, runID string, ok bool) {
	var planCount, kindCount, runCount int
	for _, tag := range tags {
		switch {
		case strings.HasPrefix(tag, "plan:"):
			planCount++
			planID = strings.TrimPrefix(tag, "plan:")
		case strings.HasPrefix(tag, "kind:"):
			kindCount++
			kind = strings.TrimPrefix(tag, "kind:")
		case strings.HasPrefix(tag, "run:"):
			runCount++
			runID = strings.TrimPrefix(tag, "run:")
		}
	}
	if planCount != 1 || kindCount != 1 || runCount != 1 {
		return "", "", "", false
	}
	if planID == "" || kind == "" || runID == "" {
		return "", "", "", false
	}
	return planID, kind, runID, true
}

// upsertOrphanCandidateTx 写入或递增孤儿候选。仅当 seen_count>=2 且 now-first_seen_at>=7*24h 时转 pending。
func upsertOrphanCandidateTx(ctx context.Context, tx *sql.Tx, repositoryID, snapshotID string, now time.Time) error {
	nowStr := now.UTC().Format(time.RFC3339)
	var (
		id        string
		source    string
		state     string
		firstSeen string
		seenCount int
	)
	err := tx.QueryRowContext(ctx,
		`SELECT id, source, state, first_seen_at, seen_count FROM snapshot_deletions
		 WHERE repository_id = ? AND snapshot_id = ?`,
		repositoryID, snapshotID,
	).Scan(&id, &source, &state, &firstSeen, &seenCount)
	if errors.Is(err, sql.ErrNoRows) {
		// 孤儿记录仍绑定仓库所属 Agent，避免 snapshot_deletions.agent_id 的外键为空。
		var agentID string
		if err := tx.QueryRowContext(ctx, "SELECT agent_id FROM repositories WHERE id = ?", repositoryID).Scan(&agentID); err != nil {
			return fmt.Errorf("find repository agent: %w", err)
		}
		newID := model.NewUUIDv7()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO snapshot_deletions
			   (id, repository_id, agent_id, snapshot_id, source, state,
			    first_seen_at, last_seen_at, seen_count, next_attempt_at, attempt,
			    run_id, lease_expires_at, error_code, error_message, requested_by,
			    created_at, updated_at, completed_at)
			 VALUES (?, ?, ?, ?, 'orphan', 'candidate', ?, ?, 1, NULL, 0,
			         NULL, NULL, NULL, NULL, '', ?, ?, NULL)`,
			newID, repositoryID, agentID, snapshotID, nowStr, nowStr, nowStr, nowStr); err != nil {
			return fmt.Errorf("insert orphan candidate: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read orphan candidate: %w", err)
	}
	// 已有 manual 意图或已进入删除流程的记录不被扫描回退或修改。
	if source == string(model.SnapshotDeletionManual) || state != string(model.SnapshotDeletionCandidate) {
		return nil
	}
	// 递增 seen_count，保留最初 first_seen_at。
	seenCount++
	nextState := string(model.SnapshotDeletionCandidate)
	if seenCount >= 2 && now.Sub(parseTime(firstSeen)) >= 7*24*time.Hour {
		nextState = string(model.SnapshotDeletionPending)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE snapshot_deletions
		 SET seen_count=?, state=?, last_seen_at=?, updated_at=?
		 WHERE id=?`,
		seenCount, nextState, nowStr, nowStr, id); err != nil {
		return fmt.Errorf("update orphan candidate: %w", err)
	}
	return nil
}

// deleteAbsentCandidatesTx 删除已从远端消失的候选记录。
func deleteAbsentCandidatesTx(ctx context.Context, tx *sql.Tx, repositoryID string, snapshots []model.Snapshot) error {
	// 收集远端存在的 snapshotID。
	present := map[string]struct{}{}
	for _, snap := range snapshots {
		present[snap.ID] = struct{}{}
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT snapshot_id FROM snapshot_deletions
		 WHERE repository_id = ? AND source = 'orphan' AND state = 'candidate'`,
		repositoryID)
	if err != nil {
		return fmt.Errorf("list orphan candidates: %w", err)
	}
	var absent []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan orphan candidate: %w", err)
		}
		if _, ok := present[id]; !ok {
			absent = append(absent, id)
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close orphan candidates: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list orphan candidates: %w", err)
	}
	for _, id := range absent {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM snapshot_deletions WHERE repository_id = ? AND snapshot_id = ? AND source = 'orphan' AND state = 'candidate'`,
			repositoryID, id); err != nil {
			return fmt.Errorf("delete absent orphan candidate: %w", err)
		}
	}
	return nil
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
