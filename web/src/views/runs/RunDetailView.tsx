import React, { useEffect, useState, useRef, useMemo } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Progress } from '@/components/ui/progress'
import { apiGet, apiPost, isApiClientError, isAbortError } from '@/api/client'
import { formatDateTime, translateEnum } from '@/i18n'
import type { Run, RunProgress, RunLog, Plan, Agent, RestoreRequestItem } from '@/api/types'
import { AppErrorState } from '@/components/AppErrorState'
import { StatusBadge } from '@/components/StatusBadge'
import { Input } from '@/components/ui/input'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { RESTORE_PHASE_TONES, isRestorePhaseBlocking } from '@/views/snapshots/Types'
import { PageLoadingState } from '@/components/PageLoadingState'
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
const MAX_RECONNECT = 5

// 按 id 去重并升序排列，只保留最新的 MAX_LOG_ROWS 条。
function mergeLogs(prev: RunLog[], incoming: RunLog[]): RunLog[] {
  const byId = new Map<number, RunLog>()
  for (const item of prev) byId.set(item.id, item)
  for (const item of incoming) byId.set(item.id, item)
  const merged = Array.from(byId.values()).sort((a, b) => a.id - b.id)
  return merged.length > MAX_LOG_ROWS ? merged.slice(merged.length - MAX_LOG_ROWS) : merged
}

// 固定格式 MM-DD HH:mm:ss.SSS，等宽字体下每行宽度一致，保证列对齐。
function formatLogTime(ts: string): string {
  const d = new Date(ts)
  if (Number.isNaN(d.getTime())) return ts
  const p = (n: number, w = 2) => String(n).padStart(w, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}.${p(d.getMilliseconds(), 3)}`
}

// 表头与日志行共用：grid 固定列宽，保证三列在所有行对齐。
const LOG_GRID = 'grid grid-cols-[18ch_7ch_minmax(0,1fr)] items-baseline gap-3 font-mono text-xs'

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
  const [restoreRequest, setRestoreRequest] = useState<RestoreRequestItem | null>(null)
  const [resolveNote, setResolveNote] = useState('')
  const [resolveStopped, setResolveStopped] = useState(false)
  const [resolveVerified, setResolveVerified] = useState(false)
  const [resolving, setResolving] = useState(false)

  const logsWrapRef = useRef<HTMLDivElement | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const reconnectCountRef = useRef(0)
  const terminalRef = useRef(false)
  const reconnectTimerRef = useRef<number | null>(null)
  const logsAbortControllerRef = useRef<AbortController | null>(null)

  const loadRun = async () => {
    if (!id) return
    setLoading(true)
    setError(null)
    try {
      const data = await apiGet<Run>(`/runs/${id}`)
      setRun(data)
      terminalRef.current = isTerminal(data.status)
      if (data.operation === 'restore') {
        // 恢复请求携带 phase 与保护快照 ID，用于展示阻塞状态与人工解除入口。
        try {
          const requests = await apiGet<RestoreRequestItem[]>('/restores', { limit: 200 })
          setRestoreRequest(requests.find((r) => r.run_id === id) ?? null)
        } catch {
          setRestoreRequest(null)
        }
      }
    } catch (err: unknown) {
      if (isAbortError(err)) return
      setError(isApiClientError(err) ? err.message : t('runDetail.loadFailed'))
    } finally {
      setLoading(false)
    }
  }

  const handleResolveRestore = async () => {
    if (!restoreRequest || !id) return
    if (!resolveNote.trim() || !resolveStopped || !resolveVerified) {
      toastError(t('restore.resolve.required'))
      return
    }
    setResolving(true)
    try {
      await apiPost(`/restores/${restoreRequest.id}/resolve`, {
        run_id: id,
        note: resolveNote.trim(),
        execution_stopped: true,
        target_verified: true,
      })
      toastSuccess(t('restore.resolve.success'))
      setResolveNote('')
      setResolveStopped(false)
      setResolveVerified(false)
      await loadRun()
    } catch (err: unknown) {
      toastError(isApiClientError(err) ? err.message : t('restore.resolve.failed'))
    } finally {
      setResolving(false)
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
    logsAbortControllerRef.current?.abort()
    const controller = new AbortController()
    logsAbortControllerRef.current = controller
    try {
      const data = await apiGet<RunLog[]>(`/runs/${id}/logs`, { limit: 500 }, { signal: controller.signal })
      setLogs((prev) => mergeLogs(prev, data))
      setHasMoreLogs(data.length >= 500)
    } catch (err: unknown) {
      if (isAbortError(err)) return
      // Non-critical
    } finally {
      if (logsAbortControllerRef.current === controller) {
        setLoadingLogs(false)
      }
    }
  }

  const loadMoreLogs = async () => {
    if (!id || logs.length === 0 || loadingLogs) return
    setLoadingLogs(true)
    try {
      const minId = Math.min(...logs.map((l) => l.id))
      const beforeId = minId - 1
      if (beforeId <= 0) {
        setHasMoreLogs(false)
        return
      }
      const data = await apiGet<RunLog[]>(`/runs/${id}/logs`, {
        before_id: beforeId,
        limit: 500,
      })
      if (data.length < 500) {
        setHasMoreLogs(false)
      }
      setLogs((prev) => mergeLogs(prev, data))
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
          reconnectCountRef.current = 0
          setWsConnected(true)
        }

        socket.onmessage = (event) => {
          if (!active) return
          try {
            const msg = JSON.parse(event.data) as WsMessage
            if (msg.type === 'state') {
              setRun(msg.run)
              terminalRef.current = isTerminal(msg.run.status)
              if (terminalRef.current) {
                if (reconnectTimerRef.current) {
                  clearTimeout(reconnectTimerRef.current)
                  reconnectTimerRef.current = null
                }
                socket.close()
              }
            } else if (msg.type === 'progress') {
              setRun((prev) => (prev ? { ...prev, progress: msg.progress } : prev))
            } else if (msg.type === 'log') {
              setLogs((prev) => {
                const last = prev[prev.length - 1]
                // 常规路径：ID 递增，直接追加，避免每条实时日志都重排整个列表。
                if (!last || msg.entry.id > last.id) {
                  const next = [...prev, msg.entry]
                  return next.length > MAX_LOG_ROWS ? next.slice(next.length - MAX_LOG_ROWS) : next
                }
                return mergeLogs(prev, [msg.entry])
              })
            }
          } catch {
            // Malformed
          }
        }

        socket.onclose = () => {
          if (!active) return
          setWsConnected(false)
          if (terminalRef.current) return

          if (reconnectTimerRef.current) {
            clearTimeout(reconnectTimerRef.current)
            reconnectTimerRef.current = null
          }

          if (reconnectCountRef.current < MAX_RECONNECT) {
            reconnectCountRef.current += 1
            reconnectTimerRef.current = window.setTimeout(connectWs, 3000)
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
      if (reconnectTimerRef.current) {
        clearTimeout(reconnectTimerRef.current)
        reconnectTimerRef.current = null
      }
      logsAbortControllerRef.current?.abort()
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
      toastSuccess(t('common.copied'))
      setTimeout(() => setCopiedSnapshot(false), 2000)
    } catch {
      toastError(t('common.copyFailed'))
    }
  }

  if (loading) {
    return <PageLoadingState />
  }

  if (error || !run) {
    return (
      <AppErrorState
        title={t('runDetail.title')}
        message={error || t('runDetail.loadFailed')}
        onRetry={loadRun}
      />
    )
  }

  const percent = Math.min(100, Math.max(0, run.progress?.percent ?? 0))

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
            aria-label={t('common.previous')}
          >
            <ArrowLeft className="h-4 w-4" aria-hidden="true" />
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
            <div className="flex items-center gap-1.5 px-2.5 py-1 rounded-full border border-emerald-500/30 bg-emerald-500/10 text-[11px] text-emerald-600 dark:text-emerald-400 font-medium">
              <Radio className="h-3 w-3 animate-pulse" aria-hidden="true" />
              <span>{t('runDetail.liveConnected')}</span>
            </div>
          ) : run && isTerminal(run.status) ? (
            // 运行终态时服务端会正常关闭日志流（close 1000），此时显示"连接已中断"
            // 会让人以为出错了；终态给中性文案。
            <div className="flex items-center gap-1.5 px-2.5 py-1 rounded-full border border-border bg-muted/40 text-[11px] text-muted-foreground">
              <span className="h-2 w-2 rounded-full bg-muted-foreground/50" aria-hidden="true" />
              <span>{t('runDetail.streamEnded')}</span>
            </div>
          ) : (
            <div className="flex items-center gap-1.5 px-2.5 py-1 rounded-full border border-border bg-muted/40 text-[11px] text-muted-foreground">
              <span className="h-2 w-2 rounded-full bg-muted-foreground/50" aria-hidden="true" />
              <span>{t('runDetail.disconnected')}</span>
            </div>
          )}
        </div>
      </div>

      {restoreRequest?.phase && (
        <Card className="border-border bg-card/60 shadow-sm">
          <CardContent className="p-4 space-y-3">
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-[11px] text-muted-foreground">{t('runDetail.progress.phase')}</span>
              <StatusBadge tone={RESTORE_PHASE_TONES[restoreRequest.phase] ?? 'secondary'}>
                {t(`restore.phases.${restoreRequest.phase}`, { defaultValue: restoreRequest.phase })}
              </StatusBadge>
              {restoreRequest.rollback_snapshot_id && (
                <span className="text-[11px] text-muted-foreground font-mono">
                  {t('restore.rollbackSnapshot')}: {restoreRequest.rollback_snapshot_id}
                </span>
              )}
            </div>
            {restoreRequest.phase === 'cancelling' && (
              <p className="text-[11px] text-amber-600 dark:text-amber-400">{t('restore.cancelPending')}</p>
            )}
            {isRestorePhaseBlocking(restoreRequest.phase) && (
              <div className="space-y-2 border-t border-border pt-3">
                <p className="text-[11px] text-destructive">{t('restore.manualRecoveryHint')}</p>
                <p className="text-[11px] font-mono text-muted-foreground">
                  {t('restore.resolve.runId', { id })}
                </p>
                <p className="text-[11px] text-muted-foreground">{t('restore.resolve.message')}</p>
                <div className="space-y-1.5">
                  <span className="text-[11px] text-muted-foreground">{t('restore.resolve.note')}</span>
                  <Input
                    value={resolveNote}
                    onChange={(e) => setResolveNote(e.target.value)}
                    placeholder={t('restore.resolve.notePlaceholder')}
                    className="h-9 text-xs"
                  />
                </div>
                <div className="flex items-center gap-2">
                  <Checkbox
                    id="resolve-stopped"
                    checked={resolveStopped}
                    onCheckedChange={(checked) => setResolveStopped(checked === true)}
                  />
                  <label htmlFor="resolve-stopped" className="text-xs cursor-pointer">
                    {t('restore.resolve.confirmExecutionStopped')}
                  </label>
                </div>
                <div className="flex items-center gap-2">
                  <Checkbox
                    id="resolve-verified"
                    checked={resolveVerified}
                    onCheckedChange={(checked) => setResolveVerified(checked === true)}
                  />
                  <label htmlFor="resolve-verified" className="text-xs cursor-pointer">
                    {t('restore.resolve.confirmTargetVerified')}
                  </label>
                </div>
                <Button
                  type="button"
                  variant="destructive"
                  size="sm"
                  onClick={handleResolveRestore}
                  disabled={resolving || !resolveNote.trim() || !resolveStopped || !resolveVerified}
                  className="h-8 text-xs gap-1.5"
                >
                  {resolving ? (
                    <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
                  ) : (
                    <RotateCcw className="h-3.5 w-3.5" aria-hidden="true" />
                  )}
                  {t('restore.resolve.action')}
                </Button>
              </div>
            )}
          </CardContent>
        </Card>
      )}

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
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => copySnapshot(run.snapshot_id!)}
                className="h-6 p-0 font-mono text-xs text-primary hover:underline gap-1.5 justify-start"
                aria-label={t('snapshots.copySnapshotId')}
              >
                <span className="truncate">{run.snapshot_id.substring(0, 10)}...</span>
                {copiedSnapshot ? (
                  <Check className="h-3 w-3 text-emerald-600 dark:text-emerald-400" aria-hidden="true" />
                ) : (
                  <Copy className="h-3 w-3" aria-hidden="true" />
                )}
              </Button>
            ) : run.operation === 'backup' && !isTerminal(run.status) ? (
              <p className="text-xs text-muted-foreground">{t('runDetail.snapshotPending')}</p>
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
            <div className="space-y-1 col-span-2 sm:col-span-4 border-t border-border pt-3">
              <span className="text-[11px] text-destructive font-medium">{t('runDetail.labels.error')}</span>
              <div className="rounded bg-destructive/10 border border-destructive/20 p-2 text-xs font-mono text-destructive">
                <span className="font-bold">[{run.error_code}] </span>
                <span>{run.error_message || t('common.error_occurred')}</span>
              </div>
            </div>
          )}
        </CardContent>
      </Card>

      {/* Progress Card (When Available and active/populated) */}
      {run.progress && (run.progress.phase || (run.progress.percent ?? 0) > 0 || (run.progress.bytes_total ?? 0) > 0) && (
        <Card className="border-border bg-card/60 shadow-sm">
          <CardHeader className="pb-2">
            <div className="flex items-center justify-between">
              <CardTitle className="text-xs font-semibold">
                {t('runDetail.progress.title')}
              </CardTitle>
              <span className="text-xs font-mono font-medium text-primary">
                {percent.toFixed(1)}%
              </span>
            </div>
          </CardHeader>
          <CardContent className="space-y-3">
            <Progress value={percent} className="h-2 bg-muted" aria-label={t('runDetail.progress.title')} />
            <dl className="grid grid-cols-2 sm:grid-cols-4 gap-3 pt-1 text-xs">
              <div>
                <dt className="text-[11px] text-muted-foreground">{t('runDetail.progress.step')}</dt>
                <dd className="font-medium text-foreground">
                  {/* 阶段文案按 operation 选命名空间：备份阶段在 runDetail.phases、恢复阶段在
                      restore.phases，另一个作回退。此前统一查 runDetail.phases，导致恢复运行
                      的「当前步骤」渲染未本地化的英文枚举原值（如 succeeded）。 */}
                  {run.operation === 'restore'
                    ? translateEnum('restore.phases', run.progress.phase, 'runDetail.phases')
                    : translateEnum('runDetail.phases', run.progress.phase, 'restore.phases')}
                </dd>
              </div>
              <div>
                <dt className="text-[11px] text-muted-foreground">{t('runDetail.progress.bytesDone')}</dt>
                <dd className="font-mono text-foreground">{formatBytes(run.progress.bytes_done)}</dd>
              </div>
              <div>
                <dt className="text-[11px] text-muted-foreground">{t('runDetail.progress.bytesTotal')}</dt>
                <dd className="font-mono text-foreground">{formatBytes(run.progress.bytes_total)}</dd>
              </div>
              <div>
                <dt className="text-[11px] text-muted-foreground">{t('runDetail.progress.files')}</dt>
                <dd className="font-mono text-foreground">
                  {run.progress.files_done ?? '—'} / {run.progress.files_total ?? '—'}
                </dd>
              </div>
            </dl>
          </CardContent>
        </Card>
      )}

      {/* Terminal Log Console */}
      <Card className="border-border bg-card shadow-xl overflow-hidden">
        <CardHeader className="py-3 px-4 border-b border-border bg-muted/40 flex flex-row items-center justify-between space-y-0">
          <div className="flex items-center gap-2">
            <Terminal className="h-4 w-4 text-primary" aria-hidden="true" />
            <CardTitle className="text-xs font-mono font-medium text-foreground">
              {t('runDetail.logs.title', { count: logs.length })}
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
                {t('runDetail.logs.autoScroll')}
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
                  <Loader2 className="h-3 w-3 animate-spin" aria-hidden="true" />
                ) : (
                  <RotateCcw className="h-3 w-3" aria-hidden="true" />
                )}
                {t('runDetail.logs.loadMore')}
              </Button>
            )}
          </div>
        </CardHeader>
        <CardContent className="p-0">
          {/* 列宽单位 ch 与日志行共用 grid 容器基准的 text-xs，表头与行必须同一基准才能对齐。 */}
          <div className={`${LOG_GRID} px-5 py-1.5 border-b border-border bg-muted/20 text-muted-foreground`}>
            <span className="text-[11px]">{t('runDetail.logs.time')}</span>
            <span className="text-[11px]">{t('runDetail.logs.source')}</span>
            <span className="text-[11px]">{t('runDetail.logs.message')}</span>
          </div>
          <div
            ref={logsWrapRef}
            className="run-log-console h-96 overflow-y-auto px-4 py-2 space-y-0.5 text-foreground/90"
          >
            {logs.length > 0 ? (
              logs.map((log) => {
                const fromServer = log.source === 'server'
                const sourceLabel = fromServer
                  ? t('runDetail.logs.sourceServer')
                  : t('runDetail.logs.sourceAgent')
                const levelClass =
                  log.level === 'error'
                    ? 'text-rose-600 dark:text-rose-400 font-medium'
                    : log.level === 'warn'
                      ? 'text-amber-600 dark:text-amber-400'
                      : 'text-foreground'
                return (
                  <div key={log.id} className={`log-row ${LOG_GRID} rounded px-1 py-0.5 hover:bg-muted/50`}>
                    <time dateTime={log.timestamp} className="text-[11px] text-muted-foreground/80 tabular-nums whitespace-nowrap">
                      {formatLogTime(log.timestamp)}
                    </time>
                    <span
                      className={
                        fromServer
                          ? 'text-[11px] font-semibold text-sky-600 dark:text-sky-400'
                          : 'text-[11px] font-medium text-muted-foreground'
                      }
                    >
                      {sourceLabel}
                    </span>
                    <Tooltip>
                      <TooltipTrigger asChild>
                        <span tabIndex={0} className={`block truncate ${levelClass}`}>
                          {log.message}
                        </span>
                      </TooltipTrigger>
                      <TooltipContent side="top" className="max-w-sm max-h-64 overflow-y-auto whitespace-pre-wrap break-words font-mono text-xs">
                        {log.message}
                      </TooltipContent>
                    </Tooltip>
                  </div>
                )
              })
            ) : (
              <div className="flex h-full items-center justify-center text-xs text-muted-foreground">
                <FileText className="h-4 w-4 mr-2 opacity-50" aria-hidden="true" />
                <span>{t('runDetail.logs.empty')}</span>
              </div>
            )}
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
