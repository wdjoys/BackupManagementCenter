import React from 'react'
import { useTranslation } from 'react-i18next'
import { Card, CardContent } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { StatusBadge } from '@/components/StatusBadge'
import { AppEmptyState } from '@/components/AppEmptyState'
import { PageLoadingState } from '@/components/PageLoadingState'
import { formatDateTime } from '@/i18n'
import type { Snapshot } from '@/api/types'
import type { SnapshotView } from './Types'
import { Camera, Folder, Trash2 } from 'lucide-react'

export interface SnapshotListProps {
  selectedRepoId: string
  snapshotsLoading: boolean
  totalSnapshots: number
  filteredSnapshots: SnapshotView[]
  onSelectSnapshot: (snapshot: Snapshot) => void
  onDeleteSnapshot: (e: React.MouseEvent, snapshot: Snapshot) => void
}

export const SnapshotList: React.FC<SnapshotListProps> = ({
  selectedRepoId,
  snapshotsLoading,
  totalSnapshots,
  filteredSnapshots,
  onSelectSnapshot,
  onDeleteSnapshot,
}) => {
  const { t } = useTranslation()

  if (!selectedRepoId) {
    return (
      <div className="p-12 border border-dashed border-border rounded-md text-center">
        <Camera className="mx-auto h-8 w-8 text-muted-foreground opacity-50 mb-2" aria-hidden="true" />
        <h3 className="text-sm font-semibold text-foreground">
          {t('snapshots.repositoryPlaceholder')}
        </h3>
        <p className="text-xs text-muted-foreground mt-1">
          {t('snapshots.selectSnapshotHint')}
        </p>
      </div>
    )
  }

  return (
    <Card className="border-border bg-card/60 shadow-sm">
      <CardContent className="p-0">
        {snapshotsLoading && filteredSnapshots.length === 0 ? (
          <PageLoadingState compact />
        ) : filteredSnapshots.length > 0 ? (
          <div className="rounded-md overflow-hidden">
            {/* Desktop table */}
            <div className="hidden md:block overflow-x-auto">
              <Table className="min-w-[850px]">
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
                      className="border-border hover:bg-muted/40"
                    >
                      <TableCell className="text-xs font-mono text-muted-foreground">
                        <button
                          type="button"
                          onClick={() => onSelectSnapshot(item.raw)}
                          className="text-left font-mono hover:text-primary hover:underline"
                        >
                          {formatDateTime(item.raw.time)}
                        </button>
                      </TableCell>
                      <TableCell className="text-xs font-medium text-foreground">
                        <button
                          type="button"
                          onClick={() => onSelectSnapshot(item.raw)}
                          className="text-left hover:text-primary hover:underline"
                        >
                          {item.planName}
                        </button>
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
                        <span className="text-[11px] text-muted-foreground/75 ml-1.5 font-mono">
                          ({item.agentDisplay.hostname})
                        </span>
                      </TableCell>
                      <TableCell className="text-xs text-right">
                        <div className="flex items-center justify-end gap-2">
                          <Button
                            variant="ghost"
                            size="sm"
                            className="h-7 text-xs text-primary gap-1"
                            onClick={() => onSelectSnapshot(item.raw)}
                          >
                            <Folder className="h-3.5 w-3.5" aria-hidden="true" />
                            {t('snapshots.viewDetails')}
                          </Button>
                          <Button
                            variant="ghost"
                            size="sm"
                            className="h-7 text-xs text-rose-600 dark:text-rose-400 hover:text-rose-700 dark:hover:text-rose-300 gap-1"
                            onClick={(e) => onDeleteSnapshot(e, item.raw)}
                            aria-label={t('common.delete')}
                          >
                            <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
                            {t('common.delete')}
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>

            {/* Mobile cards */}
            <div className="md:hidden divide-y divide-border">
              {filteredSnapshots.map((item) => (
                <div key={item.raw.id} className="p-3 space-y-2 text-xs">
                  <div className="flex items-center justify-between gap-2">
                    <button
                      type="button"
                      onClick={() => onSelectSnapshot(item.raw)}
                      className="font-semibold text-foreground hover:text-primary hover:underline text-left"
                    >
                      {item.planName}
                    </button>
                    <StatusBadge tone={item.kindTone}>{item.kindLabel}</StatusBadge>
                  </div>
                  <div className="grid grid-cols-2 gap-1 text-[11px] text-muted-foreground font-mono">
                    <div>
                      {formatDateTime(item.raw.time)}
                    </div>
                    <div className="truncate text-right">
                      {item.agentDisplay.name}
                    </div>
                  </div>
                  <div className="font-mono text-muted-foreground truncate text-[11px]">
                    {item.sourceSummary}
                  </div>
                  <div className="flex items-center justify-end gap-2 pt-1">
                    <Button
                      variant="outline"
                      size="sm"
                      className="h-7 text-xs gap-1"
                      onClick={() => onSelectSnapshot(item.raw)}
                    >
                      <Folder className="h-3.5 w-3.5" aria-hidden="true" />
                      {t('snapshots.viewDetails')}
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-7 text-xs text-rose-600 dark:text-rose-400 hover:text-rose-700 dark:hover:text-rose-300 gap-1"
                      onClick={(e) => onDeleteSnapshot(e, item.raw)}
                      aria-label={t('common.delete')}
                    >
                      <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
                      {t('common.delete')}
                    </Button>
                  </div>
                </div>
              ))}
            </div>
          </div>
        ) : (
          <div className="p-8">
            <AppEmptyState
              title={t('snapshots.noSnapshots')}
              description={
                totalSnapshots > 0
                  ? t('snapshots.noFilteredSnapshots')
                  : t('snapshots.selectSnapshotHint')
              }
            />
          </div>
        )}
      </CardContent>
    </Card>
  )
}
