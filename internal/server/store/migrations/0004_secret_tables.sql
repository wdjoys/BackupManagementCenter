-- Per-plan database credentials and per-run restore credentials. Values are
-- sealed by sqliteStore with AAD matching the table, row and column names.
CREATE TABLE IF NOT EXISTS backup_plan_secrets (
    plan_id             TEXT PRIMARY KEY REFERENCES backup_plans(id) ON DELETE CASCADE,
    encrypted_password  BLOB NOT NULL,
    updated_at          TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS run_secrets (
    run_id                    TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
    encrypted_target_password BLOB,
    created_at                TEXT NOT NULL
);
