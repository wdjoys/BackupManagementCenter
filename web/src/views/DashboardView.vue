<template>
  <div>
    <div class="section-title">{{ t('dashboard.title') }}</div>

    <div v-if="error" class="error-state">
      <el-icon><Warning /></el-icon>
      <p>{{ error }}</p>
      <el-button type="primary" @click="loadData" style="margin-top: 12px">
        {{ t('common.retry') }}
      </el-button>
    </div>

    <div v-else-if="loading" style="text-align: center; padding: 40px">
      <el-icon class="is-loading"><Loading /></el-icon>
      <p style="margin-top: 12px">{{ t('common.loading') }}</p>
    </div>

    <div v-else>
      <div class="stat-cards">
        <div class="stat-card">
          <div class="stat-label">{{ t('dashboard.onlineAgents') }}</div>
          <div class="stat-value" style="color: #67c23a">{{ dashboard.agents_online }} / {{ dashboard.agents_total }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">{{ t('dashboard.succeeded24h') }}</div>
          <div class="stat-value" style="color: #67c23a">{{ dashboard.runs_24h_succeeded }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">{{ t('dashboard.failed24h') }}</div>
          <div class="stat-value" style="color: #f56c6c">{{ dashboard.runs_24h_failed }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">{{ t('dashboard.reposNeedingCheck') }}</div>
          <div class="stat-value" style="color: #e6a23c">{{ dashboard.repos_needing_check.length }}</div>
        </div>
      </div>

      <el-row :gutter="16">
        <el-col :span="16">
          <el-card shadow="never">
            <template #header>
              <span style="font-weight: 600">{{ t('dashboard.nextScheduled') }}</span>
            </template>
            <el-table
              v-if="dashboard.next_scheduled.length > 0"
              :data="dashboard.next_scheduled"
              stripe
              style="width: 100%"
            >
              <el-table-column :label="t('dashboard.plan')" :width="200">
                <template #default="{ row }">
                  {{ row.plan_name }}
                </template>
              </el-table-column>
              <el-table-column :label="t('dashboard.nextFireAt')">
                <template #default="{ row }">
                  {{ formatTime(row.next_fire_at) }}
                </template>
              </el-table-column>
            </el-table>
            <el-empty v-else :description="t('dashboard.noUpcomingPlans')" />
          </el-card>
        </el-col>

        <el-col :span="8">
          <el-card shadow="never">
            <template #header>
              <span style="font-weight: 600">{{ t('dashboard.reposNeedingCheck') }}</span>
            </template>
            <el-table
              v-if="dashboard.repos_needing_check.length > 0"
              :data="dashboard.repos_needing_check"
              stripe
              style="width: 100%"
            >
              <el-table-column :label="t('common.name')">
                <template #default="{ row }">
                  {{ row.name }}
                </template>
              </el-table-column>
              <el-table-column :label="t('dashboard.lastCheck')">
                <template #default="{ row }">
                  {{ row.last_check_at ? formatTime(row.last_check_at) : t('common.never') }}
                </template>
              </el-table-column>
            </el-table>
            <el-empty v-else :description="t('dashboard.allReposHealthy')" />
          </el-card>
        </el-col>
      </el-row>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { apiGet } from '@/api/client'
import { formatDateTime } from '@/i18n'
import type { Dashboard } from '@/api/types'

const { t } = useI18n()

const loading = ref(false)
const error = ref('')
const dashboard = ref<Dashboard>({
  agents_online: 0,
  agents_total: 0,
  runs_24h_succeeded: 0,
  runs_24h_failed: 0,
  next_scheduled: [],
  repos_needing_check: [],
})

function formatTime(iso: string): string {
  return formatDateTime(iso)
}

async function loadData(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    dashboard.value = await apiGet<Dashboard>('/dashboard')
  } catch (err: any) {
    error.value = err.message || t('dashboard.loadFailed')
  } finally {
    loading.value = false
  }
}

onMounted(loadData)
</script>
