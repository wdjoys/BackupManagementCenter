-- 为进程日志增加可筛选的日志类型，兼容已有0008迁移产生的记录。
ALTER TABLE server_logs ADD COLUMN type TEXT NOT NULL DEFAULT 'system';
ALTER TABLE agent_logs ADD COLUMN type TEXT NOT NULL DEFAULT 'system';

CREATE INDEX idx_server_logs_type ON server_logs(type, id DESC);
CREATE INDEX idx_server_logs_level ON server_logs(level, id DESC);
CREATE INDEX idx_agent_logs_type ON agent_logs(agent_id, type, id DESC);
CREATE INDEX idx_agent_logs_level ON agent_logs(agent_id, level, id DESC);
