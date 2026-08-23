<template>
  <div>
    <el-tabs v-model="activeTab" stretch type="border-card">
      <el-tab-pane :label="t('storage.tabs.targets')" name="targets">
        <template #label>
          <span>
            <el-icon><Folder /></el-icon>
            <span>{{ t('storage.tabs.targets') }}</span>
          </span>
        </template>
        <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 16px">
          <div></div>
          <div>
            <el-button type="primary" @click="openImportDialog">
              <el-icon><Upload /></el-icon>
              <span>{{ t('storage.importRcloneConfig') }}</span>
            </el-button>
            <el-button @click="loadTargets">
              <el-icon><Refresh /></el-icon>
              <span>{{ t('common.refresh') }}</span>
            </el-button>
          </div>
        </div>

        <div v-if="targetsError" class="error-state">
          <el-icon><Warning /></el-icon>
          <p>{{ targetsError }}</p>
          <el-button type="primary" @click="loadTargets" style="margin-top: 12px">{{ t('common.retry') }}</el-button>
        </div>

        <div v-else-if="targetsLoading" style="text-align: center; padding: 40px">
          <el-icon class="is-loading"><Loading /></el-icon>
        </div>

        <el-table
          v-else
          :data="targets"
          stripe
          row-key="id"
          style="width: 100%"
        >
          <el-table-column :label="t('common.name')" width="200">
            <template #default="{ row }">
              <strong>{{ row.name }}</strong>
            </template>
          </el-table-column>
          <el-table-column :label="t('storage.columns.type')" width="100">
            <template #default="{ row }">
              <el-tag size="small" type="info">{{ row.type }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column :label="t('storage.columns.remoteName')" width="180">
            <template #default="{ row }">
              <el-tag size="small" effect="plain">{{ row.remote_name }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column :label="t('storage.columns.remotePath')">
            <template #default="{ row }">
              {{ row.remote_path || '/' }}
            </template>
          </el-table-column>
          <el-table-column :label="t('storage.columns.createdAt')" width="220">
            <template #default="{ row }">
              {{ formatTime(row.created_at) }}
            </template>
          </el-table-column>
          <el-table-column :label="t('storage.columns.updatedAt')" width="220">
            <template #default="{ row }">
              {{ formatTime(row.updated_at) }}
            </template>
          </el-table-column>
          <el-table-column :label="t('common.actions')" width="100" fixed="right">
            <template #default="{ row }">
              <el-button type="danger" text size="small" @click="handleDeleteTarget(row as StorageTarget)">
                {{ t('common.delete') }}
              </el-button>
            </template>
          </el-table-column>
        </el-table>

        <el-empty v-if="!targetsLoading && !targetsError && targets.length === 0"
          :description="t('storage.emptyTargets')" />
      </el-tab-pane>

      <el-tab-pane :label="t('storage.tabs.repositories')" name="repos">
        <template #label>
          <span>
            <el-icon><Collection /></el-icon>
            <span>{{ t('storage.tabs.repositories') }}</span>
          </span>
        </template>
        <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 16px">
          <div></div>
          <div>
            <el-button type="primary" @click="openBindRepoDialog" :disabled="targets.length === 0 || agents.length === 0">
              <el-icon><Link /></el-icon>
              <span>{{ t('storage.bindRepository') }}</span>
            </el-button>
            <el-button @click="loadRepos">
              <el-icon><Refresh /></el-icon>
              <span>{{ t('common.refresh') }}</span>
            </el-button>
          </div>
        </div>

        <div v-if="reposError" class="error-state">
          <el-icon><Warning /></el-icon>
          <p>{{ reposError }}</p>
          <el-button type="primary" @click="loadRepos" style="margin-top: 12px">{{ t('common.retry') }}</el-button>
        </div>

        <div v-else-if="reposLoading" style="text-align: center; padding: 40px">
          <el-icon class="is-loading"><Loading /></el-icon>
        </div>

        <el-table
          v-else
          :data="repos"
          stripe
          row-key="id"
          style="width: 100%"
        >
          <el-table-column :label="t('storage.columns.agent')" width="200">
            <template #default="{ row }">
              {{ row.agent_name || row.agent_id }}
            </template>
          </el-table-column>
          <el-table-column :label="t('storage.columns.storageTarget')" width="200">
            <template #default="{ row }">
              {{ row.storage_target_name || row.storage_target_id }}
            </template>
          </el-table-column>
          <el-table-column :label="t('storage.columns.repositoryPath')">
            <template #default="{ row }">
              <code style="font-size: 12px">{{ row.repository_path }}</code>
            </template>
          </el-table-column>
          <el-table-column :label="t('common.status')" width="100">
            <template #default="{ row }">
              <el-tag
                :type="row.status === 'ready' ? 'success' : 'warning'"
                size="small"
              >
                {{ statusText(row.status) }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column :label="t('storage.columns.lastCheck')" width="220">
            <template #default="{ row }">
              {{ row.last_check_at ? formatTime(row.last_check_at) : t('common.never') }}
            </template>
          </el-table-column>
        </el-table>

        <el-empty v-if="!reposLoading && !reposError && repos.length === 0"
          :description="t('storage.emptyRepositories')" />
      </el-tab-pane>
    </el-tabs>

    <!-- Import rclone Config Dialog -->
    <el-dialog
      v-model="importDialogVisible"
      :title="t('storage.importRcloneConfig')"
      width="620"
      destroy-on-close
      :close-on-click-modal="false"
    >
      <el-form :model="importForm" label-position="top" label-width="120px">
        <el-form-item :label="t('storage.importDialog.name')" required>
          <el-input v-model="importForm.name" :placeholder="t('storage.importDialog.namePlaceholder')" />
        </el-form-item>

        <el-form-item label="rclone.conf" required>
          <el-input
            v-model="importForm.rclone_conf"
            type="textarea"
            :rows="8"
            placeholder="[remote]
type = drive
token = ..."
          />
        </el-form-item>

        <el-form-item :label="t('storage.importDialog.validationAgent')" required>
          <el-select
            v-model="importForm.validate_agent_id"
            :placeholder="t('storage.importDialog.validationAgentPlaceholder')"
            style="width: 100%"
            :disabled="onlineAgents.length === 0"
          >
            <el-option
              v-for="a in onlineAgents"
              :key="a.id"
              :label="`${a.name} (${a.hostname})`"
              :value="a.id"
            />
            <el-option
              v-for="a in offlineAgents"
              :key="a.id"
              :label="t('storage.importDialog.offlineSuffix', { name: a.name, hostname: a.hostname })"
              :value="a.id"
              disabled
            />
          </el-select>
          <el-text size="small" v-if="agents.length === 0" color="warning">
            {{ t('storage.importDialog.noAgents') }}
          </el-text>
        </el-form-item>

        <el-form-item :label="t('storage.importDialog.remoteName')" required>
          <el-input v-model="importForm.remote_name" :placeholder="t('storage.importDialog.remoteNamePlaceholder')" />
        </el-form-item>

        <el-form-item :label="t('storage.importDialog.remotePath')">
          <el-input v-model="importForm.remote_path" :placeholder="t('storage.importDialog.remotePathPlaceholder')" />
        </el-form-item>
      </el-form>

      <div v-if="validateResult" style="margin-top: 12px">
        <el-alert
          :title="`remote_type: ${validateResult.remote_type}`"
          type="info"
          :closable="false"
        >
          <template #default>
            <div style="margin-top: 8px">
              <span>{{ t('storage.importDialog.lsdEntries') }} <strong>{{ validateResult.lsd_entries.length }}</strong></span>
              <el-table
                v-if="validateResult.lsd_entries.length > 0"
                :data="validateResult.lsd_entries.slice(0, 20)"
                size="small"
                style="margin-top: 8px; max-height: 160px; overflow: auto"
              >
                <el-table-column :label="t('common.name')">
                  <template #default="{ row }">
                    <el-icon v-if="row.is_dir" style="color: #e6a23c"><Folder /></el-icon>
                    <el-icon v-else><Document /></el-icon>
                    {{ row.name }}
                  </template>
                </el-table-column>
                <el-table-column :label="t('storage.importDialog.isDir')" width="60">
                  <template #default="{ row }">
                    <el-tag size="small" :type="row.is_dir ? 'warning' : 'info'">
                      {{ row.is_dir ? t('storage.importDialog.dir') : t('storage.importDialog.file') }}
                    </el-tag>
                  </template>
                </el-table-column>
              </el-table>
              <el-text v-if="validateResult.lsd_entries.length > 20" size="small" color="info">
                {{ t('storage.importDialog.moreEntries', { count: validateResult.lsd_entries.length - 20 }) }}
              </el-text>
            </div>
          </template>
        </el-alert>
      </div>

      <el-divider />

      <div style="display: flex; gap: 8px; justify-content: flex-end">
        <el-button
          type="info"
          @click="handleValidate"
          :loading="validateLoading"
          :disabled="!importForm.rclone_conf || !importForm.remote_name || !importForm.validate_agent_id"
        >
          <el-icon><Search /></el-icon>
          <span>{{ t('storage.importDialog.validateFirst') }}</span>
        </el-button>
        <el-button
          type="primary"
          @click="handleImport"
          :loading="importLoading"
          :disabled="!importForm.name || !importForm.rclone_conf || !importForm.remote_name || !importForm.validate_agent_id"
        >
          <span>{{ t('storage.importDialog.confirmImport') }}</span>
        </el-button>
      </div>
    </el-dialog>

    <!-- Bind Repository Dialog -->
    <el-dialog
      v-model="bindDialogVisible"
      :title="t('storage.bindRepository')"
      width="480"
      destroy-on-close
      :close-on-click-modal="false"
    >
      <el-form :model="bindForm" label-position="top" label-width="120px">
        <el-form-item :label="t('storage.bindDialog.agent')" required>
          <el-select
            v-model="bindForm.agent_id"
            :placeholder="t('storage.bindDialog.selectAgent')"
            style="width: 100%"
          >
            <el-option
              v-for="a in onlineAgents"
              :key="a.id"
              :label="`${a.name} (${a.hostname})`"
              :value="a.id"
            />
            <el-option
              v-for="a in offlineAgents"
              :key="a.id"
              :label="t('storage.importDialog.offlineSuffix', { name: a.name, hostname: a.hostname })"
              :value="a.id"
              disabled
            />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('storage.bindDialog.storageTarget')" required>
          <el-select
            v-model="bindForm.storage_target_id"
            :placeholder="t('storage.bindDialog.selectTarget')"
            style="width: 100%"
          >
            <el-option
              v-for="tgt in targets"
              :key="tgt.id"
              :label="`${tgt.name} (${tgt.remote_name}:${tgt.remote_path || '/'})`"
              :value="tgt.id"
            />
          </el-select>
        </el-form-item>
      </el-form>

      <div v-if="bindResult" style="margin-top: 12px">
        <el-alert :title="t('storage.bindDialog.boundSuccessfully')" type="success" :closable="false">
          <template #default>
            <div style="margin-top: 8px">
              <el-form label-position="top" size="small">
                <el-form-item :label="t('storage.columns.repositoryPath')">
                  <code>{{ bindResult.repository_path }}</code>
                </el-form-item>
                <el-form-item :label="t('common.status')">
                  <el-tag :type="bindResult.status === 'ready' ? 'success' : 'warning'">
                    {{ statusText(bindResult.status) }}
                  </el-tag>
                </el-form-item>
              </el-form>
            </div>
          </template>
        </el-alert>
      </div>

      <template #footer>
        <el-button @click="bindDialogVisible = false">{{ t('common.close') }}</el-button>
        <el-button
          v-if="!bindResult"
          type="primary"
          @click="handleBindRepo"
          :loading="bindLoading"
          :disabled="!bindForm.agent_id || !bindForm.storage_target_id"
        >
          {{ t('storage.bindRepository') }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { apiGet, apiPost, apiDelete } from '@/api/client'
import { ElMessage, ElMessageBox } from 'element-plus'
import { translateEnum, formatDateTime } from '@/i18n'
import type { Agent, StorageTarget, Repository } from '@/api/types'

const { t } = useI18n()

function statusText(status: string): string {
  return translateEnum('status', status)
}

// Local types
interface StorageTargetValidateResponse {
  remote_type: string
  lsd_entries: Array<{ name: string; is_dir: boolean }>
}

const activeTab = ref('targets')

const agents = ref<Agent[]>([])
const targets = ref<StorageTarget[]>([])
const repos = ref<Repository[]>([])

const targetsLoading = ref(false)
const targetsError = ref('')
const reposLoading = ref(false)
const reposError = ref('')

const onlineAgents = computed(() => agents.value.filter((a) => a.status === 'online'))
const offlineAgents = computed(() => agents.value.filter((a) => a.status !== 'online'))

// ---- Storage target dialog ----
const importDialogVisible = ref(false)
const importForm = ref({
  name: '',
  rclone_conf: '',
  remote_name: '',
  remote_path: '',
  validate_agent_id: '',
})
const validateLoading = ref(false)
const validateResult = ref<StorageTargetValidateResponse | null>(null)
const importLoading = ref(false)

// ---- Bind repository dialog ----
const bindDialogVisible = ref(false)
const bindForm = ref({
  agent_id: '',
  storage_target_id: '',
})
const bindLoading = ref(false)
const bindResult = ref<Repository | null>(null)

function formatTime(iso: string): string {
  return formatDateTime(iso)
}

async function loadAgents(): Promise<void> {
  try {
    agents.value = await apiGet<Agent[]>('/agents')
  } catch {
    /* keep existing data if unavailable */
  }
}

async function loadTargets(): Promise<void> {
  targetsLoading.value = true
  targetsError.value = ''
  try {
    targets.value = await apiGet<StorageTarget[]>('/storage-targets')
  } catch (err: unknown) {
    const e = err as { message?: string }
    targetsError.value = e.message || t('storage.targetsLoadFailed')
  } finally {
    targetsLoading.value = false
  }
}

async function loadRepos(): Promise<void> {
  reposLoading.value = true
  reposError.value = ''
  try {
    repos.value = await apiGet<Repository[]>('/repositories')
  } catch (err: unknown) {
    const e = err as { message?: string }
    reposError.value = e.message || t('storage.reposLoadFailed')
  } finally {
    reposLoading.value = false
  }
}

function openImportDialog(): void {
  importForm.value = { name: '', rclone_conf: '', remote_name: '', remote_path: '', validate_agent_id: '' }
  validateResult.value = null
  importDialogVisible.value = true
}

async function handleValidate(): Promise<void> {
  validateLoading.value = true
  validateResult.value = null
  try {
    validateResult.value = await apiPost<StorageTargetValidateResponse>('/storage-targets/validate', {
      rclone_conf: importForm.value.rclone_conf,
      remote_name: importForm.value.remote_name,
      validate_agent_id: importForm.value.validate_agent_id,
    })
    ElMessage.success(t('storage.importDialog.validateSucceeded'))
  } catch (err: unknown) {
    const e = err as { message?: string; code?: string }
    ElMessage.error(e.message || t('storage.importDialog.validateFailedCode', { code: e.code || 'unknown' }))
  } finally {
    validateLoading.value = false
  }
}

async function handleImport(): Promise<void> {
  importLoading.value = true
  try {
    await apiPost<StorageTarget>('/storage-targets', {
      name: importForm.value.name,
      rclone_conf: importForm.value.rclone_conf,
      remote_name: importForm.value.remote_name,
      remote_path: importForm.value.remote_path,
      validate_agent_id: importForm.value.validate_agent_id,
      validate: true,
    })
    ElMessage.success(t('storage.importDialog.imported'))
    importDialogVisible.value = false
    await loadTargets()
  } catch (err: unknown) {
    const e = err as { message?: string; code?: string }
    ElMessage.error(e.message || t('storage.importDialog.importFailedCode', { code: e.code || 'unknown' }))
  } finally {
    importLoading.value = false
  }
}

async function handleDeleteTarget(target: StorageTarget): Promise<void> {
  try {
    await ElMessageBox.confirm(
      t('storage.deleteDialog.confirm', { name: target.name }),
      t('storage.deleteDialog.title'),
      { type: 'warning', confirmButtonText: t('common.delete') },
    )
    await apiDelete(`/storage-targets/${target.id}`)
    ElMessage.success(t('storage.deleteDialog.deleted'))
    await loadTargets()
  } catch (err: unknown) {
    if (err === 'cancel') return
    const e = err as { message?: string; code?: string }
    if (e.code === 'conflict') {
      ElMessage.warning(t('storage.deleteDialog.conflict'))
    } else {
      ElMessage.error(e.message || t('storage.deleteDialog.failedCode', { code: e.code || 'unknown' }))
    }
  }
}

function openBindRepoDialog(): void {
  bindForm.value = { agent_id: '', storage_target_id: '' }
  bindResult.value = null
  bindDialogVisible.value = true
}

async function handleBindRepo(): Promise<void> {
  bindLoading.value = true
  bindResult.value = null
  try {
    bindResult.value = await apiPost<Repository>('/repositories', {
      agent_id: bindForm.value.agent_id,
      storage_target_id: bindForm.value.storage_target_id,
    })
    ElMessage.success(t('storage.bindDialog.bound'))
    await loadRepos()
  } catch (err: unknown) {
    const e = err as { message?: string; code?: string }
    ElMessage.error(e.message || t('storage.bindDialog.bindFailedCode', { code: e.code || 'unknown' }))
  } finally {
    bindLoading.value = false
  }
}

onMounted(async () => {
  await loadAgents()
  await loadTargets()
  await loadRepos()
})
</script>
