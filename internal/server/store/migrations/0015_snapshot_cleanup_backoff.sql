-- 孤儿扫描失败退避：原先 ClearSnapshotCleanupScan 接收 nextAttemptAt 参数但
-- 状态表没有对应列，参数被静默忽略，导致扫描失败后每个 scheduler tick 立即
-- 重发，形成无上限重试（7 天可累积数万 run / 数百万日志）。
ALTER TABLE snapshot_cleanup_state ADD COLUMN next_attempt_at TEXT;
