import React, { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { Badge } from '@/components/ui/badge'
import { Alert, AlertTitle, AlertDescription } from '@/components/ui/alert'
import { Separator } from '@/components/ui/separator'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { TagInput } from './TagInput'
import {
  CRON5_RE,
  CRON_PRESETS,
  IANA_TIMEZONES,
  KIND_LABELS,
  defaultSource,
} from './Constants'
import { translateEnum } from '@/i18n'
import type { Agent, Repository } from '@/api/types'
import { isAbsolutePath, isWithinMappedRoot } from '@/utils/pathMapping'
import type { PlanFormModel, PlanKind } from './Types'
import { toastWarning } from '@/lib/toast'
import { FolderTree, Loader2, Save, X } from 'lucide-react'

interface PlanFormProps {
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

    if (!model.name.trim()) errs.name = t('plans.rules.nameRequired') || 'Plan name is required'
    if (!model.agent_id) errs.agent_id = t('plans.rules.agentRequired') || 'Agent is required'
    if (!model.kind) errs.kind = t('plans.rules.kindRequired') || 'Backup kind is required'
    if (!model.repository_id) errs.repository_id = t('plans.rules.repositoryRequired') || 'Repository is required'

    if (!model.schedule.trim()) {
      errs.schedule = t('plans.rules.scheduleRequired') || 'Schedule is required'
    } else if (!CRON5_RE.test(model.schedule.trim())) {
      errs.schedule = t('plans.rules.cronFields') || 'Invalid 5-field cron expression'
    }

    if (!model.timezone) errs.timezone = t('plans.rules.timezoneRequired') || 'Timezone is required'
    if (!model.timeout_seconds || model.timeout_seconds <= 0) {
      errs.timeout_seconds = t('plans.rules.timeoutRequired') || 'Timeout must be greater than 0'
    }

    // Source specific validation
    if (model.kind === 'filesystem') {
      const paths = model.source.paths ?? []
      if (paths.length === 0) {
        errs['source.paths'] = t('plans.rules.pathsRequired') || 'At least one backup path required'
      } else if (paths.some((p) => !isAbsolutePath(p))) {
        errs['source.paths'] = t('plans.rules.pathsAbsolute') || 'All backup paths must be absolute'
      } else if (paths.some((p) => !isWithinMappedRoot(p, sourcePathMappings))) {
        errs['source.paths'] = t('plans.rules.pathsOutsideAllowedRoots') || 'Path outside allowed host roots'
      }

      const excludes = model.source.excludes ?? []
      if (
        excludes.some(
          (ex) => isAbsolutePath(ex) && !isWithinMappedRoot(ex, sourcePathMappings)
        )
      ) {
        errs['source.excludes'] = t('plans.rules.excludesOutsideAllowedRoots') || 'Exclude outside allowed host roots'
      }
    } else if (model.kind === 'sqlite') {
      const p = model.source.path?.trim() ?? ''
      if (!p) {
        errs['source.path'] = t('plans.rules.pathRequired') || 'Database path is required'
      } else if (!isAbsolutePath(p)) {
        errs['source.path'] = t('plans.rules.absolutePath') || 'Database path must be absolute'
      } else if (!isWithinMappedRoot(p, sourcePathMappings)) {
        errs['source.path'] = t('plans.rules.pathOutsideAllowedRoots') || 'Path outside allowed host roots'
      }
    } else {
      // Database types
      if (!model.source.host?.trim()) errs['source.host'] = t('plans.rules.hostRequired') || 'Host is required'
      const port = model.source.port
      if (port == null || port < 1 || port > 65535) {
        errs['source.port'] = t('plans.rules.portRange') || 'Port must be between 1 and 65535'
      }
      if (!model.source.username?.trim()) errs['source.username'] = t('plans.rules.usernameRequired') || 'Username is required'
      if (!model.source.database?.trim()) errs['source.database'] = t('plans.rules.databaseRequired') || 'Database is required'
    }

    setErrors(errs)
    return Object.keys(errs).length === 0
  }

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    if (!validate()) return

    const r = model.retention
    if (r.keep_last <= 0 && r.keep_daily <= 0 && r.keep_weekly <= 0 && r.keep_monthly <= 0) {
      toastWarning(t('plans.form.retentionWarning') || 'At least one retention policy field must be set')
      return
    }

    onSubmit(model)
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-6">
      <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
        {/* Name */}
        <div className="space-y-1.5">
          <Label htmlFor="plan-name" className="text-xs">
            {t('plans.form.name')} *
          </Label>
          <Input
            id="plan-name"
            value={model.name}
            onChange={(e) => updateModel({ name: e.target.value })}
            placeholder={t('plans.form.namePlaceholder') || 'e.g. Daily PostgreSQL Backup'}
            disabled={submitting}
            aria-describedby={errors.name ? 'plan-name-error' : undefined}
            className="h-9 text-xs"
          />
          {errors.name && (
            <p id="plan-name-error" className="text-[11px] text-destructive">
              {errors.name}
            </p>
          )}
        </div>

        {/* Kind */}
        <div className="space-y-1.5">
          <Label className="text-xs">{t('plans.form.kind')} *</Label>
          <Select
            value={model.kind}
            onValueChange={(val) => handleKindChange(val as PlanKind)}
            disabled={submitting}
          >
            <SelectTrigger className="h-9 text-xs">
              <SelectValue placeholder={t('plans.form.kindPlaceholder')} />
            </SelectTrigger>
            <SelectContent>
              {planKinds.map((k) => (
                <SelectItem key={k.value} value={k.value} className="text-xs">
                  {k.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {errors.kind && <p className="text-[11px] text-destructive">{errors.kind}</p>}
        </div>

        {/* Agent */}
        <div className="space-y-1.5">
          <Label className="text-xs">{t('plans.form.agent')} *</Label>
          <Select
            value={model.agent_id}
            onValueChange={handleAgentChange}
            disabled={submitting}
          >
            <SelectTrigger className="h-9 text-xs">
              <SelectValue placeholder={t('plans.form.agent')} />
            </SelectTrigger>
            <SelectContent>
              {agents.map((a) => (
                <SelectItem key={a.id} value={a.id} className="text-xs">
                  <span>{a.name}</span>
                  <span className="ml-2 text-[10px] text-muted-foreground">
                    ({translateEnum('status', a.status)})
                  </span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {errors.agent_id && <p className="text-[11px] text-destructive">{errors.agent_id}</p>}
        </div>

        {/* Repository */}
        <div className="space-y-1.5">
          <Label className="text-xs">{t('plans.form.repository')} *</Label>
          <Select
            value={model.repository_id}
            onValueChange={(val) => updateModel({ repository_id: val })}
            disabled={submitting || filteredRepos.length === 0}
          >
            <SelectTrigger className="h-9 text-xs">
              <SelectValue placeholder={t('plans.form.repository')} />
            </SelectTrigger>
            <SelectContent>
              {filteredRepos.map((r) => (
                <SelectItem key={r.id} value={r.id} className="text-xs">
                  {r.storage_target_name} @ {r.agent_name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {errors.repository_id && (
            <p className="text-[11px] text-destructive">{errors.repository_id}</p>
          )}
        </div>

        {/* Schedule */}
        <div className="space-y-1.5">
          <Label htmlFor="plan-schedule" className="text-xs">
            {t('plans.form.schedule')} *
          </Label>
          <Input
            id="plan-schedule"
            value={model.schedule}
            onChange={(e) => updateModel({ schedule: e.target.value })}
            placeholder={t('plans.form.schedulePlaceholder') || '0 2 * * *'}
            disabled={submitting}
            aria-describedby={errors.schedule ? 'plan-schedule-error' : undefined}
            className="h-9 text-xs font-mono"
          />
          <div className="flex flex-wrap items-center gap-1.5 pt-1">
            <span className="text-[10px] text-muted-foreground">
              {t('plans.form.presets')}:
            </span>
            {cronPresets.map((p) => (
              <Badge
                key={p.value}
                variant="outline"
                onClick={() => updateModel({ schedule: p.value })}
                className="cursor-pointer text-[10px] px-1.5 py-0 hover:bg-primary/20 hover:text-primary transition-colors"
              >
                {p.label}
              </Badge>
            ))}
          </div>
          {errors.schedule && (
            <p id="plan-schedule-error" className="text-[11px] text-destructive">
              {errors.schedule}
            </p>
          )}
        </div>

        {/* Timezone */}
        <div className="space-y-1.5">
          <Label className="text-xs">{t('plans.form.timezone')} *</Label>
          <Select
            value={model.timezone}
            onValueChange={(val) => updateModel({ timezone: val })}
            disabled={submitting}
          >
            <SelectTrigger className="h-9 text-xs">
              <SelectValue placeholder={t('plans.form.timezonePlaceholder')} />
            </SelectTrigger>
            <SelectContent className="max-h-60">
              {IANA_TIMEZONES.map((tz) => (
                <SelectItem key={tz} value={tz} className="text-xs font-mono">
                  {tz}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {errors.timezone && <p className="text-[11px] text-destructive">{errors.timezone}</p>}
        </div>

        {/* Timeout Seconds */}
        <div className="space-y-1.5">
          <Label htmlFor="plan-timeout" className="text-xs">
            {t('plans.form.timeoutSeconds')} *
          </Label>
          <Input
            id="plan-timeout"
            type="number"
            min={1}
            step={60}
            value={model.timeout_seconds}
            onChange={(e) => updateModel({ timeout_seconds: Number(e.target.value) || 3600 })}
            disabled={submitting}
            aria-describedby={errors.timeout_seconds ? 'plan-timeout-error' : undefined}
            className="h-9 text-xs"
          />
          {errors.timeout_seconds && (
            <p id="plan-timeout-error" className="text-[11px] text-destructive">
              {errors.timeout_seconds}
            </p>
          )}
        </div>
      </div>

      {/* Retention Policy */}
      <div className="space-y-2 rounded-md border border-border bg-card/40 p-3">
        <Label className="text-xs font-semibold">{t('plans.form.retention')}</Label>
        <p className="text-[11px] text-muted-foreground">{t('plans.form.retentionHint')}</p>
        <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 pt-1">
          <div className="space-y-1">
            <span className="text-[10px] text-muted-foreground">keep_last</span>
            <Input
              type="number"
              min={0}
              value={model.retention.keep_last}
              onChange={(e) => updateRetention({ keep_last: Number(e.target.value) || 0 })}
              className="h-8 text-xs font-mono"
            />
          </div>
          <div className="space-y-1">
            <span className="text-[10px] text-muted-foreground">keep_daily</span>
            <Input
              type="number"
              min={0}
              value={model.retention.keep_daily}
              onChange={(e) => updateRetention({ keep_daily: Number(e.target.value) || 0 })}
              className="h-8 text-xs font-mono"
            />
          </div>
          <div className="space-y-1">
            <span className="text-[10px] text-muted-foreground">keep_weekly</span>
            <Input
              type="number"
              min={0}
              value={model.retention.keep_weekly}
              onChange={(e) => updateRetention({ keep_weekly: Number(e.target.value) || 0 })}
              className="h-8 text-xs font-mono"
            />
          </div>
          <div className="space-y-1">
            <span className="text-[10px] text-muted-foreground">keep_monthly</span>
            <Input
              type="number"
              min={0}
              value={model.retention.keep_monthly}
              onChange={(e) => updateRetention({ keep_monthly: Number(e.target.value) || 0 })}
              className="h-8 text-xs font-mono"
            />
          </div>
        </div>
      </div>

      <Separator />

      {/* Source Details */}
      <div className="space-y-4">
        <div className="flex items-center gap-2">
          <FolderTree className="h-4 w-4 text-primary" />
          <h3 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
            {t('plans.form.source')}
          </h3>
        </div>

        {isPathBasedKind && selectedAgent && sourcePathMappings.length > 0 && (
          <Alert className="border-border/80 bg-muted/20 py-2.5 text-xs text-muted-foreground">
            <AlertTitle className="text-xs font-medium text-foreground">
              {t('plans.form.availableHostPaths')}
            </AlertTitle>
            <AlertDescription className="flex flex-wrap gap-1.5 mt-1">
              {sourcePathMappings.map((m) => (
                <Badge key={m.host_path} variant="secondary" className="font-mono text-[10px]">
                  {m.host_path}
                </Badge>
              ))}
            </AlertDescription>
          </Alert>
        )}

        {/* Filesystem */}
        {model.kind === 'filesystem' && (
          <div className="space-y-4">
            <div className="space-y-1.5">
              <Label className="text-xs">{t('plans.form.paths')} *</Label>
              <TagInput
                modelValue={model.source.paths ?? []}
                onChange={(val) => updateSource({ paths: val })}
                placeholder={t('plans.form.pathsPlaceholder')}
                disabled={submitting}
              />
              {errors['source.paths'] && (
                <p className="text-[11px] text-destructive">{errors['source.paths']}</p>
              )}
            </div>

            <div className="space-y-1.5">
              <Label className="text-xs">{t('plans.form.excludes')}</Label>
              <TagInput
                modelValue={model.source.excludes ?? []}
                onChange={(val) => updateSource({ excludes: val })}
                placeholder={t('plans.form.excludesPlaceholder')}
                disabled={submitting}
              />
              {errors['source.excludes'] && (
                <p className="text-[11px] text-destructive">{errors['source.excludes']}</p>
              )}
            </div>

            <div className="flex items-center justify-between rounded-md border border-border p-3">
              <div className="space-y-0.5">
                <Label className="text-xs">{t('plans.form.oneFileSystem')}</Label>
                <p className="text-[11px] text-muted-foreground">
                  {t('plans.form.oneFileSystemHint') || 'Do not cross filesystem boundaries'}
                </p>
              </div>
              <Switch
                checked={model.source.one_file_system === true}
                onCheckedChange={(checked) => updateSource({ one_file_system: checked })}
                disabled={submitting}
              />
            </div>
          </div>
        )}

        {/* SQLite */}
        {model.kind === 'sqlite' && (
          <div className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="sqlite-path" className="text-xs">
                {t('plans.form.databasePath')} *
              </Label>
              <Input
                id="sqlite-path"
                value={model.source.path ?? ''}
                onChange={(e) => updateSource({ path: e.target.value })}
                placeholder={t('plans.form.databasePathPlaceholder')}
                disabled={submitting}
                className="h-9 text-xs font-mono"
              />
              {errors['source.path'] && (
                <p className="text-[11px] text-destructive">{errors['source.path']}</p>
              )}
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="sqlite-dump" className="text-xs">
                {t('plans.form.estimatedDumpBytes')}
              </Label>
              <Input
                id="sqlite-dump"
                type="number"
                min={1}
                step={1073741824}
                value={model.source.estimated_dump_bytes ?? ''}
                onChange={(e) =>
                  updateSource({
                    estimated_dump_bytes: e.target.value ? Number(e.target.value) : undefined,
                  })
                }
                disabled={submitting}
                className="h-9 text-xs"
              />
              <p className="text-[10px] text-muted-foreground">{t('plans.form.dumpBytesHint')}</p>
            </div>
          </div>
        )}

        {/* DB Types (PostgreSQL / MySQL / MongoDB) */}
        {model.kind !== 'filesystem' && model.kind !== 'sqlite' && (
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div className="space-y-1.5">
              <Label htmlFor="db-host" className="text-xs">
                {t('plans.form.host')} *
              </Label>
              <Input
                id="db-host"
                value={model.source.host ?? ''}
                onChange={(e) => updateSource({ host: e.target.value })}
                placeholder={t('plans.form.hostPlaceholder') || '127.0.0.1'}
                disabled={submitting}
                className="h-9 text-xs"
              />
              {errors['source.host'] && (
                <p className="text-[11px] text-destructive">{errors['source.host']}</p>
              )}
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="db-port" className="text-xs">
                {t('plans.form.port')} *
              </Label>
              <Input
                id="db-port"
                type="number"
                min={1}
                max={65535}
                value={model.source.port ?? ''}
                onChange={(e) =>
                  updateSource({ port: e.target.value ? Number(e.target.value) : undefined })
                }
                disabled={submitting}
                className="h-9 text-xs font-mono"
              />
              {errors['source.port'] && (
                <p className="text-[11px] text-destructive">{errors['source.port']}</p>
              )}
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="db-user" className="text-xs">
                {t('plans.form.username')} *
              </Label>
              <Input
                id="db-user"
                value={model.source.username ?? ''}
                onChange={(e) => updateSource({ username: e.target.value })}
                disabled={submitting}
                className="h-9 text-xs"
              />
              {errors['source.username'] && (
                <p className="text-[11px] text-destructive">{errors['source.username']}</p>
              )}
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="db-pass" className="text-xs">
                {t('plans.form.password')}
              </Label>
              <Input
                id="db-pass"
                type="password"
                autoComplete="new-password"
                value={model.source.password ?? ''}
                onChange={(e) => updateSource({ password: e.target.value })}
                placeholder={t('plans.form.passwordPlaceholder') || 'Leave empty to keep unchanged'}
                disabled={submitting}
                className="h-9 text-xs"
              />
            </div>

            <div className="space-y-1.5 sm:col-span-2">
              <Label htmlFor="db-name" className="text-xs">
                {t('plans.form.database')} *
              </Label>
              <Input
                id="db-name"
                value={model.source.database ?? ''}
                onChange={(e) => updateSource({ database: e.target.value })}
                placeholder={t('plans.form.databasePlaceholder')}
                disabled={submitting}
                className="h-9 text-xs"
              />
              {errors['source.database'] && (
                <p className="text-[11px] text-destructive">{errors['source.database']}</p>
              )}
            </div>

            <div className="space-y-1.5 sm:col-span-2">
              <Label className="text-xs">{t('plans.form.extraArgs')}</Label>
              <TagInput
                modelValue={model.source.extra_args ?? []}
                onChange={(val) => updateSource({ extra_args: val })}
                placeholder={t('plans.form.extraArgsPlaceholder')}
                disabled={submitting}
              />
            </div>

            {model.kind === 'mongodb' && (
              <div className="flex items-center justify-between rounded-md border border-border p-3 sm:col-span-2">
                <div className="space-y-0.5">
                  <Label className="text-xs">{t('plans.form.captureOplog')}</Label>
                  <p className="text-[11px] text-muted-foreground">
                    {t('plans.form.captureOplogHint') || 'Include point-in-time oplog dump'}
                  </p>
                </div>
                <Switch
                  checked={model.source.capture_oplog === true}
                  onCheckedChange={(checked) => updateSource({ capture_oplog: checked })}
                  disabled={submitting}
                />
              </div>
            )}
          </div>
        )}
      </div>

      <div className="flex items-center justify-end gap-2 pt-4">
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={onCancel}
          disabled={submitting}
          className="h-8 text-xs gap-1.5"
        >
          <X className="h-3.5 w-3.5" />
          {t('common.cancel')}
        </Button>
        <Button type="submit" size="sm" disabled={submitting} className="h-8 text-xs gap-1.5">
          {submitting ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
          ) : (
            <Save className="h-3.5 w-3.5" />
          )}
          {t('common.save')}
        </Button>
      </div>
    </form>
  )
}
