import React from 'react'
import { useTranslation } from 'react-i18next'
import { Card, CardContent } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Button } from '@/components/ui/button'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { AppEmptyState } from '@/components/AppEmptyState'
import { AppErrorState } from '@/components/AppErrorState'
import { StatusBadge, type BadgeTone } from '@/components/StatusBadge'
import { PageLoadingState } from '@/components/PageLoadingState'
import { formatDateTime, translateEnum } from '@/i18n'
import type { Repository } from '@/api/types'
import { Link, RefreshCw, Info, RotateCcw, Unlink, Loader2 } from 'lucide-react'

export interface RepositoriesPanelProps {
  repos: Repository[]
  reposLoading: boolean
  reposError: string | null
  repoActionLoading: Record<string, boolean>
  canBind: boolean
  onLoadRepos: () => void
  onOpenBind: () => void
  onRetryRepo: (repo: Repository) => void
  onOpenUnbind: (repo: Repository) => void
}

function getRepoStatusTone(status: string): BadgeTone {
  if (status === 'ready') return 'success'
  if (status === 'error') return 'destructive'
  return 'warning'
}

export const RepositoriesPanel: React.FC<RepositoriesPanelProps> = ({
  repos,
  reposLoading,
  reposError,
  repoActionLoading,
  canBind,
  onLoadRepos,
  onOpenBind,
  onRetryRepo,
  onOpenUnbind,
}) => {
  const { t } = useTranslation()

  return (
    <div className="space-y-4 pt-2">
      <div className="flex items-center justify-between">
        <span className="text-xs text-muted-foreground">
          {t('storage.repos_count', { count: repos.length })}
        </span>
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            onClick={onOpenBind}
            disabled={!canBind}
            className="h-8 text-xs gap-1.5 bg-primary text-primary-foreground"
          >
            <Link className="h-3.5 w-3.5" aria-hidden="true" />
            {t('storage.bindRepository')}
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={onLoadRepos}
            className="h-8 text-xs gap-1.5"
          >
            <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
            {t('common.refresh')}
          </Button>
        </div>
      </div>

      <Alert className="border-border/80 bg-muted/20 py-2.5 text-xs text-muted-foreground">
        <Info className="h-4 w-4 text-primary" aria-hidden="true" />
        <AlertTitle className="text-xs font-semibold text-foreground">
          {t('storage.repositoryHelp.title')}
        </AlertTitle>
        <AlertDescription className="text-[11px] mt-0.5">
          {t('storage.repositoryHelp.description')}
        </AlertDescription>
      </Alert>

      {reposError ? (
        <AppErrorState
          title={t('storage.tabs.repositories')}
          message={reposError}
          onRetry={onLoadRepos}
        />
      ) : (
        <Card className="border-border bg-card/60 shadow-sm">
          <CardContent className="p-0">
            {reposLoading && repos.length === 0 ? (
              <PageLoadingState compact />
            ) : repos.length > 0 ? (
              <div className="rounded-md overflow-hidden">
                {/* Desktop table */}
                <div className="hidden md:block overflow-x-auto">
                  <Table className="min-w-[800px]">
                    <TableHeader>
                      <TableRow className="border-border hover:bg-transparent">
                        <TableHead className="text-xs font-medium">{t('storage.columns.agent')}</TableHead>
                        <TableHead className="text-xs font-medium">{t('storage.columns.storageTarget')}</TableHead>
                        <TableHead className="text-xs font-medium">{t('storage.columns.repositoryPath')}</TableHead>
                        <TableHead className="text-xs font-medium">{t('common.status')}</TableHead>
                        <TableHead className="text-xs font-medium">{t('storage.columns.lastCheck')}</TableHead>
                        <TableHead className="text-xs font-medium text-right">{t('common.actions')}</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {repos.map((repo) => {
                        const actionLoading = repoActionLoading[repo.id] || false
                        return (
                          <TableRow key={repo.id} className="border-border hover:bg-muted/30">
                            <TableCell className="font-medium text-xs text-foreground">
                              {repo.agent_name || repo.agent_id}
                            </TableCell>
                            <TableCell className="text-xs text-muted-foreground">
                              {repo.storage_target_name || repo.storage_target_id}
                            </TableCell>
                            <TableCell className="text-xs">
                              <code className="font-mono text-[11px] text-muted-foreground bg-muted/40 px-1.5 py-0.5 rounded">
                                {repo.repository_path}
                              </code>
                            </TableCell>
                            <TableCell className="text-xs">
                              <StatusBadge tone={getRepoStatusTone(repo.status)} dot>
                                {translateEnum('status', repo.status)}
                              </StatusBadge>
                            </TableCell>
                            <TableCell className="text-xs text-muted-foreground">
                              {repo.last_check_at ? formatDateTime(repo.last_check_at) : t('common.never')}
                            </TableCell>
                            <TableCell className="text-xs text-right">
                              <div className="flex items-center justify-end gap-1">
                                {repo.status !== 'ready' && (
                                  <Button
                                    variant="ghost"
                                    size="sm"
                                    disabled={actionLoading}
                                    className="h-7 text-xs text-primary gap-1"
                                    onClick={() => onRetryRepo(repo)}
                                  >
                                    {actionLoading ? (
                                      <Loader2 className="h-3 w-3 animate-spin" aria-hidden="true" />
                                    ) : (
                                      <RotateCcw className="h-3 w-3" aria-hidden="true" />
                                    )}
                                    {t('common.retry')}
                                  </Button>
                                )}
                                <Button
                                  variant="ghost"
                                  size="sm"
                                  disabled={actionLoading}
                                  className="h-7 text-xs text-rose-600 dark:text-rose-400 hover:text-rose-700 dark:hover:text-rose-300 gap-1"
                                  onClick={() => onOpenUnbind(repo)}
                                  aria-label={t('storage.repositoryDialog.unbind')}
                                >
                                  <Unlink className="h-3 w-3" aria-hidden="true" />
                                  {t('storage.repositoryDialog.unbind')}
                                </Button>
                              </div>
                            </TableCell>
                          </TableRow>
                        )
                      })}
                    </TableBody>
                  </Table>
                </div>

                {/* Mobile cards */}
                <div className="md:hidden divide-y divide-border">
                  {repos.map((repo) => {
                    const actionLoading = repoActionLoading[repo.id] || false
                    return (
                      <div key={repo.id} className="p-3 space-y-2 text-xs">
                        <div className="flex items-center justify-between gap-2">
                          <span className="font-semibold text-foreground">{repo.agent_name || repo.agent_id}</span>
                          <StatusBadge tone={getRepoStatusTone(repo.status)} dot>
                            {translateEnum('status', repo.status)}
                          </StatusBadge>
                        </div>
                        <div className="text-[11px] text-muted-foreground">
                          <span>Target: </span>{repo.storage_target_name || repo.storage_target_id}
                        </div>
                        <div>
                          <code className="font-mono text-[11px] text-muted-foreground bg-muted/40 px-1.5 py-0.5 rounded break-all">
                            {repo.repository_path}
                          </code>
                        </div>
                        <div className="flex items-center justify-end gap-2 pt-1">
                          {repo.status !== 'ready' && (
                            <Button
                              variant="outline"
                              size="sm"
                              disabled={actionLoading}
                              className="h-7 text-xs text-primary gap-1"
                              onClick={() => onRetryRepo(repo)}
                            >
                              {actionLoading ? (
                                <Loader2 className="h-3 w-3 animate-spin" aria-hidden="true" />
                              ) : (
                                <RotateCcw className="h-3 w-3" aria-hidden="true" />
                              )}
                              {t('common.retry')}
                            </Button>
                          )}
                          <Button
                            variant="ghost"
                            size="sm"
                            disabled={actionLoading}
                            className="h-7 text-xs text-rose-600 dark:text-rose-400 hover:text-rose-700 dark:hover:text-rose-300 gap-1"
                            onClick={() => onOpenUnbind(repo)}
                            aria-label={t('storage.repositoryDialog.unbind')}
                          >
                            <Unlink className="h-3 w-3" aria-hidden="true" />
                            {t('storage.repositoryDialog.unbind')}
                          </Button>
                        </div>
                      </div>
                    )
                  })}
                </div>
              </div>
            ) : (
              <div className="p-8">
                <AppEmptyState
                  title={t('storage.emptyRepositories')}
                  description={t('storage.emptyRepositories_desc')}
                />
              </div>
            )}
          </CardContent>
        </Card>
      )}
    </div>
  )
}
