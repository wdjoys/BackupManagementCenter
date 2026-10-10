import React, { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'
import {
  CRON5_RE,
  CRON_PRESETS,
  KIND_LABELS,
  defaultSource,
} from './Constants'
import type { Agent, Repository } from '@/api/types'
import { isAbsolutePath, isWithinMappedRoot } from '@/utils/pathMapping'
import type { PlanFormModel, PlanKind } from './Types'
import { toastWarning } from '@/lib/toast'
import { Loader2, Save, X } from 'lucide-react'
import { PlanBasicsSection } from './PlanBasicsSection'
import { PlanScheduleSection } from './PlanScheduleSection'
import { PlanRetentionSection } from './PlanRetentionSection'
import { PlanSourceSection } from './PlanSourceSection'
import { BYTES_PER_GIB } from './DumpSizeInput'

export interface PlanFormProps {
  model: PlanFormModel
  onChange: (model: PlanFormModel) => void
  agents: Agent[]
  repositories: Repository[]
  submitting: boolean
  onSubmit: (model: PlanFormModel) => void
  onCancel: () => void
}

export const PlanForm: React.FC<PlanFormProps> = ({
  model,
  onChange,
  agents,
  repositories,
  submitting,
  onSubmit,
  onCancel,
}) => {
  const { t } = useTranslation()
  const [errors, setErrors] = useState<Record<string, string>>({})

  const selectedAgent = useMemo(() => agents.find((a) => a.id === model.agent_id), [agents, model.agent_id])
  const sourcePathMappings = useMemo(() => selectedAgent?.source_path_mappings ?? [], [selectedAgent])
  const isPathBasedKind = model.kind === 'filesystem' || model.kind === 'sqlite'

  const filteredRepos = useMemo(
    () => repositories.filter((r) => r.agent_id === model.agent_id),
    [repositories, model.agent_id]
  )

  const planKinds = useMemo(
    () =>
      Object.entries(KIND_LABELS).map(([value, key]) => ({
        value: value as PlanKind,
        label: t(key),
      })),
    [t]
  )

  const cronPresets = useMemo(
    () => CRON_PRESETS.map((p) => ({ value: p.value, label: t(p.key) })),
    [t]
  )

  const updateModel = (partial: Partial<PlanFormModel>) => {
    onChange({ ...model, ...partial })
  }

  const updateSource = (partial: Partial<PlanFormModel['source']>) => {
    onChange({ ...model, source: { ...model.source, ...partial } })
  }

  const updateRetention = (partial: Partial<PlanFormModel['retention']>) => {
    onChange({ ...model, retention: { ...model.retention, ...partial } })
  }

  const handleKindChange = (kind: PlanKind) => {
    onChange({
      ...model,
      kind,
      source: defaultSource(kind),
    })
  }

  const handleAgentChange = (agentId: string) => {
    const repo = filteredRepos.find((r) => r.id === model.repository_id)
    const newRepoId = repo && repo.agent_id === agentId ? model.repository_id : ''
    onChange({
      ...model,
      agent_id: agentId,
      repository_id: newRepoId,
    })
  }

  const validate = (): boolean => {
    const errs: Record<string, string> = {}

    if (!model.name.trim()) errs.name = t('plans.rules.nameRequired')
    if (!model.agent_id) errs.agent_id = t('plans.rules.agentRequired')
    if (!model.kind) errs.kind = t('plans.rules.kindRequired')
    if (!model.repository_id) errs.repository_id = t('plans.rules.repositoryRequired')

    if (!model.schedule.trim()) {
      errs.schedule = t('plans.rules.scheduleRequired')
    } else if (!CRON5_RE.test(model.schedule.trim())) {
      errs.schedule = t('plans.rules.cronFields')
    }

    if (!model.timezone) errs.timezone = t('plans.rules.timezoneRequired')
    const timeout = Number(model.timeout_seconds)
    if (!timeout) {
      errs.timeout_seconds = t('plans.rules.timeoutRequired')
    } else if (timeout < 60 || timeout > 21600) {
      errs.timeout_seconds = t('plans.rules.timeoutRange')
    }

    // Source specific validation
    if (model.kind === 'filesystem') {
      const paths = model.source.paths ?? []
      if (paths.length === 0) {
        errs['source.paths'] = t('plans.rules.pathsRequired')
      } else if (paths.some((p) => !isAbsolutePath(p))) {
        errs['source.paths'] = t('plans.rules.pathsAbsolute')
      } else if (paths.some((p) => !isWithinMappedRoot(p, sourcePathMappings))) {
        errs['source.paths'] = t('plans.rules.pathsOutsideAllowedRoots')
      }

      const excludes = model.source.excludes ?? []
      if (
        excludes.some(
          (ex) => isAbsolutePath(ex) && !isWithinMappedRoot(ex, sourcePathMappings)
        )
      ) {
        errs['source.excludes'] = t('plans.rules.excludesOutsideAllowedRoots')
      }
    } else if (model.kind === 'sqlite') {
      const p = model.source.path?.trim() ?? ''
      if (!p) {
        errs['source.path'] = t('plans.rules.pathRequired')
      } else if (!isAbsolutePath(p)) {
        errs['source.path'] = t('plans.rules.absolutePath')
      } else if (!isWithinMappedRoot(p, sourcePathMappings)) {
        errs['source.path'] = t('plans.rules.pathOutsideAllowedRoots')
      }
    } else {
      // Database types
      if (!model.source.host?.trim()) errs['source.host'] = t('plans.rules.hostRequired')
      const port = model.source.port
      if (port == null || port < 1 || port > 65535) {
        errs['source.port'] = t('plans.rules.portRange')
      }
      if (!model.source.username?.trim()) errs['source.username'] = t('plans.rules.usernameRequired')
      if (!model.source.database?.trim()) errs['source.database'] = t('plans.rules.databaseRequired')
      const dbEstimate = model.source.estimated_dump_bytes
      if (dbEstimate == null || dbEstimate <= 0) {
        errs['source.estimated_dump_bytes'] = t('plans.rules.dumpBytesPositive')
      }
      // mongodump 限制：--oplog 只支持整实例 dump，配单库时必然失败；后端同样拒绝，
      // 这里给出表单内提示而不是等服务端 422。
      if (
        model.kind === 'mongodb' &&
        model.source.capture_oplog === true &&
        (model.source.database ?? '').trim().toLowerCase() !== 'all'
      ) {
        errs['source.capture_oplog'] = t('plans.form.captureOplogRequiresAll')
      }
    }
    // 上限对 sqlite 同样生效（后端在 kind 分支之外检查）。
    const estimate = model.source.estimated_dump_bytes
    if (estimate != null && estimate > 100 * BYTES_PER_GIB) {
      errs['source.estimated_dump_bytes'] = t('plans.rules.dumpBytesTooLarge')
    }

    setErrors(errs)
    return Object.keys(errs).length === 0
  }

  const handleSubmit = (e: React.SyntheticEvent) => {
    e.preventDefault()
    if (!validate()) return

    const r = model.retention
    if (r.keep_last <= 0 && r.keep_daily <= 0 && r.keep_weekly <= 0 && r.keep_monthly <= 0) {
      toastWarning(t('plans.form.retentionWarning'))
      return
    }

    onSubmit(model)
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-6">
      <PlanBasicsSection
        model={model}
        errors={errors}
        agents={agents}
        filteredRepos={filteredRepos}
        planKinds={planKinds}
        submitting={submitting}
        onUpdateModel={updateModel}
        onKindChange={handleKindChange}
        onAgentChange={handleAgentChange}
      />

      <PlanScheduleSection
        schedule={model.schedule}
        timezone={model.timezone}
        timeoutSeconds={model.timeout_seconds}
        errors={errors}
        submitting={submitting}
        cronPresets={cronPresets}
        onUpdateModel={updateModel}
      />

      <PlanRetentionSection
        retention={model.retention}
        submitting={submitting}
        onUpdateRetention={updateRetention}
      />

      <Separator />

      <PlanSourceSection
        kind={model.kind}
        source={model.source}
        errors={errors}
        submitting={submitting}
        isPathBasedKind={isPathBasedKind}
        hasSelectedAgent={Boolean(selectedAgent)}
        sourcePathMappings={sourcePathMappings}
        onUpdateSource={updateSource}
      />

      <div className="flex items-center justify-end gap-2 pt-4">
        <Button
          type="button"
          variant="outline"
          onClick={onCancel}
          disabled={submitting}
          className="h-9 text-xs gap-1.5"
        >
          <X className="h-3.5 w-3.5" aria-hidden="true" />
          {t('common.cancel')}
        </Button>
        <Button
          type="submit"
          disabled={submitting}
          className="h-9 text-xs gap-1.5 bg-primary text-primary-foreground"
        >
          {submitting ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
          ) : (
            <Save className="h-3.5 w-3.5" aria-hidden="true" />
          )}
          {t('common.save')}
        </Button>
      </div>
    </form>
  )
}
