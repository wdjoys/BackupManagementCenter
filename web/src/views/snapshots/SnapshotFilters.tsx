import React from 'react'
import { useTranslation } from 'react-i18next'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { RefreshCw, Loader2, AlertCircle } from 'lucide-react'
import { StatusBadge } from '@/components/StatusBadge'
import { formatDateTime } from '@/i18n'
import type { Repository, Plan } from '@/api/types'
import {
  ALL_PLANS_FILTER,
  DELETED_PLANS_FILTER,
  UNASSIGNED_PLAN_FILTER,
} from './Types'

export interface SnapshotFiltersProps {
  repos: Repository[]
  selectedRepoId: string
  onSelectRepo: (repoId: string) => void
  reposLoading: boolean
  planFilter: string
  onSelectPlanFilter: (planFilter: string) => void
  repositoryPlans: Plan[]
  snapshotsCache: string | null
  snapshotsVerifiedAt: string | null
  snapshotsCount: number
  snapshotsLoading: boolean
  snapshotsVerifying?: boolean
  snapshotsError?: string | null
  onRefresh: () => void
  onRetry?: () => void
}

export const SnapshotFilters: React.FC<SnapshotFiltersProps> = ({
  repos,
  selectedRepoId,
  onSelectRepo,
  reposLoading,
  planFilter,
  onSelectPlanFilter,
  repositoryPlans,
  snapshotsCache,
  snapshotsVerifiedAt,
  snapshotsCount,
  snapshotsLoading,
  snapshotsVerifying,
  snapshotsError,
  onRefresh,
  onRetry,
}) => {
  const { t } = useTranslation()

  return (
    <Card className="border-border bg-card/40 shadow-sm">
      <CardContent className="p-4 flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap items-center gap-3">
          {/* Repository Selector */}
          <Select value={selectedRepoId} onValueChange={onSelectRepo} disabled={reposLoading}>
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
            <Select value={planFilter} onValueChange={onSelectPlanFilter}>
              <SelectTrigger className="w-56 h-8 text-xs">
                <SelectValue placeholder={t('snapshots.planFilter.label')} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL_PLANS_FILTER} className="text-xs">
                  {t('snapshots.planFilter.all')}
                </SelectItem>
                {repositoryPlans.map((p) => (
                  <SelectItem key={p.id} value={p.id} className="text-xs">
                    {p.name}
                  </SelectItem>
                ))}
                <SelectItem value={DELETED_PLANS_FILTER} className="text-xs">
                  {t('snapshots.planFilter.deleted')}
                </SelectItem>
                <SelectItem value={UNASSIGNED_PLAN_FILTER} className="text-xs">
                  {t('snapshots.planFilter.unassigned')}
                </SelectItem>
              </SelectContent>
            </Select>
          )}

          {snapshotsVerifying ? (
            <div className="flex items-center gap-1.5 text-xs text-muted-foreground font-mono">
              <Loader2 className="h-3 w-3 animate-spin text-amber-500" aria-hidden="true" />
              <span>{t('snapshots.cache.verifying')}</span>
            </div>
          ) : snapshotsCache === 'STALE' ? (
            <div className="flex items-center gap-1.5 text-xs font-mono">
              <StatusBadge tone="warning">{t('snapshots.cache.stale')}</StatusBadge>
              {snapshotsVerifiedAt && (
                <span className="text-[11px] text-muted-foreground">
                  {t('snapshots.cache.verifiedAt', { time: formatDateTime(snapshotsVerifiedAt) })}
                </span>
              )}
            </div>
          ) : snapshotsCache === 'HIT' ? (
            <div className="flex items-center gap-1.5 text-xs font-mono">
              <StatusBadge tone="success">{t('snapshots.cache.verified')}</StatusBadge>
              {snapshotsVerifiedAt && (
                <span className="text-[11px] text-muted-foreground">
                  {t('snapshots.cache.verifiedAt', { time: formatDateTime(snapshotsVerifiedAt) })}
                </span>
              )}
            </div>
          ) : null}

          {snapshotsError && (
            <div className="flex items-center gap-2 text-xs text-rose-500 font-mono">
              <AlertCircle className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
              <span className="truncate max-w-[200px]" title={snapshotsError}>{snapshotsError}</span>
              {onRetry && (
                <Button variant="ghost" size="sm" onClick={onRetry} className="h-6 px-1.5 text-xs text-rose-500 underline">
                  {t('snapshots.cache.retryLoad')}
                </Button>
              )}
            </div>
          )}
        </div>

        {selectedRepoId && (
          <div className="flex items-center gap-2">
            <span className="text-xs text-muted-foreground font-mono">
              {snapshotsCount} {t('snapshots.count')}
            </span>
            <Button
              variant="outline"
              size="sm"
              onClick={onRefresh}
              disabled={snapshotsLoading || snapshotsVerifying}
              className="h-8 text-xs gap-1.5"
            >
              <RefreshCw className={`h-3.5 w-3.5 ${snapshotsVerifying ? 'animate-spin' : ''}`} aria-hidden="true" />
              {t('common.refresh')}
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  )
}
