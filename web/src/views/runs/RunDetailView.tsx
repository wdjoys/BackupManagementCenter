import React, { useEffect, useState, useRef, useMemo } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Progress } from '@/components/ui/progress'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { apiGet } from '@/api/client'
import { formatDateTime, translateEnum } from '@/i18n'
import type { Run, RunProgress, RunLog, Plan, Agent, ApiError } from '@/api/types'
import { AppErrorState } from '@/components/AppErrorState'
import { StatusBadge } from '@/components/StatusBadge'
import { toastSuccess, toastError } from '@/lib/toast'
import {
  statusTagType,
  operationTagType,
  formatBytes,
  formatDuration,
} from '@/utils/runDisplay'
import {
  ArrowLeft,
  Copy,
  Check,
  Radio,
  Terminal,
  RotateCcw,
  Loader2,
  FileText,
} from 'lucide-react'

interface WsStateMessage {
  type: 'state'
  run: Run
}

interface WsProgressMessage {
  type: 'progress'
  progress: RunProgress
}

interface WsLogMessage {
  type: 'log'
  entry: RunLog
}

type WsMessage = WsStateMessage | WsProgressMessage | WsLogMessage

const MAX_LOG_ROWS = 5000
const MAX_RECONNECT = 1

function isTerminal(status?: string): boolean {
  return status === 'succeeded' || status === 'failed' || status === 'cancelled'
}

export const RunDetailView: React.FC = () => {
  const { id } = useParams<{ id: string }>()
  const { t } = useTranslation()
  const navigate = useNavigate()

  const [run, setRun] = useState<Run | null>(null)
  const [plans, setPlans] = useState<Plan[]>([])
  const [agents, setAgents] = useState<Agent[]>([])
  const [logs, setLogs] = useState<RunLog[]>([])

  const [loading, setLoading] = useState(true)
  const [loadingLogs, setLoadingLogs] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [hasMoreLogs, setHasMoreLogs] = useState(false)
  const [autoScroll, setAutoScroll] = useState(true)

  const [wsConnected, setWsConnected] = useState(false)
  const [copiedSnapshot, setCopiedSnapshot] = useState(false)

  const logsWrapRef = useRef<HTMLDivElement | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const reconnectCountRef = useRef(0)

  const loadRun = async () => {
    if (!id) return
    setLoading(true)
    setError(null)
    try {
      const data = await apiGet<Run>(`/runs/${id}`)
      setRun(data)
    } catch (err: unknown) {
      const apiErr = err as ApiError
      setError(apiErr?.message || t('runDetail.loadFailed') || 'Failed to load run details')
    } finally {
      setLoading(false)
    }
  }

  const loadPlansAndAgents = async () => {
    try {
      const [planList, agentList] = await Promise.all([
        apiGet<Plan[]>('/plans'),
        apiGet<Agent[]>('/agents'),
      ])
      setPlans(planList)
      setAgents(agentList)
    } catch {
      // Non-critical
    }
  }

  const loadInitialLogs = async () => {
    if (!id) return
    setLoadingLogs(true)
    try {
      const data = await apiGet<RunLog[]>(`/runs/${id}/logs`, { limit: 500 })
      setLogs(data)
      if (data.length > 0) {
        setHasMoreLogs(true)
      }
    } catch {
      // Log endpoint optional
    } finally {
      setLoadingLogs(false)
    }
  }

  const loadMoreLogs = async () => {
    if (!id || logs.length === 0 || loadingLogs) return
    setLoadingLogs(true)
    try {
      const minSeq = Math.min(...logs.map((l) => l.seq))
      const beforeSeq = minSeq - 1
      if (beforeSeq <= 0) {
        setHasMoreLogs(false)
        return
      }
      const data = await apiGet<RunLog[]>(`/runs/${id}/logs`, {
        before_seq: beforeSeq,
        limit: 500,
      })
      if (data.length < 500) {
        setHasMoreLogs(false)
      }
      setLogs((prev) => {
        const merged = [...data, ...prev]
        const seqMap = new Map<number, RunLog>()
        for (const item of merged) {
          seqMap.set(item.seq, item)
        }
        const unique = Array.from(seqMap.values()).sort((a, b) => a.seq - b.seq)
        if (unique.length > MAX_LOG_ROWS) {
          unique.splice(0, unique.length - MAX_LOG_ROWS)
        }
        return unique
      })
    } catch {
      // Non-critical
    } finally {
      setLoadingLogs(false)
    }
  }

  // Handle Log Auto-scroll
  useEffect(() => {
    if (autoScroll && logsWrapRef.current) {
      logsWrapRef.current.scrollTop = logsWrapRef.current.scrollHeight
    }
  }, [logs, autoScroll])

  // WebSocket lifecycle
  useEffect(() => {
    if (!id || id === 'undefined' || id === 'null') return

    loadRun()
    loadPlansAndAgents()
    loadInitialLogs()

    let active = true

    const connectWs = () => {
      if (!active) return
      if (wsRef.current) {
        wsRef.current.close()
        wsRef.current = null
      }

      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
      const url = `${protocol}//${window.location.host}/ws/runs/${encodeURIComponent(id)}`

      try {
        const socket = new WebSocket(url)
        wsRef.current = socket

        socket.onopen = () => {
          if (!active) return
          setWsConnected(true)
        }

        socket.onmessage = (event) => {
          if (!active) return
          try {
            const msg = JSON.parse(event.data) as WsMessage
            if (msg.type === 'state') {
              setRun(msg.run)
              if (isTerminal(msg.run.status)) {
                socket.close()
              }
            } else if (msg.type === 'progress') {
              setRun((prev) => (prev ? { ...prev, progress: msg.progress } : prev))
            } else if (msg.type === 'log') {
              setLogs((prev) => {
                const idx = prev.findIndex((l) => l.seq === msg.entry.seq)
                if (idx >= 0) {
                  const updated = [...prev]
                  updated[idx] = msg.entry
                  return updated
                }
                const next = [...prev, msg.entry]
                if (next.length > MAX_LOG_ROWS) {
                  next.splice(0, next.length - MAX_LOG_ROWS)
                }
                return next
              })
            }
          } catch {
            // Malformed
          }
        }

        socket.onclose = () => {
          if (!active) return
          setWsConnected(false)
          // Terminal status doesn't reconnect
          if (run && isTerminal(run.status)) return

          if (reconnectCountRef.current < MAX_RECONNECT) {
            reconnectCountRef.current += 1
            setTimeout(connectWs, 3000)
          }
        }

        socket.onerror = () => {
          if (!active) return
          setWsConnected(false)
        }
      } catch {
        setWsConnected(false)
      }
    }

    connectWs()

    return () => {
      active = false
      if (wsRef.current) {
        wsRef.current.close()
        wsRef.current = null
      }
    }
  }, [id])

  const planName = useMemo(() => {
    if (!run) return ''
    const p = plans.find((item) => item.id === run.plan_id)
    return p ? p.name : run.plan_id
  }, [run, plans])

  const agentName = useMemo(() => {
    if (!run) return ''
    const a = agents.find((item) => item.id === run.agent_id)
    return a ? `${a.name} (${a.hostname})` : run.agent_id
  }, [run, agents])

  const copySnapshot = async (snapshotId: string) => {
    try {
      await navigator.clipboard.writeText(snapshotId)
      setCopiedSnapshot(true)
      toastSuccess(t('common.copied') || 'Snapshot ID copied')
      setTimeout(() => setCopiedSnapshot(false), 2000)
    } catch {
      toastError(t('common.copyFailed') || 'Failed to copy snapshot ID')
    }
  }

  if (loading) {
    return (
      <div className="flex h-64 items-center justify-center">
        <Loader2 className="h-8 w-8 animate-spin text-primary" />
      </div>
    )
  }

  if (error || !run) {
    return (
      <AppErrorState
        title={t('runDetail.title') || 'Run Details'}
        message={error || 'Run not found'}
        onRetry={loadRun}
      />
    )
  }

  return (
    <div className="space-y-6 max-w-5xl mx-auto">
      {/* Top Header */}
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
        <div className="flex items-center gap-3">
          <Button
            variant="ghost"
            size="icon"
            onClick={() => navigate('/runs')}
            className="h-8 w-8"
          >
            <ArrowLeft className="h-4 w-4" />
          </Button>
          <div>
            <div className="flex items-center gap-2">
              <h2 className="text-lg font-bold tracking-tight text-foreground font-mono">
                Run #{run.id.substring(0, 8)}
              </h2>
              <StatusBadge
                tone={statusTagType(run.status)}
                dot={run.status === 'running' || run.status === 'dispatched'}
              >
                {translateEnum('status', run.status)}
              </StatusBadge>
            </div>
            <p className="text-xs text-muted-foreground">{planName}</p>
          </div>
        </div>

        <div className="flex items-center gap-2">
          {wsConnected ? (
            <div className="flex items-center gap-1.5 px-2.5 py-1 rounded-full border border-emerald-500/30 bg-emerald-500/10 text-[11px] text-emerald-400">
              <Radio className="h-3 w-3 animate-pulse" />
              <span>{t('runDetail.liveConnected') || 'Live Stream'}</span>
            </div>
          ) : (
            <div className="flex items-center gap-1.5 px-2.5 py-1 rounded-full border border-border bg-muted/40 text-[11px] text-muted-foreground">
              <span className="h-2 w-2 rounded-full bg-muted-foreground/50" />
              <span>{t('runDetail.disconnected') || 'Static'}</span>
            </div>
          )}
        </div>
      </div>

      {/* Info Card */}
      <Card className="border-border bg-card/60 shadow-sm">
        <CardContent className="p-4 sm:p-6 grid grid-cols-2 sm:grid-cols-4 gap-4">
          <div className="space-y-1">
            <span className="text-[11px] text-muted-foreground">{t('runDetail.labels.operation')}</span>
            <div>
              <StatusBadge tone={operationTagType(run.operation)}>
                {translateEnum('runs.operations', run.operation)}
              </StatusBadge>
            </div>
          </div>

          <div className="space-y-1">
            <span className="text-[11px] text-muted-foreground">{t('runDetail.labels.agent')}</span>
            <p className="text-xs font-medium text-foreground truncate">{agentName}</p>
          </div>

          <div className="space-y-1">
            <span className="text-[11px] text-muted-foreground">{t('runDetail.labels.snapshot')}</span>
            {run.snapshot_id ? (
              <div
                onClick={() => copySnapshot(run.snapshot_id!)}
                className="flex items-center gap-1.5 font-mono text-xs text-primary cursor-pointer hover:underline"
              >
                <span className="truncate">{run.snapshot_id.substring(0, 10)}...</span>
                {copiedSnapshot ? (
                  <Check className="h-3 w-3 text-emerald-400" />
                ) : (
                  <Copy className="h-3 w-3" />
                )}
              </div>
            ) : (
              <p className="text-xs text-muted-foreground font-mono">—</p>
            )}
          </div>

          <div className="space-y-1">
            <span className="text-[11px] text-muted-foreground">{t('runs.columns.duration')}</span>
            <p className="text-xs font-mono text-foreground">
              {formatDuration(run.started_at, run.finished_at)}
            </p>
          </div>

          <div className="space-y-1">
            <span className="text-[11px] text-muted-foreground">{t('runDetail.timeline.queued')}</span>
            <p className="text-xs font-mono text-muted-foreground">{formatDateTime(run.queued_at)}</p>
          </div>

          <div className="space-y-1">
            <span className="text-[11px] text-muted-foreground">{t('runDetail.timeline.started')}</span>
            <p className="text-xs font-mono text-muted-foreground">
              {run.started_at ? formatDateTime(run.started_at) : '—'}
            </p>
          </div>

          <div className="space-y-1">
            <span className="text-[11px] text-muted-foreground">{t('runDetail.timeline.finished')}</span>
            <p className="text-xs font-mono text-muted-foreground">
              {run.finished_at ? formatDateTime(run.finished_at) : '—'}
            </p>
          </div>

          {run.error_code && (
            <div className="space-y-1">
              <span className="text-[11px] text-destructive">{t('runDetail.labels.error')}</span>
              <Tooltip>
                <TooltipTrigger asChild>
                  <p className="text-xs font-mono text-rose-400 truncate cursor-help">
                    {run.error_code}
                  </p>
                </TooltipTrigger>
                <TooltipContent className="text-xs max-w-sm">
                  {run.error_message || run.error_code}
                </TooltipContent>
              </Tooltip>
            </div>
          )}
        </CardContent>
      </Card>

      {/* Progress Card (When Available) */}
      {run.progress && (
        <Card className="border-border bg-card/60 shadow-sm">
          <CardHeader className="pb-2">
            <div className="flex items-center justify-between">
              <CardTitle className="text-xs font-semibold">
                {t('runDetail.progress.title') || 'Execution Progress'}
              </CardTitle>
              <span className="text-xs font-mono font-medium text-primary">
                {run.progress.percent}%
              </span>
            </div>
          </CardHeader>
          <CardContent className="space-y-3">
            <Progress value={run.progress.percent} className="h-2 bg-muted" />
            <div className="flex flex-wrap items-center justify-between text-xs text-muted-foreground gap-2 pt-1">
              <div>
                <span className="font-medium text-foreground">
                  {t('runDetail.progress.phase')}:{' '}
                </span>
                <span>{translateEnum('runDetail.phases', run.progress.phase)}</span>
              </div>
              <div>
                <span className="font-medium text-foreground">
                  {t('runDetail.progress.bytes')}:{' '}
                </span>
                <span className="font-mono">
                  {formatBytes(run.progress.bytes_done)} / {formatBytes(run.progress.bytes_total)}
                </span>
              </div>
              <div>
                <span className="font-medium text-foreground">
                  {t('runDetail.progress.files')}:{' '}
                </span>
                <span className="font-mono">
                  {run.progress.files_done} / {run.progress.files_total}
                </span>
              </div>
            </div>
          </CardContent>
        </Card>
      )}

      {/* Terminal Log Console */}
      <Card className="border-border bg-[#0d1117] shadow-xl overflow-hidden">
        <CardHeader className="py-3 px-4 border-b border-border/80 bg-muted/20 flex flex-row items-center justify-between space-y-0">
          <div className="flex items-center gap-2">
            <Terminal className="h-4 w-4 text-primary" />
            <CardTitle className="text-xs font-mono font-medium text-foreground">
              {t('runDetail.logs.title', { count: logs.length }) || `Logs (${logs.length})`}
            </CardTitle>
          </div>
          <div className="flex items-center gap-4">
            <div className="flex items-center gap-2">
              <Checkbox
                id="auto-scroll"
                checked={autoScroll}
                onCheckedChange={(checked) => setAutoScroll(checked === true)}
              />
              <label
                htmlFor="auto-scroll"
                className="text-[11px] text-muted-foreground cursor-pointer"
              >
                {t('runDetail.logs.autoScroll') || 'Auto-scroll'}
              </label>
            </div>
            {hasMoreLogs && (
              <Button
                variant="outline"
                size="sm"
                onClick={loadMoreLogs}
                disabled={loadingLogs}
                className="h-6 text-[11px] px-2 gap-1"
              >
                {loadingLogs ? (
                  <Loader2 className="h-3 w-3 animate-spin" />
                ) : (
                  <RotateCcw className="h-3 w-3" />
                )}
                {t('runDetail.logs.loadMore') || 'Load Earlier'}
              </Button>
            )}
          </div>
        </CardHeader>
        <CardContent className="p-0">
          <div
            ref={logsWrapRef}
            className="h-96 overflow-y-auto p-4 font-mono text-xs text-foreground/90 space-y-1 scrollbar-thin"
          >
            {logs.length > 0 ? (
              logs.map((log) => {
                const isError = log.level === 'error' || log.level === 'warn'
                return (
                  <div key={log.seq} className="flex gap-3 leading-relaxed hover:bg-white/5 py-0.5 px-1 rounded">
                    <span className="text-[11px] text-muted-foreground/60 select-none w-10 shrink-0 text-right">
                      {log.seq}
                    </span>
                    <span className="text-[11px] text-muted-foreground/80 select-none shrink-0">
                      {new Date(log.timestamp).toLocaleTimeString()}
                    </span>
                    <span
                      className={`break-all ${
                        isError ? 'text-rose-400' : 'text-slate-300'
                      }`}
                    >
                      {log.message}
                    </span>
                  </div>
                )
              })
            ) : (
              <div className="flex h-full items-center justify-center text-xs text-muted-foreground">
                <FileText className="h-4 w-4 mr-2 opacity-50" />
                <span>{t('runDetail.logs.empty') || 'No output received yet.'}</span>
              </div>
            )}
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
