import React from 'react'
import { useTranslation } from 'react-i18next'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Badge } from '@/components/ui/badge'
import { Alert, AlertTitle, AlertDescription } from '@/components/ui/alert'
import { TagInput } from './TagInput'
import type { PathMapping } from '@/api/types'
import type { PlanFormSource, PlanKind } from './Types'
import { FolderTree } from 'lucide-react'

export interface PlanSourceSectionProps {
  kind: PlanKind
  source: PlanFormSource
  errors: Record<string, string>
  submitting: boolean
  isPathBasedKind: boolean
  hasSelectedAgent: boolean
  sourcePathMappings: PathMapping[]
  onUpdateSource: (partial: Partial<PlanFormSource>) => void
}

export const PlanSourceSection: React.FC<PlanSourceSectionProps> = ({
  kind,
  source,
  errors,
  submitting,
  isPathBasedKind,
  hasSelectedAgent,
  sourcePathMappings,
  onUpdateSource,
}) => {
  const { t } = useTranslation()

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2">
        <FolderTree className="h-4 w-4 text-primary" aria-hidden="true" />
        <h3 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
          {t('plans.form.source')}
        </h3>
      </div>

      {isPathBasedKind && hasSelectedAgent && sourcePathMappings.length > 0 && (
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
      {kind === 'filesystem' && (
        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label className="text-xs">{t('plans.form.paths')} *</Label>
            <TagInput
              modelValue={source.paths ?? []}
              onChange={(val) => onUpdateSource({ paths: val })}
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
              modelValue={source.excludes ?? []}
              onChange={(val) => onUpdateSource({ excludes: val })}
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
                {t('plans.form.oneFileSystemHint')}
              </p>
            </div>
            <Switch
              checked={source.one_file_system === true}
              onCheckedChange={(checked) => onUpdateSource({ one_file_system: checked })}
              disabled={submitting}
            />
          </div>
        </div>
      )}

      {/* SQLite */}
      {kind === 'sqlite' && (
        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="sqlite-path" className="text-xs">
              {t('plans.form.databasePath')} *
            </Label>
            <Input
              id="sqlite-path"
              value={source.path ?? ''}
              onChange={(e) => onUpdateSource({ path: e.target.value })}
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
              value={source.estimated_dump_bytes ?? ''}
              onChange={(e) =>
                onUpdateSource({
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
      {kind !== 'filesystem' && kind !== 'sqlite' && (
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <div className="space-y-1.5">
            <Label htmlFor="db-host" className="text-xs">
              {t('plans.form.host')} *
            </Label>
            <Input
              id="db-host"
              value={source.host ?? ''}
              onChange={(e) => onUpdateSource({ host: e.target.value })}
              placeholder={t('plans.form.hostPlaceholder')}
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
              value={source.port ?? ''}
              onChange={(e) =>
                onUpdateSource({ port: e.target.value ? Number(e.target.value) : undefined })
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
              value={source.username ?? ''}
              onChange={(e) => onUpdateSource({ username: e.target.value })}
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
              value={source.password ?? ''}
              onChange={(e) => onUpdateSource({ password: e.target.value })}
              placeholder={t('plans.form.passwordPlaceholder')}
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
              value={source.database ?? ''}
              onChange={(e) => onUpdateSource({ database: e.target.value })}
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
              modelValue={source.extra_args ?? []}
              onChange={(val) => onUpdateSource({ extra_args: val })}
              placeholder={t('plans.form.extraArgsPlaceholder')}
              disabled={submitting}
            />
          </div>

          {kind === 'mongodb' && (
            <div className="flex items-center justify-between rounded-md border border-border p-3 sm:col-span-2">
              <div className="space-y-0.5">
                <Label className="text-xs">{t('plans.form.captureOplog')}</Label>
                <p className="text-[11px] text-muted-foreground">
                  {t('plans.form.captureOplogHint')}
                </p>
              </div>
              <Switch
                checked={source.capture_oplog === true}
                onCheckedChange={(checked) => onUpdateSource({ capture_oplog: checked })}
                disabled={submitting}
              />
            </div>
          )}
        </div>
      )}
    </div>
  )
}
