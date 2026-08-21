<template>
  <div>
    <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 16px">
      <div class="section-title" style="margin-bottom: 0">Agents</div>
      <div>
        <el-button type="primary" @click="handleGenerateToken">
          <el-icon><Key /></el-icon>
          <span>Generate Enrollment Token</span>
        </el-button>
        <el-button @click="loadAgents">
          <el-icon><Refresh /></el-icon>
          <span>Refresh</span>
        </el-button>
      </div>
    </div>

    <div v-if="error" class="error-state">
      <el-icon><Warning /></el-icon>
      <p>{{ error }}</p>
      <el-button type="primary" @click="loadAgents" style="margin-top: 12px">
        Retry
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
              <el-table-column label="Tool" width="160">
                <template #default="{ row: cap }">
                  <strong>{{ cap.name }}</strong>
                </template>
              </el-table-column>
              <el-table-column label="Version">
                <template #default="{ row: cap }">
                  {{ cap.version }}
                </template>
              </el-table-column>
              <el-table-column label="Path">
                <template #default="{ row: cap }">
                  <el-tag size="small" type="info" effect="plain">
                    {{ cap.path }}
                  </el-tag>
                </template>
              </el-table-column>
            </el-table>
            <el-empty v-else description="No capabilities detected" :image-size="40" />
          </div>
        </template>
      </el-table-column>
      <el-table-column label="Name" width="180">
        <template #default="{ row }">
          {{ row.name }}
        </template>
      </el-table-column>
      <el-table-column label="Hostname">
        <template #default="{ row }">
          {{ row.hostname }}
        </template>
      </el-table-column>
      <el-table-column label="OS" width="120">
        <template #default="{ row }">
          {{ row.os }}
        </template>
      </el-table-column>
      <el-table-column label="Arch" width="80">
        <template #default="{ row }">
          {{ row.arch }}
        </template>
      </el-table-column>
      <el-table-column label="Version" width="100">
        <template #default="{ row }">
          <el-tag size="small">{{ row.version }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="Status" width="100">
        <template #default="{ row }">
          <el-tag :type="row.status === 'online' ? 'success' : 'danger'">
            {{ row.status }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="Last Seen" width="200">
        <template #default="{ row }">
          {{ formatTime(row.last_seen_at) }}
        </template>
      </el-table-column>
      <el-table-column label="Enrolled At" width="200">
        <template #default="{ row }">
          {{ formatTime(row.enrolled_at) }}
        </template>
      </el-table-column>
      <el-table-column label="Actions" width="100" fixed="right">
        <template #default="{ row }">
          <el-button
            type="danger"
            text
            size="small"
            @click="handleRevoke(row)"
          >
            Revoke
          </el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- Enrollment Token Dialog -->
    <el-dialog
      v-model="tokenDialogVisible"
      title="New Enrollment Token"
      width="480"
      destroy-on-close
    >
      <div v-if="tokenLoading" style="text-align: center; padding: 20px">
        <el-icon class="is-loading"><Loading /></el-icon>
      </div>
      <div v-else-if="tokenData">
        <el-alert
          title="This token is shown only once. Copy it immediately."
          type="warning"
          :closable="false"
          style="margin-bottom: 12px"
        />
        <el-form label-position="top">
          <el-form-item label="Token">
            <div
              class="token-display"
              @click="copyToken"
              title="Click to copy"
            >
              {{ tokenData.token }}
            </div>
          </el-form-item>
          <el-form-item label="Expires At">
            {{ formatTime(tokenData.expires_at) }}
          </el-form-item>
        </el-form>
        <el-button type="primary" @click="copyToken" :loading="copying">
          <el-icon><CopyDocument /></el-icon>
          <span>Copy Token</span>
        </el-button>
      </div>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { apiGet, apiPost, apiDelete } from '@/api/client'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { Agent, EnrollmentTokenResponse } from '@/api/types'

const loading = ref(false)
const error = ref('')
const agents = ref<Agent[]>([])

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
  if (!iso) return ''
  try {
    const d = new Date(iso)
    if (isNaN(d.getTime())) return iso
    return d.toLocaleString(undefined, {
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
    })
  } catch {
    return iso
  }
}

async function loadAgents(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    agents.value = await apiGet<Agent[]>('/agents')
  } catch (err: any) {
    error.value = err.message || 'Failed to load agents. Is the server running?'
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
    ElMessage.error(err.message || 'Failed to generate token')
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
    ElMessage.success('Token copied to clipboard')
  } catch {
    ElMessage.error('Failed to copy to clipboard')
  } finally {
    copying.value = false
  }
}

async function handleRevoke(agent: any): Promise<void> {
  try {
    await ElMessageBox.confirm(
      `Are you sure you want to revoke agent "${agent.name}" (${agent.hostname})?`,
      'Revoke Agent',
      { type: 'warning', confirmButtonText: 'Revoke' },
    )
    await apiDelete(`/agents/${agent.id}`)
    ElMessage.success('Agent revoked')
    await loadAgents()
  } catch (err: any) {
    if (err !== 'cancel') {
      ElMessage.error(err.message || 'Revoke failed')
    }
  }
}

loadAgents()
</script>