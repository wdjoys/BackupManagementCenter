import type { Plan, PlanSource, Retention } from '@/api/types'

export type PlanKind = Plan['kind']

/** Editable form shape. `password` is only sent on create (or when re-entered);
    the server never returns it on GET. `port` / `estimated_dump_bytes` allow
    `null` so they can bind directly to el-input-number's model value type. */
export interface PlanFormSource {
  paths?: string[]
  excludes?: string[]
  one_file_system?: boolean
  host?: string
  port?: number | null
  username?: string
  password?: string
  database?: string
  auth_source?: string
  extra_args?: string[]
  estimated_dump_bytes?: number | null
  path?: string
  capture_oplog?: boolean
}

export interface PlanFormModel {
  id?: string
  name: string
  agent_id: string
  kind: PlanKind
  schedule: string
  timezone: string
  enabled: boolean
  source: PlanFormSource
  repository_id: string
  retention: Retention
  timeout_seconds: number
}

export interface ValidateResult {
  ok: boolean
  code?: string
  message?: string
  tools?: string[]
}

/** Builds the edit/create payload from the form model. Arrays are trimmed and
    emptied of blank entries; `enabled` goes out only on updates (PUT). */
export function buildPayload(model: PlanFormModel): Record<string, unknown> {
  const source: PlanFormSource = { ...model.source }
  if (source.paths) source.paths = filterTrimmed(source.paths)
  if (source.excludes) source.excludes = filterTrimmed(source.excludes)
  if (source.extra_args) source.extra_args = filterTrimmed(source.extra_args)
  if (source.port == null) delete source.port
  if (source.estimated_dump_bytes == null) delete source.estimated_dump_bytes
	const password = source.password
	delete source.password

  const payload: Record<string, unknown> = {
    name: model.name.trim(),
    agent_id: model.agent_id,
    kind: model.kind,
    schedule: model.schedule.trim(),
    timezone: model.timezone.trim(),
    source: source as PlanSource,
    repository_id: model.repository_id,
    retention: normalizeRetention(model.retention),
    timeout_seconds: model.timeout_seconds,
  }
	if (model.id) payload.enabled = model.enabled
	if (password) payload.credentials = { password }
	return payload
}

function normalizeRetention(retention: Retention): Retention {
  return {
    keep_last: retention.keep_last ?? 0,
    keep_daily: retention.keep_daily ?? 0,
    keep_weekly: retention.keep_weekly ?? 0,
    keep_monthly: retention.keep_monthly ?? 0,
  }
}

/** Payload for POST /plans/validate — never includes the password. */
export function buildValidatePayload(model: PlanFormModel): Record<string, unknown> {
  const source: PlanFormSource = { ...model.source }
  if (source.paths) source.paths = filterTrimmed(source.paths)
  if (source.excludes) source.excludes = filterTrimmed(source.excludes)
  if (source.extra_args) source.extra_args = filterTrimmed(source.extra_args)
  if (source.port == null) delete source.port
  if (source.estimated_dump_bytes == null) delete source.estimated_dump_bytes
  delete source.password
  return { kind: model.kind, source: source as PlanSource, agent_id: model.agent_id }
}

function filterTrimmed(values: string[]): string[] {
  const out: string[] = []
  for (const v of values) {
    const t = v.trim()
    if (t) out.push(t)
  }
  return out
}
