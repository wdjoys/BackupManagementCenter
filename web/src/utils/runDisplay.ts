import type { BadgeTone } from '@/components/StatusBadge'

export const STATUS_VALUE_KEYS: Record<string, string> = {
  queued: 'status.queued',
  dispatched: 'status.dispatched',
  running: 'status.running',
  succeeded: 'status.succeeded',
  failed: 'status.failed',
  cancelled: 'status.cancelled',
}

export const OPERATION_VALUE_KEYS: Record<string, string> = {
  backup: 'runs.operations.backup',
  restore: 'runs.operations.restore',
  check: 'runs.operations.check',
  forget: 'runs.operations.forget',
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
  if (bytes === null || bytes === undefined || bytes === 0) return '0 B'
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
