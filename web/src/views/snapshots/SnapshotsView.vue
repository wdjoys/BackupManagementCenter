<template>
  <div>
    <div class="section-title">Snapshots</div>

    <div v-if="mainError" class="error-state">
      <el-icon><Warning /></el-icon>
      <p>{{ mainError }}</p>
      <el-button type="primary" @click="loadRepos" style="margin-top: 12px">Retry</el-button>
    </div>

    <el-card v-else shadow="never" style="margin-bottom: 16px">
      <template #header>
        <span style="font-weight: 600">Select Repository</span>
      </template>
      <el-select
        v-model="selectedRepoId"
        placeholder="Choose a repository to browse snapshots"
        style="width: 100%"
        :loading="reposLoading"
        :disabled="reposLoading"
        filterable
      >
        <el-option
          v-for="r in repos"
          :key="r.id"
          :label="`${r.agent_name || r.agent_id} / ${r.storage_target_name || r.storage_target_id} / ${r.repository_path}`"
          :value="r.id"
        />
      </el-select>
    </el-card>

    <el-row :gutter="16" v-if="selectedRepoId">
      <el-col :span="8">
        <el-card shadow="never">
          <template #header>
            <div style="display: flex; justify-content: space-between; align-items: center">
              <span style="font-weight: 600">Snapshots</span>
              <el-button size="small" @click="loadSnapshots">
                <el-icon><Refresh /></el-icon>
              </el-button>
            </div>
          </template>

          <div v-if="snapshotsLoading" style="text-align: center; padding: 16px">
            <el-icon class="is-loading"><Loading /></el-icon>
          </div>

          <el-table
            v-else
            :data="snapshots"
            stripe
            size="small"
            @row-click="handleSelectSnapshot"
            :row-class-name="({ row }) => selectedSnapshot?.id === row.id ? 'selected-row' : ''"
            style="width: 100%; cursor: pointer"
            height="520"
          >
            <el-table-column label="ID" width="80">
              <template #default="{ row }">
                <code style="font-size: 12px">{{ shortId(row.id) }}</code>
              </template>
            </el-table-column>
            <el-table-column label="Time" width="150">
              <template #default="{ row }">
                <span style="font-size: 12px">{{ formatTime(row.time) }}</span>
              </template>
            </el-table-column>
            <el-table-column label="Host" width="100">
              <template #default="{ row }">
                <span style="font-size: 12px">{{ row.host }}</span>
              </template>
            </el-table-column>
            <el-table-column label="Tags">
              <template #default="{ row }">
                <el-tag
                  v-for="tag in row.tags.slice(0, 3)"
                  :key="tag"
                  size="small"
                  effect="plain"
                  style="margin-right: 2px; margin-bottom: 2px"
                >
                  {{ tag }}
                </el-tag>
                <el-text v-if="row.tags.length > 3" size="small" color="info">
                  +{{ row.tags.length - 3 }}
                </el-text>
              </template>
            </el-table-column>
            <el-table-column label="Paths">
              <template #default="{ row }">
                <el-tag
                  v-for="p in row.paths.slice(0, 3)"
                  :key="p"
                  size="small"
                  type="info"
                  effect="plain"
                  style="margin-right: 2px; margin-bottom: 2px"
                >
                  {{ p }}
                </el-tag>
                <el-text v-if="row.paths.length > 3" size="small" color="info">
                  +{{ row.paths.length - 3 }}
                </el-text>
              </template>
            </el-table-column>
          </el-table>

          <el-empty v-if="!snapshotsLoading && snapshots.length === 0" description="No snapshots" :image-size="40" />
        </el-card>
      </el-col>

      <el-col :span="16">
        <el-card shadow="never" style="height: 580px; display: flex; flex-direction: column">
          <template #header>
            <div style="display: flex; justify-content: space-between; align-items: center">
              <div>
                <span style="font-weight: 600">File Browser</span>
                <span v-if="selectedSnapshot" style="font-size: 12px; color: #909399; margin-left: 8px">
                  snapshot {{ shortId(selectedSnapshot.id) }}
                </span>
              </div>
              <div>
                <el-button
                  v-if="selectedSnapshot"
                  size="small"
                  type="primary"
                  @click="openRestoreWizard"
                  :disabled="treeLoading || !treePath"
                >
                  <el-icon><Download /></el-icon>
                  <span>Restore this Snapshot</span>
                </el-button>
              </div>
            </div>
          </template>

          <div v-if="!selectedSnapshot" style="flex: 1; display: flex; align-items: center; justify-content: center">
            <el-empty description="Select a snapshot to browse its contents" />
          </div>

          <div v-else style="flex: 1; display: flex; flex-direction: column">
            <!-- Breadcrumb -->
            <el-breadcrumb separator="/" style="margin-bottom: 8px; padding: 4px 0">
              <el-breadcrumb-item
                v-for="part in breadcrumbs"
                :key="part.path"
                style="cursor: pointer"
                @click="navigateBreadcrumb(part.path)"
              >
                {{ part.label }}
              </el-breadcrumb-item>
            </el-breadcrumb>

            <div v-if="treeLoading" style="text-align: center; padding: 16px">
              <el-icon class="is-loading"><Loading /></el-icon>
            </div>

            <el-table
              v-else
              :data="treeEntries"
              stripe
              size="small"
              row-key="name"
              style="flex: 1"
              @selection-change="handleTreeSelection"
            >
              <el-table-column type="selection" width="40" />
              <el-table-column label="Name">
                <template #default="{ row }">
                  <span style="cursor: pointer" :style="{ color: row.type === 'dir' ? '#e6a23c' : 'inherit' }" @click="handleRowClick(row as TreeEntry)">
                    <el-icon v-if="row.type === 'dir'" style="color: #e6a23c"><Folder /></el-icon>
                    <el-icon v-else><Document /></el-icon>
                    {{ row.name }}
                  </span>
                </template>
              </el-table-column>
              <el-table-column label="Type" width="60">
                <template #default="{ row }">
                  <el-tag :type="row.type === 'dir' ? 'warning' : 'info'" size="small">{{ row.type }}</el-tag>
                </template>
              </el-table-column>
              <el-table-column label="Size" width="120">
                <template #default="{ row }">
                  {{ formatSize(row.size) }}
                </template>
              </el-table-column>
              <el-table-column label="Modified">
                <template #default="{ row }">
                  {{ formatTime(row.mtime) }}
                </template>
              </el-table-column>
            </el-table>
          </div>
        </el-card>
      </el-col>
    </el-row>

    <!-- Restore wizard dialog -->
    <el-dialog
      v-model="restoreDialogVisible"
      title="Restore Snapshot (Filesystem)"
      width="680"
      destroy-on-close
      :close-on-click-modal="false"
    >
      <el-form :model="restoreForm" label-position="top" label-width="130px">
        <el-form-item label="Snapshot" required>
          <el-text>
            <code>{{ selectedSnapshot?.id }}</code>
            &nbsp;
            <span style="color: #909399">({{ formatTime(selectedSnapshot?.time || '') }})</span>
          </el-text>
        </el-form-item>

        <el-form-item label="Target Path" required>
          <el-input
            v-model="restoreForm.target_path"
            placeholder="/absolute/path/on/agent"
          />
          <el-text size="small" color="info">Absolute path on the target agent's filesystem.</el-text>
        </el-form-item>

        <el-form-item label="Overwrite Mode" required>
          <el-radio-group v-model="restoreForm.overwrite_mode">
            <el-radio value="never">Never</el-radio>
            <el-radio value="if-changed">If Changed</el-radio>
            <el-radio value="always">Always</el-radio>
          </el-radio-group>
        </el-form-item>

        <el-form-item label="Include Paths">
          <el-checkbox-group v-model="restoreForm.include_paths">
            <div style="display: flex; flex-direction: column; gap: 4px; max-height: 200px; overflow-y: auto">
              <el-checkbox
                v-for="p in includePathOptions"
                :key="p"
                :value="p"
                style="margin-left: 0"
              >
                {{ p }}
              </el-checkbox>
              <el-checkbox
                v-if="treeEntries.length > 0 && includePathOptions.length < treeEntries.length"
                :value="'/ (all entries in current directory)'"
                style="margin-left: 0"
              >
                / (all entries in current directory)
              </el-checkbox>
            </div>
          </el-checkbox-group>
          <el-text size="small" color="info">
            Select items to restore. Leave empty to restore everything under the snapshot.
          </el-text>
        </el-form-item>
      </el-form>

      <el-divider />

      <div v-if="dryRunResult" style="margin-top: 12px">
        <el-alert title="Dry-run Result" type="success" :closable="false">
          <template #default>
            <div style="display: flex; gap: 16px; margin-top: 8px; flex-wrap: wrap">
              <el-statistic title="Add" :value="dryRunResult.add" />
              <el-statistic title="Changed" :value="dryRunResult.changed" />
              <el-statistic title="Delete" :value="dryRunResult.delete" />
            </div>
            <div v-if="dryRunResult.sample && dryRunResult.sample.length > 0" style="margin-top: 8px">
              <el-text size="small" style="font-weight: 600">Sample changes:</el-text>
              <el-tag
                v-for="s in dryRunResult.sample.slice(0, 10)"
                :key="s"
                size="small"
                type="info"
                effect="plain"
                style="margin-right: 4px; margin-top: 4px"
              >
                {{ s }}
              </el-tag>
              <el-text v-if="dryRunResult.sample.length > 10" size="small" color="info">
                ... and {{ dryRunResult.sample.length - 10 }} more
              </el-text>
            </div>
          </template>
        </el-alert>
      </div>

      <template #footer>
        <el-button @click="restoreDialogVisible = false">Cancel</el-button>
        <el-button
          type="info"
          @click="handleDryRun"
          :loading="dryRunLoading"
          :disabled="!restoreForm.target_path"
        >
          <el-icon><Search /></el-icon>
          <span>Dry-run</span>
        </el-button>
        <el-button
          type="primary"
          @click="handleConfirmRestore"
          :loading="restoreLoading"
          :disabled="!restoreForm.target_path || !dryRunResult"
        >
          <span>Confirm Execute</span>
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useRouter } from 'vue-router'
import { apiGet, apiPost } from '@/api/client'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { Repository, Snapshot, TreeEntry, TreeResponse } from '@/api/types'

// Local types
interface DryRunResult {
  add: number
  changed: number
  delete: number
  sample: string[]
}

interface RestoreResponse {
  restore_request_id: string
  run_id: string
}

interface BreadcrumbPart {
  label: string
  path: string
}

const repos = ref<Repository[]>([])
const reposLoading = ref(false)
const mainError = ref('')
const router = useRouter()

const selectedRepoId = ref<string>('')
const snapshots = ref<Snapshot[]>([])
const snapshotsLoading = ref(false)

const selectedSnapshot = ref<Snapshot | null>(null)
const treeLoading = ref(false)
const treeEntries = ref<TreeEntry[]>([])
const treePath = ref('/')
const treeSelectedPaths = ref<string[]>([])

const restoreDialogVisible = ref(false)
const restoreForm = ref({
  target_path: '',
  overwrite_mode: 'never' as 'never' | 'if-changed' | 'always',
  include_paths: [] as string[],
})
const dryRunLoading = ref(false)
const dryRunResult = ref<DryRunResult | null>(null)
const restoreLoading = ref(false)

const breadcrumbs = computed<BreadcrumbPart[]>(() => {
  const p = treePath.value
  if (p === '/' || p === '') return [{ label: '/', path: '/' }]
  const parts = p.split('/').filter(Boolean)
  const result: BreadcrumbPart[] = [{ label: '/', path: '/' }]
  let acc = ''
  for (const part of parts) {
    acc += '/' + part
    result.push({ label: part, path: acc })
  }
  return result
})

const includePathOptions = computed<string[]>(() => {
  return treeEntries.value
    .filter((e) => e.type === 'file' || e.type === 'dir')
    .map((e) => treePath.value === '/' ? '/' + e.name : treePath.value + '/' + e.name)
})

function shortId(id: string | undefined): string {
  if (!id) return ''
  return id.length > 8 ? id.slice(0, 8) : id
}

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

function formatSize(bytes: number): string {
  if (bytes <= 0) return '-'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  if (i === 0) return `${bytes} B`
  return `${(bytes / Math.pow(1024, i)).toFixed(1)} ${units[i]}`
}

async function loadRepos(): Promise<void> {
  reposLoading.value = true
  mainError.value = ''
  try {
    repos.value = await apiGet<Repository[]>('/repositories')
    // Pre-select the first repo
    if (repos.value.length > 0 && !selectedRepoId.value) {
      selectedRepoId.value = repos.value[0].id
    }
  } catch (err: unknown) {
    const e = err as { message?: string }
    mainError.value = e.message || 'Failed to load repositories.'
  } finally {
    reposLoading.value = false
  }
}

async function loadSnapshots(): Promise<void> {
  if (!selectedRepoId.value) return
  snapshotsLoading.value = true
  selectedSnapshot.value = null
  treeEntries.value = []
  treePath.value = '/'
  treeSelectedPaths.value = []
  try {
    snapshots.value = await apiGet<Snapshot[]>(`/repositories/${selectedRepoId.value}/snapshots`)
  } catch (err: unknown) {
    const e = err as { message?: string; code?: string }
    ElMessage.error(e.message || `Failed to load snapshots (code: ${e.code || 'unknown'})`)
    snapshots.value = []
  } finally {
    snapshotsLoading.value = false
  }
}

async function handleSelectSnapshot(snapshot: Snapshot): Promise<void> {
  selectedSnapshot.value = snapshot
  treePath.value = '/'
  treeSelectedPaths.value = []
  await loadTree()
}

async function loadTree(): Promise<void> {
  if (!selectedSnapshot.value || !selectedRepoId.value) return
  treeLoading.value = true
  try {
    const resp = await apiGet<TreeResponse>('/snapshots/' + selectedSnapshot.value.id + '/tree', {
      repo: selectedRepoId.value,
      path: treePath.value,
    })
    treeEntries.value = resp.entries || []
    treePath.value = resp.path || treePath.value
  } catch (err: unknown) {
    const e = err as { message?: string; code?: string }
    ElMessage.error(e.message || `Failed to load tree (code: ${e.code || 'unknown'})`)
  } finally {
    treeLoading.value = false
  }
}

function handleRowClick(entry: TreeEntry): void {
  if (entry.type !== 'dir') return
  const newPath = treePath.value === '/' ? '/' + entry.name : treePath.value + '/' + entry.name
  treePath.value = newPath
  treeSelectedPaths.value = []
  loadTree()
}

function navigateBreadcrumb(path: string): void {
  treePath.value = path
  treeSelectedPaths.value = []
  loadTree()
}

function handleTreeSelection(rows: TreeEntry[]): void {
  treeSelectedPaths.value = rows.map((r) => {
    const parent = treePath.value === '/' ? '/' : treePath.value
    return parent + '/' + r.name
  })
}

function openRestoreWizard(): void {
  if (!selectedSnapshot.value) return
  restoreForm.value = {
    target_path: '',
    overwrite_mode: 'never',
    include_paths: [...treeSelectedPaths.value],
  }
  dryRunResult.value = null
  restoreDialogVisible.value = true
}

async function handleDryRun(): Promise<void> {
  if (!selectedSnapshot.value || !selectedRepoId.value) return
  dryRunLoading.value = true
  dryRunResult.value = null
  try {
    dryRunResult.value = await apiPost<DryRunResult>('/restores/dry-run', {
      repository_id: selectedRepoId.value,
      snapshot_id: selectedSnapshot.value.id,
      include_paths: restoreForm.value.include_paths,
      target_path: restoreForm.value.target_path,
    })
    ElMessage.success('Dry-run completed')
  } catch (err: unknown) {
    const e = err as { message?: string; code?: string }
    ElMessage.error(e.message || `Dry-run failed (code: ${e.code || 'unknown'})`)
  } finally {
    dryRunLoading.value = false
  }
}

async function handleConfirmRestore(): Promise<void> {
  if (!selectedSnapshot.value || !selectedRepoId.value || !dryRunResult.value) return
  try {
const { value } = await ElMessageBox.prompt(
      'Confirm restore by typing the snapshot ID or a plan name:',
      'Confirm Restore',
      {
        confirmButtonText: 'Execute',
        cancelButtonText: 'Cancel',
        inputPlaceholder: `e.g. ${shortId(selectedSnapshot.value.id)}`,
        inputPattern: /.+/,
        inputErrorMessage: 'Please enter a confirmation string',
      },
    )
    if (!value) return

    restoreLoading.value = true
    const resp = await apiPost<RestoreResponse>('/restores', {
      repository_id: selectedRepoId.value,
      snapshot_id: selectedSnapshot.value.id,
      restore_kind: 'filesystem',
      target: {
        target_path: restoreForm.value.target_path,
        include_paths: restoreForm.value.include_paths,
        overwrite_mode: restoreForm.value.overwrite_mode,
      },
      overwrite: restoreForm.value.overwrite_mode !== 'never',
      confirmation: value,
    })

    ElMessage.success('Restore initiated, redirecting to run detail...')
    restoreDialogVisible.value = false
    router.push(`/runs/${resp.run_id}`)
  } catch (err: unknown) {
    if (err === 'cancel') return
    const e = err as { message?: string; code?: string }
    ElMessage.error(e.message || `Restore failed (code: ${e.code || 'unknown'})`)
  } finally {
    restoreLoading.value = false
  }
}

watch(selectedRepoId, (newId) => {
  if (newId) {
    loadSnapshots()
  } else {
    snapshots.value = []
    selectedSnapshot.value = null
    treeEntries.value = []
  }
})

loadRepos()
</script>