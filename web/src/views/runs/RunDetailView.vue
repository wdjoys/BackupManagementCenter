<template>
  <div class="detail-page">
    <div class="page-head">
      <h2 class="page-title">{{ t('runDetail.title') }}</h2>
    </div>

    <div v-if="error" class="error-state">
      <el-icon><Warning /></el-icon>
      <p>{{ error }}</p>
      <el-button type="primary" @click="loadRun" style="margin-top: 12px">{{ t('common.retry') }}</el-button>
    </div>

    <template v-else-if="run">
      <!-- Status card -->
      <el-card shadow="never" class="status-card">
        <div class="status-grid">
          <div class="status-item">
            <span class="status-label">{{ t('runDetail.labels.status') }}</span>
            <el-tag
              :type="statusTagType(run.status)"
              :effect="isRunning(run.status) ? 'dark' : 'plain'"
            >
              {{ runStatusText(run.status) }}
            </el-tag>
          </div>
          <div class="status-item">
            <span class="status-label">{{ t('runDetail.labels.operation') }}</span>
            <el-tag :type="operationTagType(run.operation)" size="default">
              {{ operationText(run.operation) }}
            </el-tag>
          </div>
          <div class="status-item">
            <span class="status-label">{{ t('runDetail.labels.plan') }}</span>
            <span>{{ planName(run.plan_id) || run.plan_id }}</span>
          </div>
          <div class="status-item">
            <span class="status-label">{{ t('runDetail.labels.agent') }}</span>
            <span>{{ agentName(run.agent_id) || run.agent_id }}</span>
          </div>
          <div class="status-item" v-if="run.snapshot_id">
            <span class="status-label">{{ t('runDetail.labels.snapshot') }}</span>
            <span class="copyable" @click="copyText(run.snapshot_id!)">
              <el-icon><CopyDocument /></el-icon>
              {{ run.snapshot_id }}
            </span>
          </div>
          <div class="status-item" v-if="run.error_code">
            <span class="status-label">{{ t('runDetail.labels.error') }}</span>
            <el-tooltip :content="run.error_message || run.error_code" placement="top">
              <el-tag type="danger" size="default" effect="plain">
                {{ run.error_code }}
              </el-tag>
            </el-tooltip>
          </div>
          <div class="status-item" v-if="run.error_message && run.error_code === null">
            <span class="status-label">{{ t('runDetail.labels.message') }}</span>
            <span>{{ run.error_message }}</span>
          </div>
        </div>

        <!-- Timeline -->
        <div class="timeline">
          <el-steps
            :active="timelineStep"
            finish-status="success"
            align-center
          >
            <el-step
              :title="t('runDetail.timeline.queued')"
              :description="formatTime(run.queued_at)"
            />
            <el-step
              :title="t('runDetail.timeline.started')"
              :description="formatTime(run.started_at || '')"
            />
            <el-step
              :title="t('runDetail.timeline.finished')"
              :description="formatTime(run.finished_at || '')"
            />
          </el-steps>
        </div>
      </el-card>

      <!-- Progress section -->
      <el-card v-if="run.progress" shadow="never" class="progress-card">
        <template #header>
          <span>{{ t('runDetail.progress.title') }}</span>
        </template>
        <div class="progress-body">
          <el-progress
            :percentage="run.progress.percent"
            :status="run.status === 'failed' ? 'exception' : undefined"
            :stroke-width="20"
            style="margin-bottom: 12px"
          />
          <div class="progress-meta">
            <span class="phase-text"><strong>{{ t('runDetail.progress.phase') }}</strong> {{ phaseText(run.progress.phase) }}</span>
            <span v-if="hasBytes(run.progress)" class="bytes-text">
              <strong>{{ t('runDetail.progress.bytes') }}</strong> {{ formatBytes(run.progress.bytes_done) }} / {{ formatBytes(run.progress.bytes_total) }}
            </span>
            <span v-if="hasFiles(run.progress)" class="files-text">
              <strong>{{ t('runDetail.progress.files') }}</strong> {{ run.progress.files_done }} / {{ run.progress.files_total }}
            </span>
            <span v-if="run.started_at && !run.finished_at" class="duration-text">
              <strong>{{ t('runDetail.progress.elapsed') }}</strong> {{ formatDuration(run.started_at, new Date().toISOString()) }}<el-icon class="is-loading" style="margin-left:4px"><Loading /></el-icon>
            </span>
          </div>
        </div>
      </el-card>

      <!-- Logs panel -->
      <el-card shadow="never" class="logs-card">
        <template #header>
          <div class="logs-header">
            <span>{{ t('runDetail.logs.title', { count: logs.length }) }}</span>
            <div class="logs-controls">
              <el-checkbox v-model="autoScroll" :label="t('runDetail.logs.autoScroll')" />
              <el-button
                type="primary"
                size="small"
                :disabled="loadingLogs || !hasMoreLogs"
                @click="loadMoreLogs"
              >
                {{ loadingLogs ? t('runDetail.logs.loading') : t('runDetail.logs.loadMore') }}
              </el-button>
              <el-tag size="small" :type="wsConnected ? 'success' : 'info'" class="ws-tag">
                <el-icon v-if="wsConnected" class="is-loading"><Loading /></el-icon>
                <el-icon v-else-if="wsClosed"><Connection /></el-icon>
                {{ wsConnected ? t('runDetail.logs.streaming') : wsClosed ? t('runDetail.logs.connected') : t('runDetail.logs.offline') }}
              </el-tag>
            </div>
          </div>
        </template>

        <div class="logs-table-wrap" ref="logsWrapEl">
          <el-table
            :data="displayLogs"
            :max-height="400"
            stripe
            size="small"
          >
            <el-table-column :label="t('runDetail.logs.seq')" width="70">
              <template #default="{ row }">{{ row.seq }}</template>
            </el-table-column>
            <el-table-column :label="t('runDetail.logs.time')" width="180">
              <template #default="{ row }">{{ formatTime(row.timestamp) }}</template>
            </el-table-column>
            <el-table-column :label="t('runDetail.logs.level')" width="80">
              <template #default="{ row }">
                <span :class="`log-level level-${row.level}`">{{ row.level }}</span>
              </template>
            </el-table-column>
            <el-table-column :label="t('runDetail.logs.message')">
              <template #default="{ row }">
                <span class="log-message">{{ row.message }}</span>
              </template>
            </el-table-column>
          </el-table>
        </div>
      </el-card>
    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount, watch, nextTick, computed } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { apiGet } from '@/api/client'
import type { Run, RunLog, RunProgress, Plan, Agent } from '@/api/types'
import { Loading, Warning, CopyDocument, Connection } from '@element-plus/icons-vue'
import { formatDateTime, translateEnum } from '@/i18n'

interface WsStateMessage {
  type: 'state'
  run: Run
}

interface WsProgressMessage {
  type: 'progress'
  progress: RunProgress
}

interface WsLogMessage {
  type: 'log'
  entry: RunLog
}

type WsMessage = WsStateMessage | WsProgressMessage | WsLogMessage

const route = useRoute()
const { t } = useI18n()
const runId = computed(() => route.params.id as string)

const STATUS_VALUE_KEYS: Record<string, string> = {
  queued: 'runs.status.queued',
  dispatched: 'runs.status.dispatched',
  running: 'runs.status.running',
  succeeded: 'runs.status.succeeded',
  failed: 'runs.status.failed',
  cancelled: 'runs.status.cancelled',
}

const OPERATION_VALUE_KEYS: Record<string, string> = {
  backup: 'runs.operations.backup',
  restore: 'runs.operations.restore',
  check: 'runs.operations.check',
  forget: 'runs.operations.forget',
}

function runStatusText(status: string): string {
  return t(STATUS_VALUE_KEYS[status] ?? 'common.status')
}

function operationText(op: string): string {
  return t(OPERATION_VALUE_KEYS[op] ?? op)
}

function phaseText(phase: string): string {
  return translateEnum('runDetail.phases', phase)
}

const run = ref<Run | null>(null)
const plans = ref<Plan[]>([])
const agents = ref<Agent[]>([])
const logs = ref<RunLog[]>([])
const loadingLogs = ref(false)
const autoScroll = ref(true)
const hasMoreLogs = ref(false)
const logsWrapEl = ref<HTMLElement | null>(null)
const error = ref('')
const wsConnected = ref(false)
const wsClosed = ref(false)

const MAX_LOG_ROWS = 5000
let ws: WebSocket | null = null
let reconnectAttempts = 0
const MAX_RECONNECT = 1

// Computed helpers
const timelineStep = computed(() => {
  if (!run.value) return 0
  if (run.value.finished_at) return 3
  if (run.value.started_at) return 2
  return 1
})

const displayLogs = computed<RunLog[]>(() => {
  // Already capped at MAX_LOG_ROWS in pushLog
  return logs.value
})

async function loadRun(): Promise<void> {
  error.value = ''
  try {
    const data = await apiGet<Run>(`/runs/${runId.value}`)
    run.value = data
    wsClosed.value = false
    wsConnected.value = false
    if (isTerminal(data.status)) {
      wsClosed.value = true
    }
  } catch (err: any) {
    error.value = err.message || t('runDetail.loadFailed')
  }
}

async function loadPlansAndAgents(): Promise<void> {
  try {
    plans.value = await apiGet<Plan[]>('/plans')
  } catch {
    // non-critical
  }
  try {
    agents.value = await apiGet<Agent[]>('/agents')
  } catch {
    // non-critical
  }
}

async function loadInitialLogs(): Promise<void> {
  loadingLogs.value = true
  try {
    const data = await apiGet<RunLog[]>(`/runs/${runId.value}/logs`, {
      limit: 500,
    })
    logs.value = data
    if (data.length > 0) {
      hasMoreLogs.value = true
    }
  } catch {
    // If logs endpoint unavailable, keep existing
  } finally {
    loadingLogs.value = false
  }
}

async function loadMoreLogs(): Promise<void> {
  if (logs.value.length === 0 || loadingLogs.value) return
  loadingLogs.value = true
  try {
    const beforeSeq = Math.min(...logs.value.map((l) => l.seq)) - 1
    if (beforeSeq <= 0) {
      hasMoreLogs.value = false
      return
    }
    const data = await apiGet<RunLog[]>(`/runs/${runId.value}/logs`, {
      before_seq: String(beforeSeq),
      limit: 500,
    })
    if (data.length < 500) {
      hasMoreLogs.value = false
    }
    // Prepend and maintain order
    const merged = [...data, ...logs.value]
    // Ensure uniqueness by seq
    const seqMap = new Map<number, RunLog>()
    for (const log of merged) {
      seqMap.set(log.seq, log)
    }
    const unique = Array.from(seqMap.values()).sort((a, b) => a.seq - b.seq)
    if (unique.length > MAX_LOG_ROWS) {
      unique.splice(0, unique.length - MAX_LOG_ROWS)
    }
    logs.value = unique
  } catch {
    // non-fatal
  } finally {
    loadingLogs.value = false
  }
}

function pushLog(entry: RunLog): void {
  // Avoid duplicates
  const idx = logs.value.findIndex((l) => l.seq === entry.seq)
  if (idx >= 0) {
    logs.value[idx] = entry
    return
  }
  logs.value.push(entry)
  if (logs.value.length > MAX_LOG_ROWS) {
    logs.value.splice(0, logs.value.length - MAX_LOG_ROWS)
  }
  if (autoScroll.value) {
    nextTick(() => {
      const el = logsWrapEl.value
      if (el) el.scrollTop = el.scrollHeight
    })
  }
}

function pushProgress(progress: RunProgress): void {
  if (run.value) {
    run.value = { ...run.value, progress }
  }
}

function pushState(r: Run): void {
  run.value = r
  if (isTerminal(r.status)) {
    wsClosed.value = true
    closeWs()
  }
}

function connectWs(): void {
  const id = runId.value
  // A detail view can briefly be mounted while the router is resolving a
  // redirect. Never open a socket for an invalid ID; it only creates noisy
  // /ws/runs/undefined requests and cannot deliver run events.
  if (!id || id === 'undefined' || id === 'null') {
    closeWs()
    wsClosed.value = true
    return
  }
  if (ws) {
    ws.close()
    ws = null
  }
  reconnectAttempts = 0
  wsConnected.value = false
  const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
  const url = `${protocol}//${location.host}/ws/runs/${encodeURIComponent(id)}`
  try {
    ws = new WebSocket(url)
  } catch {
    wsClosed.value = true
    return
  }

  ws.addEventListener('open', () => {
    wsConnected.value = true
    wsClosed.value = false
  })

  ws.addEventListener('message', (event: MessageEvent) => {
    try {
      const msg = JSON.parse(event.data) as WsMessage
      switch (msg.type) {
        case 'state':
          pushState(msg.run)
          break
        case 'progress':
          pushProgress(msg.progress)
          break
        case 'log':
          pushLog(msg.entry)
          break
        default:
          // Unknown type, ignore
          break
      }
    } catch {
      // Malformed message, skip
    }
  })

  ws.addEventListener('close', () => {
    wsConnected.value = false
    if (run.value && isTerminal(run.value.status)) {
      wsClosed.value = true
      return
    }
    // Reconnect once
    if (reconnectAttempts < MAX_RECONNECT) {
      reconnectAttempts++
      ws = null
      wsConnected.value = false
      setTimeout(connectWs, 3000)
    } else {
      wsClosed.value = true
    }
  })

  ws.addEventListener('error', () => {
    wsConnected.value = false
  })
}

function closeWs(): void {
  if (ws) {
    ws.close()
    ws = null
  }
  wsConnected.value = false
}

function isTerminal(status: Run['status']): boolean {
  return status === 'succeeded' || status === 'failed' || status === 'cancelled'
}

function currentRunTerminal(): boolean {
  const r = run.value
  return !r || isTerminal(r.status)
}

function isRunning(status: Run['status']): boolean {
  return status === 'running' || status === 'dispatched'
}

function hasBytes(p: RunProgress): boolean {
  return p.bytes_total > 0 || p.bytes_done > 0
}

function hasFiles(p: RunProgress): boolean {
  return p.files_total > 0 || p.files_done > 0
}

function planName(planId: string): string | undefined {
  return plans.value.find((p) => p.id === planId)?.name
}

function agentName(agentId: string): string | undefined {
  return agents.value.find((a) => a.id === agentId)?.name
}

function formatTime(iso: string | null | undefined): string {
  if (!iso) return '—'
  return formatDateTime(iso, { second: '2-digit' })
}

function formatBytes(bytes: number): string {
  if (bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  const idx = Math.min(i, units.length - 1)
  return (bytes / Math.pow(1024, idx)).toFixed(1) + ' ' + units[idx]
}

function formatDuration(started: string, finished: string): string {
  const start = new Date(started).getTime()
  const finish = new Date(finished).getTime()
  if (isNaN(start) || isNaN(finish)) return '—'
  const diff = Math.max(0, finish - start)
  const totalSec = Math.floor(diff / 1000)
  const h = Math.floor(totalSec / 3600)
  const m = Math.floor((totalSec % 3600) / 60)
  const s = totalSec % 60
  if (totalSec < 60) return `${s}${t('time.secShort')}`
  if (h > 0) return `${h}${t('time.hourShort')} ${m}${t('time.minShort')} ${s}${t('time.secShort')}`
  if (m > 0) return `${m}${t('time.minShort')} ${s}${t('time.secShort')}`
  return `${s}${t('time.secShort')}`
}

function statusTagType(status: Run['status']): 'success' | 'danger' | 'primary' | 'warning' | 'info' {
  switch (status) {
    case 'succeeded':
      return 'success'
    case 'failed':
      return 'danger'
    case 'running':
    case 'dispatched':
      return 'primary'
    case 'cancelled':
      return 'warning'
    default:
      return 'info'
  }
}

function operationTagType(op: Run['operation']): 'success' | 'danger' | 'primary' | 'warning' | 'info' {
  switch (op) {
    case 'backup':
      return 'success'
    case 'restore':
      return 'primary'
    case 'check':
      return 'info'
    case 'forget':
      return 'warning'
    default:
      return 'info'
  }
}

async function copyText(text: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    // fail silently
  }
}

// Watch for runId change (route change)
watch(
  runId,
  async (newId) => {
    if (!newId) return
    closeWs()
    logs.value = []
    run.value = null
    error.value = ''
    await loadRun()
    await loadInitialLogs()
    if (!error.value && !currentRunTerminal()) {
      connectWs()
    }
  },
)

onMounted(async () => {
  await loadPlansAndAgents()
  await loadRun()
  if (!error.value) {
    await loadInitialLogs()
    if (run.value && !isTerminal(run.value.status)) {
      connectWs()
    }
  }
})

onBeforeUnmount(() => {
  closeWs()
})
</script>

<style scoped>
.detail-page {
  max-width: 1400px;
  margin: 0 auto;
}
.page-head {
  margin-bottom: 16px;
}
.page-title {
  margin: 0;
  font-size: 22px;
  font-weight: 600;
}
.status-card {
  margin-bottom: 16px;
}
.status-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 12px;
  margin-bottom: 20px;
}
.status-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.status-label {
  font-size: 12px;
  color: #909399;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}
.timeline {
  padding-top: 16px;
  border-top: 1px solid #ebeef5;
}
.progress-card {
  margin-bottom: 16px;
}
.progress-body {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.progress-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 16px;
  font-size: 13px;
  color: #303133;
}
.phase-text,
.bytes-text,
.files-text,
.duration-text {
  white-space: nowrap;
}
.copyable {
  cursor: pointer;
  font-family: 'Courier New', Courier, monospace;
  font-size: 13px;
  color: #409eff;
  user-select: none;
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
.copyable:hover {
  color: #2b7fd5;
}
.logs-card {
  margin-bottom: 16px;
}
.logs-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.logs-controls {
  display: flex;
  align-items: center;
  gap: 12px;
}
.ws-tag {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
.log-table-wrap {
  font-family: 'Courier New', Courier, monospace;
}
.log-message {
  white-space: pre-wrap;
  word-break: break-all;
}
.log-level {
  font-weight: 600;
  font-size: 11px;
  text-transform: uppercase;
}
.level-error {
  color: #f56c6c;
}
.level-warn,
.level-warning {
  color: #e6a23c;
}
.level-info {
  color: #409eff;
}
.level-debug,
.level-trace {
  color: #909399;
}
.level-fatal {
  color: #f56c6c;
}
.error-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  padding: 40px;
  color: #f56c6c;
}
</style>
