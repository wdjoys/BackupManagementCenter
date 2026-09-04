import React, { useEffect, useState, useMemo, useRef } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Card, CardContent } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { apiGet, apiPost, isApiClientError, isAbortError } from '@/api/client'
import { formatDateTime, translateEnum } from '@/i18n'
import type { Run, Agent, Plan } from '@/api/types'
import { AppEmptyState } from '@/components/AppEmptyState'
import { AppErrorState } from '@/components/AppErrorState'
import { StatusBadge } from '@/components/StatusBadge'
import { ConfirmActionDialog } from '@/components/ConfirmActionDialog'
import { PageLoadingState } from '@/components/PageLoadingState'
import { toastSuccess, toastError } from '@/lib/toast'
import {
  STATUS_VALUE_KEYS,
  OPERATION_VALUE_KEYS,
  statusTagType,
  operationTagType,
  formatDuration,
} from '@/utils/runDisplay'
import { RefreshCw, Search, XSquare, ChevronLeft, ChevronRight, RotateCcw } from 'lucide-react'

export interface RunFilters {
  planId: string
  agentId: string
  status: string
  operation: string
}

export interface RunQuery {
  filters: RunFilters
  limit: number
  offset: number
}

const DEFAULT_FILTERS: RunFilters = {
  planId: '',
  agentId: 'all',
  status: 'all',
  operation: 'all',
}

export const RunsView: React.FC = () => {
  const { t } = useTranslation()

  const [runs, setRuns] = useState<Run[]>([])
  const [agents, setAgents] = useState<Agent[]>([])
  const [plans, setPlans] = useState<Plan[]>([])

  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  // Filters state
  const [filters, setFilters] = useState<RunFilters>(DEFAULT_FILTERS)
  const [planIdInput, setPlanIdInput] = useState('')

  // Pagination state
  const [limit, setLimit] = useState(20)
  const [offset, setOffset] = useState(0)

  // Cancel run dialog
  const [cancelDialogOpen, setCancelDialogOpen] = useState(false)
  const [runToCancel, setRunToCancel] = useState<Run | null>(null)

  const abortControllerRef = useRef<AbortController | null>(null)
  const currentQueryRef = useRef<RunQuery>({
    filters: DEFAULT_FILTERS,
    limit: 20,
    offset: 0,
  })
  const didMountRef = useRef(false)

  const statusOptions = useMemo(
    () =>
      Object.entries(STATUS_VALUE_KEYS).map(([value, key]) => ({
        value,
        label: t(key),
      })),
    [t]
  )

  const operationOptions = useMemo(
    () =>
      Object.entries(OPERATION_VALUE_KEYS).map(([value, key]) => ({
        value,
        label: t(key),
      })),
    [t]
  )

  const planMap = useMemo(() => {
    const map = new Map<string, Plan>()
    for (const p of plans) {
      map.set(p.id, p)
    }
    return map
  }, [plans])

  const loadRuns = async (query: RunQuery) => {
    abortControllerRef.current?.abort()
    const controller = new AbortController()
    abortControllerRef.current = controller
    currentQueryRef.current = query

    setLoading(true)
    setError(null)
    try {
      const params: Record<string, string | number | undefined> = {
        limit: query.limit,
        offset: query.offset,
        plan_id: query.filters.planId.trim() || undefined,
        agent_id: query.filters.agentId === 'all' ? undefined : query.filters.agentId,
        status: query.filters.status === 'all' ? undefined : query.filters.status,
        operation: query.filters.operation === 'all' ? undefined : query.filters.operation,
      }
      const data = await apiGet<Run[]>('/runs', params, { signal: controller.signal })
      setRuns(data)
      setOffset(query.offset)
      setLimit(query.limit)
    } catch (err: unknown) {
      if (isAbortError(err)) return
      setError(isApiClientError(err) ? err.message : t('runs.loadFailed'))
    } finally {
      if (abortControllerRef.current === controller) {
        setLoading(false)
      }
    }
  }

  const loadMeta = async () => {
    try {
      const [agentList, planList] = await Promise.all([
        apiGet<Agent[]>('/agents'),
        apiGet<Plan[]>('/plans'),
      ])
      setAgents(agentList)
      setPlans(planList)
    } catch {
      // Non-blocking
    }
  }

  useEffect(() => {
    loadMeta()
    loadRuns({
      filters: DEFAULT_FILTERS,
      limit: 20,
      offset: 0,
    })
    return () => {
      abortControllerRef.current?.abort()
    }
  }, [])

  // 400ms debounce on planIdInput (skipping first render)
  useEffect(() => {
    if (!didMountRef.current) {
      didMountRef.current = true
      return
    }
    const timer = setTimeout(() => {
      const nextFilters: RunFilters = { ...filters, planId: planIdInput }
      setFilters(nextFilters)
      loadRuns({ filters: nextFilters, limit, offset: 0 })
    }, 400)
    return () => clearTimeout(timer)
  }, [planIdInput])

  const handleSearch = (e: React.FormEvent) => {
    e.preventDefault()
    const nextFilters: RunFilters = { ...filters, planId: planIdInput }
    setFilters(nextFilters)
    loadRuns({ filters: nextFilters, limit, offset: 0 })
  }

  const handleReset = () => {
    setPlanIdInput('')
    setFilters(DEFAULT_FILTERS)
    loadRuns({ filters: DEFAULT_FILTERS, limit, offset: 0 })
  }

  const handleAgentChange = (val: string) => {
    const nextFilters: RunFilters = { ...filters, agentId: val }
    setFilters(nextFilters)
    loadRuns({ filters: nextFilters, limit, offset: 0 })
  }

  const handleStatusChange = (val: string) => {
    const nextFilters: RunFilters = { ...filters, status: val }
    setFilters(nextFilters)
    loadRuns({ filters: nextFilters, limit, offset: 0 })
  }

  const handleOpChange = (val: string) => {
    const nextFilters: RunFilters = { ...filters, operation: val }
    setFilters(nextFilters)
    loadRuns({ filters: nextFilters, limit, offset: 0 })
  }

  const handleLimitChange = (val: string) => {
    const newLimit = Number(val)
    setLimit(newLimit)
    loadRuns({ filters, limit: newLimit, offset: 0 })
  }

  const handlePageChange = (newOffset: number) => {
    loadRuns({ filters, limit, offset: newOffset })
  }

  const isCancelable = (status: string) => {
    return status === 'queued' || status === 'dispatched' || status === 'running'
  }

  const openCancelDialog = (e: React.MouseEvent, run: Run) => {
    e.stopPropagation()
    setRunToCancel(run)
    setCancelDialogOpen(true)
  }

  const handleCancelConfirm = async () => {
    if (!runToCancel) return
    try {
      await apiPost(`/runs/${runToCancel.id}/cancel`, {})
      toastSuccess(t('runs.cancelled'))
      await loadRuns(currentQueryRef.current)
    } catch (err: unknown) {
      toastError(isApiClientError(err) ? err.message : t('runs.cancelFailed'))
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
        <div>
          <h2 className="text-xl font-bold tracking-tight text-foreground">
            {t('runs.title')}
          </h2>
          <p className="text-xs text-muted-foreground">
            {t('runs.subtitle')}
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={() => loadRuns(currentQueryRef.current)}
          disabled={loading}
          className="h-8 text-xs gap-1.5 self-start sm:self-auto"
        >
          <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
          {t('common.refresh')}
        </Button>
      </div>

      {/* Filter Card */}
      <Card className="border-border bg-card/40 shadow-sm">
        <CardContent className="p-4">
          <form onSubmit={handleSearch} className="grid grid-cols-1 sm:grid-cols-2 md:grid-cols-4 lg:grid-cols-5 gap-3">
            <div className="space-y-1">
              <Label className="text-[11px] text-muted-foreground">{t('runs.filters.plan')}</Label>
              <Input
                placeholder={t('runs.filters.planIdPlaceholder')}
                value={planIdInput}
                onChange={(e) => setPlanIdInput(e.target.value)}
                className="h-8 text-xs font-mono"
              />
            </div>

            <div className="space-y-1">
              <Label className="text-[11px] text-muted-foreground">{t('runs.filters.agent')}</Label>
              <Select value={filters.agentId} onValueChange={handleAgentChange}>
                <SelectTrigger className="h-8 text-xs">
                  <SelectValue placeholder={t('runs.filters.agent')} />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all" className="text-xs">
                    {t('common.allAgents')}
                  </SelectItem>
                  {agents.map((a) => (
                    <SelectItem key={a.id} value={a.id} className="text-xs">
                      {a.name} ({a.hostname})
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-1">
              <Label className="text-[11px] text-muted-foreground">{t('runs.filters.status')}</Label>
              <Select value={filters.status} onValueChange={handleStatusChange}>
                <SelectTrigger className="h-8 text-xs">
                  <SelectValue placeholder={t('runs.filters.status')} />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all" className="text-xs">
                    {t('common.all')}
                  </SelectItem>
                  {statusOptions.map((s) => (
                    <SelectItem key={s.value} value={s.value} className="text-xs">
                      {s.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-1">
              <Label className="text-[11px] text-muted-foreground">{t('runs.filters.operation')}</Label>
              <Select value={filters.operation} onValueChange={handleOpChange}>
                <SelectTrigger className="h-8 text-xs">
                  <SelectValue placeholder={t('runs.filters.operation')} />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all" className="text-xs">
                    {t('common.all')}
                  </SelectItem>
                  {operationOptions.map((o) => (
                    <SelectItem key={o.value} value={o.value} className="text-xs">
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="flex items-end gap-2 col-span-1 sm:col-span-2 md:col-span-4 lg:col-span-1">
              <Button type="submit" size="sm" className="h-8 text-xs flex-1 gap-1">
                <Search className="h-3 w-3" aria-hidden="true" />
                {t('common.search')}
              </Button>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={handleReset}
                className="h-8 text-xs gap-1"
              >
                <RotateCcw className="h-3 w-3" aria-hidden="true" />
                {t('common.reset')}
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>

      {error ? (
        <AppErrorState title={t('runs.title')} message={error} onRetry={() => loadRuns(currentQueryRef.current)} />
      ) : (
        <Card className="border-border bg-card/60 shadow-sm">
          <CardContent className="p-0">
            {loading && runs.length === 0 ? (
              <PageLoadingState compact />
            ) : runs.length > 0 ? (
              <div className="rounded-md overflow-hidden">
                {/* Desktop table */}
                <div className="hidden md:block overflow-x-auto">
                  <Table className="min-w-[850px]">
                    <TableHeader>
                      <TableRow className="border-border hover:bg-transparent">
                        <TableHead className="text-xs font-medium">{t('runs.columns.queuedAt')}</TableHead>
                        <TableHead className="text-xs font-medium">{t('dashboard.plan')}</TableHead>
                        <TableHead className="text-xs font-medium">{t('runs.filters.operation')}</TableHead>
                        <TableHead className="text-xs font-medium">{t('common.status')}</TableHead>
                        <TableHead className="text-xs font-medium">{t('runs.columns.snapshot')}</TableHead>
                        <TableHead className="text-xs font-medium">{t('runs.columns.duration')}</TableHead>
                        <TableHead className="text-xs font-medium text-right">{t('common.actions')}</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {runs.map((run) => {
                        const plan = planMap.get(run.plan_id)
                        const planLabel = plan ? plan.name : run.plan_id
                        const canCancel = isCancelable(run.status)
                        return (
                          <TableRow key={run.id} className="border-border hover:bg-muted/40">
                            <TableCell className="text-xs text-muted-foreground font-mono">
                              <Link
                                to={`/runs/${run.id}`}
                                className="hover:text-primary hover:underline"
                              >
                                {formatDateTime(run.queued_at)}
                              </Link>
                            </TableCell>
                            <TableCell className="font-medium text-xs text-foreground">
                              <Link
                                to={`/runs/${run.id}`}
                                className="hover:text-primary hover:underline"
                              >
                                {planLabel}
                              </Link>
                            </TableCell>
                            <TableCell className="text-xs">
                              <StatusBadge tone={operationTagType(run.operation)}>
                                {translateEnum('runs.operations', run.operation)}
                              </StatusBadge>
                            </TableCell>
                            <TableCell className="text-xs">
                              <StatusBadge
                                tone={statusTagType(run.status)}
                                dot={run.status === 'running' || run.status === 'dispatched'}
                              >
                                {translateEnum('status', run.status)}
                              </StatusBadge>
                            </TableCell>
                            <TableCell className="text-xs text-muted-foreground font-mono">
                              {run.snapshot_id ? (
                                <Tooltip>
                                  <TooltipTrigger asChild>
                                    <span className="truncate block max-w-[100px] underline decoration-dotted">
                                      {run.snapshot_id.substring(0, 8)}...
                                    </span>
                                  </TooltipTrigger>
                                  <TooltipContent className="text-[11px] font-mono">
                                    {run.snapshot_id}
                                  </TooltipContent>
                                </Tooltip>
                              ) : (
                                '—'
                              )}
                            </TableCell>
                            <TableCell className="text-xs text-muted-foreground font-mono">
                              {formatDuration(run.started_at, run.finished_at)}
                            </TableCell>
                            <TableCell className="text-xs text-right">
                              {canCancel && (
                                <Button
                                  variant="ghost"
                                  size="sm"
                                  onClick={(e) => openCancelDialog(e, run)}
                                  className="h-7 text-xs text-rose-600 dark:text-rose-400 hover:text-rose-700 dark:hover:text-rose-300 gap-1"
                                  aria-label={t('runs.cancel')}
                                >
                                  <XSquare className="h-3.5 w-3.5" aria-hidden="true" />
                                  {t('common.cancel')}
                                </Button>
                              )}
                            </TableCell>
                          </TableRow>
                        )
                      })}
                    </TableBody>
                  </Table>
                </div>

                {/* Mobile Cards */}
                <div className="md:hidden divide-y divide-border">
                  {runs.map((run) => {
                    const plan = planMap.get(run.plan_id)
                    const planLabel = plan ? plan.name : run.plan_id
                    const canCancel = isCancelable(run.status)
                    return (
                      <div key={run.id} className="p-3 space-y-2 text-xs">
                        <div className="flex items-center justify-between gap-2">
                          <Link
                            to={`/runs/${run.id}`}
                            className="font-semibold text-foreground hover:text-primary hover:underline"
                          >
                            {planLabel}
                          </Link>
                          <StatusBadge
                            tone={statusTagType(run.status)}
                            dot={run.status === 'running' || run.status === 'dispatched'}
                          >
                            {translateEnum('status', run.status)}
                          </StatusBadge>
                        </div>
                        <div className="grid grid-cols-2 gap-1 text-[11px] text-muted-foreground font-mono">
                          <div>
                            <span className="text-muted-foreground/70">{t('runs.columns.queuedAt')}: </span>
                            {formatDateTime(run.queued_at)}
                          </div>
                          <div>
                            <span className="text-muted-foreground/70">{t('runs.columns.duration')}: </span>
                            {formatDuration(run.started_at, run.finished_at)}
                          </div>
                        </div>
                        <div className="flex items-center justify-between pt-1">
                          <StatusBadge tone={operationTagType(run.operation)}>
                            {translateEnum('runs.operations', run.operation)}
                          </StatusBadge>
                          <div className="flex items-center gap-2">
                            {canCancel && (
                              <Button
                                variant="ghost"
                                size="sm"
                                onClick={(e) => openCancelDialog(e, run)}
                                className="h-7 text-xs text-rose-600 dark:text-rose-400 hover:text-rose-700 dark:hover:text-rose-300 gap-1"
                                aria-label={t('runs.cancel')}
                              >
                                <XSquare className="h-3.5 w-3.5" aria-hidden="true" />
                                {t('common.cancel')}
                              </Button>
                            )}
                            <Button
                              asChild
                              variant="outline"
                              size="sm"
                              className="h-7 text-xs"
                            >
                              <Link to={`/runs/${run.id}`}>
                                {t('common.actions')}
                              </Link>
                            </Button>
                          </div>
                        </div>
                      </div>
                    )
                  })}
                </div>

                {/* Pagination footer */}
                <div className="flex items-center justify-between p-3 border-t border-border bg-card/40">
                  <div className="flex items-center gap-2">
                    <span className="text-xs text-muted-foreground">{t('runs.rowsPerPage')}</span>
                    <Select value={String(limit)} onValueChange={handleLimitChange}>
                      <SelectTrigger className="h-7 w-16 text-xs">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="10" className="text-xs">10</SelectItem>
                        <SelectItem value="20" className="text-xs">20</SelectItem>
                        <SelectItem value="50" className="text-xs">50</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>

                  <div className="flex items-center gap-2">
                    <span className="text-xs text-muted-foreground">
                      {t('runs.offset')} {offset} – {offset + runs.length}
                    </span>
                    <Button
                      variant="outline"
                      size="icon"
                      disabled={offset === 0 || loading}
                      onClick={() => handlePageChange(Math.max(0, offset - limit))}
                      className="h-7 w-7"
                      aria-label={t('common.previous')}
                    >
                      <ChevronLeft className="h-3.5 w-3.5" aria-hidden="true" />
                    </Button>
                    <Button
                      variant="outline"
                      size="icon"
                      disabled={runs.length < limit || loading}
                      onClick={() => handlePageChange(offset + limit)}
                      className="h-7 w-7"
                      aria-label={t('common.next')}
                    >
                      <ChevronRight className="h-3.5 w-3.5" aria-hidden="true" />
                    </Button>
                  </div>
                </div>
              </div>
            ) : (
              <div className="p-8">
                <AppEmptyState
                  title={t('runs.emptyRuns')}
                  description={t('runs.emptyRuns_desc')}
                />
              </div>
            )}
          </CardContent>
        </Card>
      )}

      {/* Cancel Confirmation Dialog */}
      <ConfirmActionDialog
        open={cancelDialogOpen}
        onOpenChange={setCancelDialogOpen}
        title={t('runs.cancelConfirmTitle')}
        description={t('runs.cancelConfirmDesc')}
        confirmText={t('runs.cancel')}
        destructive
        onConfirm={handleCancelConfirm}
      />
    </div>
  )
}
