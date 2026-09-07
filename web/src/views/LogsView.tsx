import React, { useEffect, useState, useMemo, useRef } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Button } from '@/components/ui/button'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { apiGet, isApiClientError, isAbortError } from '@/api/client'
import { formatDateTime } from '@/i18n'
import type { SystemLog, Agent } from '@/api/types'
import { AppEmptyState } from '@/components/AppEmptyState'
import { AppErrorState } from '@/components/AppErrorState'
import { StatusBadge, type BadgeTone } from '@/components/StatusBadge'
import { PageLoadingState } from '@/components/PageLoadingState'
import { RefreshCw, RotateCcw, Loader2, Server, Activity, Info } from 'lucide-react'

const LOG_LEVELS = ['debug', 'info', 'warn', 'error']
const LOG_TYPES = ['system', 'auth', 'plan', 'agent', 'run', 'storage']

const LEVEL_TONES: Record<string, BadgeTone> = {
  debug: 'secondary',
  info: 'default',
  warn: 'warning',
  error: 'destructive',
}

export const LogsView: React.FC = () => {
  const { t } = useTranslation()
  const [searchParams, setSearchParams] = useSearchParams()

  const [scope, setScope] = useState<'server' | 'agent'>('server')
  const [selectedAgentId, setSelectedAgentId] = useState<string>('')
  const [levelFilter, setLevelFilter] = useState<string>('all')
  const [typeFilter, setTypeFilter] = useState<string>('all')

  const [logs, setLogs] = useState<SystemLog[]>([])
  const [agents, setAgents] = useState<Agent[]>([])
  const [loading, setLoading] = useState(false)
  const [hasMore, setHasMore] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const abortControllerRef = useRef<AbortController | null>(null)

  // Initialize query sync
  useEffect(() => {
    const agentQuery = searchParams.get('agent_id')
    if (agentQuery) {
      setScope('agent')
      setSelectedAgentId(agentQuery)
    }
  }, [searchParams])

  const loadAgents = async () => {
    try {
      const data = await apiGet<Agent[]>('/agents')
      setAgents(data)
    } catch {
      // Non-blocking
    }
  }

  useEffect(() => {
    loadAgents()
  }, [])

  const loadLogs = async (reset = false) => {
    if (scope === 'agent' && !selectedAgentId) {
      setLogs([])
      setHasMore(false)
      return
    }

    abortControllerRef.current?.abort()
    const controller = new AbortController()
    abortControllerRef.current = controller

    setLoading(true)
    setError(null)
    try {
      const endpoint = scope === 'server' ? '/logs/server' : '/logs/agent'
      const params: Record<string, string | number | undefined> = {
        limit: 500,
      }
      if (scope === 'agent') {
        params.agent_id = selectedAgentId
      }

      if (!reset && logs.length > 0) {
        const minId = Math.min(...logs.map((l) => l.id))
        params.before_id = minId
      }

      const data = await apiGet<SystemLog[]>(endpoint, params, { signal: controller.signal })
      if (reset) {
        setLogs(data)
      } else {
        setLogs((prev) => {
          const map = new Map<number, SystemLog>()
          for (const item of [...prev, ...data]) {
            map.set(item.id, item)
          }
          return Array.from(map.values()).sort((a, b) => b.id - a.id)
        })
      }
      setHasMore(data.length >= 500)
    } catch (err: unknown) {
      if (isAbortError(err)) return
      setError(isApiClientError(err) ? err.message : t('logs.loadFailed'))
    } finally {
      if (abortControllerRef.current === controller) {
        setLoading(false)
      }
    }
  }

  useEffect(() => {
    return () => {
      abortControllerRef.current?.abort()
    }
  }, [])

  useEffect(() => {
    loadLogs(true)
  }, [scope, selectedAgentId])

  const handleScopeChange = (nextScope: 'server' | 'agent') => {
    setScope(nextScope)
    if (nextScope === 'server') {
      searchParams.delete('agent_id')
      setSearchParams(searchParams)
    }
  }

  const handleAgentChange = (val: string) => {
    const nextAgent = val === 'none' ? '' : val
    setSelectedAgentId(nextAgent)
    if (nextAgent) {
      searchParams.set('agent_id', nextAgent)
      setSearchParams(searchParams)
    } else {
      searchParams.delete('agent_id')
      setSearchParams(searchParams)
    }
  }

  const filteredLogs = useMemo(() => {
    return logs.filter((log) => {
      if (levelFilter !== 'all' && log.level !== levelFilter) return false
      if (typeFilter !== 'all' && log.type !== typeFilter) return false
      return true
    })
  }, [logs, levelFilter, typeFilter])

  return (
    <div className="space-y-6">
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
        <div>
          <h2 className="text-xl font-bold tracking-tight text-foreground">
            {t('logs.title')}
          </h2>
          <p className="text-xs text-muted-foreground">
            {t('logs.subtitle')}
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={() => loadLogs(true)}
          disabled={loading}
          className="h-8 text-xs gap-1.5 self-start sm:self-auto"
        >
          <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
          {t('common.refresh')}
        </Button>
      </div>

      {/* Control Bar */}
      <Card className="border-border bg-card/40 shadow-sm">
        <CardContent className="p-4 flex flex-wrap items-center gap-3">
          {/* Scope Toggle */}
          <Tabs
            value={scope}
            onValueChange={(val) => handleScopeChange(val as 'server' | 'agent')}
          >
            <TabsList className="h-8 p-0.5">
              <TabsTrigger value="server" className="text-xs h-7 px-3 gap-1.5">
                <Server className="h-3.5 w-3.5" aria-hidden="true" />
                <span>{t('logs.server')}</span>
              </TabsTrigger>
              <TabsTrigger value="agent" className="text-xs h-7 px-3 gap-1.5">
                <Activity className="h-3.5 w-3.5" aria-hidden="true" />
                <span>{t('logs.agent')}</span>
              </TabsTrigger>
            </TabsList>
          </Tabs>

          {/* Agent Selector when scope === agent */}
          {scope === 'agent' && (
            <Select value={selectedAgentId || 'none'} onValueChange={handleAgentChange}>
              <SelectTrigger className="w-56 h-8 text-xs">
                <SelectValue placeholder={t('logs.agentPlaceholder')} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="none" className="text-xs">
                  {t('logs.selectAgent')}
                </SelectItem>
                {agents.map((a) => (
                  <SelectItem key={a.id} value={a.id} className="text-xs">
                    {a.name} ({a.hostname})
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}

          {/* Level Filter */}
          <Select value={levelFilter} onValueChange={setLevelFilter}>
            <SelectTrigger className="w-36 h-8 text-xs">
              <SelectValue placeholder={t('logs.filters.level')} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all" className="text-xs">
                {t('logs.filters.allLevels') || 'All Levels'}
              </SelectItem>
              {LOG_LEVELS.map((lvl) => (
                <SelectItem key={lvl} value={lvl} className="text-xs uppercase font-mono">
                  {lvl}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          {/* Type Filter */}
          <Select value={typeFilter} onValueChange={setTypeFilter}>
            <SelectTrigger className="w-36 h-8 text-xs">
              <SelectValue placeholder={t('logs.filters.type')} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all" className="text-xs">
                {t('logs.filters.allTypes') || 'All Types'}
              </SelectItem>
              {LOG_TYPES.map((tp) => (
                <SelectItem key={tp} value={tp} className="text-xs">
                  {tp}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </CardContent>
      </Card>

      {scope === 'agent' && !selectedAgentId && (
        <Alert className="border-border/80 bg-muted/20 py-2.5 text-xs text-muted-foreground">
          <Info className="h-4 w-4 text-primary" />
          <AlertTitle className="text-xs font-semibold text-foreground">
            {t('logs.selectAgentHint')}
          </AlertTitle>
          <AlertDescription className="text-xs mt-0.5">
            {t('logs.selectAgentDescription') || 'Please choose an online host agent to query its event stream.'}
          </AlertDescription>
        </Alert>
      )}

      {error ? (
        <AppErrorState title={t('logs.title')} message={error} onRetry={() => loadLogs(true)} />
      ) : (
        <Card className="border-border bg-card/60 shadow-sm">
          <CardHeader className="py-3 px-4 border-b border-border flex flex-row items-center justify-between space-y-0">
            <CardTitle className="text-xs font-semibold">
              {scope === 'server' ? t('logs.serverLogs') : t('logs.agentLogs')} ({filteredLogs.length})
            </CardTitle>
            {hasMore && (
              <Button
                variant="outline"
                size="sm"
                onClick={() => loadLogs(false)}
                disabled={loading}
                className="h-7 text-xs gap-1.5"
              >
                {loading ? <Loader2 className="h-3 w-3 animate-spin" aria-hidden="true" /> : <RotateCcw className="h-3 w-3" aria-hidden="true" />}
                {t('logs.loadMore')}
              </Button>
            )}
          </CardHeader>
          <CardContent className="p-0">
            {loading && logs.length === 0 ? (
              <PageLoadingState compact />
            ) : filteredLogs.length > 0 ? (
              <div className="rounded-md overflow-hidden">
                {/* Desktop Table */}
                <div className="hidden md:block overflow-x-auto">
                  <Table className="min-w-[800px]">
                    <TableHeader>
                      <TableRow className="border-border hover:bg-transparent">
                        <TableHead className="text-xs font-medium w-16">{t('logs.columns.id')}</TableHead>
                        <TableHead className="text-xs font-medium w-24">{t('logs.columns.type')}</TableHead>
                        {scope === 'agent' && (
                          <TableHead className="text-xs font-medium w-20">{t('logs.columns.seq')}</TableHead>
                        )}
                        <TableHead className="text-xs font-medium w-44">{t('logs.columns.time')}</TableHead>
                        <TableHead className="text-xs font-medium w-20">{t('logs.columns.level')}</TableHead>
                        <TableHead className="text-xs font-medium">{t('logs.columns.message')}</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {filteredLogs.map((item) => (
                        <TableRow key={item.id} className="border-border hover:bg-muted/30">
                          <TableCell className="text-xs font-mono text-muted-foreground py-2">
                            {item.id}
                          </TableCell>
                          <TableCell className="text-xs py-2">
                            <span className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">
                              {item.type}
                            </span>
                          </TableCell>
                          {scope === 'agent' && (
                            <TableCell className="text-xs font-mono text-muted-foreground py-2">
                              {item.source_seq ?? '—'}
                            </TableCell>
                          )}
                          <TableCell className="text-xs font-mono text-muted-foreground py-2">
                            {formatDateTime(item.timestamp)}
                          </TableCell>
                          <TableCell className="text-xs py-2">
                            <StatusBadge tone={LEVEL_TONES[item.level] || 'secondary'}>
                              {item.level.toUpperCase()}
                            </StatusBadge>
                          </TableCell>
                          <TableCell className="text-xs font-mono text-foreground break-all py-2">
                            {item.message}
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </div>

                {/* Mobile Cards */}
                <div className="md:hidden divide-y divide-border">
                  {filteredLogs.map((item) => (
                    <div key={item.id} className="p-3 space-y-1.5 text-xs">
                      <div className="flex items-center justify-between gap-2">
                        <span className="font-mono text-muted-foreground text-[11px]">
                          {formatDateTime(item.timestamp)}
                        </span>
                        <div className="flex items-center gap-1.5">
                          <span className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">
                            {item.type}
                          </span>
                          <StatusBadge tone={LEVEL_TONES[item.level] || 'secondary'}>
                            {item.level.toUpperCase()}
                          </StatusBadge>
                        </div>
                      </div>
                      {scope === 'agent' && item.source_seq != null && (
                        <div className="text-[11px] text-muted-foreground font-mono">
                          {t('logs.columns.seq')}: #{item.source_seq}
                        </div>
                      )}
                      <p className="font-mono text-foreground break-all text-xs">
                        {item.message}
                      </p>
                    </div>
                  ))}
                </div>
              </div>
            ) : (
              <div className="p-8">
                <AppEmptyState
                  title={t('logs.empty')}
                  description={t('logs.empty_desc')}
                />
              </div>
            )}
          </CardContent>
        </Card>
      )}
    </div>
  )
}
