import type { PlanKind, PlanFormSource } from './Types'

export const KIND_LABELS: Record<PlanKind, string> = {
  filesystem: 'plans.kinds.filesystem',
  postgresql: 'plans.kinds.postgresql',
  mysql: 'plans.kinds.mysql',
  mongodb: 'plans.kinds.mongodb',
  sqlite: 'plans.kinds.sqlite',
}

export const KIND_TAG_TYPE: Record<PlanKind, 'primary' | 'success' | 'warning' | 'danger' | 'info'> = {
  filesystem: 'info',
  postgresql: 'success',
  mysql: 'warning',
  mongodb: 'danger',
  sqlite: 'primary',
}

export const CRON_PRESETS: readonly { key: string; value: string }[] = [
  { key: 'plans.cronPresets.every15Minutes', value: '*/15 * * * *' },
  { key: 'plans.cronPresets.hourly', value: '0 * * * *' },
  { key: 'plans.cronPresets.daily3am', value: '0 3 * * *' },
  { key: 'plans.cronPresets.daily12pm', value: '0 12 * * *' },
]

export const IANA_TIMEZONES: readonly string[] = [
  'UTC',
  'Africa/Johannesburg',
  'Africa/Lagos',
  'Africa/Nairobi',
  'America/Anchorage',
  'America/Argentina/Buenos_Aires',
  'America/Bogota',
  'America/Caracas',
  'America/Chicago',
  'America/Denver',
  'America/Los_Angeles',
  'America/Mexico_City',
  'America/New_York',
  'America/Sao_Paulo',
  'America/Toronto',
  'Asia/Bangkok',
  'Asia/Calcutta',
  'Asia/Colombo',
  'Asia/Dubai',
  'Asia/Hong_Kong',
  'Asia/Jakarta',
  'Asia/Manila',
  'Asia/Seoul',
  'Asia/Shanghai',
  'Asia/Singapore',
  'Asia/Taipei',
  'Asia/Tokyo',
  'Asia/Ulaanbaatar',
  'Asia/Urumqi',
  'Australia/Brisbane',
  'Australia/Melbourne',
  'Australia/Sydney',
  'Europe/Athens',
  'Europe/Berlin',
  'Europe/Brussels',
  'Europe/Dublin',
  'Europe/Helsinki',
  'Europe/London',
  'Europe/Madrid',
  'Europe/Moscow',
  'Europe/Paris',
  'Europe/Stockholm',
  'Europe/Warsaw',
  'Pacific/Auckland',
  'Pacific/Fiji',
  'Pacific/Honolulu',
]

export const DEFAULT_DB_PORTS: Record<PlanKind, number> = {
  filesystem: 0,
  postgresql: 5432,
  mysql: 3306,
  mongodb: 27017,
  sqlite: 0,
}

export const CRON5_RE = /^\S+\s+\S+\s+\S+\s+\S+\s+\S+$/

export function defaultSource(kind: PlanKind): PlanFormSource {
  if (kind === 'filesystem') {
    return { paths: [], excludes: [], one_file_system: false }
  }
  if (kind === 'sqlite') {
    return { path: '', estimated_dump_bytes: undefined }
  }
  if (kind === 'mongodb') {
    return {
      host: '',
      port: 27017,
      username: '',
      database: 'all',
      extra_args: [],
      estimated_dump_bytes: undefined,
      capture_oplog: false,
      password: '',
    }
  }
  return {
    host: '',
    port: DEFAULT_DB_PORTS[kind],
    username: '',
    database: 'all',
    extra_args: [],
    estimated_dump_bytes: undefined,
    password: '',
  }
}