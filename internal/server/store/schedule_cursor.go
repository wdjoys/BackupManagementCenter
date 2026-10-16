package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// GetScheduleCursor returns the next persisted cron slot, if one exists.
func (s *sqliteStore) GetScheduleCursor(ctx context.Context, planID string) (time.Time, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT next_fire_at FROM schedule_cursor WHERE plan_id=?`, planID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrNotFound
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("get schedule cursor: %w", err)
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse schedule cursor: %w", err)
	}
	return t.UTC(), nil
}

func (s *sqliteStore) SaveScheduleCursor(ctx context.Context, planID string, next time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO schedule_cursor (plan_id, next_fire_at) VALUES (?, ?)
		 ON CONFLICT(plan_id) DO UPDATE SET next_fire_at=excluded.next_fire_at`,
		planID, next.UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("save schedule cursor: %w", err)
	}
	return nil
}
