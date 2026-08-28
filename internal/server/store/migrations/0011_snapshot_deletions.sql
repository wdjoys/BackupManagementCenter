-- 0011_snapshot_deletions.sql — 快照删除意图与孤儿扫描状态。

CREATE TABLE snapshot_deletions (
    id                  TEXT PRIMARY KEY,
    repository_id       TEXT NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    agent_id            TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    snapshot_id         TEXT NOT NULL,
    source              TEXT NOT NULL CHECK (source IN ('manual', 'orphan')),
    state               TEXT NOT NULL CHECK (state IN ('candidate', 'pending', 'running', 'succeeded')),
    first_seen_at       TEXT NOT NULL,
    last_seen_at        TEXT NOT NULL,
    seen_count          INTEGER NOT NULL DEFAULT 1,
    next_attempt_at     TEXT,
    attempt             INTEGER NOT NULL DEFAULT 0,
    run_id              TEXT REFERENCES runs(id) ON DELETE SET NULL,
    lease_expires_at    TEXT,
    error_code          TEXT,
    error_message       TEXT,
    requested_by        TEXT,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    completed_at        TEXT,
    UNIQUE (repository_id, snapshot_id)
);
CREATE INDEX idx_snapshot_deletions_repo_state ON snapshot_deletions(repository_id, state);
CREATE INDEX idx_snapshot_deletions_due ON snapshot_deletions(state, next_attempt_at) WHERE state = 'pending';

CREATE TABLE snapshot_cleanup_state (
    repository_id           TEXT PRIMARY KEY REFERENCES repositories(id) ON DELETE CASCADE,
    scan_run_id             TEXT REFERENCES runs(id) ON DELETE SET NULL,
    last_scan_started_at    TEXT,
    last_scan_completed_at  TEXT,
    updated_at              TEXT NOT NULL
);