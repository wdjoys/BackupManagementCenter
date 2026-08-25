-- 进程级日志。ID由Server分配，保证分页和重启后的顺序稳定。
CREATE TABLE server_logs (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp TEXT NOT NULL,
    level     TEXT NOT NULL CHECK (level IN ('debug','info','warn','error')),
    message   TEXT NOT NULL
);

CREATE INDEX idx_server_logs_id ON server_logs(id DESC);

CREATE TABLE agent_logs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id    TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    source_seq  INTEGER NOT NULL,
    timestamp   TEXT NOT NULL,
    level       TEXT NOT NULL CHECK (level IN ('debug','info','warn','error')),
    message     TEXT NOT NULL
);

CREATE INDEX idx_agent_logs_agent_id ON agent_logs(agent_id, id DESC);
