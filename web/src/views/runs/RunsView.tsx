import React, { useEffect, useState, useMemo } from 'react'
import { useNavigate } from 'react-router-dom'
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
import { apiGet, apiPost } from '@/api/client'
import { formatDateTime, translateEnum } from '@/i18n'
import type { Run, Agent, Plan, ApiError } from '@/api/types'
import { AppEmptyState } from '@/components/AppEmptyState'
import { AppErrorState } from '@/components/AppErrorState'
import { StatusBadge } from '@/components/StatusBadge'
import { ConfirmActionDialog } from '@/components/ConfirmActionDialog'
import { toastSuccess, toastError } from '@/lib/toast'
import {
  STATUS_VALUE_KEYS,
  OPERATION_VALUE_KEYS,
  statusTagType,
  operationTagType,
  formatDuration,
} from '@/utils/runDisplay'
import { RefreshCw, Search, XSquare, ChevronLeft, ChevronRight, Loader2, RotateCcw } from 'lucide-react'

export const RunsView: React.FC = () => {
  const { t } = useTranslation()
  const navigate = useNavigate()

  const [runs, setRuns] = useState<Run[]>([])
  const [agents, setAgents] = useState<Agent[]>([])
  const [plans, setPlans] = useState<Plan[]>([])

  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  // Filters
  const [planIdInput, setPlanIdInput] = useState('')
  const [selectedAgent, setSelectedAgent] = useState('all')
  const [selectedStatus, setSelectedStatus] = useState('all')
  const [selectedOp, setSelectedOp] = useState('all')

  // Pagination
  const [limit, setLimit] = useState(20)
  const [offset, setOffset] = useState(0)

  // Cancel run dialog
  const [cancelDialogOpen, setCancelDialogOpen] = useState(false)
  const [runToCancel, setRunToCancel] = useState<Run | null>(null)

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

  const loadRuns = async (newOffset = offset) => {
    setLoading(true)
    setError(null)
    try {
      const params: Record<string, string | number | undefined> = {
        limit,
        offset: newOffset,
        plan_id: planIdInput.trim() || undefined,
        agent_id: selectedAgent === 'all' ? undefined : selectedAgent,
        status: selectedStatus === 'all' ? undefined : selectedStatus,
        operation: selectedOp === 'all' ? undefined : selectedOp,
      }
      const data = await apiGet<Run[]>('/runs', params)
      setRuns(data)
      setOffset(newOffset)
    } catch (err: unknown) {
      const apiErr = err as ApiError
      setError(apiErr?.message || t('runs.loadFailed') || 'Failed to load runs')
    } finally {
      setLoading(false)
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
    loadRuns(0)
  }, [])

  // 400ms debounce on planIdInput
  useEffect(() => {
    const timer = setTimeout(() => {
      loadRuns(0)
    }, 400)
    return () => clearTimeout(timer)
  }, [planIdInput])

  const handleSearch = (e: React.FormEvent) => {
    e.preventDefault()
    loadRuns(0)
  }

  const handleReset = () => {
    setPlanIdInput('')
    setSelectedAgent('all')
    setSelectedStatus('all')
    setSelectedOp('all')
    setOffset(0)
    setTimeout(() => {
      loadRuns(0)
    }, 0)
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
      toastSuccess(t('runs.cancelled') || 'Run cancel request submitted')
      await loadRuns(offset)
    } catch (err: unknown) {
      const apiErr = err as ApiError
      toastError(apiErr?.message || t('runs.cancelFailed') || 'Failed to cancel run')
    } finally {
      // cleanup
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
            {t('runs.subtitle') || 'Audit history, state progression, and real-time execution logs'}
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={() => loadRuns(offset)}
          className="h-8 text-xs gap-1.5 self-start sm:self-auto"
        >
          <RefreshCw className="h-3.5 w-3.5" />
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
                placeholder={t('runs.filters.planIdPlaceholder') || 'Filter by plan ID...'}
                value={planIdInput}
                onChange={(e) => setPlanIdInput(e.target.value)}
                className="h-8 text-xs font-mono"
              />
            </div>

            <div className="space-y-1">
              <Label className="text-[11px] text-muted-foreground">{t('runs.filters.agent')}</Label>
              <Select
                value={selectedAgent}
                onValueChange={(val) => {
                  setSelectedAgent(val)
                  loadRuns(0)
                }}
              >
                <SelectTrigger className="h-8 text-xs">
                  <SelectValue placeholder={t('runs.filters.agent')} />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all" className="text-xs">
                    {t('common.all') || 'All Agents'}
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
              <Select
                value={selectedStatus}
                onValueChange={(val) => {
                  setSelectedStatus(val)
                  loadRuns(0)
                }}
              >
                <SelectTrigger className="h-8 text-xs">
                  <SelectValue placeholder={t('runs.filters.status')} />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all" className="text-xs">
                    {t('common.all') || 'All Statuses'}
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
              <Select
                value={selectedOp}
                onValueChange={(val) => {
                  setSelectedOp(val)
                  loadRuns(0)
                }}
              >
                <SelectTrigger className="h-8 text-xs">
                  <SelectValue placeholder={t('runs.filters.operation')} />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all" className="text-xs">
                    {t('common.all') || 'All Operations'}
                  </SelectItem>
                  {operationOptions.map((o) => (
                    <SelectItem key={o.value} value={o.value} className="text-xs">
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="flex items-end gap-2">
              <Button type="submit" size="sm" className="h-8 text-xs flex-1 gap-1">
                <Search className="h-3 w-3" />
                {t('common.search')}
              </Button>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={handleReset}
                className="h-8 text-xs gap-1"
              >
                <RotateCcw className="h-3 w-3" />
                {t('common.reset')}
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>

      {error ? (
        <AppErrorState title={t('runs.title')} message={error} onRetry={() => loadRuns(offset)} />
      ) : (
        <Card className="border-border bg-card/60 shadow-sm">
          <CardContent className="p-0">
            {loading ? (
              <div className="flex h-48 items-center justify-center">
                <Loader2 className="h-6 w-6 animate-spin text-primary" />
              </div>
            ) : runs.length > 0 ? (
              <div className="rounded-md overflow-hidden">
                <Table>
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
                        <TableRow
                          key={run.id}
                          onClick={() => navigate(`/runs/${run.id}`)}
                          className="border-border hover:bg-muted/40 cursor-pointer"
                        >
                          <TableCell className="text-xs text-muted-foreground font-mono">
                            {formatDateTime(run.queued_at)}
                          </TableCell>
                          <TableCell className="font-medium text-xs text-foreground">
                            {planLabel}
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
                                className="h-7 text-xs text-rose-400 hover:text-rose-300 gap-1"
                              >
                                <XSquare className="h-3.5 w-3.5" />
                                {t('common.cancel')}
                              </Button>
                            )}
                          </TableCell>
                        </TableRow>
                      )
                    })}
                  </TableBody>
                </Table>

                {/* Pagination footer */}
                <div className="flex items-center justify-between p-3 border-t border-border bg-card/40">
                  <div className="flex items-center gap-2">
                    <span className="text-xs text-muted-foreground">Rows per page:</span>
                    <Select
                      value={String(limit)}
                      onValueChange={(val) => {
                        setLimit(Number(val))
                        loadRuns(0)
                      }}
                    >
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
                      Offset: {offset} – {offset + runs.length}
                    </span>
                    <Button
                      variant="outline"
                      size="icon"
                      disabled={offset === 0}
                      onClick={() => loadRuns(Math.max(0, offset - limit))}
                      className="h-7 w-7"
                    >
                      <ChevronLeft className="h-3.5 w-3.5" />
                    </Button>
                    <Button
                      variant="outline"
                      size="icon"
                      disabled={runs.length < limit}
                      onClick={() => loadRuns(offset + limit)}
                      className="h-7 w-7"
                    >
                      <ChevronRight className="h-3.5 w-3.5" />
                    </Button>
                  </div>
                </div>
              </div>
            ) : (
              <div className="p-8">
                <AppEmptyState
                  title={t('runs.emptyRuns') || 'No Runs Found'}
                  description={
                    t('runs.emptyRuns_desc') ||
                    'Run executions will appear here when scheduled plans or manual runs trigger.'
                  }
                />
              </div>
            )}
          </CardContent>
        </Card>
      )}

      {/* Cancel Run Dialog */}
      <ConfirmActionDialog
        open={cancelDialogOpen}
        onOpenChange={setCancelDialogOpen}
        title={t('runs.cancelConfirmTitle') || 'Cancel Active Run?'}
        description={
          runToCancel
            ? t('runs.cancelConfirmDesc', { id: runToCancel.id.substring(0, 8) }) ||
              `Are you sure you want to request cancellation for run ${runToCancel.id.substring(0, 8)}?`
            : ''
        }
        destructive
        onConfirm={handleCancelConfirm}
      />
    </div>
  )
}
