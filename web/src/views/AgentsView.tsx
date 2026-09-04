import React, { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Card, CardContent } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { apiGet, apiPost, apiDelete, apiPatch, isApiClientError } from '@/api/client'
import { translateEnum, formatDateTime } from '@/i18n'
import type { Agent, EnrollmentTokenResponse } from '@/api/types'
import { AppEmptyState } from '@/components/AppEmptyState'
import { AppErrorState } from '@/components/AppErrorState'
import { StatusBadge } from '@/components/StatusBadge'
import { PageLoadingState } from '@/components/PageLoadingState'
import { ConfirmActionDialog } from '@/components/ConfirmActionDialog'
import { toastSuccess, toastError } from '@/lib/toast'
import {
  Key,
  RefreshCw,
  Edit2,
  FileText,
  Trash2,
  ChevronDown,
  ChevronRight,
  Copy,
  Check,
  X,
  Loader2,
  AlertTriangle,
  ShieldAlert,
} from 'lucide-react'

export const AgentsView: React.FC = () => {
  const { t } = useTranslation()
  const navigate = useNavigate()

  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [agents, setAgents] = useState<Agent[]>([])
  const [expandedRows, setExpandedRows] = useState<Set<string>>(new Set())

  // Token Dialog
  const [tokenDialogOpen, setTokenDialogOpen] = useState(false)
  const [tokenLoading, setTokenLoading] = useState(false)
  const [tokenData, setTokenData] = useState<EnrollmentTokenResponse | null>(null)
  const [copied, setCopied] = useState(false)

  // Rename Dialog
  const [renameDialogOpen, setRenameDialogOpen] = useState(false)
  const [targetAgent, setTargetAgent] = useState<Agent | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [renaming, setRenaming] = useState(false)

  // Revoke Dialog
  const [revokeDialogOpen, setRevokeDialogOpen] = useState(false)
  const [agentToRevoke, setAgentToRevoke] = useState<Agent | null>(null)

  const loadAgents = async () => {
    setLoading(true)
    setError(null)
    try {
      const data = await apiGet<Agent[]>('/agents')
      setAgents(data)
    } catch (err: unknown) {
      setError(isApiClientError(err) ? err.message : t('agents.loadFailed'))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    loadAgents()
  }, [])

  const toggleExpand = (id: string) => {
    setExpandedRows((prev) => {
      const next = new Set(prev)
      if (next.has(id)) {
        next.delete(id)
      } else {
        next.add(id)
      }
      return next
    })
  }

  const handleGenerateToken = async () => {
    setTokenDialogOpen(true)
    setTokenLoading(true)
    setTokenData(null)
    setCopied(false)
    try {
      const res = await apiPost<EnrollmentTokenResponse>('/enrollment-tokens', {})
      setTokenData(res)
    } catch (err: unknown) {
      toastError(isApiClientError(err) ? err.message : t('agents.tokenDialog.generateFailed'))
      setTokenDialogOpen(false)
    } finally {
      setTokenLoading(false)
    }
  }

  const handleTakeover = async (agent: Agent) => {
    setTokenDialogOpen(true)
    setTokenLoading(true)
    setTokenData(null)
    setCopied(false)
    try {
      const res = await apiPost<EnrollmentTokenResponse>('/enrollment-tokens', {
        target_agent_id: agent.id,
      })
      setTokenData(res)
    } catch (err: unknown) {
      toastError(isApiClientError(err) ? err.message : t('agents.tokenDialog.generateFailed'))
    } finally {
      setTokenLoading(false)
    }
  }

  const copyToken = async () => {
    if (!tokenData) return
    try {
      await navigator.clipboard.writeText(tokenData.token)
      setCopied(true)
      toastSuccess(t('common.copied'))
      setTimeout(() => setCopied(false), 2000)
    } catch {
      toastError(t('common.copyFailed'))
    }
  }

  const openRename = (agent: Agent) => {
    setTargetAgent(agent)
    setRenameValue(agent.name)
    setRenameDialogOpen(true)
  }

  const handleRenameConfirm = async (e: React.SyntheticEvent) => {
    e.preventDefault()
    if (!targetAgent || !renameValue.trim()) return
    setRenaming(true)
    try {
      await apiPatch<Agent>(`/agents/${targetAgent.id}`, { name: renameValue.trim() })
      toastSuccess(t('agents.renamed'))
      setRenameDialogOpen(false)
      await loadAgents()
    } catch (err: unknown) {
      toastError(isApiClientError(err) ? err.message : t('agents.renameFailed'))
    } finally {
      setRenaming(false)
    }
  }

  const openRevoke = (agent: Agent) => {
    setAgentToRevoke(agent)
    setRevokeDialogOpen(true)
  }

  const handleRevokeConfirm = async () => {
    if (!agentToRevoke) return
    try {
      await apiDelete(`/agents/${agentToRevoke.id}`)
      toastSuccess(t('agents.revoked'))
      await loadAgents()
    } catch (err: unknown) {
      toastError(isApiClientError(err) ? err.message : t('agents.revokeFailed'))
    }
  }

  if (loading) {
    return <PageLoadingState />
  }

  if (error) {
    return (
      <AppErrorState
        title={t('agents.title')}
        message={error}
        onRetry={loadAgents}
      />
    )
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
        <div>
          <h2 className="text-xl font-bold tracking-tight text-foreground">
            {t('agents.title')}
          </h2>
          <p className="text-xs text-muted-foreground">
            {t('agents.subtitle')}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            onClick={handleGenerateToken}
            className="h-8 text-xs gap-1.5 bg-primary text-primary-foreground"
          >
            <Key className="h-3.5 w-3.5" aria-hidden="true" />
            {t('agents.generateToken')}
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={loadAgents}
            className="h-8 text-xs gap-1.5"
          >
            <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
            {t('common.refresh')}
          </Button>
        </div>
      </div>

      <Card className="border-border bg-card/60 shadow-sm">
        <CardContent className="p-0">
          {agents.length > 0 ? (
            <div className="rounded-md overflow-hidden">
              {/* Desktop Table */}
              <div className="hidden md:block overflow-x-auto">
                <Table className="min-w-[850px]">
                  <TableHeader>
                    <TableRow className="border-border hover:bg-transparent">
                      <TableHead className="w-9"></TableHead>
                      <TableHead className="text-xs font-medium">{t('common.name')}</TableHead>
                      <TableHead className="text-xs font-medium">{t('agents.columns.hostname')}</TableHead>
                      <TableHead className="text-xs font-medium">{t('agents.columns.os')}</TableHead>
                      <TableHead className="text-xs font-medium">{t('agents.columns.arch')}</TableHead>
                      <TableHead className="text-xs font-medium">{t('agents.columns.version')}</TableHead>
                      <TableHead className="text-xs font-medium">{t('common.status')}</TableHead>
                      <TableHead className="text-xs font-medium">{t('agents.columns.lastSeen')}</TableHead>
                      <TableHead className="text-xs font-medium">{t('agents.columns.enrolledAt')}</TableHead>
                      <TableHead className="text-xs font-medium text-right">{t('common.actions')}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {agents.map((agent) => {
                      const isExpanded = expandedRows.has(agent.id)
                      return (
                        <React.Fragment key={agent.id}>
                          <TableRow className="border-border hover:bg-muted/30">
                            <TableCell className="p-2">
                              <Button
                                variant="ghost"
                                size="icon"
                                className="h-6 w-6 text-muted-foreground"
                                onClick={() => toggleExpand(agent.id)}
                                aria-label={t('agents.toggleExpand')}
                              >
                                {isExpanded ? (
                                  <ChevronDown className="h-3.5 w-3.5" aria-hidden="true" />
                                ) : (
                                  <ChevronRight className="h-3.5 w-3.5" aria-hidden="true" />
                                )}
                              </Button>
                            </TableCell>
                            <TableCell className="font-medium text-xs text-foreground">
                              <div className="flex items-center gap-1.5">
                                <span>{agent.name}</span>
                                <Button
                                  variant="ghost"
                                  size="icon"
                                  className="h-5 w-5 text-muted-foreground hover:text-foreground"
                                  onClick={() => openRename(agent)}
                                  title={t('agents.rename')}
                                  aria-label={t('agents.rename')}
                                >
                                  <Edit2 className="h-3 w-3" aria-hidden="true" />
                                </Button>
                              </div>
                            </TableCell>
                            <TableCell className="text-xs text-muted-foreground">
                              {agent.hostname}
                            </TableCell>
                            <TableCell className="text-xs text-muted-foreground">{agent.os}</TableCell>
                            <TableCell className="text-xs text-muted-foreground">{agent.arch}</TableCell>
                            <TableCell className="text-xs">
                              <span className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">
                                {agent.version}
                              </span>
                            </TableCell>
                            <TableCell className="text-xs">
                              {agent.revoked ? (
                                <StatusBadge tone="secondary">
                                  {t('status.revoked')}
                                </StatusBadge>
                              ) : (
                                <StatusBadge
                                  tone={agent.status === 'online' ? 'success' : 'destructive'}
                                  dot
                                >
                                  {translateEnum('status', agent.status)}
                                </StatusBadge>
                              )}
                            </TableCell>
                            <TableCell className="text-xs text-muted-foreground">
                              {formatDateTime(agent.last_seen_at)}
                            </TableCell>
                            <TableCell className="text-xs text-muted-foreground">
                              {formatDateTime(agent.enrolled_at)}
                            </TableCell>
                            <TableCell className="text-xs text-right">
                              <div className="flex items-center justify-end gap-1">
                                <Button
                                  variant="ghost"
                                  size="sm"
                                  className="h-7 text-xs text-primary gap-1"
                                  onClick={() => navigate(`/logs?agent_id=${agent.id}`)}
                                >
                                  <FileText className="h-3.5 w-3.5" aria-hidden="true" />
                                  {t('agents.viewLogs')}
                                </Button>
                                {agent.status === 'offline' && !agent.revoked && (
                                  <Button
                                    variant="ghost"
                                    size="sm"
                                    className="h-7 text-xs text-amber-600 dark:text-amber-400 hover:text-amber-700 dark:hover:text-amber-300 gap-1"
                                    onClick={() => handleTakeover(agent)}
                                  >
                                    <ShieldAlert className="h-3.5 w-3.5" aria-hidden="true" />
                                    {t('agents.takeover')}
                                  </Button>
                                )}
                                <Button
                                  variant="ghost"
                                  size="sm"
                                  className="h-7 text-xs text-rose-600 dark:text-rose-400 hover:text-rose-700 dark:hover:text-rose-300 gap-1"
                                  disabled={agent.revoked}
                                  onClick={() => openRevoke(agent)}
                                  aria-label={t('agents.revoke')}
                                >
                                  <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
                                  {t('agents.revoke')}
                                </Button>
                              </div>
                            </TableCell>
                          </TableRow>

                          {/* Capabilities Expand Area */}
                          {isExpanded && (
                            <TableRow className="bg-muted/10 border-border">
                              <TableCell colSpan={10} className="p-4 pl-12">
                                <div className="space-y-3">
                                  <span className="text-xs font-semibold text-foreground tracking-tight">
                                    {t('agents.capabilities.title')}
                                  </span>
                                  {agent.capabilities && agent.capabilities.length > 0 ? (
                                    <div className="rounded border border-border/80 overflow-hidden max-w-2xl bg-card/40">
                                      <Table>
                                        <TableHeader>
                                          <TableRow className="border-border">
                                            <TableHead className="text-[11px] h-8 font-medium">
                                              {t('agents.capabilities.tool')}
                                            </TableHead>
                                            <TableHead className="text-[11px] h-8 font-medium">
                                              {t('agents.columns.version')}
                                            </TableHead>
                                            <TableHead className="text-[11px] h-8 font-medium">
                                              {t('agents.capabilities.path')}
                                            </TableHead>
                                          </TableRow>
                                        </TableHeader>
                                        <TableBody>
                                          {agent.capabilities.map((cap) => (
                                            <TableRow key={cap.name} className="border-border">
                                              <TableCell className="text-xs font-medium py-1.5">
                                                {cap.name}
                                              </TableCell>
                                              <TableCell className="text-xs text-muted-foreground py-1.5">
                                                {cap.version || '—'}
                                              </TableCell>
                                              <TableCell className="text-xs font-mono text-muted-foreground py-1.5">
                                                {cap.path || '—'}
                                              </TableCell>
                                            </TableRow>
                                          ))}
                                        </TableBody>
                                      </Table>
                                    </div>
                                  ) : (
                                    <p className="text-xs text-muted-foreground italic">
                                      {t('agents.capabilities.empty')}
                                    </p>
                                  )}
                                </div>
                              </TableCell>
                            </TableRow>
                          )}
                        </React.Fragment>
                      )
                    })}
                  </TableBody>
                </Table>
              </div>

              {/* Mobile Cards */}
              <div className="md:hidden divide-y divide-border">
                {agents.map((agent) => (
                  <div key={agent.id} className="p-3 space-y-2 text-xs">
                    <div className="flex items-center justify-between gap-2">
                      <div className="flex items-center gap-1.5 font-semibold text-foreground">
                        <span>{agent.name}</span>
                        <Button
                          variant="ghost"
                          size="icon"
                          className="h-5 w-5 text-muted-foreground hover:text-foreground"
                          onClick={() => openRename(agent)}
                          aria-label={t('agents.rename')}
                        >
                          <Edit2 className="h-3 w-3" aria-hidden="true" />
                        </Button>
                      </div>
                      {agent.revoked ? (
                        <StatusBadge tone="secondary">
                          {t('status.revoked')}
                        </StatusBadge>
                      ) : (
                        <StatusBadge
                          tone={agent.status === 'online' ? 'success' : 'destructive'}
                          dot
                        >
                          {translateEnum('status', agent.status)}
                        </StatusBadge>
                      )}
                    </div>
                    <div className="grid grid-cols-2 gap-1 text-[11px] text-muted-foreground font-mono">
                      <div>
                        <span>Host: </span>{agent.hostname}
                      </div>
                      <div className="text-right">
                        <span>v</span>{agent.version}
                      </div>
                    </div>
                    <div className="flex items-center justify-end gap-2 pt-1">
                      <Button
                        variant="ghost"
                        size="sm"
                        className="h-7 text-xs text-primary gap-1"
                        onClick={() => navigate(`/logs?agent_id=${agent.id}`)}
                      >
                        <FileText className="h-3.5 w-3.5" aria-hidden="true" />
                        {t('agents.viewLogs')}
                      </Button>
                      {agent.status === 'offline' && !agent.revoked && (
                        <Button
                          variant="ghost"
                          size="sm"
                          className="h-7 text-xs text-amber-600 dark:text-amber-400 hover:text-amber-700 dark:hover:text-amber-300 gap-1"
                          onClick={() => handleTakeover(agent)}
                        >
                          <ShieldAlert className="h-3.5 w-3.5" aria-hidden="true" />
                          {t('agents.takeover')}
                        </Button>
                      )}
                      <Button
                        variant="ghost"
                        size="sm"
                        className="h-7 text-xs text-rose-600 dark:text-rose-400 hover:text-rose-700 dark:hover:text-rose-300 gap-1"
                        disabled={agent.revoked}
                        onClick={() => openRevoke(agent)}
                        aria-label={t('agents.revoke')}
                      >
                        <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
                        {t('agents.revoke')}
                      </Button>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          ) : (
            <div className="p-8">
              <AppEmptyState
                title={t('agents.noAgents')}
                description={t('agents.noAgents_desc')}
              />
            </div>
          )}
        </CardContent>
      </Card>

      {/* Enrollment Token Dialog */}
      <Dialog open={tokenDialogOpen} onOpenChange={setTokenDialogOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle className="text-sm font-semibold">
              {t('agents.tokenDialog.title')}
            </DialogTitle>
            <DialogDescription className="text-xs">
              {t('agents.tokenDialog.subtitle')}
            </DialogDescription>
          </DialogHeader>
          {tokenLoading ? (
            <div className="flex h-32 items-center justify-center">
              <Loader2 className="h-6 w-6 animate-spin text-primary" aria-hidden="true" />
            </div>
          ) : tokenData ? (
            <div className="space-y-4 pt-2">
              <Alert className="border-amber-500/30 bg-amber-500/10 text-amber-600 dark:text-amber-400 py-2.5">
                <AlertTriangle className="h-4 w-4 text-amber-600 dark:text-amber-400" aria-hidden="true" />
                <AlertDescription className="text-xs">
                  {t('agents.tokenDialog.onceWarning')}
                </AlertDescription>
              </Alert>

              {tokenData.target_agent_id && (
                <Alert className="border-sky-500/30 bg-sky-500/10 text-sky-600 dark:text-sky-400 py-2.5">
                  <AlertDescription className="text-xs">
                    {t('agents.tokenDialog.takeoverHint', { id: tokenData.target_agent_id })}
                  </AlertDescription>
                </Alert>
              )}
              <div className="space-y-1.5">
                <span className="text-xs font-medium text-muted-foreground">
                  {t('agents.tokenDialog.token')}
                </span>
                <Button
                  type="button"
                  variant="ghost"
                  onClick={copyToken}
                  className="w-full flex items-center justify-between rounded-md border border-border bg-muted/40 px-3 py-2 font-mono text-xs text-foreground hover:bg-muted/70 transition-colors h-auto text-left"
                  aria-label={t('agents.copyToken')}
                >
                  <span className="truncate pr-2">{tokenData.token}</span>
                  {copied ? (
                    <Check className="h-4 w-4 text-emerald-600 dark:text-emerald-400 shrink-0" aria-hidden="true" />
                  ) : (
                    <Copy className="h-4 w-4 text-muted-foreground shrink-0" aria-hidden="true" />
                  )}
                </Button>
              </div>

              {tokenData.target_agent_id && (
                <div className="space-y-1.5">
                  <span className="text-xs font-medium text-muted-foreground">
                    {t('agents.tokenDialog.envConfig')}
                  </span>
                  <div className="rounded-md border border-border bg-muted/40 p-2.5 font-mono text-xs text-foreground leading-relaxed select-all">
                    <div>BMC_TARGET_AGENT_ID={tokenData.target_agent_id}</div>
                    <div>BMC_ENROLLMENT_TOKEN={tokenData.token}</div>
                  </div>
                </div>
              )}
              <div className="text-xs text-muted-foreground">
                <span className="font-medium text-foreground">
                  {t('agents.tokenDialog.expiresAt')}:{' '}
                </span>
                {formatDateTime(tokenData.expires_at)}
              </div>

              <DialogFooter className="pt-2">
                <Button onClick={copyToken} className="w-full h-8 text-xs gap-1.5">
                  {copied ? <Check className="h-3.5 w-3.5" aria-hidden="true" /> : <Copy className="h-3.5 w-3.5" aria-hidden="true" />}
                  {t('agents.tokenDialog.copyButton')}
                </Button>
              </DialogFooter>
            </div>
          ) : null}
        </DialogContent>
      </Dialog>

      {/* Rename Dialog */}
      <Dialog open={renameDialogOpen} onOpenChange={setRenameDialogOpen}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle className="text-sm font-semibold">
              {t('agents.renameDialog.title')}
            </DialogTitle>
            <DialogDescription className="text-xs">
              {t('agents.renameDialog.message')}
            </DialogDescription>
          </DialogHeader>
          <form onSubmit={handleRenameConfirm} className="space-y-4">
            <Input
              value={renameValue}
              onChange={(e) => setRenameValue(e.target.value)}
              disabled={renaming}
              className="h-9 text-xs"
              autoFocus
            />
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => setRenameDialogOpen(false)}
                disabled={renaming}
                className="h-8 text-xs gap-1.5"
              >
                <X className="h-3.5 w-3.5" aria-hidden="true" />
                {t('common.cancel')}
              </Button>
              <Button
                type="submit"
                size="sm"
                disabled={renaming || !renameValue.trim()}
                className="h-8 text-xs gap-1.5"
              >
                {renaming ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
                ) : (
                  <Check className="h-3.5 w-3.5" aria-hidden="true" />
                )}
                {t('common.save')}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      {/* Revoke Confirm Dialog */}
      <ConfirmActionDialog
        open={revokeDialogOpen}
        onOpenChange={setRevokeDialogOpen}
        title={t('agents.revokeDialog.title')}
        description={
          agentToRevoke
            ? t('agents.revokeDialog.confirm', {
                name: agentToRevoke.name,
                hostname: agentToRevoke.hostname,
              })
            : ''
        }
        destructive
        onConfirm={handleRevokeConfirm}
      />
    </div>
  )
}
