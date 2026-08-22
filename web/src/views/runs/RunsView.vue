<template>
  <div>
    <div class="page-head">
      <h2 class="page-title">Runs</h2>
    </div>

    <el-card class="filter-card" shadow="never">
      <el-form :model="filters" inline>
        <el-form-item label="Plan">
          <el-input
            v-model="filters.plan_id"
            placeholder="Plan ID"
            clearable
            style="width: 220px"
            @input="debounceReset"
          />
        </el-form-item>
        <el-form-item label="Agent">
          <el-select
            v-model="filters.agent_id"
            placeholder="Agent"
            clearable
            filterable
            style="width: 220px"
            @change="onFilterChange"
          >
            <el-option
              v-for="a in agents"
              :key="a.id"
              :label="`${a.name} (${a.hostname})`"
              :value="a.id"
            />
          </el-select>
        </el-form-item>
        <el-form-item label="Status">
          <el-select
            v-model="filters.status"
            placeholder="Status"
            clearable
            style="width: 160px"
            @change="onFilterChange"
          >
            <el-option
              v-for="s in statusOptions"
              :key="s.value"
              :label="s.label"
              :value="s.value"
            />
          </el-select>
        </el-form-item>
        <el-form-item label="Operation">
          <el-select
            v-model="filters.operation"
            placeholder="Operation"
            clearable
            style="width: 160px"
            @change="onFilterChange"
          >
            <el-option
              v-for="o in operationOptions"
              :key="o.value"
              :label="o.label"
              :value="o.value"
            />
          </el-select>
        </el-form-item>
        <el-form-item>
          <el-button type="primary" @click="loadRuns">Search</el-button>
          <el-button @click="resetFilters">Reset</el-button>
        </el-form-item>
      </el-form>
    </el-card>

    <div v-if="error" class="error-state">
      <el-icon><Warning /></el-icon>
      <p>{{ error }}</p>
      <el-button type="primary" @click="loadRuns" style="margin-top: 12px">Retry</el-button>
    </div>

    <div v-else-if="loading" style="text-align: center; padding: 40px">
      <el-icon class="is-loading"><Loading /></el-icon>
    </div>

    <el-table
      v-else
      :data="runs"
      stripe
      row-key="id"
      style="width: 100%"
      @row-click="onRowClick"
    >
      <el-table-column label="Queued At" width="200" sortable>
        <template #default="{ row }">
          {{ formatTime(row.queued_at) }}
        </template>
      </el-table-column>
      <el-table-column label="Plan" width="180">
        <template #default="{ row }">
          {{ planName(row.plan_id) || row.plan_id }}
        </template>
      </el-table-column>
      <el-table-column label="Operation" width="100">
        <template #default="{ row }">
          <el-tag :type="operationTagType(row.operation)" size="small">
            {{ row.operation }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="Status" width="120">
        <template #default="{ row }">
          <el-tag
            :type="statusTagType(row.status)"
            size="small"
            :effect="isRunning(row.status) ? 'dark' : 'plain'"
          >
            {{ row.status }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="Snapshot" width="120">
        <template #default="{ row }">
          <template v-if="row.snapshot_id">
            <el-tooltip :content="row.snapshot_id" placement="top">
              <span
                class="copyable"
                @click.stop="copyText(row.snapshot_id!)"
              >
                <el-icon><CopyDocument /></el-icon>
                {{ shortCode(row.snapshot_id!) }}
              </span>
            </el-tooltip>
          </template>
          <span v-else class="dim-text">—</span>
        </template>
      </el-table-column>
      <el-table-column label="Duration" width="110">
        <template #default="{ row }">
          <template v-if="row.started_at && row.finished_at">
            {{ formatDuration(row.started_at, row.finished_at) }}
          </template>
          <template v-else-if="row.started_at && !row.finished_at">
            <span class="dim-text">{{ formatDuration(row.started_at, new Date().toISOString()) }}<el-icon class="is-loading" style="margin-left:4px"><Loading /></el-icon></span>
          </template>
          <span v-else class="dim-text">—</span>
        </template>
      </el-table-column>
      <el-table-column label="Error" width="140">
        <template #default="{ row }">
          <el-tooltip
            v-if="row.error_code"
            :content="row.error_message || row.error_code"
            placement="top"
          >
            <el-tag type="danger" size="small" effect="plain">
              {{ row.error_code }}
            </el-tag>
          </el-tooltip>
          <span v-else class="dim-text">—</span>
        </template>
      </el-table-column>
      <el-table-column label="Actions" width="80" fixed="right">
        <template #default="{ row }">
          <el-button
            v-if="row.status === 'running'"
            size="small"
            type="danger"
            link
            @click.stop="cancelRun(row.id)"
          >
            Cancel
          </el-button>
          <span v-else>&nbsp;</span>
        </template>
      </el-table-column>
    </el-table>

    <el-pagination
      v-if="!loading && !error"
      :current-page="page"
      :page-size="limit"
      :page-sizes="[10, 20, 50]"
      layout="prev, pager, next, sizes"
      style="margin-top: 16px; justify-content: flex-end"
      @current-change="onPageChange"
      @size-change="onSizeChange"
    />
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { useRouter } from 'vue-router'
import { apiGet, apiPost } from '@/api/client'
import type { Run, Plan, Agent, RunQueryParams } from '@/api/types'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Loading, Warning, CopyDocument } from '@element-plus/icons-vue'

const router = useRouter()

const runs = ref<Run[]>([])
const plans = ref<Plan[]>([])
const agents = ref<Agent[]>([])
const loading = ref(false)
const error = ref('')
const page = ref(1)
const limit = ref(20)

const filters = ref<RunQueryParams>({
  plan_id: '',
  agent_id: '',
  status: '',
  operation: '',
  limit: undefined,
  offset: undefined,
})

const statusOptions: Array<{ value: string; label: string }> = [
  { value: 'queued', label: 'Queued' },
  { value: 'dispatched', label: 'Dispatched' },
  { value: 'running', label: 'Running' },
  { value: 'succeeded', label: 'Succeeded' },
  { value: 'failed', label: 'Failed' },
  { value: 'cancelled', label: 'Cancelled' },
]

const operationOptions: Array<{ value: string; label: string }> = [
  { value: 'backup', label: 'Backup' },
  { value: 'restore', label: 'Restore' },
  { value: 'check', label: 'Check' },
  { value: 'forget', label: 'Forget' },
]

let debounceTimer: ReturnType<typeof setTimeout> | null = null

function debounceReset(): void {
  if (debounceTimer) clearTimeout(debounceTimer)
  debounceTimer = setTimeout(() => {
    page.value = 1
    loadRuns()
  }, 400)
}

function onFilterChange(): void {
  page.value = 1
  loadRuns()
}

function resetFilters(): void {
  filters.value = {
    plan_id: '',
    agent_id: '',
    status: '',
    operation: '',
    limit: undefined,
    offset: undefined,
  }
  page.value = 1
  loadRuns()
}

async function loadRuns(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const params: Record<string, string | number | undefined> = {
      ...filters.value,
      limit: limit.value,
      offset: (page.value - 1) * limit.value,
    }
    // Strip empty strings so server ignores them
    Object.keys(params).forEach((k) => {
      if (params[k] === '') {
        delete params[k]
      }
    })
    runs.value = await apiGet<Run[]>('/runs', params)
  } catch (err: any) {
    error.value = err.message || 'Failed to load runs'
    runs.value = []
  } finally {
    loading.value = false
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

function planName(planId: string): string | undefined {
  return plans.value.find((p) => p.id === planId)?.name
}

function onPageChange(p: number): void {
  page.value = p
  loadRuns()
}

function onSizeChange(size: number): void {
  limit.value = size
  page.value = 1
  loadRuns()
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
    case 'queued':
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

function isRunning(status: Run['status']): boolean {
  return status === 'running' || status === 'dispatched'
}

function shortCode(snapshotId: string): string {
  return snapshotId.length > 12 ? snapshotId.slice(-8) : snapshotId
}

function formatTime(iso: string | null): string {
  if (!iso) return '—'
  try {
    const d = new Date(iso)
    if (isNaN(d.getTime())) return iso
    return d.toLocaleString(undefined, {
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    })
  } catch {
    return iso
  }
}

function formatDuration(started: string, finished: string): string {
  const start = new Date(started).getTime()
  const finish = new Date(finished).getTime()
  if (isNaN(start) || isNaN(finish)) return '—'
  const diff = Math.max(0, finish - start)
  const totalSec = Math.floor(diff / 1000)
  if (totalSec < 60) return `${totalSec}s`
  const h = Math.floor(totalSec / 3600)
  const m = Math.floor((totalSec % 3600) / 60)
  const s = totalSec % 60
  if (h > 0) return `${h}h ${m}m ${s}s`
  if (m > 0) return `${m}m ${s}s`
  return `${s}s`
}

async function cancelRun(runId: string): Promise<void> {
  try {
    await ElMessageBox.confirm('Are you sure you want to cancel this run?', 'Cancel Run', {
      type: 'warning',
      confirmButtonText: 'Cancel Run',
      cancelButtonText: 'Keep Running',
    })
    await apiPost(`/runs/${runId}/cancel`)
    ElMessage.success('Run cancellation requested')
    await loadRuns()
  } catch (err: any) {
    if (err === 'cancel' || err === 'closed') return
    ElMessage.error(err.message || 'Failed to cancel run')
  }
}

function onRowClick(row: Run): void {
  router.push(`/runs/${row.id}`)
}

async function copyText(text: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success('Copied to clipboard')
  } catch {
    ElMessage.error('Failed to copy')
  }
}

onMounted(async () => {
  await loadPlansAndAgents()
  await loadRuns()
})

onBeforeUnmount(() => {
  if (debounceTimer) clearTimeout(debounceTimer)
})
</script>

<style scoped>
.page-head {
  margin-bottom: 16px;
}
.page-title {
  margin: 0;
  font-size: 22px;
  font-weight: 600;
}
.filter-card {
  margin-bottom: 16px;
}
.error-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  padding: 40px;
  color: #f56c6c;
}
.copyable {
  cursor: pointer;
  font-family: 'Courier New', Courier, monospace;
  font-size: 12px;
  color: #409eff;
  user-select: none;
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
.copyable:hover {
  color: #2b7fd5;
}
.dim-text {
  color: #909399;
}
.el-pagination {
  display: flex;
}
</style>