import React, { useEffect, useState, useMemo } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Card, CardContent } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { apiGet, apiPost, apiPut, apiDelete } from '@/api/client'
import { translateEnum, formatDateTime } from '@/i18n'
import type { Plan, Agent, Repository, Run, ApiError } from '@/api/types'
import { AppEmptyState } from '@/components/AppEmptyState'
import { AppErrorState } from '@/components/AppErrorState'
import { StatusBadge, type BadgeTone } from '@/components/StatusBadge'
import { ConfirmActionDialog } from '@/components/ConfirmActionDialog'
import { toastSuccess, toastError, toastWarning } from '@/lib/toast'
import { PlanForm } from './PlanForm'
import { KIND_LABELS, defaultSource } from './Constants'
import {
  buildPayload,
  buildValidatePayload,
  type PlanFormModel,
  type ValidateResult,
} from './Types'
import { Plus, RefreshCw, Play, Edit2, Trash2, Loader2 } from 'lucide-react'

const KIND_TONE_MAP: Record<string, BadgeTone> = {
  filesystem: 'default',
  postgresql: 'secondary',
  mysql: 'secondary',
  mongodb: 'secondary',
  sqlite: 'outline',
}

function blankForm(): PlanFormModel {
  return {
    name: '',
    agent_id: '',
    kind: 'filesystem',
    schedule: '',
    timezone: 'UTC',
    enabled: true,
    source: defaultSource('filesystem'),
    repository_id: '',
    retention: { keep_last: 7, keep_daily: 7, keep_weekly: 4, keep_monthly: 3 },
    timeout_seconds: 3600,
  }
}

export const PlansView: React.FC = () => {
  const { t } = useTranslation()
  const navigate = useNavigate()

  const [plans, setPlans] = useState<Plan[]>([])
  const [agents, setAgents] = useState<Agent[]>([])
  const [repositories, setRepositories] = useState<Repository[]>([])

  const [filterAgentId, setFilterAgentId] = useState<string>('all')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const [dialogOpen, setDialogOpen] = useState(false)
  const [editing, setEditing] = useState(false)
  const [saving, setSaving] = useState(false)
  const [formModel, setFormModel] = useState<PlanFormModel>(blankForm())

  const [runningId, setRunningId] = useState<string | null>(null)
  const [toggleLoading, setToggleLoading] = useState<Record<string, boolean>>({})

  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false)
  const [planToDelete, setPlanToDelete] = useState<Plan | null>(null)

  const loadPlans = async (agentId?: string) => {
    setLoading(true)
    setError(null)
    try {
      const selectedAgent = agentId === 'all' || !agentId ? undefined : agentId
      const data = await apiGet<Plan[]>('/plans', { agent_id: selectedAgent })
      setPlans(data)
    } catch (err: unknown) {
      const apiErr = err as ApiError
      setError(apiErr?.message || t('plans.loadFailed') || 'Failed to load plans')
    } finally {
      setLoading(false)
    }
  }

  const loadMeta = async () => {
    try {
      const [agentList, repoList] = await Promise.all([
        apiGet<Agent[]>('/agents'),
        apiGet<Repository[]>('/repositories'),
      ])
      setAgents(agentList)
      setRepositories(repoList)
    } catch {
      // Non-blocking
    }
  }

  useEffect(() => {
    loadPlans()
    loadMeta()
  }, [])

  const handleAgentFilterChange = (agentId: string) => {
    setFilterAgentId(agentId)
    loadPlans(agentId)
  }

  const repositoryMap = useMemo(() => {
    const map = new Map<string, Repository>()
    for (const r of repositories) {
      map.set(r.id, r)
    }
    return map
  }, [repositories])

  const openCreate = () => {
    setFormModel(blankForm())
    setEditing(false)
    setDialogOpen(true)
  }

  const startEdit = (plan: Plan) => {
    setFormModel({
      id: plan.id,
      name: plan.name,
      agent_id: plan.agent_id,
      kind: plan.kind,
      schedule: plan.schedule,
      timezone: plan.timezone,
      enabled: plan.enabled,
      source: { ...plan.source, password: '' },
      repository_id: plan.repository_id,
      retention: { ...plan.retention },
      timeout_seconds: plan.timeout_seconds,
    })
    setEditing(true)
    setDialogOpen(true)
  }

  const toggleEnabled = async (plan: Plan) => {
    const nextEnabled = !plan.enabled
    setToggleLoading((prev) => ({ ...prev, [plan.id]: true }))
    try {
      await apiPut(`/plans/${plan.id}`, {
        ...buildPayload({
          ...plan,
          source: { ...plan.source },
          retention: { ...plan.retention },
        }),
        enabled: nextEnabled,
      })
      setPlans((prev) =>
        prev.map((p) => (p.id === plan.id ? { ...p, enabled: nextEnabled } : p))
      )
      toastSuccess(t('plans.statusUpdated') || 'Plan status updated')
    } catch (err: unknown) {
      const apiErr = err as ApiError
      toastError(apiErr?.message || t('plans.statusUpdateFailed') || 'Failed to update plan status')
    } finally {
      setToggleLoading((prev) => ({ ...prev, [plan.id]: false }))
    }
  }

  const runPlan = async (plan: Plan) => {
    setRunningId(plan.id)
    try {
      const run = await apiPost<Run>(`/plans/${plan.id}/run`, {})
      toastSuccess(t('plans.runDispatched') || 'Backup run dispatched')
      navigate(`/runs/${run.id}`)
    } catch (err: unknown) {
      const apiErr = err as ApiError
      toastError(apiErr?.message || t('plans.runFailed') || 'Failed to dispatch backup run')
      setRunningId(null)
    }
  }

  const openDeleteDialog = (plan: Plan) => {
    setPlanToDelete(plan)
    setDeleteDialogOpen(true)
  }

  const handleDeleteConfirm = async () => {
    if (!planToDelete) return
    try {
      await apiDelete(`/plans/${planToDelete.id}`)
      toastSuccess(t('plans.deleted') || 'Plan deleted successfully')
      await loadPlans(filterAgentId)
    } catch (err: unknown) {
      const apiErr = err as ApiError
      if (apiErr?.code === 'plan_has_snapshots' || apiErr?.code === 'conflict') {
        toastWarning(
          t('plans.deleteDialog.snapshotsRequired') ||
            'Cannot delete plan with existing snapshots. Purge snapshots first.'
        )
        return
      }
      toastError(apiErr?.message || t('plans.deleteFailed') || 'Failed to delete plan')
    }
  }

  const handleSavePlan = async (model: PlanFormModel) => {
    setSaving(true)
    try {
      const result = await apiPost<ValidateResult>(
        '/plans/validate',
        buildValidatePayload(model)
      )
      if (!result.ok) {
        const code = result.code || 'validation_failed'
        const msg = result.message || t('plans.validationFailed') || 'Validation failed'
        toastError(`${code}: ${msg}`)
        return
      }

      if (editing && model.id) {
        await apiPut(`/plans/${model.id}`, buildPayload(model))
        toastSuccess(t('plans.updated') || 'Plan updated successfully')
      } else {
        await apiPost('/plans', buildPayload(model))
        toastSuccess(t('plans.created') || 'Plan created successfully')
      }
      setDialogOpen(false)
      await loadPlans(filterAgentId)
    } catch (err: unknown) {
      const apiErr = err as ApiError
      toastError(apiErr?.message || t('plans.saveFailed') || 'Failed to save plan')
    } finally {
      setSaving(false)
    }
  }

  const sourcePaths = (p: Plan): string => {
    if (p.kind === 'filesystem') return p.source.paths?.join(', ') || '—'
    if (p.kind === 'sqlite') return p.source.path || '—'
    return (
      [p.source.host, p.source.port, p.source.database]
        .filter((val) => val !== undefined && val !== '')
        .join(':') || '—'
    )
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
        <div>
          <h2 className="text-xl font-bold tracking-tight text-foreground">
            {t('nav.plans')}
          </h2>
          <p className="text-xs text-muted-foreground">
            {t('plans.subtitle') || 'Scheduled and manual backup execution policies'}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button size="sm" onClick={openCreate} className="h-8 text-xs gap-1.5 bg-primary text-primary-foreground">
            <Plus className="h-3.5 w-3.5" />
            {t('plans.newPlan')}
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={() => loadPlans(filterAgentId)}
            className="h-8 text-xs gap-1.5"
          >
            <RefreshCw className="h-3.5 w-3.5" />
            {t('common.refresh')}
          </Button>
        </div>
      </div>

      <div className="flex items-center gap-3">
        <Select value={filterAgentId} onValueChange={handleAgentFilterChange}>
          <SelectTrigger className="w-56 h-8 text-xs">
            <SelectValue placeholder={t('plans.filterByAgent')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all" className="text-xs">
              {t('common.allAgents') || 'All Agents'}
            </SelectItem>
            {agents.map((a) => (
              <SelectItem key={a.id} value={a.id} className="text-xs">
                {a.name} ({translateEnum('status', a.status)})
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {error ? (
        <AppErrorState
          title={t('nav.plans')}
          message={error}
          onRetry={() => loadPlans(filterAgentId)}
        />
      ) : (
        <Card className="border-border bg-card/60 shadow-sm">
          <CardContent className="p-0">
            {loading ? (
              <div className="flex h-48 items-center justify-center">
                <Loader2 className="h-6 w-6 animate-spin text-primary" />
              </div>
            ) : plans.length > 0 ? (
              <div className="rounded-md overflow-hidden">
                <Table>
                  <TableHeader>
                    <TableRow className="border-border hover:bg-transparent">
                      <TableHead className="text-xs font-medium">{t('common.name')}</TableHead>
                      <TableHead className="text-xs font-medium">{t('plans.columns.kind')}</TableHead>
                      <TableHead className="text-xs font-medium">{t('plans.columns.schedule')}</TableHead>
                      <TableHead className="text-xs font-medium">{t('plans.columns.enabled')}</TableHead>
                      <TableHead className="text-xs font-medium">{t('plans.columns.repository')}</TableHead>
                      <TableHead className="text-xs font-medium">{t('plans.columns.path')}</TableHead>
                      <TableHead className="text-xs font-medium">{t('plans.columns.lastRunAt')}</TableHead>
                      <TableHead className="text-xs font-medium">{t('plans.columns.timeout')}</TableHead>
                      <TableHead className="text-xs font-medium text-right">{t('common.actions')}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {plans.map((plan) => {
                      const repo = repositoryMap.get(plan.repository_id)
                      const isRunning = runningId === plan.id
                      const isToggling = toggleLoading[plan.id] || false
                      return (
                        <TableRow key={plan.id} className="border-border hover:bg-muted/30">
                          <TableCell className="font-medium text-xs text-foreground">
                            {plan.name}
                          </TableCell>
                          <TableCell className="text-xs">
                            <StatusBadge tone={KIND_TONE_MAP[plan.kind] || 'default'}>
                              {t(KIND_LABELS[plan.kind])}
                            </StatusBadge>
                          </TableCell>
                          <TableCell className="text-xs">
                            <div className="flex flex-col font-mono text-[11px] leading-tight">
                              <span className="text-foreground">{plan.schedule}</span>
                              <span className="text-[10px] text-muted-foreground">{plan.timezone}</span>
                            </div>
                          </TableCell>
                          <TableCell className="text-xs">
                            <Switch
                              checked={plan.enabled}
                              disabled={isToggling}
                              onCheckedChange={() => toggleEnabled(plan)}
                            />
                          </TableCell>
                          <TableCell className="text-xs text-muted-foreground">
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <span className="cursor-help underline decoration-dotted underline-offset-2">
                                  {repo?.storage_target_name || plan.repository_id}
                                </span>
                              </TooltipTrigger>
                              <TooltipContent className="text-[11px] font-mono">
                                {repo?.repository_path || 'No path available'}
                              </TooltipContent>
                            </Tooltip>
                          </TableCell>
                          <TableCell className="text-xs text-muted-foreground max-w-xs truncate">
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <span className="truncate block font-mono text-[11px]">
                                  {sourcePaths(plan)}
                                </span>
                              </TooltipTrigger>
                              <TooltipContent className="text-[11px] max-w-sm break-all font-mono">
                                {sourcePaths(plan)}
                              </TooltipContent>
                            </Tooltip>
                          </TableCell>
                          <TableCell className="text-xs text-muted-foreground">
                            {plan.last_run_at ? formatDateTime(plan.last_run_at) : '—'}
                          </TableCell>
                          <TableCell className="text-xs text-muted-foreground font-mono">
                            {plan.timeout_seconds}s
                          </TableCell>
                          <TableCell className="text-xs text-right">
                            <div className="flex items-center justify-end gap-1">
                              <Button
                                variant="ghost"
                                size="sm"
                                disabled={isRunning}
                                className="h-7 text-xs text-primary gap-1"
                                onClick={() => runPlan(plan)}
                              >
                                {isRunning ? (
                                  <Loader2 className="h-3 w-3 animate-spin" />
                                ) : (
                                  <Play className="h-3 w-3" />
                                )}
                                {t('common.run')}
                              </Button>
                              <Button
                                variant="ghost"
                                size="sm"
                                className="h-7 text-xs text-muted-foreground hover:text-foreground gap-1"
                                onClick={() => startEdit(plan)}
                              >
                                <Edit2 className="h-3 w-3" />
                                {t('common.edit')}
                              </Button>
                              <Button
                                variant="ghost"
                                size="sm"
                                className="h-7 text-xs text-rose-400 hover:text-rose-300 gap-1"
                                onClick={() => openDeleteDialog(plan)}
                              >
                                <Trash2 className="h-3 w-3" />
                                {t('common.delete')}
                              </Button>
                            </div>
                          </TableCell>
                        </TableRow>
                      )
                    })}
                  </TableBody>
                </Table>
              </div>
            ) : (
              <div className="p-8">
                <AppEmptyState
                  title={t('plans.emptyPlans') || 'No Backup Plans'}
                  description={
                    t('plans.emptyPlans_desc') ||
                    'Create backup schedules with source directories, retention, and targets.'
                  }
                />
              </div>
            )}
          </CardContent>
        </Card>
      )}

      {/* Create / Edit Plan Dialog */}
      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="sm:max-w-2xl max-h-[90vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle className="text-sm font-semibold">
              {editing ? t('plans.editPlan') : t('plans.newPlan')}
            </DialogTitle>
            <DialogDescription className="text-xs">
              {t('plans.form.description') || 'Configure parameters, target agent, schedule, and retention rules.'}
            </DialogDescription>
          </DialogHeader>
          <div className="pt-2">
            <PlanForm
              model={formModel}
              onChange={setFormModel}
              agents={agents}
              repositories={repositories}
              submitting={saving}
              onSubmit={handleSavePlan}
              onCancel={() => setDialogOpen(false)}
            />
          </div>
        </DialogContent>
      </Dialog>

      {/* Delete Confirm Dialog */}
      <ConfirmActionDialog
        open={deleteDialogOpen}
        onOpenChange={setDeleteDialogOpen}
        title={t('plans.deleteDialog.title')}
        description={
          planToDelete
            ? t('plans.deleteDialog.confirm', { name: planToDelete.name })
            : ''
        }
        destructive
        onConfirm={handleDeleteConfirm}
      />
    </div>
  )
}
