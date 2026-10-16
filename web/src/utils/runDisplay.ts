import type { BadgeTone } from '@/components/StatusBadge'

/** 运行状态取值（渲染文案统一走 translateEnum('runs.status', value)）。
    此前这里映射到 status.* 命名空间，但该命名空间只有 Agent 状态，导致
    状态筛选下拉渲染出字面 key（status.queued 等）。 */
export const RUN_STATUS_VALUES: readonly string[] = [
  'queued',
  'dispatched',
  'running',
  'succeeded',
  'failed',
  'cancelled',
]

/** 运行操作类型取值，须与后端 internal/model/model.go 的 Op* 常量一一对应；
    此前只列了 4 个，其余 6 个操作无法在筛选下拉中选择。 */
export const OPERATION_VALUE_KEYS: Record<string, string> = {
  backup: 'runs.operations.backup',
  restore: 'runs.operations.restore',
  restore_dry_run: 'runs.operations.restore_dry_run',
  check: 'runs.operations.check',
  forget: 'runs.operations.forget',
  snapshots: 'runs.operations.snapshots',
  snapshot_ls: 'runs.operations.snapshot_ls',
  verify_storage_remote: 'runs.operations.verify_storage_remote',
  validate_paths: 'runs.operations.validate_paths',
  probe_capabilities: 'runs.operations.probe_capabilities',
}

export function statusTagType(status: string): BadgeTone {
  switch (status) {
    case 'succeeded':
      return 'success'
    case 'failed':
      return 'destructive'
    case 'running':
    case 'dispatched':
      return 'default'
    case 'queued':
      return 'warning'
    case 'cancelled':
      return 'secondary'
    default:
      return 'outline'
  }
}

export function operationTagType(operation: string): BadgeTone {
  switch (operation) {
    case 'backup':
      return 'default'
    case 'restore':
      return 'warning'
    case 'check':
      return 'secondary'
    case 'forget':
      return 'outline'
    default:
      return 'outline'
  }
}

export function formatBytes(bytes: number | null | undefined): string {
  if (bytes === null || bytes === undefined) return '—'
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  return `${(bytes / Math.pow(1024, i)).toFixed(2)} ${units[i]}`
}

export function formatDuration(startedAt: string | null, finishedAt: string | null): string {
  if (!startedAt) return '-'
  const start = new Date(startedAt).getTime()
  const end = finishedAt ? new Date(finishedAt).getTime() : Date.now()
  const sec = Math.max(0, Math.floor((end - start) / 1000))
  if (sec < 60) return `${sec}s`
  const min = Math.floor(sec / 60)
  const remSec = sec % 60
  if (min < 60) return `${min}m ${remSec}s`
  const hr = Math.floor(min / 60)
  const remMin = min % 60
  return `${hr}h ${remMin}m`
}
