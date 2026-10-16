-- Keep repository credentials when an operator unbinds a repository. This is
-- a logical detach so remote Restic data remains adoptable on a later bind.
ALTER TABLE repositories ADD COLUMN detached_at TEXT;

CREATE INDEX IF NOT EXISTS idx_repositories_detached ON repositories(detached_at);
