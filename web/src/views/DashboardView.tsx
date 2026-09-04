import React, { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Button } from '@/components/ui/button'
import { apiGet, isApiClientError } from '@/api/client'
import { formatDateTime } from '@/i18n'
import type { Dashboard } from '@/api/types'
import { AppEmptyState } from '@/components/AppEmptyState'
import { AppErrorState } from '@/components/AppErrorState'
import { PageLoadingState } from '@/components/PageLoadingState'
import { StatusBadge } from '@/components/StatusBadge'
import {
  Server,
  CheckCircle2,
  XCircle,
  AlertTriangle,
  Calendar,
  Database,
  RefreshCw,
} from 'lucide-react'

export const DashboardView: React.FC = () => {
  const { t } = useTranslation()

  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [dashboard, setDashboard] = useState<Dashboard>({
    agents_online: 0,
    agents_total: 0,
    runs_24h_succeeded: 0,
    runs_24h_failed: 0,
    next_scheduled: [],
    repos_needing_check: [],
  })

  const loadData = async () => {
    setLoading(true)
    setError(null)
    try {
      const data = await apiGet<Dashboard>('/dashboard')
      setDashboard(data)
    } catch (err: unknown) {
      setError(isApiClientError(err) ? err.message : t('common.load_failed'))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    loadData()
  }, [])

  if (loading) {
    return <PageLoadingState />
  }

  if (error) {
    return (
      <AppErrorState
        title={t('dashboard.title')}
        message={error}
        onRetry={loadData}
      />
    )
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-xl font-bold tracking-tight text-foreground">
            {t('dashboard.title')}
          </h2>
          <p className="text-xs text-muted-foreground">
            {t('dashboard.subtitle')}
          </p>
        </div>
        <Button variant="outline" size="sm" onClick={loadData} className="h-8 text-xs gap-1.5">
          <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
          {t('common.refresh')}
        </Button>
      </div>

      {/* KPI Cards Grid */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
        {/* Agents Online */}
        <Card className="border-border bg-card/60 shadow-sm">
          <CardHeader className="flex flex-row items-center justify-between pb-2 space-y-0">
            <CardTitle className="text-xs font-medium text-muted-foreground">
              {t('dashboard.onlineAgents')}
            </CardTitle>
            <Server className="h-4 w-4 text-emerald-600 dark:text-emerald-400" aria-hidden="true" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold text-foreground">
              {dashboard.agents_online}{' '}
              <span className="text-xs font-normal text-muted-foreground">
                / {dashboard.agents_total}
              </span>
            </div>
            <p className="text-[11px] text-muted-foreground mt-1">
              {t('dashboard.agents_active_desc')}
            </p>
          </CardContent>
        </Card>

        {/* 24h Succeeded */}
        <Card className="border-border bg-card/60 shadow-sm">
          <CardHeader className="flex flex-row items-center justify-between pb-2 space-y-0">
            <CardTitle className="text-xs font-medium text-muted-foreground">
              {t('dashboard.succeeded24h')}
            </CardTitle>
            <CheckCircle2 className="h-4 w-4 text-emerald-600 dark:text-emerald-400" aria-hidden="true" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold text-emerald-600 dark:text-emerald-400">
              {dashboard.runs_24h_succeeded}
            </div>
            <p className="text-[11px] text-muted-foreground mt-1">
              {t('dashboard.runs_succeeded_desc')}
            </p>
          </CardContent>
        </Card>

        {/* 24h Failed */}
        <Card className="border-border bg-card/60 shadow-sm">
          <CardHeader className="flex flex-row items-center justify-between pb-2 space-y-0">
            <CardTitle className="text-xs font-medium text-muted-foreground">
              {t('dashboard.failed24h')}
            </CardTitle>
            <XCircle className="h-4 w-4 text-rose-600 dark:text-rose-400" aria-hidden="true" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold text-rose-600 dark:text-rose-400">
              {dashboard.runs_24h_failed}
            </div>
            <p className="text-[11px] text-muted-foreground mt-1">
              {t('dashboard.runs_failed_desc')}
            </p>
          </CardContent>
        </Card>

        {/* Repos Needing Check */}
        <Card className="border-border bg-card/60 shadow-sm">
          <CardHeader className="flex flex-row items-center justify-between pb-2 space-y-0">
            <CardTitle className="text-xs font-medium text-muted-foreground">
              {t('dashboard.reposNeedingCheck')}
            </CardTitle>
            <AlertTriangle className="h-4 w-4 text-amber-600 dark:text-amber-400" aria-hidden="true" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold text-amber-600 dark:text-amber-400">
              {dashboard.repos_needing_check.length}
            </div>
            <p className="text-[11px] text-muted-foreground mt-1">
              {t('dashboard.repos_pending_desc')}
            </p>
          </CardContent>
        </Card>
      </div>

      {/* Main Tables Grid */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
        {/* Next Scheduled Plans */}
        <Card className="lg:col-span-2 border-border bg-card/60 shadow-sm">
          <CardHeader className="pb-3 flex flex-row items-center gap-2 space-y-0">
            <Calendar className="h-4 w-4 text-primary" aria-hidden="true" />
            <CardTitle className="text-sm font-semibold">
              {t('dashboard.nextScheduled')}
            </CardTitle>
          </CardHeader>
          <CardContent>
            {dashboard.next_scheduled.length > 0 ? (
              <div className="rounded-md border border-border overflow-hidden">
                <Table>
                  <TableHeader>
                    <TableRow className="border-border hover:bg-transparent">
                      <TableHead className="text-xs font-medium">{t('dashboard.plan')}</TableHead>
                      <TableHead className="text-xs font-medium text-right">
                        {t('dashboard.nextFireAt')}
                      </TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {dashboard.next_scheduled.map((item) => (
                      <TableRow key={item.plan_id} className="border-border hover:bg-muted/30">
                        <TableCell className="font-medium text-xs text-foreground">
                          {item.plan_name}
                        </TableCell>
                        <TableCell className="text-xs text-muted-foreground text-right">
                          {formatDateTime(item.next_fire_at)}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            ) : (
              <AppEmptyState
                title={t('dashboard.noUpcomingPlans')}
                description={t('dashboard.noUpcomingPlans_desc')}
              />
            )}
          </CardContent>
        </Card>

        {/* Repositories Needing Check */}
        <Card className="border-border bg-card/60 shadow-sm">
          <CardHeader className="pb-3 flex flex-row items-center gap-2 space-y-0">
            <Database className="h-4 w-4 text-amber-600 dark:text-amber-400" aria-hidden="true" />
            <CardTitle className="text-sm font-semibold">
              {t('dashboard.reposNeedingCheck')}
            </CardTitle>
          </CardHeader>
          <CardContent>
            {dashboard.repos_needing_check.length > 0 ? (
              <div className="rounded-md border border-border overflow-hidden">
                <Table>
                  <TableHeader>
                    <TableRow className="border-border hover:bg-transparent">
                      <TableHead className="text-xs font-medium">{t('common.name')}</TableHead>
                      <TableHead className="text-xs font-medium text-right">
                        {t('dashboard.lastCheck')}
                      </TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {dashboard.repos_needing_check.map((repo) => (
                      <TableRow key={repo.id} className="border-border hover:bg-muted/30">
                        <TableCell className="font-medium text-xs text-foreground">
                          {repo.name}
                        </TableCell>
                        <TableCell className="text-xs text-muted-foreground text-right">
                          {repo.last_check_at ? (
                            formatDateTime(repo.last_check_at)
                          ) : (
                            <StatusBadge tone="warning">
                              {t('common.never')}
                            </StatusBadge>
                          )}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            ) : (
              <AppEmptyState
                title={t('dashboard.allReposHealthy')}
                description={t('dashboard.allReposHealthy_desc')}
              />
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  )
}
