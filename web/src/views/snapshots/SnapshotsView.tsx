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
import {
  getCachedSnapshotList,
  setCachedSnapshotList,
  removeCachedSnapshotList,
  getCachedTree,
  setCachedTree,
  removeCachedTree,
  removeSnapshotFromCache,
  reconcileCachedTrees,
  normalizeSnapshotPath,
  type CacheStatus,
} from './browseCache'
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
  const [snapshotsVerifying, setSnapshotsVerifying] = useState(false)
  const [snapshotsCache, setSnapshotsCache] = useState<CacheStatus>(null)
  const [snapshotsVerifiedAt, setSnapshotsVerifiedAt] = useState<string | null>(null)
  const [serverConfirmedHit, setServerConfirmedHit] = useState(false)
  const [snapshotsLoadError, setSnapshotsLoadError] = useState<string | null>(null)

  // Drawer / Tree Explorer
  const [selectedSnapshot, setSelectedSnapshot] = useState<Snapshot | null>(null)
  const [detailDrawerOpen, setDetailDrawerOpen] = useState(false)
  const [treeLoading, setTreeLoading] = useState(false)
  const [treeVerifying, setTreeVerifying] = useState(false)
  const [treeCacheStatus, setTreeCacheStatus] = useState<CacheStatus>(null)
  const [treeVerifiedAt, setTreeVerifiedAt] = useState<string | null>(null)
  const [treeServerConfirmedHit, setTreeServerConfirmedHit] = useState(false)
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
  const selectedRepoIdRef = useRef(selectedRepoId)
  const selectedSnapshotRef = useRef<Snapshot | null>(null)
  useEffect(() => {
    selectedRepoIdRef.current = selectedRepoId
  }, [selectedRepoId])
  useEffect(() => {
    selectedSnapshotRef.current = selectedSnapshot
  }, [selectedSnapshot])
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

  const canDelete = Boolean(
    serverConfirmedHit && !snapshotsLoading && !snapshotsVerifying
  )

  const canRestore = useMemo(() => {
    return Boolean(
      selectedSnapshotView?.kind === 'filesystem' &&
      selectedSnapshot &&
      selectedRepo?.agent_id &&
      serverConfirmedHit &&
      treeServerConfirmedHit &&
      !snapshotsLoading &&
      !snapshotsVerifying &&
      !treeLoading &&
      !treeVerifying
    )
  }, [
    selectedSnapshotView,
    selectedSnapshot,
    selectedRepo,
    serverConfirmedHit,
    treeServerConfirmedHit,
    snapshotsLoading,
    snapshotsVerifying,
    treeLoading,
    treeVerifying,
  ])
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
    const repoId = selectedRepoIdRef.current
    if (!repoId) return
    const reqId = ++snapshotsReqRef.current
    snapshotsAbortRef.current?.abort()
    const controller = new AbortController()
    snapshotsAbortRef.current = controller

    setSnapshotsLoadError(null)

    // 1. 同步展示内存项（若存在且非强制刷新）
    const memCached = !refresh ? getCachedSnapshotList(repoId) : undefined
    if (memCached) {
      setSnapshots(memCached.snapshots)
      setSnapshotsCache(memCached.cacheStatus)
      setSnapshotsVerifiedAt(memCached.verifiedAt)
      setServerConfirmedHit(false)
      setSnapshotsLoading(false)
    } else {
      setSnapshotsLoading(true)
    }

    setSnapshotsVerifying(true)

    try {
      if (refresh) {
        const response = await apiGetWithMeta<Snapshot[]>(
          `/repositories/${repoId}/snapshots`,
          { refresh: 1 },
          { signal: controller.signal }
        )
        if (reqId !== snapshotsReqRef.current || repoId !== selectedRepoIdRef.current) return
        const data = response.data || []
        setSnapshots(data)
        setSnapshotsCache('HIT')
        setSnapshotsVerifiedAt(response.meta.verifiedAt)
        setServerConfirmedHit(true)
        setCachedSnapshotList(repoId, {
          snapshots: data,
          cacheStatus: 'HIT',
          verifiedAt: response.meta.verifiedAt,
        })
        reconcileCachedTrees(repoId, new Set(data.map((s) => s.id)))
        return
      }

      // 先查 cached=1
      const cachedResp = await apiGetWithMeta<Snapshot[]>(
        `/repositories/${repoId}/snapshots`,
        { cached: 1 },
        { signal: controller.signal }
      )
      if (reqId !== snapshotsReqRef.current || repoId !== selectedRepoIdRef.current) return

      // 服务器 204: 冷缓存或已失效
      if (cachedResp.data === undefined || cachedResp.data === null) {
        removeCachedSnapshotList(repoId)
        if (memCached) {
          setSnapshots([])
          setSnapshotsCache(null)
          setSnapshotsVerifiedAt(null)
          setSnapshotsLoading(true)
        }
        const freshResp = await apiGetWithMeta<Snapshot[]>(
          `/repositories/${repoId}/snapshots`,
          undefined,
          { signal: controller.signal }
        )
        if (reqId !== snapshotsReqRef.current || repoId !== selectedRepoIdRef.current) return
        const freshData = freshResp.data || []
        const isConfirmed =
          freshResp.meta.cache === 'HIT' ||
          (freshResp.meta.cache === 'MISS' && Boolean(freshResp.meta.verifiedAt))
        setSnapshots(freshData)
        setSnapshotsCache(isConfirmed ? 'HIT' : (freshResp.meta.cache === 'STALE' ? 'STALE' : null))
        setSnapshotsVerifiedAt(freshResp.meta.verifiedAt)
        setServerConfirmedHit(isConfirmed)
        setCachedSnapshotList(repoId, {
          snapshots: freshData,
          cacheStatus: isConfirmed ? 'HIT' : (freshResp.meta.cache === 'STALE' ? 'STALE' : null),
          verifiedAt: freshResp.meta.verifiedAt,
        })
        reconcileCachedTrees(repoId, new Set(freshData.map((s) => s.id)))
        return
      }

      // 服务器 200 HIT: 无需调 Agent
      const cachedData = cachedResp.data
      if (cachedResp.meta.cache === 'HIT') {
        setSnapshots(cachedData)
        setSnapshotsCache('HIT')
        setSnapshotsVerifiedAt(cachedResp.meta.verifiedAt)
        setServerConfirmedHit(true)
        setCachedSnapshotList(repoId, {
          snapshots: cachedData,
          cacheStatus: 'HIT',
          verifiedAt: cachedResp.meta.verifiedAt,
        })
        reconcileCachedTrees(repoId, new Set(cachedData.map((s) => s.id)))
        return
      }

      // 服务器 200 STALE: 立即展示待验证列表，后台发起原默认 GET 核验
      setSnapshots(cachedData)
      setSnapshotsCache('STALE')
      setSnapshotsVerifiedAt(cachedResp.meta.verifiedAt)
      setServerConfirmedHit(false)
      setCachedSnapshotList(repoId, {
        snapshots: cachedData,
        cacheStatus: 'STALE',
        verifiedAt: cachedResp.meta.verifiedAt,
      })
      setSnapshotsLoading(false)

      try {
        const verifyResp = await apiGetWithMeta<Snapshot[]>(
          `/repositories/${repoId}/snapshots`,
          undefined,
          { signal: controller.signal }
        )
        if (reqId !== snapshotsReqRef.current || repoId !== selectedRepoIdRef.current) return
        const verifyData = verifyResp.data || []
        const isConfirmed =
          verifyResp.meta.cache === 'HIT' ||
          (verifyResp.meta.cache === 'MISS' && Boolean(verifyResp.meta.verifiedAt))
        setSnapshots(verifyData)
        setSnapshotsCache(isConfirmed ? 'HIT' : (verifyResp.meta.cache === 'STALE' ? 'STALE' : null))
        setSnapshotsVerifiedAt(verifyResp.meta.verifiedAt)
        setServerConfirmedHit(isConfirmed)
        setCachedSnapshotList(repoId, {
          snapshots: verifyData,
          cacheStatus: isConfirmed ? 'HIT' : (verifyResp.meta.cache === 'STALE' ? 'STALE' : null),
          verifiedAt: verifyResp.meta.verifiedAt,
        })
        reconcileCachedTrees(repoId, new Set(verifyData.map((s) => s.id)))
      } catch (err: unknown) {
        if (isAbortError(err)) return
        if (reqId !== snapshotsReqRef.current || repoId !== selectedRepoIdRef.current) return
        const msg = isApiClientError(err) ? err.message : t('snapshots.messages.snapshotsLoadFailed')
        toastError(msg)
        setSnapshotsLoadError(msg)
      }
    } catch (err: unknown) {
      if (isAbortError(err)) return
      if (reqId !== snapshotsReqRef.current || repoId !== selectedRepoIdRef.current) return
      const msg = isApiClientError(err) ? err.message : t('snapshots.messages.snapshotsLoadFailed')
      toastError(msg)
      setSnapshotsLoadError(msg)
    } finally {
      if (reqId === snapshotsReqRef.current) {
        setSnapshotsLoading(false)
        setSnapshotsVerifying(false)
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
    selectedSnapshotRef.current = null
    setSelectedSnapshot(null)
    setDetailDrawerOpen(false)
    setSnapshotsLoadError(null)
    setTreeEntries([])
    setTreePath('/')
    setTreeCacheStatus(null)
    setTreeVerifiedAt(null)
    setTreeServerConfirmedHit(false)
    if (selectedRepoId) {
      loadSnapshots(false)
    } else {
      setSnapshots([])
      setSnapshotsCache(null)
      setSnapshotsVerifiedAt(null)
      setServerConfirmedHit(false)
    }
  }, [selectedRepoId])

  const loadTree = async (
    repoId = selectedRepoIdRef.current,
    snapshotId = selectedSnapshotRef.current?.id,
    path = treePath,
    refresh = false
  ) => {
    if (!snapshotId || !repoId) return
    const normPath = normalizeSnapshotPath(path)
    const reqId = ++treeReqRef.current

    treeAbortRef.current?.abort()
    const controller = new AbortController()
    treeAbortRef.current = controller

    const memCached = !refresh ? getCachedTree(repoId, snapshotId, normPath) : undefined
    if (memCached) {
      setTreeEntries(memCached.entries)
      setTreePath(memCached.path)
      setTreeCacheStatus(memCached.cacheStatus)
      setTreeVerifiedAt(memCached.verifiedAt)
      setTreeServerConfirmedHit(false)
      setTreeLoading(false)
    } else {
      setTreeEntries([])
      setTreeLoading(true)
    }

    setTreeVerifying(true)

    try {
      if (refresh) {
        const response = await apiGetWithMeta<TreeResponse>(
          `/snapshots/${snapshotId}/tree`,
          {
            repo: repoId,
            path: normPath,
            refresh: 1,
          },
          { signal: controller.signal }
        )
        if (reqId !== treeReqRef.current || snapshotId !== selectedSnapshotRef.current?.id || repoId !== selectedRepoIdRef.current) return
        const entries = response.data.entries || []
        const retPath = response.data.path || normPath
        setTreeEntries(entries)
        setTreePath(retPath)
        setTreeCacheStatus('HIT')
        setTreeVerifiedAt(response.meta.verifiedAt)
        setTreeServerConfirmedHit(true)
        setCachedTree(repoId, snapshotId, normPath, {
          entries,
          path: retPath,
          cacheStatus: 'HIT',
          verifiedAt: response.meta.verifiedAt,
        })
        return
      }

      // 先查 cached=1
      const cachedResp = await apiGetWithMeta<TreeResponse>(
        `/snapshots/${snapshotId}/tree`,
        {
          repo: repoId,
          path: normPath,
          cached: 1,
        },
        { signal: controller.signal }
      )
      if (reqId !== treeReqRef.current || snapshotId !== selectedSnapshotRef.current?.id || repoId !== selectedRepoIdRef.current) return

      // 服务器 204: 冷缓存或已失效
      if (cachedResp.data === undefined || cachedResp.data === null) {
        removeCachedTree(repoId, snapshotId, normPath)
        if (memCached) {
          setTreeEntries([])
          setTreeLoading(true)
        }
        const freshResp = await apiGetWithMeta<TreeResponse>(
          `/snapshots/${snapshotId}/tree`,
          {
            repo: repoId,
            path: normPath,
          },
          { signal: controller.signal }
        )
        if (reqId !== treeReqRef.current || snapshotId !== selectedSnapshotRef.current?.id || repoId !== selectedRepoIdRef.current) return
        const freshEntries = freshResp.data.entries || []
        const freshPath = freshResp.data.path || normPath
        const isConfirmed =
          freshResp.meta.cache === 'HIT' ||
          (freshResp.meta.cache === 'MISS' && Boolean(freshResp.meta.verifiedAt))
        setTreeEntries(freshEntries)
        setTreePath(freshPath)
        setTreeCacheStatus(isConfirmed ? 'HIT' : (freshResp.meta.cache === 'STALE' ? 'STALE' : null))
        setTreeVerifiedAt(freshResp.meta.verifiedAt)
        setTreeServerConfirmedHit(isConfirmed)
        setCachedTree(repoId, snapshotId, normPath, {
          entries: freshEntries,
          path: freshPath,
          cacheStatus: isConfirmed ? 'HIT' : (freshResp.meta.cache === 'STALE' ? 'STALE' : null),
          verifiedAt: freshResp.meta.verifiedAt,
        })
        return
      }

      // 服务器 200 HIT: 无需调 Agent
      if (cachedResp.meta.cache === 'HIT') {
        const entries = cachedResp.data.entries || []
        const retPath = cachedResp.data.path || normPath
        setTreeEntries(entries)
        setTreePath(retPath)
        setTreeCacheStatus('HIT')
        setTreeVerifiedAt(cachedResp.meta.verifiedAt)
        setTreeServerConfirmedHit(true)
        setCachedTree(repoId, snapshotId, normPath, {
          entries,
          path: retPath,
          cacheStatus: 'HIT',
          verifiedAt: cachedResp.meta.verifiedAt,
        })
        return
      }

      // 服务器 200 STALE: 立即展示待验证树，后台发起默认 GET 核验
      const staleEntries = cachedResp.data.entries || []
      const stalePath = cachedResp.data.path || normPath
      setTreeEntries(staleEntries)
      setTreePath(stalePath)
      setTreeCacheStatus('STALE')
      setTreeVerifiedAt(cachedResp.meta.verifiedAt)
      setTreeServerConfirmedHit(false)
      setCachedTree(repoId, snapshotId, normPath, {
        entries: staleEntries,
        path: stalePath,
        cacheStatus: 'STALE',
        verifiedAt: cachedResp.meta.verifiedAt,
      })
      setTreeLoading(false)

      try {
        const verifyResp = await apiGetWithMeta<TreeResponse>(
          `/snapshots/${snapshotId}/tree`,
          {
            repo: repoId,
            path: normPath,
          },
          { signal: controller.signal }
        )
        if (reqId !== treeReqRef.current || snapshotId !== selectedSnapshotRef.current?.id || repoId !== selectedRepoIdRef.current) return
        const entries = verifyResp.data.entries || []
        const retPath = verifyResp.data.path || normPath
        const isConfirmed =
          verifyResp.meta.cache === 'HIT' ||
          (verifyResp.meta.cache === 'MISS' && Boolean(verifyResp.meta.verifiedAt))
        setTreeEntries(entries)
        setTreePath(retPath)
        setTreeCacheStatus(isConfirmed ? 'HIT' : (verifyResp.meta.cache === 'STALE' ? 'STALE' : null))
        setTreeVerifiedAt(verifyResp.meta.verifiedAt)
        setTreeServerConfirmedHit(isConfirmed)
        setCachedTree(repoId, snapshotId, normPath, {
          entries,
          path: retPath,
          cacheStatus: isConfirmed ? 'HIT' : (verifyResp.meta.cache === 'STALE' ? 'STALE' : null),
          verifiedAt: verifyResp.meta.verifiedAt,
        })
      } catch (err: unknown) {
        if (isAbortError(err)) return
        if (reqId !== treeReqRef.current) return
        toastError(isApiClientError(err) ? err.message : t('snapshots.messages.treeLoadFailed'))
      }
    } catch (err: unknown) {
      if (isAbortError(err)) return
      if (reqId !== treeReqRef.current) return
      toastError(isApiClientError(err) ? err.message : t('snapshots.messages.treeLoadFailed'))
    } finally {
      if (reqId === treeReqRef.current) {
        setTreeLoading(false)
        setTreeVerifying(false)
      }
    }
  }

  const handleSelectSnapshot = (snapshot: Snapshot) => {
    selectedSnapshotRef.current = snapshot
    setSelectedSnapshot(snapshot)
    setDetailDrawerOpen(true)
    setTreePath('/')
    setTreeSelectedPaths([])
    setTreeEntries([])
    setDryRunResult(null)
    loadTree(selectedRepoIdRef.current, snapshot.id, '/', false)
  }

  const navigateBreadcrumb = (path: string) => {
    setTreePath(path)
    setTreeSelectedPaths([])
    const snap = selectedSnapshotRef.current
    const repo = selectedRepoIdRef.current
    if (snap && repo) {
      loadTree(repo, snap.id, path, false)
    }
  }

  const handleNavigateDir = (nextPath: string) => {
    setTreePath(nextPath)
    setTreeSelectedPaths([])
    const snap = selectedSnapshotRef.current
    const repo = selectedRepoIdRef.current
    if (snap && repo) {
      loadTree(repo, snap.id, nextPath, false)
    }
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
      removeSnapshotFromCache(selectedRepoId, snapshotToDelete.id)
      if (selectedSnapshotRef.current?.id === snapshotToDelete.id) {
        setDetailDrawerOpen(false)
        selectedSnapshotRef.current = null
        setSelectedSnapshot(null)
      }
      setSnapshots((prev) => prev.filter((s) => s.id !== snapshotToDelete.id))
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
            snapshotsVerifying={snapshotsVerifying}
            snapshotsError={snapshotsLoadError}
            onRefresh={() => loadSnapshots(true)}
            onRetry={() => loadSnapshots(false)}
          />

          <SnapshotList
            selectedRepoId={selectedRepoId}
            snapshotsLoading={snapshotsLoading}
            totalSnapshots={snapshots.length}
            filteredSnapshots={filteredSnapshots}
            onSelectSnapshot={handleSelectSnapshot}
            onDeleteSnapshot={openDeletePrompt}
            canDelete={canDelete}
          />
        </div>
      )}

      <SnapshotDetailSheet
        open={detailDrawerOpen}
        onOpenChange={(open) => {
          setDetailDrawerOpen(open)
          if (!open) {
            selectedSnapshotRef.current = null
            setSelectedSnapshot(null)
            treeAbortRef.current?.abort()
          }
        }}
        selectedSnapshotView={selectedSnapshotView}
        canRestore={canRestore}
        copiedId={copiedId}
        treeLoading={treeLoading}
        treeVerifying={treeVerifying}
        treeCacheStatus={treeCacheStatus}
        treeVerifiedAt={treeVerifiedAt}
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
