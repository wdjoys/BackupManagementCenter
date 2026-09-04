import React from 'react'
import { useTranslation } from 'react-i18next'
import { Card, CardContent } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Button } from '@/components/ui/button'
import { AppEmptyState } from '@/components/AppEmptyState'
import { AppErrorState } from '@/components/AppErrorState'
import { PageLoadingState } from '@/components/PageLoadingState'
import { formatDateTime } from '@/i18n'
import type { StorageTarget } from '@/api/types'
import { Upload, RefreshCw, Edit2, Trash2 } from 'lucide-react'

export interface StorageTargetsPanelProps {
  targets: StorageTarget[]
  targetsLoading: boolean
  targetsError: string | null
  onLoadTargets: () => void
  onOpenImport: () => void
  onOpenEdit: (target: StorageTarget) => void
  onOpenDelete: (target: StorageTarget) => void
}

export const StorageTargetsPanel: React.FC<StorageTargetsPanelProps> = ({
  targets,
  targetsLoading,
  targetsError,
  onLoadTargets,
  onOpenImport,
  onOpenEdit,
  onOpenDelete,
}) => {
  const { t } = useTranslation()

  return (
    <div className="space-y-4 pt-2">
      <div className="flex items-center justify-between">
        <span className="text-xs text-muted-foreground">
          {t('storage.targets_count', { count: targets.length })}
        </span>
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            onClick={onOpenImport}
            className="h-8 text-xs gap-1.5 bg-primary text-primary-foreground"
          >
            <Upload className="h-3.5 w-3.5" aria-hidden="true" />
            {t('storage.importRcloneConfig')}
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={onLoadTargets}
            className="h-8 text-xs gap-1.5"
          >
            <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
            {t('common.refresh')}
          </Button>
        </div>
      </div>

      {targetsError ? (
        <AppErrorState
          title={t('storage.tabs.targets')}
          message={targetsError}
          onRetry={onLoadTargets}
        />
      ) : (
        <Card className="border-border bg-card/60 shadow-sm">
          <CardContent className="p-0">
            {targetsLoading && targets.length === 0 ? (
              <PageLoadingState compact />
            ) : targets.length > 0 ? (
              <div className="rounded-md overflow-hidden">
                {/* Desktop table */}
                <div className="hidden md:block overflow-x-auto">
                  <Table className="min-w-[800px]">
                    <TableHeader>
                      <TableRow className="border-border hover:bg-transparent">
                        <TableHead className="text-xs font-medium">{t('common.name')}</TableHead>
                        <TableHead className="text-xs font-medium">{t('storage.columns.type')}</TableHead>
                        <TableHead className="text-xs font-medium">{t('storage.columns.remoteName')}</TableHead>
                        <TableHead className="text-xs font-medium">{t('storage.columns.remotePath')}</TableHead>
                        <TableHead className="text-xs font-medium">{t('storage.columns.createdAt')}</TableHead>
                        <TableHead className="text-xs font-medium">{t('storage.columns.updatedAt')}</TableHead>
                        <TableHead className="text-xs font-medium text-right">{t('common.actions')}</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {targets.map((tgt) => (
                        <TableRow key={tgt.id} className="border-border hover:bg-muted/30">
                          <TableCell className="font-medium text-xs text-foreground">
                            {tgt.name}
                          </TableCell>
                          <TableCell className="text-xs">
                            <span className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">
                              {tgt.type}
                            </span>
                          </TableCell>
                          <TableCell className="text-xs font-mono text-muted-foreground">
                            {tgt.remote_name}
                          </TableCell>
                          <TableCell className="text-xs text-muted-foreground">
                            {tgt.remote_path || '/'}
                          </TableCell>
                          <TableCell className="text-xs text-muted-foreground">
                            {formatDateTime(tgt.created_at)}
                          </TableCell>
                          <TableCell className="text-xs text-muted-foreground">
                            {formatDateTime(tgt.updated_at)}
                          </TableCell>
                          <TableCell className="text-xs text-right">
                            <div className="flex items-center justify-end gap-1">
                              <Button
                                variant="ghost"
                                size="sm"
                                className="h-7 text-xs text-primary gap-1"
                                onClick={() => onOpenEdit(tgt)}
                              >
                                <Edit2 className="h-3 w-3" aria-hidden="true" />
                                {t('common.edit')}
                              </Button>
                              <Button
                                variant="ghost"
                                size="sm"
                                className="h-7 text-xs text-rose-600 dark:text-rose-400 hover:text-rose-700 dark:hover:text-rose-300 gap-1"
                                onClick={() => onOpenDelete(tgt)}
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
                  {targets.map((tgt) => (
                    <div key={tgt.id} className="p-3 space-y-2 text-xs">
                      <div className="flex items-center justify-between gap-2">
                        <span className="font-semibold text-foreground text-xs">{tgt.name}</span>
                        <span className="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
                          {tgt.type}
                        </span>
                      </div>
                      <div className="grid grid-cols-2 gap-1 text-[11px] text-muted-foreground font-mono">
                        <div>
                          <span>Remote: </span>{tgt.remote_name}
                        </div>
                        <div className="truncate text-right">
                          <span>Path: </span>{tgt.remote_path || '/'}
                        </div>
                      </div>
                      <div className="flex items-center justify-end gap-2 pt-1">
                        <Button
                          variant="outline"
                          size="sm"
                          className="h-7 text-xs gap-1"
                          onClick={() => onOpenEdit(tgt)}
                        >
                          <Edit2 className="h-3 w-3" aria-hidden="true" />
                          {t('common.edit')}
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          className="h-7 text-xs text-rose-600 dark:text-rose-400 hover:text-rose-700 dark:hover:text-rose-300 gap-1"
                          onClick={() => onOpenDelete(tgt)}
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
                  title={t('storage.emptyTargets')}
                  description={t('storage.emptyTargets_desc')}
                />
              </div>
            )}
          </CardContent>
        </Card>
      )}
    </div>
  )
}
