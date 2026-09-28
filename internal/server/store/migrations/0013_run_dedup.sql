-- 队列去重：同一任务参数在未终结（queued/dispatched/running）期间只允许存在一个 run。
-- dedup_key 由 jobs 层写入，格式见 jobs.manualRunDedupKey / systemRunDedupKey /
-- restoreRunDedupKey：
--   plan<NUL><planID>                                        手动运行同一计划
--   sys<NUL><operation><NUL><agentID><NUL><repositoryID><NUL><paramsJSON>[<NUL><confHash>]
--   restore<NUL><repoID><NUL><snapshotID><NUL><kind><NUL><targetJSON><NUL><overwrite><NUL><credentialHMAC>
-- 口令/临时配置只以“密钥化指纹”形式参与（secrets.Sealer.Fingerprint = master key 的
-- HMAC-SHA256）；列内不含任何明文 secret，也无法在只拿到数据库时离线穷举。轮换
-- master key 会让已有指纹失配，只影响未终结 run 的去重窗口，不影响数据。
--
-- 旧库既有行 dedup_key 为 NULL（NULL 互不相等，不参与去重）；run 进入终态后即
-- 离开该部分索引，因此取消/失败/成功之后可以再次发起同样的任务。
ALTER TABLE runs ADD COLUMN dedup_key TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS idx_runs_active_dedup
    ON runs(dedup_key)
    WHERE dedup_key IS NOT NULL AND status IN ('queued','dispatched','running');
