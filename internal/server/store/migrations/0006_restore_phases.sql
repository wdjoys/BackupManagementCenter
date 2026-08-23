ALTER TABLE restore_requests ADD COLUMN pre_restore_run_id TEXT;
ALTER TABLE restore_requests ADD COLUMN rollback_snapshot_id TEXT;
ALTER TABLE restore_requests ADD COLUMN phase TEXT NOT NULL DEFAULT 'queued';
