<template>
  <div>
    <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 16px">
      <div class="section-title" style="margin-bottom: 0">{{ t('nav.plans') }}</div>
      <div>
        <el-button type="primary" @click="openCreate">
          <el-icon><Plus /></el-icon>
          <span>{{ t('plans.newPlan') }}</span>
        </el-button>
        <el-button @click="refresh">
          <el-icon><Refresh /></el-icon>
          <span>{{ t('common.refresh') }}</span>
        </el-button>
      </div>
    </div>

    <div style="margin-bottom: 12px">
      <el-select
        v-model="filterAgentId"
        :placeholder="t('plans.filterByAgent')"
        clearable
        filterable
        style="width: 240px"
        @change="loadPlans"
      >
        <el-option v-for="a in agents" :key="a.id" :label="`${a.name} (${statusText(a.status)})`" :value="a.id" />
      </el-select>
    </div>

    <div v-if="error" class="error-state">
      <el-icon><Warning /></el-icon>
      <p>{{ error }}</p>
      <el-button type="primary" @click="refresh" style="margin-top: 12px">{{ t('common.retry') }}</el-button>
    </div>

    <div v-else-if="loading" style="text-align: center; padding: 40px">
      <el-icon class="is-loading"><Loading /></el-icon>
    </div>

    <el-table v-else :data="plans" stripe row-key="id" style="width: 100%">
      <el-table-column :label="t('common.name')">
        <template #default="{ row: p }">
          {{ p.name }}
        </template>
      </el-table-column>
      <el-table-column :label="t('plans.columns.kind')" width="110">
        <template #default="{ row: p }">
          <el-tag :type="KIND_TAG_TYPE[(p as Plan).kind]">{{ t(KIND_LABELS[(p as Plan).kind]) }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column :label="t('plans.columns.schedule')" width="130">
        <template #default="{ row: p }">
          <code style="font-size: 12px">{{ p.schedule }}</code>
        </template>
      </el-table-column>
      <el-table-column :label="t('plans.columns.timezone')" width="140">
        <template #default="{ row: p }">
          {{ p.timezone }}
        </template>
      </el-table-column>
      <el-table-column :label="t('plans.columns.enabled')" width="80">
        <template #default="{ row: p }">
          <el-switch
            :model-value="p.enabled"
            :before-change="() => toggleEnabled(p as Plan)"
            inline-prompt
            :active-text="t('common.on')"
            :inactive-text="t('common.off')"
          />
        </template>
      </el-table-column>
      <el-table-column :label="t('plans.columns.repository')" min-width="150">
        <template #default="{ row: p }">
          <el-tooltip :content="repositoryFor(p as Plan)?.repository_path || ''" placement="top">
            <span>{{ repositoryFor(p as Plan)?.storage_target_name || p.repository_id }}</span>
          </el-tooltip>
        </template>
      </el-table-column>
      <el-table-column :label="t('plans.columns.path')" min-width="220">
        <template #default="{ row: p }">
          <el-tooltip :content="sourcePaths(p as Plan)" placement="top">
            <span class="plan-source-path">{{ sourcePaths(p as Plan) }}</span>
          </el-tooltip>
        </template>
      </el-table-column>
      <el-table-column :label="t('plans.columns.lastRunAt')" width="180">
        <template #default="{ row: p }">
          {{ formatDateTime((p as Plan).last_run_at) }}
        </template>
      </el-table-column>
      <el-table-column :label="t('plans.columns.timeout')" width="100">
        <template #default="{ row: p }">
          {{ p.timeout_seconds }}s
        </template>
      </el-table-column>
      <el-table-column :label="t('common.actions')" width="150" fixed="right">
        <template #default="{ row: p }">
          <el-button text size="small" :disabled="runningId === p.id" @click="runPlan(p as Plan)">
            <el-icon style="margin-right: 2px"><VideoPlay /></el-icon>
            {{ t('common.run') }}
          </el-button>
          <el-button text size="small" @click="startEdit(p as Plan)">
            <el-icon style="margin-right: 2px"><Edit /></el-icon>
            {{ t('common.edit') }}
          </el-button>
          <el-button text size="small" type="danger" @click="deletePlan(p as Plan)">
            <el-icon style="margin-right: 2px"><Delete /></el-icon>
            {{ t('common.delete') }}
          </el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog
      v-model="dialogVisible"
      :title="editing ? t('plans.editPlan') : t('plans.newPlan')"
      width="720px"
      destroy-on-close
      @closed="dialogClosed"
    >
      <PlanForm
        :model="form"
        :agents="agents"
        :repositories="repositories"
        :submitting="saving"
        @submit="savePlan"
        @cancel="dialogVisible = false"
      />
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh, Warning, Loading, VideoPlay, Edit, Delete } from '@element-plus/icons-vue'
import { apiDelete, apiGet, apiPost, apiPut } from '@/api/client'
import type { Agent, Plan, Repository, Run } from '@/api/types'
import { translateEnum } from '@/i18n'
import PlanForm from './PlanForm.vue'
import { KIND_LABELS, KIND_TAG_TYPE, defaultSource } from './Constants'
import { buildPayload, buildValidatePayload, type PlanFormModel, type ValidateResult } from './Types'

const router = useRouter()
const { t } = useI18n()

function statusText(status: string): string {
  return translateEnum('status', status)
}

const plans = ref<Plan[]>([])
const agents = ref<Agent[]>([])
const repositories = ref<Repository[]>([])
const filterAgentId = ref('')
const loading = ref(false)
const error = ref('')

const dialogVisible = ref(false)
const editing = ref(false)
const saving = ref(false)
const runningId = ref('')

function blankForm(): PlanFormModel {
  return {
    name: '',
    agent_id: '',
    kind: 'filesystem',
    schedule: '',
    timezone: 'UTC',
    enabled: true,
    source: defaultSource('filesystem'),
    repository_id: '',
    retention: { keep_last: 7, keep_daily: 7, keep_weekly: 4, keep_monthly: 3 },
    timeout_seconds: 3600,
  }
}
const form = reactive<PlanFormModel>(blankForm())

function repositoryFor(p: Plan): Repository | undefined {
  return repositories.value.find((r) => r.id === p.repository_id)
}
function sourcePaths(p: Plan): string {
  if (p.kind === 'filesystem') return p.source.paths?.join(', ') || '-'
  if (p.kind === 'sqlite') return p.source.path || '-'
  return [p.source.host, p.source.port, p.source.database].filter((value) => value !== undefined && value !== '').join(':') || '-'
}

function formatDateTime(value: string | null): string {
  if (!value) return '-'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}

function errorMessage(err: unknown): string {
  if (err instanceof Error) {
    const code = (err as Error & { code?: unknown }).code
    const codeStr = typeof code === 'string' && code ? code : ''
    return codeStr ? `${codeStr}: ${err.message}` : err.message
  }
  return String(err)
}

async function loadPlans(): Promise<void> {
  error.value = ''
  loading.value = true
  try {
    plans.value = await apiGet<Plan[]>('/plans', {
      agent_id: filterAgentId.value || undefined,
    })
  } catch (err: unknown) {
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

async function loadMeta(): Promise<void> {
  try {
    const [agentList, repoList] = await Promise.all([
      apiGet<Agent[]>('/agents'),
      apiGet<Repository[]>('/repositories'),
    ])
    agents.value = agentList
    repositories.value = repoList
  } catch (err: unknown) {
    error.value = errorMessage(err)
  }
}

async function refresh(): Promise<void> {
  await Promise.all([loadPlans(), loadMeta()])
}

onMounted(() => {
  void refresh()
})

function openCreate(): void {
  Object.assign(form, blankForm())
  editing.value = false
  dialogVisible.value = true
}

function startEdit(row: Plan): void {
  Object.assign(form, {
    id: row.id,
    name: row.name,
    agent_id: row.agent_id,
    kind: row.kind,
    schedule: row.schedule,
    timezone: row.timezone,
    enabled: row.enabled,
    source: { ...row.source, password: '' },
    repository_id: row.repository_id,
    retention: { ...row.retention },
    timeout_seconds: row.timeout_seconds,
  })
  editing.value = true
  dialogVisible.value = true
}

function dialogClosed(): void {
  runningId.value = ''
}

async function toggleEnabled(row: Plan): Promise<boolean> {
  const target = !row.enabled
  try {
    await apiPut(`/plans/${row.id}`, {
      ...buildPayload({
        ...row,
        source: { ...row.source },
        retention: { ...row.retention },
      }),
      enabled: target,
    })
    row.enabled = target
    return true
  } catch (err: unknown) {
    ElMessage.error(errorMessage(err))
    return false
  }
}

async function runPlan(row: Plan): Promise<void> {
  runningId.value = row.id
  try {
    const run = await apiPost<Run>(`/plans/${row.id}/run`, {})
    await router.push(`/runs/${run.id}`)
  } catch (err: unknown) {
    ElMessage.error(errorMessage(err))
    runningId.value = ''
  }
}

async function deletePlan(row: Plan): Promise<void> {
  try {
    await ElMessageBox.confirm(
      t('plans.deleteDialog.confirm', { name: row.name }),
      t('plans.deleteDialog.title'),
      { type: 'warning', confirmButtonText: t('common.delete'), cancelButtonText: t('common.cancel') },
    )
  } catch {
    return
  }
  try {
    await apiDelete(`/plans/${row.id}`)
    ElMessage.success(t('plans.deleted'))
    await loadPlans()
  } catch (err: unknown) {
    const e = err as { code?: string; message?: string }
    if (e.code === 'conflict') {
      ElMessage.warning(t('plans.deleteDialog.snapshotsRequired'))
      return
    }
    ElMessage.error(errorMessage(err))
  }
}

async function savePlan(model: PlanFormModel): Promise<void> {
  saving.value = true
  try {
    const result = await apiPost<ValidateResult>('/plans/validate', buildValidatePayload(model))
    if (!result.ok) {
      const code = result.code || 'validation_failed'
      const msg = result.message || t('plans.validationFailed')
      ElMessage.error(`${code}: ${msg}`)
      return
    }
    if (editing.value && model.id) {
      await apiPut(`/plans/${model.id}`, buildPayload(model))
      ElMessage.success(t('plans.updated'))
    } else {
      await apiPost('/plans', buildPayload(model))
      ElMessage.success(t('plans.created'))
    }
    dialogVisible.value = false
    await loadPlans()
  } catch (err: unknown) {
    ElMessage.error(errorMessage(err))
  } finally {
    saving.value = false
  }
}
</script>
