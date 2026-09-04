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
import { RefreshCw } from 'lucide-react'
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
  onRefresh: () => void
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
  onRefresh,
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
              {snapshotsCount} {t('snapshots.count')}
            </span>
            <Button
              variant="outline"
              size="sm"
              onClick={onRefresh}
              disabled={snapshotsLoading}
              className="h-8 text-xs gap-1.5"
            >
              <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
              {t('common.refresh')}
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  )
}
