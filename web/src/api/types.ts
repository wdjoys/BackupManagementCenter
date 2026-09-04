export interface ApiError {
  code: string
  message: string
  status?: number
}

export interface PathMapping {
  host_path: string
  runtime_path: string
  read_only: boolean
}

export interface Agent {
  id: string
  name: string
  hostname: string
  os: string
  arch: string
  version: string
  status: 'online' | 'offline'
  revoked?: boolean
  last_seen_at: string
  enrolled_at: string
  capabilities: ToolCapability[]
  source_path_mappings: PathMapping[]
  restore_path_mappings: PathMapping[]
}

export interface ToolCapability {
  name: string
  path: string
  version: string
}

export interface StorageTarget {
  id: string
  name: string
  type: 'rclone'
  remote_name: string
  remote_path: string
  created_at: string
  updated_at: string
}

export interface Repository {
  id: string
  agent_id: string
  storage_target_id: string
  repository_path: string
  status: 'ready' | 'pending' | 'error'
  last_check_at: string | null
  agent_name?: string
  storage_target_name?: string
}

export interface Snapshot {
  id: string
  time: string
  host: string
  tags: string[]
  paths: string[]
}

export interface SnapshotDeletionResponse {
  deletion_id: string
  status: 'pending' | 'running' | 'succeeded'
}

export interface TreeEntry {
  name: string
  type: 'dir' | 'file'
  size: number
  mtime: string
}

export interface TreeResponse {
  entries: TreeEntry[]
  path: string
}

export interface PlanSource {
  paths?: string[]
  excludes?: string[]
  one_file_system?: boolean
  host?: string
  port?: number
  username?: string
  database?: string
  auth_source?: string
  extra_args?: string[]
  estimated_dump_bytes?: number
  path?: string
  capture_oplog?: boolean
}

export interface Retention {
  keep_last: number
  keep_daily: number
  keep_weekly: number
  keep_monthly: number
}

export interface Plan {
  id: string
  name: string
  agent_id: string
  kind: 'filesystem' | 'postgresql' | 'mysql' | 'mongodb' | 'sqlite'
  schedule: string
  timezone: string
  enabled: boolean
  source: PlanSource
  credentials?: { password_set: boolean }
  repository_id: string
  retention: Retention
  timeout_seconds: number
  last_run_at: string | null
  created_at: string
  updated_at: string
}

export interface RunProgress {
  phase: string
  percent: number
  bytes_done: number
  bytes_total: number
  files_done: number
  files_total: number
}

export interface Run {
  id: string
  plan_id: string
  agent_id: string
  operation: 'backup' | 'restore' | 'check' | 'forget'
  status: 'queued' | 'dispatched' | 'running' | 'succeeded' | 'failed' | 'cancelled'
  queued_at: string
  started_at: string | null
  finished_at: string | null
  progress: RunProgress | null
  snapshot_id: string | null
  error_code: string | null
  error_message: string | null
  attempt?: number
  lease_expires_at?: string | null
}

export interface RunLog {
  seq: number
  timestamp: string
  level: string
  message: string
}
export interface SystemLog {
  id: number
  agent_id?: string
  source_seq?: number
  timestamp: string
  type: string
  level: 'debug' | 'info' | 'warn' | 'error' | string
  message: string
}


export interface RestoreRequest {
  id: string
  run_id: string
  snapshot_id: string
  restore_kind: 'filesystem' | 'postgresql' | 'mysql' | 'mongodb' | 'sqlite'
  target: RestoreTarget
  overwrite: boolean
  created_at: string
}

export type RestoreTarget =
  | { target_path: string; include_paths: string[]; overwrite_mode: 'never' | 'if-changed' | 'always' }
  | { host: string; port: number; username: string; database: string; auth_source?: string }

export interface RestoreDryRun {
  add: number
  changed: number
  skipped: number
  delete: number
  sample: string[]
}

export interface RestoreResponse {
  restore_request_id: string
  run_id: string
  pre_restore_run_id?: string
  rollback_snapshot_id?: string
  phase?: string
}

export interface Dashboard {
  agents_online: number
  agents_total: number
  runs_24h_succeeded: number
  runs_24h_failed: number
  next_scheduled: ScheduledPlan[]
  repos_needing_check: RepoCheck[]
}

export interface ScheduledPlan {
  plan_id: string
  plan_name: string
  next_fire_at: string
}

export interface RepoCheck {
  id: string
  name: string
  last_check_at: string | null
}

export interface EnrollmentToken {
  id: string
  expires_at: string
  used_at: string | null
}

export interface EnrollmentTokenResponse {
  token: string
  expires_at: string
  target_agent_id?: string
}

export interface SetupStatus {
  initialized: boolean
}

export interface AuthUser {
  username: string
}

export interface TelegramSettings {
  configured: boolean
  chat_id?: string
  updated_at?: string
}

export interface TelegramSettingsUpdate {
  bot_token: string
  chat_id: string
}

export interface PaginationParams {
  limit?: number
  offset?: number
  before_seq?: number
}

export interface AgentQueryParams {
  agent_id?: string
}

export interface PlanQueryParams {
  agent_id?: string
}

export interface RunQueryParams {
  plan_id?: string
  agent_id?: string
  status?: string
  operation?: string
  limit?: number
  offset?: number
}

export interface PlanValidateRequest {
  kind: string
  source: PlanSource
  agent_id: string
}

export interface StorageTargetValidateRequest {
  rclone_conf: string
  remote_name: string
  validate_agent_id: string
}

export interface StorageTargetValidateResponse {
  remote_type: string
  lsd_entries: Array<{ name: string; is_dir: boolean }>
}

export interface CreatePlanRequest {
  name: string
  agent_id: string
  kind: string
  schedule: string
  timezone: string
  source: PlanSource
  repository_id: string
  retention: Retention
  timeout_seconds: number
}

export interface CreateRepositoryRequest {
  agent_id: string
  storage_target_id: string
}

export interface CreateRestoreRequest {
  repository_id: string
  snapshot_id: string
  restore_kind: string
  target: RestoreTarget
  overwrite: boolean
  confirmation?: string
}

export interface DryRunRequest {
  repository_id: string
  snapshot_id: string
  include_paths: string[]
  target_path: string
  overwrite_mode: 'never' | 'if-changed' | 'always'
}
