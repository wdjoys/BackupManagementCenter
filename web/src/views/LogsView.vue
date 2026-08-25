<template>
  <div class="detail-page process-logs-page">
    <div class="page-head">
      <h2 class="page-title">{{ t('logs.title') }}</h2>
      <div class="logs-toolbar">
        <el-radio-group v-model="scope" size="small" @change="handleScopeChange">
          <el-radio-button value="server">{{ t('logs.server') }}</el-radio-button>
          <el-radio-button value="agent">{{ t('logs.agent') }}</el-radio-button>
        </el-radio-group>
        <el-select
          v-if="scope === 'agent'"
          v-model="selectedAgentId"
          size="small"
          clearable
          filterable
          :placeholder="t('logs.agentPlaceholder')"
          style="width: 240px"
          @change="handleAgentChange"
        >
          <el-option
            v-for="agent in agents"
            :key="agent.id"
            :label="`${agent.name} (${agent.hostname})`"
            :value="agent.id"
          />
        </el-select>
        <el-select
          v-model="levelFilter"
          size="small"
          clearable
          :placeholder="t('logs.filters.level')"
          style="width: 150px"
          @change="handleFilterChange"
        >
          <el-option :label="t('logs.filters.allLevels')" value="" />
          <el-option
            v-for="level in LOG_LEVELS"
            :key="level"
            :label="t(`logs.levels.${level}`)"
            :value="level"
          />
        </el-select>
        <el-select
          v-model="typeFilter"
          size="small"
          clearable
          :placeholder="t('logs.filters.type')"
          style="width: 170px"
          @change="handleFilterChange"
        >
          <el-option :label="t('logs.filters.allTypes')" value="" />
          <el-option
            v-for="type in LOG_TYPES"
            :key="type"
            :label="t(`logs.types.${type}`)"
            :value="type"
          />
        </el-select>
        <el-button size="small" :loading="loading" @click="loadLogs(true)">
          <el-icon><Refresh /></el-icon>
          <span>{{ t('common.refresh') }}</span>
        </el-button>
      </div>
    </div>

    <el-alert
      v-if="scope === 'agent' && !selectedAgentId"
      :title="t('logs.selectAgentHint')"
      type="info"
      :closable="false"
      show-icon
      style="margin-bottom: 16px"
    />

    <div v-if="error" class="error-state">
      <el-icon><Warning /></el-icon>
      <p>{{ error }}</p>
      <el-button type="primary" @click="loadLogs(true)" style="margin-top: 12px">
        {{ t('common.retry') }}
      </el-button>
    </div>

    <el-card v-else shadow="never" class="logs-card">
      <template #header>
        <div class="logs-header">
          <span>{{ currentTitle }}（{{ logs.length }}）</span>
          <el-button
            type="primary"
            size="small"
            :disabled="loading || !hasMore"
            @click="loadMore"
          >
            {{ loading ? t('logs.loading') : t('logs.loadMore') }}
          </el-button>
        </div>
      </template>

      <div v-if="loading && logs.length === 0" class="logs-loading">
        <el-icon class="is-loading"><Loading /></el-icon>
      </div>
      <el-empty v-else-if="logs.length === 0" :description="t('logs.empty')" />
      <el-table v-else :data="displayLogs" stripe size="small" max-height="620">
        <el-table-column :label="t('logs.columns.id')" width="92">
          <template #default="{ row }">
            <code>{{ row.id }}</code>
          </template>
        </el-table-column>
        <el-table-column :label="t('logs.columns.type')" width="130">
          <template #default="{ row }">
            <el-tag size="small" type="info">{{ typeText(row.type) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column v-if="scope === 'agent'" :label="t('logs.columns.sourceSeq')" width="92">
          <template #default="{ row }">
            {{ row.source_seq ?? '-' }}
          </template>
        </el-table-column>
        <el-table-column :label="t('logs.columns.time')" width="190">
          <template #default="{ row }">{{ formatTime(row.timestamp) }}</template>
        </el-table-column>
        <el-table-column :label="t('logs.columns.level')" width="92">
          <template #default="{ row }">
            <el-tag size="small" :type="levelTagType(row.level)">{{ row.level }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('logs.columns.message')" min-width="420">
          <template #default="{ row }">
            <span class="process-log-message">{{ row.message }}</span>
          </template>
        </el-table-column>
      </el-table>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { apiGet } from '@/api/client'
import type { Agent, SystemLog } from '@/api/types'
import { formatDateTime, translateEnum } from '@/i18n'

const PAGE_SIZE = 500
const LOG_LEVELS = ['debug', 'info', 'warn', 'error'] as const
const LOG_TYPES = ['system', 'http', 'agent', 'run', 'scheduler', 'dispatcher', 'connection', 'command', 'notification'] as const
const route = useRoute()
const router = useRouter()
const { t } = useI18n()

type LogScope = 'server' | 'agent'

const initialAgentID = typeof route.query.agent_id === 'string' ? route.query.agent_id : ''
const scope = ref<LogScope>(initialAgentID ? 'agent' : 'server')
const selectedAgentId = ref(initialAgentID)
const levelFilter = ref('')
const typeFilter = ref('')
const agents = ref<Agent[]>([])
const logs = ref<SystemLog[]>([])
const loading = ref(false)
const error = ref('')
const hasMore = ref(false)

const currentTitle = computed(() => {
  if (scope.value === 'server') return t('logs.server')
  const agent = agents.value.find((item) => item.id === selectedAgentId.value)
  return agent ? t('logs.agentTitle', { name: agent.name }) : t('logs.agent')
})

const displayLogs = computed(() => [...logs.value].sort((a, b) => b.id - a.id))

function formatTime(value: string): string {
  return formatDateTime(value)
}
function typeText(type: string): string {
  return translateEnum('logs.types', type)
}


function levelTagType(level: string): 'info' | 'success' | 'warning' | 'danger' {
  switch (level) {
    case 'error':
      return 'danger'
    case 'warn':
      return 'warning'
    case 'debug':
      return 'info'
    default:
      return 'success'
  }
}

function endpoint(): string | null {
  if (scope.value === 'server') return '/logs/server'
  if (!selectedAgentId.value) return null
  return `/agents/${encodeURIComponent(selectedAgentId.value)}/logs`
}

async function loadAgents(): Promise<void> {
  try {
    agents.value = await apiGet<Agent[]>('/agents')
  } catch {
    agents.value = []
  }
}

async function loadLogs(reset: boolean): Promise<void> {
  const path = endpoint()
  if (!path) {
    logs.value = []
    hasMore.value = false
    return
  }
  loading.value = true
  error.value = ''
  try {
    const beforeId = reset || logs.value.length === 0 ? undefined : Math.min(...logs.value.map((item) => item.id))
    const data = await apiGet<SystemLog[]>(path, {
      limit: PAGE_SIZE,
      before_id: beforeId,
      level: levelFilter.value || undefined,
      type: typeFilter.value || undefined,
    })
    const merged = reset ? data : [...data, ...logs.value]
    const byID = new Map<number, SystemLog>()
    for (const entry of merged) byID.set(entry.id, entry)
    logs.value = Array.from(byID.values())
    hasMore.value = data.length === PAGE_SIZE
  } catch (err: any) {
    error.value = err.message || t('logs.loadFailed')
  } finally {
    loading.value = false
  }
}

function handleFilterChange(): void {
  logs.value = []
  hasMore.value = false
  void loadLogs(true)
}

async function loadMore(): Promise<void> {
  if (loading.value || !hasMore.value) return
  await loadLogs(false)
}

function handleScopeChange(): void {
  logs.value = []
  hasMore.value = false
  if (scope.value === 'server') {
    selectedAgentId.value = ''
    void router.replace({ query: {} })
  } else {
    void router.replace({ query: selectedAgentId.value ? { agent_id: selectedAgentId.value } : {} })
  }
  void loadLogs(true)
}

function handleAgentChange(): void {
  void router.replace({ query: selectedAgentId.value ? { agent_id: selectedAgentId.value } : {} })
  void loadLogs(true)
}

watch(
  () => route.query.agent_id,
  (value) => {
    if (scope.value !== 'agent') return
    const agentID = typeof value === 'string' ? value : ''
    if (agentID === selectedAgentId.value) return
    selectedAgentId.value = agentID
    void loadLogs(true)
  },
)

onMounted(async () => {
  await loadAgents()
  await loadLogs(true)
})
</script>
<style scoped>
.detail-page {
  max-width: 1400px;
  margin: 0 auto;
}
.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 16px;
}
.page-title {
  margin: 0;
  font-size: 22px;
  font-weight: 600;
}
.logs-toolbar {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 10px;
  flex-wrap: wrap;
}
.logs-card {
  margin-bottom: 16px;
}
.logs-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.logs-loading {
  display: flex;
  justify-content: center;
  padding: 40px;
}
.process-log-message {
  display: block;
  white-space: pre-wrap;
  word-break: break-word;
  font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, monospace;
  line-height: 1.5;
}
@media (max-width: 760px) {
  .page-head {
    align-items: flex-start;
    flex-direction: column;
  }
  .logs-toolbar {
    width: 100%;
    justify-content: flex-start;
  }
}
</style>
