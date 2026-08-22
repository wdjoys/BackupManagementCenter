<template>
  <div>
    <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 16px">
      <div class="section-title" style="margin-bottom: 0">Plans</div>
      <div>
        <el-button type="primary" @click="openCreate">
          <el-icon><Plus /></el-icon>
          <span>New Plan</span>
        </el-button>
        <el-button @click="refresh">
          <el-icon><Refresh /></el-icon>
          <span>Refresh</span>
        </el-button>
      </div>
    </div>

    <div style="margin-bottom: 12px">
      <el-select
        v-model="filterAgentId"
        placeholder="Filter by agent"
        clearable
        filterable
        style="width: 240px"
        @change="loadPlans"
      >
        <el-option v-for="a in agents" :key="a.id" :label="`${a.name} (${a.status})`" :value="a.id" />
      </el-select>
    </div>

    <div v-if="error" class="error-state">
      <el-icon><Warning /></el-icon>
      <p>{{ error }}</p>
      <el-button type="primary" @click="refresh" style="margin-top: 12px">Retry</el-button>
    </div>

    <div v-else-if="loading" style="text-align: center; padding: 40px">
      <el-icon class="is-loading"><Loading /></el-icon>
    </div>

    <el-table v-else :data="plans" stripe row-key="id" style="width: 100%">
      <el-table-column label="Name">
        <template #default="{ row: p }">
          {{ p.name }}
        </template>
      </el-table-column>
      <el-table-column label="Kind" width="110">
        <template #default="{ row: p }">
          <el-tag :type="KIND_TAG_TYPE[(p as Plan).kind]">{{ KIND_LABELS[(p as Plan).kind] }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="Schedule" width="130">
        <template #default="{ row: p }">
          <code style="font-size: 12px">{{ p.schedule }}</code>
        </template>
      </el-table-column>
      <el-table-column label="Timezone" width="140">
        <template #default="{ row: p }">
          {{ p.timezone }}
        </template>
      </el-table-column>
      <el-table-column label="Enabled" width="80">
        <template #default="{ row: p }">
          <el-switch
            :model-value="p.enabled"
            :before-change="() => toggleEnabled(p as Plan)"
            inline-prompt
            active-text="On"
            inactive-text="Off"
          />
        </template>
      </el-table-column>
      <el-table-column label="Repository" min-width="150">
        <template #default="{ row: p }">
          <el-tooltip :content="repositoryFor(p as Plan)?.repository_path || ''" placement="top">
            <span>{{ repositoryFor(p as Plan)?.storage_target_name || p.repository_id }}</span>
          </el-tooltip>
        </template>
      </el-table-column>
      <el-table-column label="Timeout" width="100">
        <template #default="{ row: p }">
          {{ p.timeout_seconds }}s
        </template>
      </el-table-column>
      <el-table-column label="Actions" width="150" fixed="right">
        <template #default="{ row: p }">
          <el-button text size="small" :disabled="runningId === p.id" @click="runPlan(p as Plan)">
            <el-icon style="margin-right: 2px"><VideoPlay /></el-icon>
            Run
          </el-button>
          <el-button text size="small" @click="startEdit(p as Plan)">
            <el-icon style="margin-right: 2px"><Edit /></el-icon>
            Edit
          </el-button>
          <el-button text size="small" type="danger" @click="deletePlan(p as Plan)">
            <el-icon style="margin-right: 2px"><Delete /></el-icon>
            Delete
          </el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog
      v-model="dialogVisible"
      :title="editing ? 'Edit Plan' : 'New Plan'"
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
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh, Warning, Loading, VideoPlay, Edit, Delete } from '@element-plus/icons-vue'
import { apiDelete, apiGet, apiPost, apiPut } from '@/api/client'
import type { Agent, Plan, Repository, Run } from '@/api/types'
import PlanForm from './PlanForm.vue'
import { KIND_LABELS, KIND_TAG_TYPE, defaultSource } from './Constants'
import { buildPayload, buildValidatePayload, type PlanFormModel, type ValidateResult } from './Types'

const router = useRouter()

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
      `Delete plan "${row.name}"? This cannot be undone.`,
      'Delete plan',
      { type: 'warning', confirmButtonText: 'Delete', cancelButtonText: 'Cancel' },
    )
  } catch {
    return
  }
  try {
    await apiDelete(`/plans/${row.id}`)
    ElMessage.success('Plan deleted')
    await loadPlans()
  } catch (err: unknown) {
    ElMessage.error(errorMessage(err))
  }
}

async function savePlan(model: PlanFormModel): Promise<void> {
  saving.value = true
  try {
    const result = await apiPost<ValidateResult>('/plans/validate', buildValidatePayload(model))
    if (!result.ok) {
      const code = result.code || 'validation_failed'
      const msg = result.message || 'Plan validation failed'
      ElMessage.error(`${code}: ${msg}`)
      return
    }
    if (editing.value && model.id) {
      await apiPut(`/plans/${model.id}`, buildPayload(model))
      ElMessage.success('Plan updated')
    } else {
      await apiPost('/plans', buildPayload(model))
      ElMessage.success('Plan created')
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