-- 0001_init.sql — full phase-1 schema.
-- IDs are UUIDv7 TEXT; times are RFC3339 UTC TEXT; secrets are AES-256-GCM
-- sealed BLOBs with AAD "<table>:<row-id>:<column>".

CREATE TABLE admins (
    id            TEXT PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,             -- argon2id PHC string
    created_at    TEXT NOT NULL,
    last_login_at TEXT
);

CREATE TABLE sessions (
    id_hash     TEXT PRIMARY KEY,            -- SHA-256 hex of session token
    admin_id    TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    expires_at  TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    last_seen_at TEXT NOT NULL
);
CREATE INDEX idx_sessions_expiry ON sessions(expires_at);

CREATE TABLE agents (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    hostname          TEXT NOT NULL DEFAULT '',
    os                TEXT NOT NULL DEFAULT '',
    arch              TEXT NOT NULL DEFAULT '',
    version           TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'offline' CHECK (status IN ('online','offline')),
    last_seen_at      TEXT,
    enrolled_at       TEXT NOT NULL,
    token_hash        TEXT NOT NULL UNIQUE,  -- SHA-256 hex of agent secret
    capabilities_json TEXT NOT NULL DEFAULT '[]',
    revoked           INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE enrollment_tokens (
    id         TEXT PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TEXT NOT NULL,
    used_at    TEXT
);

CREATE TABLE storage_targets (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL UNIQUE,
    type             TEXT NOT NULL CHECK (type = 'rclone'),
    remote_name      TEXT NOT NULL,
    remote_path      TEXT NOT NULL DEFAULT '',
    encrypted_config BLOB NOT NULL,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);

CREATE TABLE repositories (
    id                 TEXT PRIMARY KEY,
    agent_id           TEXT NOT NULL REFERENCES agents(id),
    storage_target_id  TEXT NOT NULL REFERENCES storage_targets(id),
    repository_path    TEXT NOT NULL,
    encrypted_password BLOB NOT NULL,
    status             TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','ready','error')),
    last_check_at      TEXT,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    UNIQUE (agent_id, storage_target_id)
);

CREATE TABLE backup_plans (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    agent_id        TEXT NOT NULL REFERENCES agents(id),
    kind            TEXT NOT NULL CHECK (kind IN ('filesystem','postgresql','mysql','mongodb','sqlite')),
    schedule        TEXT NOT NULL,
    timezone        TEXT NOT NULL,
    enabled         INTEGER NOT NULL DEFAULT 1,
    source_json     TEXT NOT NULL,
    repository_id   TEXT NOT NULL REFERENCES repositories(id),
    retention_json  TEXT NOT NULL DEFAULT '{}',
    timeout_seconds INTEGER NOT NULL DEFAULT 3600,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);
CREATE INDEX idx_plans_agent ON backup_plans(agent_id);

CREATE TABLE runs (
    id            TEXT PRIMARY KEY,
    plan_id       TEXT NOT NULL REFERENCES backup_plans(id),
    agent_id      TEXT NOT NULL,
    operation     TEXT NOT NULL CHECK (operation IN ('backup','restore','check','forget','restore_dry_run','snapshots','snapshot_ls','verify_storage_remote','validate_paths','probe_capabilities')),
    status        TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','dispatched','running','succeeded','failed','cancelled')),
    queued_at     TEXT NOT NULL,
    started_at    TEXT,
    finished_at   TEXT,
    progress_json TEXT NOT NULL DEFAULT '{}',
    snapshot_id   TEXT,
    error_code    TEXT,
    error_message TEXT,
    scheduled_at  TEXT,
    -- one run per cron slot per plan; manual runs keep scheduled_at NULL
    UNIQUE (plan_id, scheduled_at)
);
CREATE INDEX idx_runs_status ON runs(status);
CREATE INDEX idx_runs_agent_status ON runs(agent_id, status);
CREATE INDEX idx_runs_plan_queued ON runs(plan_id, queued_at DESC);

CREATE TABLE run_logs (
    run_id    TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    seq       INTEGER NOT NULL,
    timestamp TEXT NOT NULL,
    level     TEXT NOT NULL CHECK (level IN ('debug','info','warn','error')),
    message   TEXT NOT NULL,
    PRIMARY KEY (run_id, seq)
);

CREATE TABLE restore_requests (
    id                 TEXT PRIMARY KEY,
    run_id             TEXT NOT NULL REFERENCES runs(id),
    snapshot_id        TEXT NOT NULL,
    restore_kind       TEXT NOT NULL,
    target_json        TEXT NOT NULL,
    overwrite          INTEGER NOT NULL DEFAULT 0,
    confirmation_hash  TEXT,
    created_at         TEXT NOT NULL
);

CREATE TABLE audit_events (
    id            TEXT PRIMARY KEY,
    occurred_at   TEXT NOT NULL,
    actor_type    TEXT NOT NULL,
    actor_id      TEXT NOT NULL DEFAULT '',
    action        TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id   TEXT NOT NULL DEFAULT '',
    detail_json   TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_audit_time ON audit_events(occurred_at DESC);

-- Scheduler bookkeeping: next fire time per enabled plan.
CREATE TABLE schedule_cursor (
    plan_id      TEXT PRIMARY KEY REFERENCES backup_plans(id) ON DELETE CASCADE,
    next_fire_at TEXT NOT NULL
);
