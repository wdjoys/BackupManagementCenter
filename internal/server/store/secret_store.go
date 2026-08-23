package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"backupmanagementcenter/internal/model"
)

// SavePlanDBPassword stores a plan's database credential outside source_json.
// The plaintext exists only for the duration of this call.
func (s *sqliteStore) SavePlanDBPassword(ctx context.Context, planID, password string) error {
	if password == "" {
		return s.DeletePlanDBPassword(ctx, planID)
	}
	sealed, err := s.seal.Seal("backup_plan_secrets", planID, "encrypted_password", password)
	if err != nil {
		return fmt.Errorf("seal plan database password: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO backup_plan_secrets (plan_id, encrypted_password, updated_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(plan_id) DO UPDATE SET encrypted_password=excluded.encrypted_password,
		 updated_at=excluded.updated_at`,
		planID, sealed, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("save plan database password: %w", err)
	}
	return nil
}

func (s *sqliteStore) DeletePlanDBPassword(ctx context.Context, planID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM backup_plan_secrets WHERE plan_id = ?`, planID)
	if err != nil {
		return fmt.Errorf("delete plan database password: %w", err)
	}
	return nil
}

// GetPlanDBPassword also performs a one-time lazy migration of the old
// source_json.password shape. This keeps existing installations runnable while
// removing the plaintext from the plan row before returning it to callers.
func (s *sqliteStore) GetPlanDBPassword(ctx context.Context, planID string) (string, error) {
	var sealed []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT encrypted_password FROM backup_plan_secrets WHERE plan_id = ?`, planID).Scan(&sealed)
	if err == nil {
		password, openErr := s.seal.Open("backup_plan_secrets", planID, "encrypted_password", sealed)
		if openErr != nil {
			return "", fmt.Errorf("open plan database password: %w", openErr)
		}
		return password, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("get plan database password: %w", err)
	}

	var sourceJSON string
	if err := s.db.QueryRowContext(ctx, `SELECT source_json FROM backup_plans WHERE id = ?`, planID).Scan(&sourceJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("read legacy plan source: %w", err)
	}
	var legacy struct {
		model.PlanSource
		Password string `json:"password,omitempty"`
	}
	if err := json.Unmarshal([]byte(sourceJSON), &legacy); err != nil || legacy.Password == "" {
		return "", ErrNotFound
	}
	if err := s.SavePlanDBPassword(ctx, planID, legacy.Password); err != nil {
		return "", err
	}
	clean, err := json.Marshal(legacy.PlanSource)
	if err == nil {
		_, _ = s.db.ExecContext(ctx, `UPDATE backup_plans SET source_json=? WHERE id=?`, string(clean), planID)
	}
	return legacy.Password, nil
}

func (s *sqliteStore) HasPlanDBPassword(ctx context.Context, planID string) bool {
	_, err := s.GetPlanDBPassword(ctx, planID)
	return err == nil
}

func (s *sqliteStore) SaveRunTargetPassword(ctx context.Context, runID, password string) error {
	if password == "" {
		return nil
	}
	sealed, err := s.seal.Seal("run_secrets", runID, "encrypted_target_password", password)
	if err != nil {
		return fmt.Errorf("seal run target password: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO run_secrets (run_id, encrypted_target_password, created_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(run_id) DO UPDATE SET encrypted_target_password=excluded.encrypted_target_password`,
		runID, sealed, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("save run target password: %w", err)
	}
	return nil
}

func (s *sqliteStore) SaveRunRcloneConfig(ctx context.Context, runID, config string) error {
	if config == "" { return nil }
	sealed, err := s.seal.Seal("run_secrets", runID, "encrypted_rclone_config", config)
	if err != nil { return fmt.Errorf("seal run rclone config: %w", err) }
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO run_secrets (run_id, encrypted_rclone_config, created_at) VALUES (?, ?, ?)
		 ON CONFLICT(run_id) DO UPDATE SET encrypted_rclone_config=excluded.encrypted_rclone_config`,
		runID, sealed, time.Now().UTC().Format(time.RFC3339))
	if err != nil { return fmt.Errorf("save run rclone config: %w", err) }
	return nil
}

func (s *sqliteStore) GetRunRcloneConfig(ctx context.Context, runID string) (string, error) {
	var sealed []byte
	err := s.db.QueryRowContext(ctx, `SELECT encrypted_rclone_config FROM run_secrets WHERE run_id=?`, runID).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) || len(sealed) == 0 { return "", ErrNotFound }
	if err != nil { return "", fmt.Errorf("get run rclone config: %w", err) }
	plain, err := s.seal.Open("run_secrets", runID, "encrypted_rclone_config", sealed)
	if err != nil { return "", fmt.Errorf("open run rclone config: %w", err) }
	return plain, nil
}

func (s *sqliteStore) GetRunTargetPassword(ctx context.Context, runID string) (string, error) {
	var sealed []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT encrypted_target_password FROM run_secrets WHERE run_id = ?`, runID).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("get run target password: %w", err)
	}
	password, err := s.seal.Open("run_secrets", runID, "encrypted_target_password", sealed)
	if err != nil {
		return "", fmt.Errorf("open run target password: %w", err)
	}
	return password, nil
}

func (s *sqliteStore) DeleteRunSecrets(ctx context.Context, runID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM run_secrets WHERE run_id = ?`, runID)
	if err != nil {
		return fmt.Errorf("delete run secrets: %w", err)
	}
	return nil
}
