-- 0010_snapshot_caches.sql — 快照列表、目录和持久化 generation。
CREATE TABLE repository_cache_state (
    repository_id     TEXT PRIMARY KEY REFERENCES repositories(id) ON DELETE CASCADE,
    generation        INTEGER NOT NULL DEFAULT 0,
    list_verified_at  TEXT,
    list_fingerprint  TEXT NOT NULL DEFAULT '',
    updated_at        TEXT NOT NULL
);

CREATE TABLE snapshot_list_cache (
    repository_id TEXT PRIMARY KEY REFERENCES repositories(id) ON DELETE CASCADE,
    generation    INTEGER NOT NULL,
    snapshots_json TEXT NOT NULL,
    fingerprint   TEXT NOT NULL,
    verified_at   TEXT NOT NULL
);
CREATE INDEX idx_snapshot_list_cache_verified ON snapshot_list_cache(repository_id, verified_at);

CREATE TABLE snapshot_tree_cache (
    repository_id TEXT NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    snapshot_id   TEXT NOT NULL,
    path          TEXT NOT NULL,
    generation    INTEGER NOT NULL,
    tree_json     TEXT NOT NULL,
    verified_at   TEXT NOT NULL,
    PRIMARY KEY (repository_id, snapshot_id, path)
);
CREATE INDEX idx_snapshot_tree_cache_generation ON snapshot_tree_cache(repository_id, generation);

INSERT INTO repository_cache_state (repository_id, generation, list_verified_at, list_fingerprint, updated_at)
SELECT id, 0, NULL, '', CURRENT_TIMESTAMP FROM repositories
WHERE NOT EXISTS (SELECT 1 FROM repository_cache_state c WHERE c.repository_id = repositories.id);
