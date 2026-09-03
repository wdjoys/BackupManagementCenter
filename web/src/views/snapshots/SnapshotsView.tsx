import React, { useEffect, useState, useMemo, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Card, CardContent } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { apiGet, apiGetWithMeta, apiPost, apiDelete } from '@/api/client'
import { formatDateTime } from '@/i18n'
import { KIND_LABELS } from '@/views/plans/Constants'
import type {
  Agent,
  Plan,
  Repository,
  Snapshot,
  SnapshotDeletionResponse,
  TreeEntry,
  TreeResponse,
  ApiError,
} from '@/api/types'
import { hostPathRoots, isAbsolutePath, isWithinMappedRoot } from '@/utils/pathMapping'
import { AppEmptyState } from '@/components/AppEmptyState'
import { AppErrorState } from '@/components/AppErrorState'
import { StatusBadge, type BadgeTone } from '@/components/StatusBadge'
import { toastSuccess, toastError, toastWarning } from '@/lib/toast'
import {
  Camera,
  RefreshCw,
  Folder,
  FileCode,
  Download,
  Trash2,
  Copy,
  Check,
  Search,
  ChevronRight,
  ExternalLink,
  CheckCircle2,
  AlertTriangle,
  Loader2,
} from 'lucide-react'

interface DryRunResult {
  add: number
  changed: number
  skipped: number
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

interface SnapshotView {
  raw: Snapshot
  planID: string
  planName: string
  plan?: Plan
  kind: string
  kindLabel: string
  kindTone: BadgeTone
  sourceSummary: string
  agentDisplay: { name: string; hostname: string }
  runID: string
  extraTags: string[]
}

const ALL_PLANS_FILTER = 'all'
const DELETED_PLANS_FILTER = 'deleted'
const UNASSIGNED_PLAN_FILTER = 'unassigned'

function formatSize(bytes: number): string {
  if (bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  return `${(bytes / 1024 ** i).toFixed(1)} ${units[i]}`
}

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

  const restoreTargetValid = useMemo(() => {
    const trimmed = restoreTargetPath.trim()
    return Boolean(trimmed) && isAbsolutePath(trimmed) && isWithinMappedRoot(trimmed, restorePathMappings)
  }, [restoreTargetPath, restorePathMappings])

  const restoreTargetValidationMessage = useMemo(() => {
    const trimmed = restoreTargetPath.trim()
    if (!trimmed) return ''
    if (!isAbsolutePath(trimmed)) return t('snapshots.restoreDialog.absolutePathRequired') || 'Absolute path required'
    if (!isWithinMappedRoot(trimmed, restorePathMappings)) return t('snapshots.restoreDialog.pathOutsideAllowedRoots') || 'Path outside allowed host roots'
    return ''
  }, [restoreTargetPath, restorePathMappings, t])

  const repositoryPlans = useMemo(
    () => plans.filter((p) => p.repository_id === selectedRepoId),
    [plans, selectedRepoId]
  )

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
      planName: plan?.name || (planIds.length ? t('snapshots.deletedPlan') || 'Deleted Plan' : t('snapshots.unassignedPlan') || 'Unassigned'),
      kind,
      kindLabel: known ? t(KIND_LABELS[kind as Plan['kind']]) : t('snapshots.unknownType') || kind,
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
      const apiErr = err as ApiError
      setMainError(apiErr?.message || t('snapshots.messages.reposLoadFailed') || 'Failed to load repositories')
    } finally {
      setReposLoading(false)
    }
  }

  const loadSnapshots = async (refresh = false) => {
    if (!selectedRepoId) return
    const reqId = ++snapshotsReqRef.current
    const repoId = selectedRepoId
    setSnapshotsLoading(true)
    try {
      const response = await apiGetWithMeta<Snapshot[]>(`/repositories/${repoId}/snapshots`, {
        refresh: refresh ? 1 : undefined,
      })
      if (reqId !== snapshotsReqRef.current || repoId !== selectedRepoId) return
      setSnapshots(response.data)
      setSnapshotsCache(response.meta.cache)
      setSnapshotsVerifiedAt(response.meta.verifiedAt)
    } catch (err: unknown) {
      if (reqId !== snapshotsReqRef.current || repoId !== selectedRepoId) return
      const apiErr = err as ApiError
      toastError(apiErr?.message || t('snapshots.messages.snapshotsLoadFailed') || 'Failed to load snapshots')
    } finally {
      if (reqId === snapshotsReqRef.current) {
        setSnapshotsLoading(false)
      }
    }
  }

  useEffect(() => {
    loadRepos()
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
    setTreeLoading(true)
    try {
      const response = await apiGetWithMeta<TreeResponse>(`/snapshots/${snapshotId}/tree`, {
        repo: repoId,
        path,
        refresh: refresh ? 1 : undefined,
      })
      if (reqId !== treeReqRef.current || snapshotId !== selectedSnapshot?.id || repoId !== selectedRepoId) return
      setTreeEntries(response.data.entries || [])
      setTreePath(response.data.path || path)
    } catch (err: unknown) {
      if (reqId !== treeReqRef.current) return
      const apiErr = err as ApiError
      toastError(apiErr?.message || t('snapshots.messages.treeLoadFailed') || 'Failed to load snapshot directory tree')
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

  const handleEntryClick = (entry: TreeEntry) => {
    if (entry.type !== 'dir') return
    const nextPath = treePath === '/' ? `/${entry.name}` : `${treePath}/${entry.name}`
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
      toastSuccess(t('common.copied') || 'Snapshot ID copied')
      setTimeout(() => setCopiedId(false), 2000)
    } catch {
      toastError(t('common.copyFailed') || 'Failed to copy snapshot ID')
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
      toastWarning(t('snapshots.delete.inputMismatch') || 'Confirmation ID does not match')
      return
    }

    setDeletingSnapshot(true)
    try {
      await apiDelete<SnapshotDeletionResponse>(
        `/repositories/${selectedRepoId}/snapshots/${snapshotToDelete.id}`
      )
      toastSuccess(t('snapshots.delete.initiated') || 'Snapshot deletion scheduled')
      setDeletePromptOpen(false)
      if (selectedSnapshot?.id === snapshotToDelete.id) {
        setDetailDrawerOpen(false)
        setSelectedSnapshot(null)
      }
      await loadSnapshots()
    } catch (err: unknown) {
      const apiErr = err as ApiError
      toastError(apiErr?.message || t('snapshots.delete.failed') || 'Failed to delete snapshot')
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
      toastSuccess(t('snapshots.messages.dryRunCompleted') || 'Dry run completed successfully')
    } catch (err: unknown) {
      const apiErr = err as ApiError
      toastError(apiErr?.message || t('snapshots.messages.dryRunFailed') || 'Dry run verification failed')
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
      toastWarning(t('snapshots.prompt.inputRequired') || 'Confirmation string is required')
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
      toastSuccess(t('snapshots.messages.restoreInitiated') || 'Restore run dispatched successfully')
      setConfirmPromptOpen(false)
      setRestoreDialogOpen(false)
      setDetailDrawerOpen(false)
      navigate(`/runs/${res.run_id}`)
    } catch (err: unknown) {
      const apiErr = err as ApiError
      toastError(apiErr?.message || t('snapshots.messages.restoreFailed') || 'Restore execution failed')
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
            {t('snapshots.subtitle') || 'Explore restic snapshot trees, perform file recovery, and dry-run restores'}
          </p>
        </div>
      </div>

      {mainError ? (
        <AppErrorState title={t('snapshots.title')} message={mainError} onRetry={loadRepos} />
      ) : (
        <div className="space-y-4">
          {/* Top Filter Bar */}
          <Card className="border-border bg-card/40 shadow-sm">
            <CardContent className="p-4 flex flex-wrap items-center justify-between gap-3">
              <div className="flex flex-wrap items-center gap-3">
                {/* Repository Selector */}
                <Select value={selectedRepoId} onValueChange={setSelectedRepoId} disabled={reposLoading}>
                  <SelectTrigger className="w-72 h-8 text-xs font-mono">
                    <SelectValue placeholder={t('snapshots.repositoryPlaceholder')} />
                  </SelectTrigger>
                  <SelectContent>
                    {repos.map((r) => (
                      <SelectItem key={r.id} value={r.id} className="text-xs">
                        <span>{r.agent_name || r.agent_id}</span>
                        <span className="text-muted-foreground ml-2">({r.storage_target_name})</span>
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>

                {/* Plan Filter */}
                {selectedRepoId && (
                  <Select value={planFilter} onValueChange={setPlanFilter}>
                    <SelectTrigger className="w-56 h-8 text-xs">
                      <SelectValue placeholder={t('snapshots.planFilter.label')} />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={ALL_PLANS_FILTER} className="text-xs">
                        {t('snapshots.planFilter.all') || 'All Plans'}
                      </SelectItem>
                      {repositoryPlans.map((p) => (
                        <SelectItem key={p.id} value={p.id} className="text-xs">
                          {p.name}
                        </SelectItem>
                      ))}
                      <SelectItem value={DELETED_PLANS_FILTER} className="text-xs">
                        {t('snapshots.planFilter.deleted') || 'Deleted Plans'}
                      </SelectItem>
                      <SelectItem value={UNASSIGNED_PLAN_FILTER} className="text-xs">
                        {t('snapshots.planFilter.unassigned') || 'Unassigned Snapshots'}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                )}

                {snapshotsCache && (
                  <span className="text-[11px] text-muted-foreground">
                    Cache: {snapshotsCache}{' '}
                    {snapshotsVerifiedAt && `(${new Date(snapshotsVerifiedAt).toLocaleTimeString()})`}
                  </span>
                )}
              </div>

              {selectedRepoId && (
                <div className="flex items-center gap-2">
                  <span className="text-xs text-muted-foreground font-mono">
                    {filteredSnapshots.length} {t('snapshots.count') || 'snapshots'}
                  </span>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => loadSnapshots(true)}
                    disabled={snapshotsLoading}
                    className="h-8 text-xs gap-1.5"
                  >
                    <RefreshCw className="h-3.5 w-3.5" />
                    {t('common.refresh')}
                  </Button>
                </div>
              )}
            </CardContent>
          </Card>

          {/* Snapshots Table */}
          {selectedRepoId ? (
            <Card className="border-border bg-card/60 shadow-sm">
              <CardContent className="p-0">
                {snapshotsLoading && snapshots.length === 0 ? (
                  <div className="flex h-48 items-center justify-center">
                    <Loader2 className="h-6 w-6 animate-spin text-primary" />
                  </div>
                ) : filteredSnapshots.length > 0 ? (
                  <div className="rounded-md overflow-hidden">
                    <Table>
                      <TableHeader>
                        <TableRow className="border-border hover:bg-transparent">
                          <TableHead className="text-xs font-medium">{t('snapshots.browseTable.time')}</TableHead>
                          <TableHead className="text-xs font-medium">{t('snapshots.browseTable.plan')}</TableHead>
                          <TableHead className="text-xs font-medium">{t('snapshots.browseTable.type')}</TableHead>
                          <TableHead className="text-xs font-medium">{t('snapshots.browseTable.content')}</TableHead>
                          <TableHead className="text-xs font-medium">{t('snapshots.browseTable.agent')}</TableHead>
                          <TableHead className="text-xs font-medium text-right">{t('common.actions')}</TableHead>
                        </TableRow>
                      </TableHeader>
                      <TableBody>
                        {filteredSnapshots.map((item) => (
                          <TableRow
                            key={item.raw.id}
                            onClick={() => handleSelectSnapshot(item.raw)}
                            className="border-border hover:bg-muted/40 cursor-pointer"
                          >
                            <TableCell className="text-xs font-mono text-muted-foreground">
                              {formatDateTime(item.raw.time)}
                            </TableCell>
                            <TableCell className="text-xs font-medium text-foreground">
                              {item.planName}
                            </TableCell>
                            <TableCell className="text-xs">
                              <StatusBadge tone={item.kindTone}>{item.kindLabel}</StatusBadge>
                            </TableCell>
                            <TableCell className="text-xs text-muted-foreground font-mono max-w-xs truncate">
                              <Tooltip>
                                <TooltipTrigger asChild>
                                  <span className="truncate block">{item.sourceSummary}</span>
                                </TooltipTrigger>
                                <TooltipContent className="text-xs font-mono max-w-sm break-all">
                                  {item.sourceSummary}
                                </TooltipContent>
                              </Tooltip>
                            </TableCell>
                            <TableCell className="text-xs text-muted-foreground">
                              <span>{item.agentDisplay.name}</span>
                              <span className="text-[10px] text-muted-foreground/60 ml-1.5">
                                ({item.agentDisplay.hostname})
                              </span>
                            </TableCell>
                            <TableCell className="text-xs text-right">
                              <div className="flex items-center justify-end gap-2">
                                <Button
                                  variant="ghost"
                                  size="sm"
                                  className="h-7 text-xs text-primary gap-1"
                                  onClick={(e) => {
                                    e.stopPropagation()
                                    handleSelectSnapshot(item.raw)
                                  }}
                                >
                                  <Folder className="h-3.5 w-3.5" />
                                  {t('snapshots.viewDetails') || 'Browse'}
                                </Button>
                                <Button
                                  variant="ghost"
                                  size="sm"
                                  className="h-7 text-xs text-rose-400 hover:text-rose-300 gap-1"
                                  onClick={(e) => openDeletePrompt(e, item.raw)}
                                >
                                  <Trash2 className="h-3.5 w-3.5" />
                                  {t('common.delete')}
                                </Button>
                              </div>
                            </TableCell>
                          </TableRow>
                        ))}
                      </TableBody>
                    </Table>
                  </div>
                ) : (
                  <div className="p-8">
                    <AppEmptyState
                      title={t('snapshots.noSnapshots') || 'No Snapshots Found'}
                      description={
                        snapshots.length
                          ? t('snapshots.noFilteredSnapshots') || 'No snapshots matching filter criteria.'
                          : t('snapshots.noSnapshotsDesc') || 'Trigger a backup plan to generate initial snapshots.'
                      }
                    />
                  </div>
                )}
              </CardContent>
            </Card>
          ) : (
            <div className="p-12 border border-dashed border-border rounded-md text-center">
              <Camera className="mx-auto h-8 w-8 text-muted-foreground opacity-50 mb-2" />
              <h3 className="text-sm font-semibold text-foreground">
                {t('snapshots.repositoryPlaceholder')}
              </h3>
              <p className="text-xs text-muted-foreground mt-1">
                {t('snapshots.selectRepoPrompt') || 'Select a repository above to explore its snapshots and files.'}
              </p>
            </div>
          )}
        </div>
      )}

      {/* Snapshot Details and File Tree Drawer */}
      <Sheet open={detailDrawerOpen} onOpenChange={setDetailDrawerOpen}>
        <SheetContent side="right" className="w-full sm:max-w-2xl p-0 bg-card border-l border-border flex flex-col">
          {selectedSnapshotView && (
            <>
              <SheetHeader className="p-4 border-b border-border bg-muted/20">
                <div className="flex items-center justify-between">
                  <div className="space-y-0.5">
                    <SheetTitle className="text-sm font-semibold">
                      {selectedSnapshotView.planName}
                    </SheetTitle>
                    <SheetDescription className="text-xs font-mono">
                      {selectedSnapshotView.kindLabel} · {formatDateTime(selectedSnapshotView.raw.time)}
                    </SheetDescription>
                  </div>
                  <div className="flex items-center gap-2">
                    {selectedSnapshotView.runID && (
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={() => navigate(`/runs/${selectedSnapshotView.runID}`)}
                        className="h-7 text-xs gap-1"
                      >
                        <ExternalLink className="h-3 w-3" />
                        {t('snapshots.viewRun') || 'View Run'}
                      </Button>
                    )}
                    <Button
                      size="sm"
                      disabled={!canRestore}
                      onClick={openRestoreWizard}
                      className="h-7 text-xs gap-1.5 bg-primary text-primary-foreground"
                    >
                      <Download className="h-3.5 w-3.5" />
                      {t('snapshots.restoreThisSnapshot') || 'Restore'}
                    </Button>
                  </div>
                </div>
              </SheetHeader>

              {/* Snapshot Info summary */}
              <div className="p-4 border-b border-border bg-card/40 space-y-2 text-xs">
                <div className="flex items-center justify-between font-mono bg-muted/30 p-2 rounded">
                  <span className="text-muted-foreground">Snapshot ID:</span>
                  <div
                    onClick={copySnapshotId}
                    className="flex items-center gap-1.5 text-primary cursor-pointer hover:underline"
                  >
                    <span>{selectedSnapshotView.raw.id}</span>
                    {copiedId ? <Check className="h-3.5 w-3.5 text-emerald-400" /> : <Copy className="h-3.5 w-3.5" />}
                  </div>
                </div>
                <div className="flex items-center justify-between text-muted-foreground">
                  <span>Paths:</span>
                  <span className="font-mono text-foreground">{selectedSnapshotView.raw.paths.join(', ') || '—'}</span>
                </div>
              </div>

              {/* Breadcrumbs */}
              <div className="px-4 py-2 border-b border-border/80 bg-muted/10 flex items-center gap-1.5 text-xs font-mono overflow-x-auto">
                <span className="text-muted-foreground">Path:</span>
                {breadcrumbs.map((b, idx) => (
                  <React.Fragment key={b.path}>
                    {idx > 0 && <ChevronRight className="h-3 w-3 text-muted-foreground shrink-0" />}
                    <button
                      type="button"
                      onClick={() => navigateBreadcrumb(b.path)}
                      className={`hover:underline cursor-pointer truncate ${
                        b.path === treePath ? 'text-primary font-bold' : 'text-muted-foreground'
                      }`}
                    >
                      {b.label}
                    </button>
                  </React.Fragment>
                ))}
              </div>

              {/* Directory Tree Entries */}
              <div className="flex-1 overflow-y-auto p-2">
                {treeLoading ? (
                  <div className="flex h-64 items-center justify-center">
                    <Loader2 className="h-6 w-6 animate-spin text-primary" />
                  </div>
                ) : treeEntries.length > 0 ? (
                  <Table>
                    <TableHeader>
                      <TableRow className="border-border hover:bg-transparent">
                        <TableHead className="w-8"></TableHead>
                        <TableHead className="text-xs font-medium">{t('common.name')}</TableHead>
                        <TableHead className="text-xs font-medium w-16">{t('snapshots.browseTable.type')}</TableHead>
                        <TableHead className="text-xs font-medium w-24 text-right">
                          {t('snapshots.browseTable.size')}
                        </TableHead>
                        <TableHead className="text-xs font-medium w-36 text-right">Modified</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {treeEntries.map((entry) => {
                        const fullPath = treePath === '/' ? `/${entry.name}` : `${treePath}/${entry.name}`
                        const isSelected = treeSelectedPaths.includes(fullPath)
                        return (
                          <TableRow
                            key={entry.name}
                            className="border-border hover:bg-muted/30 cursor-pointer text-xs"
                            onClick={() => handleEntryClick(entry)}
                          >
                            <TableCell className="p-2" onClick={(e) => e.stopPropagation()}>
                              <Checkbox
                                checked={isSelected}
                                onCheckedChange={() => toggleTreeSelection(entry.name)}
                              />
                            </TableCell>
                            <TableCell className="font-medium font-mono text-foreground flex items-center gap-1.5">
                              {entry.type === 'dir' ? (
                                <Folder className="h-3.5 w-3.5 text-amber-400 shrink-0" />
                              ) : (
                                <FileCode className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
                              )}
                              <span className="truncate">{entry.name}</span>
                            </TableCell>
                            <TableCell className="text-[11px] text-muted-foreground font-mono">
                              {entry.type}
                            </TableCell>
                            <TableCell className="text-xs text-muted-foreground font-mono text-right">
                              {formatSize(entry.size)}
                            </TableCell>
                            <TableCell className="text-xs text-muted-foreground font-mono text-right">
                              {formatDateTime(entry.mtime)}
                            </TableCell>
                          </TableRow>
                        )
                      })}
                    </TableBody>
                  </Table>
                ) : (
                  <div className="p-12 text-center text-xs text-muted-foreground">
                    Empty directory.
                  </div>
                )}
              </div>
            </>
          )}
        </SheetContent>
      </Sheet>

      {/* Restore Wizard Dialog */}
      <Dialog open={restoreDialogOpen} onOpenChange={setRestoreDialogOpen}>
        <DialogContent className="sm:max-w-lg max-h-[90vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle className="text-sm font-semibold">
              {t('snapshots.restoreDialog.title')}
            </DialogTitle>
            <DialogDescription className="text-xs">
              {t('snapshots.restoreDialog.subtitle') || 'Restore files from this snapshot to target host filesystem'}
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4 pt-2">
            <div className="space-y-1.5">
              <Label className="text-xs">{t('snapshots.restoreDialog.targetPath')} *</Label>
              <Input
                placeholder="/restore/target/directory"
                value={restoreTargetPath}
                onChange={(e) => {
                  setRestoreTargetPath(e.target.value)
                  setDryRunResult(null)
                }}
                className="h-9 text-xs font-mono"
              />
              {restoreTargetValidationMessage && (
                <p className="text-[11px] text-destructive">{restoreTargetValidationMessage}</p>
              )}
              {restoreHostRoots.length > 0 ? (
                <div className="text-[11px] text-muted-foreground flex flex-wrap items-center gap-1.5 pt-1">
                  <span>Allowed Roots:</span>
                  {restoreHostRoots.map((r) => (
                    <span key={r} className="rounded bg-muted px-1 py-0.5 font-mono text-[10px]">
                      {r}
                    </span>
                  ))}
                </div>
              ) : null}
            </div>

            <div className="space-y-1.5">
              <Label className="text-xs">{t('snapshots.restoreDialog.overwriteMode')} *</Label>
              <RadioGroup
                value={overwriteMode}
                onValueChange={(val) => setOverwriteMode(val as 'never' | 'if-changed' | 'always')}
                className="flex gap-4 pt-1"
              >
                <div className="flex items-center space-x-2">
                  <RadioGroupItem value="never" id="ow-never" />
                  <label htmlFor="ow-never" className="text-xs cursor-pointer">
                    {t('snapshots.restoreDialog.never') || 'Never'}
                  </label>
                </div>
                <div className="flex items-center space-x-2">
                  <RadioGroupItem value="if-changed" id="ow-changed" />
                  <label htmlFor="ow-changed" className="text-xs cursor-pointer">
                    {t('snapshots.restoreDialog.ifChanged') || 'If Changed'}
                  </label>
                </div>
                <div className="flex items-center space-x-2">
                  <RadioGroupItem value="always" id="ow-always" />
                  <label htmlFor="ow-always" className="text-xs cursor-pointer">
                    {t('snapshots.restoreDialog.always') || 'Always'}
                  </label>
                </div>
              </RadioGroup>
            </div>

            {selectedIncludePaths.length > 0 && (
              <div className="space-y-1">
                <Label className="text-xs">Selected Included Items ({selectedIncludePaths.length})</Label>
                <div className="max-h-24 overflow-y-auto rounded border border-border bg-muted/20 p-1.5 font-mono text-[11px]">
                  {selectedIncludePaths.map((p) => (
                    <div key={p} className="truncate text-muted-foreground">
                      {p}
                    </div>
                  ))}
                </div>
              </div>
            )}

            {dryRunResult && (
              <Alert className="border-emerald-500/30 bg-emerald-500/10 text-emerald-400 py-2.5 text-xs">
                <CheckCircle2 className="h-4 w-4 text-emerald-400" />
                <AlertTitle className="text-xs font-semibold">
                  {t('snapshots.restoreDialog.dryRunResult')}
                </AlertTitle>
                <AlertDescription className="text-xs grid grid-cols-4 gap-2 pt-2">
                  <div>
                    <span className="text-[10px] text-muted-foreground block">Added</span>
                    <span className="font-mono font-bold">{dryRunResult.add}</span>
                  </div>
                  <div>
                    <span className="text-[10px] text-muted-foreground block">Changed</span>
                    <span className="font-mono font-bold">{dryRunResult.changed}</span>
                  </div>
                  <div>
                    <span className="text-[10px] text-muted-foreground block">Skipped</span>
                    <span className="font-mono font-bold">{dryRunResult.skipped}</span>
                  </div>
                  <div>
                    <span className="text-[10px] text-muted-foreground block">Deleted</span>
                    <span className="font-mono font-bold">{dryRunResult.delete}</span>
                  </div>
                </AlertDescription>
              </Alert>
            )}

            <DialogFooter className="gap-2 pt-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={handleDryRun}
                disabled={dryRunLoading || !restoreTargetValid}
                className="h-8 text-xs gap-1.5"
              >
                {dryRunLoading ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Search className="h-3.5 w-3.5" />}
                {t('snapshots.restoreDialog.dryRunButton')}
              </Button>
              <Button
                type="button"
                size="sm"
                onClick={openConfirmPrompt}
                disabled={!restoreTargetValid || !dryRunResult || restoreLoading}
                className="h-8 text-xs gap-1.5 bg-primary text-primary-foreground"
              >
                <Download className="h-3.5 w-3.5" />
                {t('snapshots.restoreDialog.confirmExecute')}
              </Button>
            </DialogFooter>
          </div>
        </DialogContent>
      </Dialog>

      {/* Confirmation String Dialog for Restore */}
      <Dialog open={confirmPromptOpen} onOpenChange={setConfirmPromptOpen}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle className="text-sm font-semibold">
              {t('snapshots.prompt.title')}
            </DialogTitle>
            <DialogDescription className="text-xs">
              {t('snapshots.prompt.message')}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 pt-2">
            <Input
              placeholder={t('snapshots.prompt.inputPlaceholder', {
                example: selectedSnapshot?.id.slice(0, 8),
              })}
              value={confirmationInput}
              onChange={(e) => setConfirmationInput(e.target.value)}
              disabled={restoreLoading}
              className="h-9 text-xs font-mono"
              autoFocus
            />
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => setConfirmPromptOpen(false)}
                disabled={restoreLoading}
                className="h-8 text-xs"
              >
                {t('common.cancel')}
              </Button>
              <Button
                type="button"
                size="sm"
                onClick={handleExecuteRestore}
                disabled={restoreLoading || !confirmationInput.trim()}
                className="h-8 text-xs gap-1.5"
              >
                {restoreLoading && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
                {t('snapshots.prompt.execute')}
              </Button>
            </DialogFooter>
          </div>
        </DialogContent>
      </Dialog>

      {/* Delete Snapshot Verification Dialog */}
      <Dialog open={deletePromptOpen} onOpenChange={setDeletePromptOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle className="text-sm font-semibold text-destructive flex items-center gap-2">
              <AlertTriangle className="h-4 w-4 text-destructive" />
              <span>{t('snapshots.delete.title')}</span>
            </DialogTitle>
            <DialogDescription className="text-xs">
              {t('snapshots.delete.message')}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 pt-2">
            <div className="space-y-1">
              <span className="text-xs font-mono text-muted-foreground block">
                Target ID: <strong className="text-foreground">{snapshotToDelete?.id}</strong>
              </span>
              <p className="text-[11px] text-muted-foreground">
                Type the full snapshot ID to confirm deletion:
              </p>
            </div>
            <Input
              placeholder={snapshotToDelete?.id}
              value={deleteConfirmInput}
              onChange={(e) => setDeleteConfirmInput(e.target.value)}
              disabled={deletingSnapshot}
              className="h-9 text-xs font-mono"
            />
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => setDeletePromptOpen(false)}
                disabled={deletingSnapshot}
                className="h-8 text-xs"
              >
                {t('common.cancel')}
              </Button>
              <Button
                type="button"
                variant="destructive"
                size="sm"
                onClick={handleDeleteSnapshotConfirm}
                disabled={deletingSnapshot || deleteConfirmInput.trim() !== snapshotToDelete?.id}
                className="h-8 text-xs gap-1.5"
              >
                {deletingSnapshot && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
                {t('snapshots.delete.confirm')}
              </Button>
            </DialogFooter>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  )
}
