<template>
  <el-form ref="formRef" :model="props.model" :rules="rules" label-position="top" size="default">
    <el-row :gutter="16">
      <el-col :span="12">
        <el-form-item :label="t('plans.form.name')" prop="name">
          <el-input v-model="props.model.name" :placeholder="t('plans.form.namePlaceholder')" />
        </el-form-item>
      </el-col>
      <el-col :span="12">
        <el-form-item :label="t('plans.form.kind')" prop="kind">
          <el-select v-model="props.model.kind" :placeholder="t('plans.form.kindPlaceholder')" @change="onKindChange">
            <el-option v-for="k in planKinds" :key="k.value" :label="k.label" :value="k.value" />
          </el-select>
        </el-form-item>
      </el-col>
      <el-col :span="12">
        <el-form-item :label="t('plans.form.agent')" prop="agent_id">
          <el-select v-model="props.model.agent_id" filterable :placeholder="t('plans.form.agent')" @change="onAgentChange">
            <el-option v-for="a in props.agents" :key="a.id" :label="`${a.name} (${statusText(a.status)})`" :value="a.id">
              <span>{{ a.name }}</span>
              <el-tag
                :type="a.status === 'online' ? 'success' : 'info'"
                size="small"
                style="margin-left: 8px"
              >
                {{ statusText(a.status) }}
              </el-tag>
            </el-option>
          </el-select>
        </el-form-item>
      </el-col>
      <el-col :span="12">
        <el-form-item :label="t('plans.form.repository')" prop="repository_id">
          <el-select v-model="props.model.repository_id" :placeholder="t('plans.form.repository')" filterable>
            <el-option v-for="r in filteredRepos" :key="r.id" :label="`${r.storage_target_name} @ ${r.agent_name}`" :value="r.id">
              <span>{{ r.storage_target_name }} @ {{ r.agent_name }}</span>
              <span style="color: #999; margin-left: 8px; font-size: 12px">{{ r.repository_path }}</span>
            </el-option>
          </el-select>
        </el-form-item>
      </el-col>
      <el-col :span="12">
        <el-form-item :label="t('plans.form.schedule')" prop="schedule">
          <el-input v-model="props.model.schedule" :placeholder="t('plans.form.schedulePlaceholder')" />
          <div class="presets">
            <span class="preset-label">{{ t('plans.form.presets') }}</span>
            <el-tag
              v-for="p in cronPresets"
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
        <el-form-item :label="t('plans.form.timezone')" prop="timezone">
          <el-select v-model="props.model.timezone" filterable :placeholder="t('plans.form.timezonePlaceholder')" clearable>
            <el-option v-for="tz in IANA_TIMEZONES" :key="tz" :label="tz" :value="tz" />
          </el-select>
        </el-form-item>
      </el-col>
      <el-col :span="12">
        <el-form-item :label="t('plans.form.timeoutSeconds')" prop="timeout_seconds">
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
        <el-form-item :label="t('plans.form.retention')" prop="retention">
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
          <div class="retention-hint">{{ t('plans.form.retentionHint') }}</div>
        </el-form-item>
      </el-col>
    </el-row>

    <el-divider content-position="left">{{ t('plans.form.source') }}</el-divider>

    <template v-if="props.model.kind === 'filesystem'">
      <el-row :gutter="16">
        <el-col :span="24">
          <el-form-item :label="t('plans.form.paths')" prop="source.paths">
            <TagInput v-model="props.model.source.paths" :placeholder="t('plans.form.pathsPlaceholder')" />
          </el-form-item>
        </el-col>
        <el-col :span="24">
          <el-form-item :label="t('plans.form.excludes')">
            <TagInput v-model="props.model.source.excludes" :placeholder="t('plans.form.excludesPlaceholder')" />
          </el-form-item>
        </el-col>
        <el-col :span="24">
          <el-form-item :label="t('plans.form.oneFileSystem')">
            <el-switch
              :model-value="props.model.source.one_file_system === true"
              :active-text="t('plans.form.yes')"
              :inactive-text="t('plans.form.no')"
              @update:model-value="(v) => (props.model.source.one_file_system = v === true)"
            />
          </el-form-item>
        </el-col>
      </el-row>
    </template>

    <template v-else-if="props.model.kind === 'sqlite'">
      <el-row :gutter="16">
        <el-col :span="24">
          <el-form-item :label="t('plans.form.databasePath')" prop="source.path">
            <el-input v-model="props.model.source.path" :placeholder="t('plans.form.databasePathPlaceholder')" />
          </el-form-item>
        </el-col>
        <el-col :span="24">
          <el-form-item :label="t('plans.form.estimatedDumpBytes')" prop="source.estimated_dump_bytes">
            <el-input-number
              :model-value="props.model.source.estimated_dump_bytes"
              :min="1"
              :step="1073741824"
              :controls-position="'right'"
              @update:model-value="(v) => (props.model.source.estimated_dump_bytes = v ?? undefined)"
            />
            <div class="hint">{{ t('plans.form.dumpBytesHint') }}</div>
          </el-form-item>
        </el-col>
      </el-row>
    </template>

    <template v-else>
      <el-row :gutter="16">
        <el-col :span="16">
          <el-form-item :label="t('plans.form.host')" prop="source.host">
            <el-input v-model="props.model.source.host" :placeholder="t('plans.form.hostPlaceholder')" />
          </el-form-item>
        </el-col>
        <el-col :span="8">
          <el-form-item :label="t('plans.form.port')" prop="source.port">
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
          <el-form-item :label="t('plans.form.username')" prop="source.username">
            <el-input v-model="props.model.source.username" />
          </el-form-item>
        </el-col>
        <el-col :span="12">
          <el-form-item :label="t('plans.form.password')">
            <el-input
              v-model="props.model.source.password"
              type="password"
              show-password
              :placeholder="t('plans.form.passwordPlaceholder')"
            />
          </el-form-item>
        </el-col>
        <el-col :span="12">
          <el-form-item :label="t('plans.form.database')" prop="source.database">
            <el-input v-model="props.model.source.database" :placeholder="t('plans.form.databasePlaceholder')" />
          </el-form-item>
        </el-col>
        <el-col :span="12">
          <el-form-item :label="t('plans.form.extraArgs')">
            <TagInput v-model="props.model.source.extra_args" :placeholder="t('plans.form.extraArgsPlaceholder')" />
          </el-form-item>
        </el-col>
        <el-col :span="24">
          <el-form-item :label="t('plans.form.estimatedDumpBytes')" prop="source.estimated_dump_bytes">
            <el-input-number
              :model-value="props.model.source.estimated_dump_bytes"
              :min="1"
              :step="1073741824"
              :controls-position="'right'"
              @update:model-value="(v) => (props.model.source.estimated_dump_bytes = v ?? undefined)"
            />
            <div class="hint">{{ t('plans.form.dumpBytesHint') }}</div>
          </el-form-item>
        </el-col>
        <template v-if="props.model.kind === 'mongodb'">
          <el-col :span="24">
            <el-form-item :label="t('plans.form.captureOplog')">
              <el-switch
                :model-value="props.model.source.capture_oplog === true"
                :active-text="t('plans.form.yes')"
                :inactive-text="t('plans.form.no')"
                @update:model-value="(v) => (props.model.source.capture_oplog = v === true)"
              />
            </el-form-item>
          </el-col>
        </template>
      </el-row>
    </template>
  </el-form>
  <div class="form-actions">
    <el-button @click="() => emit('cancel')">{{ t('common.cancel') }}</el-button>
    <el-button type="primary" :loading="props.submitting" @click="handleSubmit">{{ t('common.save') }}</el-button>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
import TagInput from './TagInput.vue'
import {
  CRON5_RE,
  CRON_PRESETS,
  IANA_TIMEZONES,
  KIND_LABELS,
  defaultSource,
} from './Constants'
import { translateEnum } from '@/i18n'
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

const { t } = useI18n()

function statusText(status: string): string {
  return translateEnum('status', status)
}

const formRef = ref<FormInstance | null>(null)

// Labels resolve at render time so a language switch updates open forms too.
const planKinds = computed(() =>
  Object.entries(KIND_LABELS).map(([value, key]) => ({
    value: value as PlanKind,
    label: t(key),
  })),
)

const cronPresets = computed(() =>
  CRON_PRESETS.map((p) => ({ value: p.value, label: t(p.key) })),
)

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
  if (!s) { callback(new Error(t('plans.rules.scheduleRequired'))); return }
  if (!CRON5_RE.test(s)) callback(new Error(t('plans.rules.cronFields')))
  else callback()
}
function positiveNumberValidator(_rule: unknown, value: unknown, callback: (error?: string | Error) => void, ..._rest: unknown[]): void {
  const n = typeof value === 'number' ? value : NaN
  if (!Number.isFinite(n) || n <= 0) callback(new Error(t('plans.rules.greaterThanZero')))
  else callback()
}
function portValidator(_rule: unknown, value: unknown, callback: (error?: string | Error) => void, ..._rest: unknown[]): void {
  const n = typeof value === 'number' ? value : NaN
  if (!Number.isFinite(n) || n < 1 || n > 65535) callback(new Error(t('plans.rules.portRange')))
  else callback()
}
function absolutePathValidator(_rule: unknown, value: unknown, callback: (error?: string | Error) => void, ..._rest: unknown[]): void {
  const p = typeof value === 'string' ? value.trim() : ''
  if (!p) callback(new Error(t('plans.rules.pathRequired')))
  else if (!p.startsWith('/')) callback(new Error(t('plans.rules.absolutePath')))
  else callback()
}
function pathsValidator(_rule: unknown, value: unknown, callback: (error?: string | Error) => void, ..._rest: unknown[]): void {
  const items: unknown[] = Array.isArray(value) ? value : []
  const paths: string[] = items.filter((p): p is string => typeof p === 'string')
  if (paths.length === 0) callback(new Error(t('plans.rules.pathsRequired')))
  else if (paths.some((p) => !p.startsWith('/'))) callback(new Error(t('plans.rules.pathsAbsolute')))
  else callback()
}

const rules = computed<FormRules>(() => ({
  name: [{ required: true, message: t('plans.rules.nameRequired'), trigger: 'blur' }],
  agent_id: [{ required: true, message: t('plans.rules.agentRequired'), trigger: 'change' }],
  kind: [{ required: true, message: t('plans.rules.kindRequired'), trigger: 'change' }],
  repository_id: [{ required: true, message: t('plans.rules.repositoryRequired'), trigger: 'change' }],
  schedule: [{ validator: cronValidator, trigger: 'blur' }],
  timezone: [{ required: true, message: t('plans.rules.timezoneRequired'), trigger: 'change' }],
  timeout_seconds: [{ required: true, message: t('plans.rules.timeoutRequired'), trigger: 'change' }],
  source: {
    host: [{ required: true, message: t('plans.rules.hostRequired'), trigger: 'blur' }],
    port: [{ required: true, message: t('plans.rules.portRequired'), trigger: 'change' }, { validator: portValidator, trigger: 'change' }],
    username: [{ required: true, message: t('plans.rules.usernameRequired'), trigger: 'blur' }],
    database: [{ required: true, message: t('plans.rules.databaseRequired'), trigger: 'blur' }],
    estimated_dump_bytes: [{ validator: positiveNumberValidator, message: t('plans.rules.dumpBytesPositive'), trigger: 'change' }],
    path: [{ validator: absolutePathValidator, trigger: 'blur' }],
    paths: [{ validator: pathsValidator, trigger: 'change' }],
  },
}))

async function handleSubmit(): Promise<void> {
  let valid = false
  try { valid = await formRef.value?.validate() ?? false } catch { valid = false }
  if (!valid) return
  const r = props.model.retention
  if (r.keep_last <= 0 && r.keep_daily <= 0 && r.keep_weekly <= 0 && r.keep_monthly <= 0) {
    ElMessage.warning(t('plans.form.retentionWarning'))
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
