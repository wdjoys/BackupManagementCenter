<template>
  <el-form ref="formRef" :model="props.model" :rules="rules" label-position="top" size="default">
    <el-row :gutter="16">
      <el-col :span="12">
        <el-form-item label="Name" prop="name">
          <el-input v-model="props.model.name" placeholder="Plan name" />
        </el-form-item>
      </el-col>
      <el-col :span="12">
        <el-form-item label="Kind" prop="kind">
          <el-select v-model="props.model.kind" placeholder="Pick kind" @change="onKindChange">
            <el-option v-for="k in planKinds" :key="k.value" :label="k.label" :value="k.value" />
          </el-select>
        </el-form-item>
      </el-col>
      <el-col :span="12">
        <el-form-item label="Agent" prop="agent_id">
          <el-select v-model="props.model.agent_id" filterable placeholder="Agent" @change="onAgentChange">
            <el-option v-for="a in props.agents" :key="a.id" :label="`${a.name} (${a.status})`" :value="a.id">
              <span>{{ a.name }}</span>
              <el-tag
                :type="a.status === 'online' ? 'success' : 'info'"
                size="small"
                style="margin-left: 8px"
              >
                {{ a.status }}
              </el-tag>
            </el-option>
          </el-select>
        </el-form-item>
      </el-col>
      <el-col :span="12">
        <el-form-item label="Repository" prop="repository_id">
          <el-select v-model="props.model.repository_id" placeholder="Repository" filterable>
            <el-option v-for="r in filteredRepos" :key="r.id" :label="`${r.storage_target_name} @ ${r.agent_name}`" :value="r.id">
              <span>{{ r.storage_target_name }} @ {{ r.agent_name }}</span>
              <span style="color: #999; margin-left: 8px; font-size: 12px">{{ r.repository_path }}</span>
            </el-option>
          </el-select>
        </el-form-item>
      </el-col>
      <el-col :span="12">
        <el-form-item label="Schedule (cron 5-field)" prop="schedule">
          <el-input v-model="props.model.schedule" placeholder="e.g. 0 3 * * *" />
          <div class="presets">
            <span class="preset-label">Presets:</span>
            <el-tag
              v-for="p in CRON_PRESETS"
              :key="p.value"
              size="small"
              class="preset-tag"
              @click="props.model.schedule = p.value"
            >
              {{ p.label }}
            </el-tag>
          </div>
        </el-form-item>
      </el-col>
      <el-col :span="12">
        <el-form-item label="Timezone" prop="timezone">
          <el-select v-model="props.model.timezone" filterable placeholder="IANA timezone" clearable>
            <el-option v-for="tz in IANA_TIMEZONES" :key="tz" :label="tz" :value="tz" />
          </el-select>
        </el-form-item>
      </el-col>
      <el-col :span="12">
        <el-form-item label="Timeout (seconds)" prop="timeout_seconds">
          <el-input-number
            :model-value="props.model.timeout_seconds"
            :min="1"
            :step="60"
            :controls-position="'right'"
            @update:model-value="v => props.model.timeout_seconds = v ?? 3600"
          />
        </el-form-item>
      </el-col>
      <el-col :span="24">
        <el-form-item label="Retention" prop="retention">
          <el-row :gutter="8">
            <el-col :span="6">
              <el-input-number
                :model-value="props.model.retention.keep_last"
                :min="0"
                :step="1"
                :controls-position="'right'"
                placeholder="keep_last"
                @update:model-value="v => props.model.retention.keep_last = v ?? 0"
              />
            </el-col>
            <el-col :span="6">
              <el-input-number
                :model-value="props.model.retention.keep_daily"
                :min="0"
                :step="1"
                :controls-position="'right'"
                placeholder="keep_daily"
                @update:model-value="v => props.model.retention.keep_daily = v ?? 0"
              />
            </el-col>
            <el-col :span="6">
              <el-input-number
                :model-value="props.model.retention.keep_weekly"
                :min="0"
                :step="1"
                :controls-position="'right'"
                placeholder="keep_weekly"
                @update:model-value="v => props.model.retention.keep_weekly = v ?? 0"
              />
            </el-col>
            <el-col :span="6">
              <el-input-number
                :model-value="props.model.retention.keep_monthly"
                :min="0"
                :step="1"
                :controls-position="'right'"
                placeholder="keep_monthly"
                @update:model-value="v => props.model.retention.keep_monthly = v ?? 0"
              />
            </el-col>
          </el-row>
          <div class="retention-hint">At least one value must be greater than 0</div>
        </el-form-item>
      </el-col>
    </el-row>

    <el-divider content-position="left">Source</el-divider>

    <template v-if="props.model.kind === 'filesystem'">
      <el-row :gutter="16">
        <el-col :span="24">
          <el-form-item label="Paths" prop="source.paths">
            <TagInput v-model="props.model.source.paths" placeholder="/absolute/path" />
          </el-form-item>
        </el-col>
        <el-col :span="24">
          <el-form-item label="Excludes">
            <TagInput v-model="props.model.source.excludes" placeholder="/path/to/exclude (glob ok)" />
          </el-form-item>
        </el-col>
        <el-col :span="24">
          <el-form-item label="One filesystem">
            <el-switch
              :model-value="props.model.source.one_file_system === true"
              active-text="Yes"
              inactive-text="No"
              @update:model-value="(v) => (props.model.source.one_file_system = v === true)"
            />
          </el-form-item>
        </el-col>
      </el-row>
    </template>

    <template v-else-if="props.model.kind === 'sqlite'">
      <el-row :gutter="16">
        <el-col :span="24">
          <el-form-item label="Database path" prop="source.path">
            <el-input v-model="props.model.source.path" placeholder="/absolute/path/to/database.sqlite" />
          </el-form-item>
        </el-col>
        <el-col :span="24">
          <el-form-item label="Estimated dump bytes" prop="source.estimated_dump_bytes">
            <el-input-number
              :model-value="props.model.source.estimated_dump_bytes"
              :min="1"
              :step="1073741824"
              :controls-position="'right'"
              @update:model-value="(v) => (props.model.source.estimated_dump_bytes = v ?? undefined)"
            />
            <div class="hint">Estimate by largest DB; agent needs ~1.2× of this for temp space</div>
          </el-form-item>
        </el-col>
      </el-row>
    </template>

    <template v-else>
      <el-row :gutter="16">
        <el-col :span="16">
          <el-form-item label="Host" prop="source.host">
            <el-input v-model="props.model.source.host" placeholder="host or IP" />
          </el-form-item>
        </el-col>
        <el-col :span="8">
          <el-form-item label="Port" prop="source.port">
            <el-input-number
              :model-value="props.model.source.port"
              :min="1"
              :max="65535"
              :controls-position="'right'"
              @update:model-value="(v) => (props.model.source.port = v ?? undefined)"
            />
          </el-form-item>
        </el-col>
        <el-col :span="12">
          <el-form-item label="Username" prop="source.username">
            <el-input v-model="props.model.source.username" />
          </el-form-item>
        </el-col>
        <el-col :span="12">
          <el-form-item label="Password">
            <el-input
              v-model="props.model.source.password"
              type="password"
              show-password
              placeholder="Leave empty to keep existing"
            />
          </el-form-item>
        </el-col>
        <el-col :span="12">
          <el-form-item label="Database" prop="source.database">
            <el-input v-model="props.model.source.database" placeholder="Single database or 'all'" />
          </el-form-item>
        </el-col>
        <el-col :span="12">
          <el-form-item label="Extra args">
            <TagInput v-model="props.model.source.extra_args" placeholder="--arg" />
          </el-form-item>
        </el-col>
        <el-col :span="24">
          <el-form-item label="Estimated dump bytes" prop="source.estimated_dump_bytes">
            <el-input-number
              :model-value="props.model.source.estimated_dump_bytes"
              :min="1"
              :step="1073741824"
              :controls-position="'right'"
              @update:model-value="(v) => (props.model.source.estimated_dump_bytes = v ?? undefined)"
            />
            <div class="hint">Estimate by largest DB; agent needs ~1.2× of this for temp space</div>
          </el-form-item>
        </el-col>
        <template v-if="props.model.kind === 'mongodb'">
          <el-col :span="24">
            <el-form-item label="Capture oplog">
              <el-switch
                :model-value="props.model.source.capture_oplog === true"
                active-text="Yes"
                inactive-text="No"
                @update:model-value="(v) => (props.model.source.capture_oplog = v === true)"
              />
            </el-form-item>
          </el-col>
        </template>
      </el-row>
    </template>
  </el-form>
  <div class="form-actions">
    <el-button @click="() => emit('cancel')">Cancel</el-button>
    <el-button type="primary" :loading="props.submitting" @click="handleSubmit">Save</el-button>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
import TagInput from './TagInput.vue'
import {
  CRON5_RE,
  CRON_PRESETS,
  IANA_TIMEZONES,
  KIND_LABELS,
  defaultSource,
} from './Constants'
import type { Agent, Repository } from '@/api/types'
import type { PlanFormModel, PlanKind } from './Types'

const props = defineProps<{
  model: PlanFormModel
  agents: Agent[]
  repositories: Repository[]
  submitting: boolean
}>()
const emit = defineEmits<{
  (e: 'submit', model: PlanFormModel): void
  (e: 'cancel'): void
}>()

const formRef = ref<FormInstance | null>(null)

const planKinds = Object.entries(KIND_LABELS).map(([value, label]) => ({
  value: value as PlanKind,
  label,
}))

const filteredRepos = computed(() =>
  props.repositories.filter((r) => r.agent_id === props.model.agent_id),
)

function onKindChange(kind: PlanKind): void {
  props.model.source = defaultSource(kind)
}
function onAgentChange(): void {
  const repo = filteredRepos.value.find((r) => r.id === props.model.repository_id)
  if (repo && repo.agent_id !== props.model.agent_id) {
    props.model.repository_id = ''
  }
}

function cronValidator(_rule: unknown, value: unknown, callback: (error?: string | Error) => void, ..._rest: unknown[]): void {
  const s = typeof value === 'string' ? value : ''
  if (!s) { callback(new Error('Schedule is required')); return }
  if (!CRON5_RE.test(s)) callback(new Error('Cron must be 5 space-separated fields'))
  else callback()
}
function positiveNumberValidator(_rule: unknown, value: unknown, callback: (error?: string | Error) => void, ..._rest: unknown[]): void {
  const n = typeof value === 'number' ? value : NaN
  if (!Number.isFinite(n) || n <= 0) callback(new Error('Must be greater than 0'))
  else callback()
}
function portValidator(_rule: unknown, value: unknown, callback: (error?: string | Error) => void, ..._rest: unknown[]): void {
  const n = typeof value === 'number' ? value : NaN
  if (!Number.isFinite(n) || n < 1 || n > 65535) callback(new Error('Port must be between 1 and 65535'))
  else callback()
}
function absolutePathValidator(_rule: unknown, value: unknown, callback: (error?: string | Error) => void, ..._rest: unknown[]): void {
  const p = typeof value === 'string' ? value.trim() : ''
  if (!p) callback(new Error('Path is required'))
  else if (!p.startsWith('/')) callback(new Error('Must be an absolute path'))
  else callback()
}
function pathsValidator(_rule: unknown, value: unknown, callback: (error?: string | Error) => void, ..._rest: unknown[]): void {
  const items: unknown[] = Array.isArray(value) ? value : []
  const paths: string[] = items.filter((p): p is string => typeof p === 'string')
  if (paths.length === 0) callback(new Error('At least one path is required'))
  else if (paths.some((p) => !p.startsWith('/'))) callback(new Error('Every path must be an absolute path'))
  else callback()
}

const rules: FormRules = {
  name: [{ required: true, message: 'Name is required', trigger: 'blur' }],
  agent_id: [{ required: true, message: 'Agent is required', trigger: 'change' }],
  kind: [{ required: true, message: 'Kind is required', trigger: 'change' }],
  repository_id: [{ required: true, message: 'Repository is required', trigger: 'change' }],
  schedule: [{ validator: cronValidator, trigger: 'blur' }],
  timezone: [{ required: true, message: 'Timezone is required', trigger: 'change' }],
  timeout_seconds: [{ required: true, message: 'Timeout is required', trigger: 'change' }],
  source: {
    host: [{ required: true, message: 'Host is required', trigger: 'blur' }],
    port: [{ required: true, message: 'Port is required', trigger: 'change' }, { validator: portValidator, trigger: 'change' }],
    username: [{ required: true, message: 'Username is required', trigger: 'blur' }],
    database: [{ required: true, message: "Database is required, or 'all'", trigger: 'blur' }],
    estimated_dump_bytes: [{ validator: positiveNumberValidator, message: 'Estimated dump bytes must be > 0', trigger: 'change' }],
    path: [{ validator: absolutePathValidator, trigger: 'blur' }],
    paths: [{ validator: pathsValidator, trigger: 'change' }],
  },
}

async function handleSubmit(): Promise<void> {
  let valid = false
  try { valid = await formRef.value?.validate() ?? false } catch { valid = false }
  if (!valid) return
  const r = props.model.retention
  if (r.keep_last <= 0 && r.keep_daily <= 0 && r.keep_weekly <= 0 && r.keep_monthly <= 0) {
    ElMessage.warning('At least one retention value must be greater than 0')
    return
  }
  emit('submit', props.model)
}


</script>

<style scoped>
.presets {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  align-items: center;
  margin-top: 6px;
}
.preset-label {
  font-size: 12px;
  color: #999;
}
.preset-tag {
  cursor: pointer;
}
.form-actions {
  padding: 12px 0 4px;
  border-top: 1px solid #f0f0f0;
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
.retention-hint {
  margin-top: 4px;
  font-size: 12px;
  color: #999;
}
.hint {
  margin-top: 4px;
  font-size: 12px;
  color: #999;
}
</style>