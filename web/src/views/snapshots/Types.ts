import type { Snapshot, Plan } from '@/api/types'
import type { BadgeTone } from '@/components/StatusBadge'

export interface DryRunResult {
  add: number
  changed: number
  skipped: number
  delete: number
  sample: string[]
}

export interface BreadcrumbPart {
  label: string
  path: string
}

export interface SnapshotView {
  raw: Snapshot
  planID: string
  planName: string
  plan?: Plan
  kind: string
  kindLabel: string
  kindTone: BadgeTone
  sourceSummary: string
  agentDisplay: { name: string; hostname: string }
  runID: string
  extraTags: string[]
}

export const ALL_PLANS_FILTER = 'all'
export const DELETED_PLANS_FILTER = 'deleted'
export const UNASSIGNED_PLAN_FILTER = 'unassigned'

export function formatSize(bytes: number): string {
  if (bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  return `${(bytes / 1024 ** i).toFixed(1)} ${units[i]}`
}

/** 与后端 secrets.HashToken 等价的 SHA-256 十六进制摘要（用于覆盖/新建确认）。 */
export async function hashConfirmation(value: string): Promise<string> {
  const data = new TextEncoder().encode(value)
  const digest = await crypto.subtle.digest('SHA-256', data)
  return Array.from(new Uint8Array(digest))
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('')
}

/** 数据库恢复的阶段语义：不安全阶段会阻塞后续恢复并需要人工介入。 */
export const RESTORE_PHASE_TONES: Record<string, BadgeTone> = {
  queued: 'secondary',
  pre_backup: 'warning',
  restoring: 'warning',
  rolling_back: 'warning',
  cancelling: 'warning',
  succeeded: 'success',
  failed: 'outline',
  pre_backup_failed: 'outline',
  rolled_back: 'outline',
  new_target_cleaned: 'outline',
  rollback_failed: 'destructive',
  manual_recovery_required: 'destructive',
  manual_recovery_resolved: 'outline',
}

export function isRestorePhaseBlocking(phase?: string): boolean {
  return phase === 'rollback_failed' || phase === 'manual_recovery_required'
}

export function isRestorePhaseUnresolved(phase?: string): boolean {
  if (!phase) return false
  return ![
    'succeeded',
    'failed',
    'pre_backup_failed',
    'rolled_back',
    'new_target_cleaned',
    'manual_recovery_resolved',
  ].includes(phase)
}
