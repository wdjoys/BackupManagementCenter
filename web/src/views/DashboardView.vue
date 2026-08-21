<template>
  <div>
    <div class="section-title">Dashboard</div>

    <div v-if="error" class="error-state">
      <el-icon><Warning /></el-icon>
      <p>{{ error }}</p>
      <el-button type="primary" @click="loadData" style="margin-top: 12px">
        Retry
      </el-button>
    </div>

    <div v-else-if="loading" style="text-align: center; padding: 40px">
      <el-icon class="is-loading"><Loading /></el-icon>
      <p style="margin-top: 12px">Loading...</p>
    </div>

    <div v-else>
      <div class="stat-cards">
        <div class="stat-card">
          <div class="stat-label">Online Agents</div>
          <div class="stat-value" style="color: #67c23a">{{ dashboard.agents_online }} / {{ dashboard.agents_total }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Last 24h Succeeded</div>
          <div class="stat-value" style="color: #67c23a">{{ dashboard.runs_24h_succeeded }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Last 24h Failed</div>
          <div class="stat-value" style="color: #f56c6c">{{ dashboard.runs_24h_failed }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">Repos Needing Check</div>
          <div class="stat-value" style="color: #e6a23c">{{ dashboard.repos_needing_check.length }}</div>
        </div>
      </div>

      <el-row :gutter="16">
        <el-col :span="16">
          <el-card shadow="never">
            <template #header>
              <span style="font-weight: 600">Next Scheduled</span>
            </template>
            <el-table
              v-if="dashboard.next_scheduled.length > 0"
              :data="dashboard.next_scheduled"
              stripe
              style="width: 100%"
            >
              <el-table-column label="Plan" :width="200">
                <template #default="{ row }">
                  {{ row.plan_name }}
                </template>
              </el-table-column>
              <el-table-column label="Next Fire At">
                <template #default="{ row }">
                  {{ formatTime(row.next_fire_at) }}
                </template>
              </el-table-column>
            </el-table>
            <el-empty v-else description="No upcoming scheduled plans" />
          </el-card>
        </el-col>

        <el-col :span="8">
          <el-card shadow="never">
            <template #header>
              <span style="font-weight: 600">Repos Needing Check</span>
            </template>
            <el-table
              v-if="dashboard.repos_needing_check.length > 0"
              :data="dashboard.repos_needing_check"
              stripe
              style="width: 100%"
            >
              <el-table-column label="Name">
                <template #default="{ row }">
                  {{ row.name }}
                </template>
              </el-table-column>
              <el-table-column label="Last Check">
                <template #default="{ row }">
                  {{ row.last_check_at ? formatTime(row.last_check_at) : 'Never' }}
                </template>
              </el-table-column>
            </el-table>
            <el-empty v-else description="All repositories are healthy" />
          </el-card>
        </el-col>
      </el-row>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { apiGet } from '@/api/client'
import type { Dashboard } from '@/api/types'

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

async function loadData(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    dashboard.value = await apiGet<Dashboard>('/dashboard')
  } catch (err: any) {
    error.value = err.message || 'Failed to load dashboard data. Is the server running?'
  } finally {
    loading.value = false
  }
}

onMounted(loadData)
</script>