<template>
  <div>
    <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 16px">
      <div class="section-title" style="margin-bottom: 0">{{ t('agents.title') }}</div>
      <div>
        <el-button type="primary" @click="handleGenerateToken">
          <el-icon><Key /></el-icon>
          <span>{{ t('agents.generateToken') }}</span>
        </el-button>
        <el-button @click="loadAgents">
          <el-icon><Refresh /></el-icon>
          <span>{{ t('common.refresh') }}</span>
        </el-button>
      </div>
    </div>

    <div v-if="error" class="error-state">
      <el-icon><Warning /></el-icon>
      <p>{{ error }}</p>
      <el-button type="primary" @click="loadAgents" style="margin-top: 12px">
        {{ t('common.retry') }}
      </el-button>
    </div>

    <div v-else-if="loading" style="text-align: center; padding: 40px">
      <el-icon class="is-loading"><Loading /></el-icon>
    </div>

    <el-table
      v-else
      :data="agents"
      stripe
      row-key="id"
      :expand-row-keys="expandedRows"
      @expand-change="handleExpand"
      style="width: 100%"
    >
      <el-table-column type="expand" width="32">
        <template #default="{ row }">
          <div style="padding: 12px 20px">
            <el-table
              v-if="row.capabilities && row.capabilities.length > 0"
              :data="row.capabilities"
              size="small"
              class="capabilities-table"
            >
              <el-table-column :label="t('agents.capabilities.tool')" width="160">
                <template #default="{ row: cap }">
                  <strong>{{ cap.name }}</strong>
                </template>
              </el-table-column>
              <el-table-column :label="t('agents.columns.version')">
                <template #default="{ row: cap }">
                  {{ cap.version }}
                </template>
              </el-table-column>
              <el-table-column :label="t('agents.capabilities.path')">
                <template #default="{ row: cap }">
                  <el-tag size="small" type="info" effect="plain">
                    {{ cap.path }}
                  </el-tag>
                </template>
              </el-table-column>
            </el-table>
            <el-empty v-else :description="t('agents.capabilities.empty')" :image-size="40" />
          </div>
        </template>
      </el-table-column>
      <el-table-column :label="t('common.name')" width="180">
        <template #default="{ row }">
          {{ row.name }}
        </template>
      </el-table-column>
      <el-table-column :label="t('agents.columns.hostname')">
        <template #default="{ row }">
          {{ row.hostname }}
        </template>
      </el-table-column>
      <el-table-column :label="t('agents.columns.os')" width="120">
        <template #default="{ row }">
          {{ row.os }}
        </template>
      </el-table-column>
      <el-table-column :label="t('agents.columns.arch')" width="80">
        <template #default="{ row }">
          {{ row.arch }}
        </template>
      </el-table-column>
      <el-table-column :label="t('agents.columns.version')" width="100">
        <template #default="{ row }">
          <el-tag size="small">{{ row.version }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column :label="t('common.status')" width="100">
        <template #default="{ row }">
          <el-tag :type="row.status === 'online' ? 'success' : 'danger'">
            {{ statusText(row.status) }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column :label="t('agents.columns.lastSeen')" width="200">
        <template #default="{ row }">
          {{ formatTime(row.last_seen_at) }}
        </template>
      </el-table-column>
      <el-table-column :label="t('agents.columns.enrolledAt')" width="200">
        <template #default="{ row }">
          {{ formatTime(row.enrolled_at) }}
        </template>
      </el-table-column>
      <el-table-column :label="t('common.actions')" width="100" fixed="right">
        <template #default="{ row }">
          <el-button
            type="danger"
            text
            size="small"
            @click="handleRevoke(row)"
          >
            {{ t('agents.revoke') }}
          </el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- Enrollment Token Dialog -->
    <el-dialog
      v-model="tokenDialogVisible"
      :title="t('agents.tokenDialog.title')"
      width="480"
      destroy-on-close
    >
      <div v-if="tokenLoading" style="text-align: center; padding: 20px">
        <el-icon class="is-loading"><Loading /></el-icon>
      </div>
      <div v-else-if="tokenData">
        <el-alert
          :title="t('agents.tokenDialog.onceWarning')"
          type="warning"
          :closable="false"
          style="margin-bottom: 12px"
        />
        <el-form label-position="top">
          <el-form-item :label="t('agents.tokenDialog.token')">
            <div
              class="token-display"
              @click="copyToken"
              :title="t('agents.tokenDialog.clickToCopy')"
            >
              {{ tokenData.token }}
            </div>
          </el-form-item>
          <el-form-item :label="t('agents.tokenDialog.expiresAt')">
            {{ formatTime(tokenData.expires_at) }}
          </el-form-item>
        </el-form>
        <el-button type="primary" @click="copyToken" :loading="copying">
          <el-icon><CopyDocument /></el-icon>
          <span>{{ t('agents.tokenDialog.copyButton') }}</span>
        </el-button>
      </div>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { apiGet, apiPost, apiDelete } from '@/api/client'
import { ElMessage, ElMessageBox } from 'element-plus'
import { translateEnum, formatDateTime } from '@/i18n'
import type { Agent, EnrollmentTokenResponse } from '@/api/types'

const { t } = useI18n()

const loading = ref(false)
const error = ref('')
const agents = ref<Agent[]>([])

function statusText(status: string): string {
  return translateEnum('status', status)
}

const expandedRows = ref<string[]>([])

function handleExpand(_row: unknown, expanded: any): void {
  expandedRows.value = expanded.map((r: any) => r.id as string)
}

// Token generation
const tokenDialogVisible = ref(false)
const tokenLoading = ref(false)
const tokenData = ref<EnrollmentTokenResponse | null>(null)
const copying = ref(false)

function formatTime(iso: string): string {
  return formatDateTime(iso)
}

async function loadAgents(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    agents.value = await apiGet<Agent[]>('/agents')
  } catch (err: any) {
    error.value = err.message || t('agents.loadFailed')
  } finally {
    loading.value = false
  }
}

async function handleGenerateToken(): Promise<void> {
  tokenDialogVisible.value = true
  tokenLoading.value = true
  tokenData.value = null
  try {
    tokenData.value = await apiPost<EnrollmentTokenResponse>('/enrollment-tokens', {})
  } catch (err: any) {
    ElMessage.error(err.message || t('agents.tokenDialog.generateFailed'))
    tokenDialogVisible.value = false
  } finally {
    tokenLoading.value = false
  }
}

async function copyToken(): Promise<void> {
  if (!tokenData.value) return
  copying.value = true
  try {
    await navigator.clipboard.writeText(tokenData.value.token)
    ElMessage.success(t('common.copied'))
  } catch {
    ElMessage.error(t('common.copyFailed'))
  } finally {
    copying.value = false
  }
}

async function handleRevoke(agent: any): Promise<void> {
  try {
    await ElMessageBox.confirm(
      t('agents.revokeDialog.confirm', { name: agent.name, hostname: agent.hostname }),
      t('agents.revokeDialog.title'),
      { type: 'warning', confirmButtonText: t('agents.revoke') },
    )
    await apiDelete(`/agents/${agent.id}`)
    ElMessage.success(t('agents.revoked'))
    await loadAgents()
  } catch (err: any) {
    if (err !== 'cancel') {
      ElMessage.error(err.message || t('agents.revokeFailed'))
    }
  }
}

loadAgents()
</script>
