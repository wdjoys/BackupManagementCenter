-- Durable dispatch claims. A lease prevents a process restart from losing a
-- queued job and gives the watchdog enough information to reclaim stale work.
ALTER TABLE runs ADD COLUMN attempt INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN lease_expires_at TEXT;

CREATE INDEX IF NOT EXISTS idx_runs_lease ON runs(status, lease_expires_at);
