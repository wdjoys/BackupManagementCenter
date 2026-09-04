import React, { useEffect, useState, useMemo, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { apiGet, apiGetWithMeta, apiPost, apiDelete, isApiClientError, isAbortError } from '@/api/client'
import { KIND_LABELS } from '@/views/plans/Constants'
import type {
  Agent,
  Plan,
  Repository,
  Snapshot,
  SnapshotDeletionResponse,
  TreeEntry,
  TreeResponse,
} from '@/api/types'
import { hostPathRoots, isAbsolutePath, isWithinMappedRoot } from '@/utils/pathMapping'
import { AppErrorState } from '@/components/AppErrorState'
import type { BadgeTone } from '@/components/StatusBadge'
import { toastSuccess, toastError, toastWarning } from '@/lib/toast'
import {
  ALL_PLANS_FILTER,
  DELETED_PLANS_FILTER,
  UNASSIGNED_PLAN_FILTER,
  type BreadcrumbPart,
  type DryRunResult,
  type RestoreResponse,
  type SnapshotView,
} from './Types'
import { SnapshotFilters } from './SnapshotFilters'
import { SnapshotList } from './SnapshotList'
import { SnapshotDetailSheet } from './SnapshotDetailSheet'
import { SnapshotRestoreDialogs } from './SnapshotRestoreDialogs'

export const SnapshotsView: React.FC = () => {
  const { t } = useTranslation()
  const navigate = useNavigate()

  const [repos, setRepos] = useState<Repository[]>([])
  const [plans, setPlans] = useState<Plan[]>([])
  const [agents, setAgents] = useState<Agent[]>([])
  const [reposLoading, setReposLoading] = useState(false)
  const [mainError, setMainError] = useState<string | null>(null)
  const [selectedRepoId, setSelectedRepoId] = useState<string>('')

  const [planFilter, setPlanFilter] = useState<string>(ALL_PLANS_FILTER)
  const [snapshots, setSnapshots] = useState<Snapshot[]>([])
  const [snapshotsLoading, setSnapshotsLoading] = useState(false)
  const [snapshotsCache, setSnapshotsCache] = useState<string | null>(null)
  const [snapshotsVerifiedAt, setSnapshotsVerifiedAt] = useState<string | null>(null)

  // Drawer / Tree Explorer
  const [selectedSnapshot, setSelectedSnapshot] = useState<Snapshot | null>(null)
  const [detailDrawerOpen, setDetailDrawerOpen] = useState(false)
  const [treeLoading, setTreeLoading] = useState(false)
  const [treeEntries, setTreeEntries] = useState<TreeEntry[]>([])
  const [treePath, setTreePath] = useState('/')
  const [treeSelectedPaths, setTreeSelectedPaths] = useState<string[]>([])
  const [copiedId, setCopiedId] = useState(false)

  // Restore Modal State
  const [restoreDialogOpen, setRestoreDialogOpen] = useState(false)
  const [restoreTargetPath, setRestoreTargetPath] = useState('')
  const [overwriteMode, setOverwriteMode] = useState<'never' | 'if-changed' | 'always'>('never')
  const [selectedIncludePaths, setSelectedIncludePaths] = useState<string[]>([])
  const [dryRunLoading, setDryRunLoading] = useState(false)
  const [dryRunResult, setDryRunResult] = useState<DryRunResult | null>(null)
  const [restoreLoading, setRestoreLoading] = useState(false)

  // Restore Confirmation Prompt Dialog
  const [confirmPromptOpen, setConfirmPromptOpen] = useState(false)
  const [confirmationInput, setConfirmationInput] = useState('')

  // Delete Snapshot Prompt Dialog
  const [deletePromptOpen, setDeletePromptOpen] = useState(false)
  const [snapshotToDelete, setSnapshotToDelete] = useState<Snapshot | null>(null)
  const [deleteConfirmInput, setDeleteConfirmInput] = useState('')
  const [deletingSnapshot, setDeletingSnapshot] = useState(false)

  const snapshotsReqRef = useRef(0)
  const treeReqRef = useRef(0)
  const snapshotsAbortRef = useRef<AbortController | null>(null)
  const treeAbortRef = useRef<AbortController | null>(null)

  const selectedRepo = useMemo(
    () => repos.find((r) => r.id === selectedRepoId),
    [repos, selectedRepoId]
  )
  const selectedAgent = useMemo(
    () => agents.find((a) => a.id === selectedRepo?.agent_id),
    [agents, selectedRepo]
  )
  const restorePathMappings = useMemo(
    () => selectedAgent?.restore_path_mappings ?? [],
    [selectedAgent]
  )
  const restoreHostRoots = useMemo(
    () => hostPathRoots(restorePathMappings),
    [restorePathMappings]
  )

  const restoreTargetValidationMessage = useMemo(() => {
    const trimmed = restoreTargetPath.trim()
    if (!trimmed) return null
    if (!isAbsolutePath(trimmed)) {
      return t('snapshots.validation.absolutePathRequired')
    }
    if (!isWithinMappedRoot(trimmed, restorePathMappings)) {
      return t('snapshots.validation.pathOutsideAllowedRoots')
    }
    return null
  }, [restoreTargetPath, restoreHostRoots, t])

  const restoreTargetValid = useMemo(() => {
    const trimmed = restoreTargetPath.trim()
    return Boolean(trimmed && !restoreTargetValidationMessage)
  }, [restoreTargetPath, restoreTargetValidationMessage])

  const repositoryPlans = useMemo(() => {
    if (!selectedRepo) return []
    return plans.filter((p) => p.repository_id === selectedRepo.id)
  }, [plans, selectedRepo])

  const tagValues = (snapshot: Snapshot, prefix: string): string[] => {
    return [
      ...new Set(
        snapshot.tags
          .filter((tag) => tag.startsWith(prefix) && tag.slice(prefix.length))
          .map((tag) => tag.slice(prefix.length))
      ),
    ]
  }

  const snapshotView = (snapshot: Snapshot): SnapshotView => {
    const planIds = tagValues(snapshot, 'plan:')
    const plan = planIds.length === 1 ? plans.find((p) => p.id === planIds[0]) : undefined
    const planID = plan ? plan.id : planIds.length ? DELETED_PLANS_FILTER : UNASSIGNED_PLAN_FILTER
    const kind = tagValues(snapshot, 'kind:')[0] || plan?.kind || 'unknown'
    const known = kind in KIND_LABELS
    const runs = tagValues(snapshot, 'run:')

    let kindTone: BadgeTone = 'secondary'
    if (kind === 'filesystem') kindTone = 'default'
    else if (kind === 'sqlite') kindTone = 'outline'
    else if (known) kindTone = 'warning'

    let summary = ''
    if (plan) {
      if (plan.kind === 'filesystem') summary = plan.source.paths?.join(', ') || ''
      else if (plan.kind === 'sqlite') summary = plan.source.path || ''
      else if (plan.source.host && plan.source.database) {
        summary = `${plan.source.host}${plan.source.port ? `:${plan.source.port}` : ''}/${plan.source.database}`
      }
    }

    return {
      raw: snapshot,
      planID,
      plan,
      planName: plan?.name || (planIds.length ? t('snapshots.deletedPlan') : t('snapshots.unassignedPlan')),
      kind,
      kindLabel: known ? t(KIND_LABELS[kind as Plan['kind']]) : t('snapshots.unknownType'),
      kindTone,
      sourceSummary: summary || snapshot.paths.join(', ') || '—',
      agentDisplay: {
        name: selectedAgent?.name || 'Agent',
        hostname: selectedAgent?.hostname || snapshot.host,
      },
      runID: runs[0] || '',
      extraTags: snapshot.tags.filter(
        (tag) => !tag.startsWith('plan:') && !tag.startsWith('kind:') && !tag.startsWith('run:')
      ),
    }
  }

  const snapshotViews = useMemo(() => {
    return snapshots.map(snapshotView).sort((a, b) => Date.parse(b.raw.time) - Date.parse(a.raw.time))
  }, [snapshots, plans, selectedAgent, t])

  const filteredSnapshots = useMemo(() => {
    if (planFilter === ALL_PLANS_FILTER) return snapshotViews
    return snapshotViews.filter((s) => s.planID === planFilter)
  }, [snapshotViews, planFilter])

  const selectedSnapshotView = useMemo(() => {
    return selectedSnapshot ? snapshotView(selectedSnapshot) : null
  }, [selectedSnapshot, plans, selectedAgent, t])

  const canRestore = useMemo(() => {
    return selectedSnapshotView?.kind === 'filesystem'
  }, [selectedSnapshotView])

  const loadRepos = async () => {
    setReposLoading(true)
    setMainError(null)
    try {
      const [repoList, planList, agentList] = await Promise.all([
        apiGet<Repository[]>('/repositories'),
        apiGet<Plan[]>('/plans'),
        apiGet<Agent[]>('/agents'),
      ])
      setRepos(repoList)
      setPlans(planList)
      setAgents(agentList)
      if (!selectedRepoId && repoList.length > 0) {
        setSelectedRepoId(repoList[0].id)
      }
    } catch (err: unknown) {
      setMainError(isApiClientError(err) ? err.message : t('snapshots.messages.reposLoadFailed'))
    } finally {
      setReposLoading(false)
    }
  }

  const loadSnapshots = async (refresh = false) => {
    if (!selectedRepoId) return
    const reqId = ++snapshotsReqRef.current
    const repoId = selectedRepoId

    snapshotsAbortRef.current?.abort()
    const controller = new AbortController()
    snapshotsAbortRef.current = controller

    setSnapshotsLoading(true)
    try {
      const response = await apiGetWithMeta<Snapshot[]>(
        `/repositories/${repoId}/snapshots`,
        { refresh: refresh ? 1 : undefined },
        { signal: controller.signal }
      )
      if (reqId !== snapshotsReqRef.current || repoId !== selectedRepoId) return
      setSnapshots(response.data)
      setSnapshotsCache(response.meta.cache)
      setSnapshotsVerifiedAt(response.meta.verifiedAt)
    } catch (err: unknown) {
      if (isAbortError(err)) return
      if (reqId !== snapshotsReqRef.current || repoId !== selectedRepoId) return
      toastError(isApiClientError(err) ? err.message : t('snapshots.messages.snapshotsLoadFailed'))
    } finally {
      if (reqId === snapshotsReqRef.current) {
        setSnapshotsLoading(false)
      }
    }
  }

  useEffect(() => {
    loadRepos()
    return () => {
      snapshotsAbortRef.current?.abort()
      treeAbortRef.current?.abort()
    }
  }, [])

  useEffect(() => {
    setPlanFilter(ALL_PLANS_FILTER)
    setSnapshots([])
    setSnapshotsCache(null)
    setSnapshotsVerifiedAt(null)
    setSelectedSnapshot(null)
    setDetailDrawerOpen(false)
    if (selectedRepoId) {
      loadSnapshots()
    }
  }, [selectedRepoId])

  const loadTree = async (path = treePath, refresh = false) => {
    if (!selectedSnapshot || !selectedRepoId) return
    const reqId = ++treeReqRef.current
    const snapshotId = selectedSnapshot.id
    const repoId = selectedRepoId

    treeAbortRef.current?.abort()
    const controller = new AbortController()
    treeAbortRef.current = controller

    setTreeLoading(true)
    try {
      const response = await apiGetWithMeta<TreeResponse>(
        `/snapshots/${snapshotId}/tree`,
        {
          repo: repoId,
          path,
          refresh: refresh ? 1 : undefined,
        },
        { signal: controller.signal }
      )
      if (reqId !== treeReqRef.current || snapshotId !== selectedSnapshot?.id || repoId !== selectedRepoId) return
      setTreeEntries(response.data.entries || [])
      setTreePath(response.data.path || path)
    } catch (err: unknown) {
      if (isAbortError(err)) return
      if (reqId !== treeReqRef.current) return
      toastError(isApiClientError(err) ? err.message : t('snapshots.messages.treeLoadFailed'))
    } finally {
      if (reqId === treeReqRef.current) {
        setTreeLoading(false)
      }
    }
  }

  const handleSelectSnapshot = (snapshot: Snapshot) => {
    setSelectedSnapshot(snapshot)
    setDetailDrawerOpen(true)
    setTreePath('/')
    setTreeSelectedPaths([])
    setTreeEntries([])
    setDryRunResult(null)
    loadTree('/')
  }

  const navigateBreadcrumb = (path: string) => {
    setTreePath(path)
    setTreeSelectedPaths([])
    loadTree(path)
  }

  const handleNavigateDir = (nextPath: string) => {
    setTreePath(nextPath)
    setTreeSelectedPaths([])
    loadTree(nextPath)
  }

  const toggleTreeSelection = (entryName: string) => {
    const fullPath = treePath === '/' ? `/${entryName}` : `${treePath}/${entryName}`
    setTreeSelectedPaths((prev) => {
      if (prev.includes(fullPath)) {
        return prev.filter((p) => p !== fullPath)
      } else {
        return [...prev, fullPath]
      }
    })
  }

  const breadcrumbs = useMemo<BreadcrumbPart[]>(() => {
    const parts = treePath.split('/').filter(Boolean)
    const result: BreadcrumbPart[] = [{ label: '/', path: '/' }]
    let cur = ''
    for (const p of parts) {
      cur += `/${p}`
      result.push({ label: p, path: cur })
    }
    return result
  }, [treePath])

  const copySnapshotId = async () => {
    if (!selectedSnapshot) return
    try {
      await navigator.clipboard.writeText(selectedSnapshot.id)
      setCopiedId(true)
      toastSuccess(t('common.copied'))
      setTimeout(() => setCopiedId(false), 2000)
    } catch {
      toastError(t('common.copyFailed'))
    }
  }

  // Delete Snapshot
  const openDeletePrompt = (e: React.MouseEvent, snapshot: Snapshot) => {
    e.stopPropagation()
    setSnapshotToDelete(snapshot)
    setDeleteConfirmInput('')
    setDeletePromptOpen(true)
  }

  const handleDeleteSnapshotConfirm = async () => {
    if (!snapshotToDelete || !selectedRepoId) return
    if (deleteConfirmInput.trim() !== snapshotToDelete.id) {
      toastWarning(t('snapshots.delete.inputMismatch'))
      return
    }

    setDeletingSnapshot(true)
    try {
      await apiDelete<SnapshotDeletionResponse>(
        `/repositories/${selectedRepoId}/snapshots/${snapshotToDelete.id}`
      )
      toastSuccess(t('snapshots.delete.initiated'))
      setDeletePromptOpen(false)
      if (selectedSnapshot?.id === snapshotToDelete.id) {
        setDetailDrawerOpen(false)
        setSelectedSnapshot(null)
      }
      await loadSnapshots()
    } catch (err: unknown) {
      toastError(isApiClientError(err) ? err.message : t('snapshots.delete.failed'))
    } finally {
      setDeletingSnapshot(false)
    }
  }

  // Restore Wizard
  const openRestoreWizard = () => {
    if (!selectedSnapshot || !canRestore) return
    setRestoreTargetPath('')
    setOverwriteMode('never')
    setSelectedIncludePaths([...treeSelectedPaths])
    setDryRunResult(null)
    setRestoreDialogOpen(true)
  }

  const handleDryRun = async () => {
    if (!selectedSnapshot || !selectedRepoId || !restoreTargetValid) return
    setDryRunLoading(true)
    setDryRunResult(null)
    try {
      const res = await apiPost<DryRunResult>('/restores/dry-run', {
        repository_id: selectedRepoId,
        snapshot_id: selectedSnapshot.id,
        include_paths: selectedIncludePaths,
        target_path: restoreTargetPath.trim(),
        overwrite_mode: overwriteMode,
      })
      setDryRunResult(res)
      toastSuccess(t('snapshots.messages.dryRunCompleted'))
    } catch (err: unknown) {
      toastError(isApiClientError(err) ? err.message : t('snapshots.messages.dryRunFailed'))
    } finally {
      setDryRunLoading(false)
    }
  }

  const openConfirmPrompt = () => {
    setConfirmationInput('')
    setConfirmPromptOpen(true)
  }

  const handleExecuteRestore = async () => {
    if (!selectedSnapshot || !selectedRepoId || !dryRunResult || !restoreTargetValid) return
    if (!confirmationInput.trim()) {
      toastWarning(t('snapshots.prompt.inputRequired'))
      return
    }

    setRestoreLoading(true)
    try {
      const res = await apiPost<RestoreResponse>('/restores', {
        repository_id: selectedRepoId,
        snapshot_id: selectedSnapshot.id,
        restore_kind: 'filesystem',
        target: {
          target_path: restoreTargetPath.trim(),
          include_paths: selectedIncludePaths,
          overwrite_mode: overwriteMode,
        },
        overwrite: overwriteMode !== 'never',
        confirmation: confirmationInput.trim(),
      })
      toastSuccess(t('snapshots.messages.restoreInitiated'))
      setConfirmPromptOpen(false)
      setRestoreDialogOpen(false)
      setDetailDrawerOpen(false)
      navigate(`/runs/${res.run_id}`)
    } catch (err: unknown) {
      toastError(isApiClientError(err) ? err.message : t('snapshots.messages.restoreFailed'))
    } finally {
      setRestoreLoading(false)
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
        <div>
          <h2 className="text-xl font-bold tracking-tight text-foreground">
            {t('snapshots.title')}
          </h2>
          <p className="text-xs text-muted-foreground">
            {t('snapshots.subtitle')}
          </p>
        </div>
      </div>

      {mainError ? (
        <AppErrorState title={t('snapshots.title')} message={mainError} onRetry={loadRepos} />
      ) : (
        <div className="space-y-4">
          <SnapshotFilters
            repos={repos}
            selectedRepoId={selectedRepoId}
            onSelectRepo={setSelectedRepoId}
            reposLoading={reposLoading}
            planFilter={planFilter}
            onSelectPlanFilter={setPlanFilter}
            repositoryPlans={repositoryPlans}
            snapshotsCache={snapshotsCache}
            snapshotsVerifiedAt={snapshotsVerifiedAt}
            snapshotsCount={filteredSnapshots.length}
            snapshotsLoading={snapshotsLoading}
            onRefresh={() => loadSnapshots(true)}
          />

          <SnapshotList
            selectedRepoId={selectedRepoId}
            snapshotsLoading={snapshotsLoading}
            totalSnapshots={snapshots.length}
            filteredSnapshots={filteredSnapshots}
            onSelectSnapshot={handleSelectSnapshot}
            onDeleteSnapshot={openDeletePrompt}
          />
        </div>
      )}

      <SnapshotDetailSheet
        open={detailDrawerOpen}
        onOpenChange={setDetailDrawerOpen}
        selectedSnapshotView={selectedSnapshotView}
        canRestore={canRestore}
        copiedId={copiedId}
        treeLoading={treeLoading}
        treeEntries={treeEntries}
        treePath={treePath}
        breadcrumbs={breadcrumbs}
        treeSelectedPaths={treeSelectedPaths}
        onViewRun={(runId) => navigate(`/runs/${runId}`)}
        onOpenRestore={openRestoreWizard}
        onCopySnapshotId={copySnapshotId}
        onNavigateBreadcrumb={navigateBreadcrumb}
        onNavigateDir={handleNavigateDir}
        onToggleTreeSelection={toggleTreeSelection}
      />

      <SnapshotRestoreDialogs
        restoreDialogOpen={restoreDialogOpen}
        onRestoreDialogOpenChange={setRestoreDialogOpen}
        restoreTargetPath={restoreTargetPath}
        onRestoreTargetPathChange={(val) => {
          setRestoreTargetPath(val)
          setDryRunResult(null)
        }}
        restoreTargetValidationMessage={restoreTargetValidationMessage}
        restoreHostRoots={restoreHostRoots}
        overwriteMode={overwriteMode}
        onOverwriteModeChange={setOverwriteMode}
        selectedIncludePaths={selectedIncludePaths}
        dryRunResult={dryRunResult}
        dryRunLoading={dryRunLoading}
        restoreTargetValid={restoreTargetValid}
        restoreLoading={restoreLoading}
        onDryRun={handleDryRun}
        onOpenConfirmPrompt={openConfirmPrompt}
        confirmPromptOpen={confirmPromptOpen}
        onConfirmPromptOpenChange={setConfirmPromptOpen}
        selectedSnapshot={selectedSnapshot}
        confirmationInput={confirmationInput}
        onConfirmationInputChange={setConfirmationInput}
        onExecuteRestore={handleExecuteRestore}
        deletePromptOpen={deletePromptOpen}
        onDeletePromptOpenChange={setDeletePromptOpen}
        snapshotToDelete={snapshotToDelete}
        deleteConfirmInput={deleteConfirmInput}
        onDeleteConfirmInputChange={setDeleteConfirmInput}
        deletingSnapshot={deletingSnapshot}
        onDeleteConfirm={handleDeleteSnapshotConfirm}
      />
    </div>
  )
}
