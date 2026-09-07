import type { Snapshot, Plan } from '@/api/types'
import type { BadgeTone } from '@/components/StatusBadge'

export interface DryRunResult {
  add: number
  changed: number
  skipped: number
  delete: number
  sample: string[]
}

export interface RestoreResponse {
  restore_request_id: string
  run_id: string
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
