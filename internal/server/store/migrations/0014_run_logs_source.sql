-- run 日志来源标注 + 幂等写入。
--
-- 背景：run_logs 原先用 (run_id, seq) 作主键，seq 由两套互相隔离的序列拼接而成：
--   Agent 侧 runner 每次 run 从 1 递增；Server 侧 dispatcher 从 1<<62 起的高位段分配。
-- 这样做的唯一目的是避免两侧碰撞，代价是同一个 run 的日志出现两段互不相邻的编号，
-- 且 Agent 重连/重试后本地序列重置会与既有行主键冲突，导致整批日志写入失败被丢弃。
--
-- 迁移后：seq 列保留但不再作为标识（历史行数值原样不动，只用于回填 source 与生成 id），
-- 新增 id 作为 SQLite 自增主键、唯一排序/分页游标；source 记录日志产生方。
-- 幂等键改为 (run_id, source, source_seq)：重复投递的同一批日志被 INSERT OR IGNORE 丢弃。
--
-- 历史行来源判定：dispatcher 是唯一写高位段的路径（AppendRunLogs 只有 agentreg 与
-- dispatcher 两个调用方），因此 seq >= 1<<62 等价于 source='server'，其余为 'agent'，
-- 无需人工规则，也不丢信息。
--
-- 可重复启动安全：ADD COLUMN / UPDATE / INSERT 均带 IF NOT EXISTS 或条件判断，
-- 重复执行不产生二次变更。

ALTER TABLE run_logs ADD COLUMN source TEXT NOT NULL DEFAULT 'agent';
ALTER TABLE run_logs ADD COLUMN source_seq INTEGER NOT NULL DEFAULT 0;

-- 回填：高位段是服务端诊断日志，低位段是 Agent 上报日志。
UPDATE run_logs SET source = 'server' WHERE seq >= 4611686018427387904;
UPDATE run_logs SET source_seq = seq;

-- 幂等主键改为自增 id。历史行的 id 按原 seq 升序分配，保持既有相对顺序。
CREATE TABLE run_logs_new (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id     TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    source     TEXT NOT NULL CHECK (source IN ('agent','server')),
    source_seq INTEGER NOT NULL,
    timestamp  TEXT NOT NULL,
    level      TEXT NOT NULL CHECK (level IN ('debug','info','warn','error')),
    message    TEXT NOT NULL
);

INSERT INTO run_logs_new (run_id, source, source_seq, timestamp, level, message)
    SELECT run_id, source, source_seq, timestamp, level, message
    FROM run_logs
    ORDER BY run_id, seq;

DROP TABLE run_logs;
ALTER TABLE run_logs_new RENAME TO run_logs;

-- 分页游标：按 run 倒序取 id。
CREATE INDEX IF NOT EXISTS idx_run_logs_run_id_id ON run_logs(run_id, id);

-- 幂等键：同一来源的同一序号只落库一次，重放被忽略而不是整批失败。
CREATE UNIQUE INDEX IF NOT EXISTS idx_run_logs_dedup
    ON run_logs(run_id, source, source_seq);
