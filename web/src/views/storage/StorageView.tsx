import React, { useEffect, useState, useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Card, CardContent } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { apiGet, apiPost, apiDelete, apiPatch } from '@/api/client'
import { translateEnum, formatDateTime } from '@/i18n'
import type {
  StorageTarget,
  Repository,
  Agent,
  StorageTargetValidateResponse,
  ApiError,
} from '@/api/types'
import { AppEmptyState } from '@/components/AppEmptyState'
import { AppErrorState } from '@/components/AppErrorState'
import { StatusBadge, type BadgeTone } from '@/components/StatusBadge'
import { ConfirmActionDialog } from '@/components/ConfirmActionDialog'
import { toastSuccess, toastError } from '@/lib/toast'
import {
  Folder,
  Upload,
  RefreshCw,
  Edit2,
  Trash2,
  Link,
  Search,
  Loader2,
  FileCode,
  CheckCircle2,
  Info,
  RotateCcw,
  Unlink,
  X,
  Save,
  Check,
} from 'lucide-react'

export const StorageView: React.FC = () => {
  const { t } = useTranslation()

  const [activeTab, setActiveTab] = useState<'targets' | 'repos'>('targets')

  // Data
  const [targets, setTargets] = useState<StorageTarget[]>([])
  const [targetsLoading, setTargetsLoading] = useState(true)
  const [targetsError, setTargetsError] = useState<string | null>(null)

  const [repos, setRepos] = useState<Repository[]>([])
  const [reposLoading, setReposLoading] = useState(true)
  const [reposError, setReposError] = useState<string | null>(null)

  const [agents, setAgents] = useState<Agent[]>([])
  const [repoActionLoading, setRepoActionLoading] = useState<Record<string, boolean>>({})

  // Import Dialog State
  const [importDialogOpen, setImportDialogOpen] = useState(false)
  const [importLoading, setImportLoading] = useState(false)
  const [validateLoading, setValidateLoading] = useState(false)
  const [validateResult, setValidateResult] = useState<StorageTargetValidateResponse | null>(null)
  const [importForm, setImportForm] = useState({
    name: '',
    rclone_conf: '',
    validate_agent_id: '',
    remote_name: '',
    remote_path: '',
  })

  // Rename Target Dialog State
  const [editTargetDialogOpen, setEditTargetDialogOpen] = useState(false)
  const [targetToEdit, setTargetToEdit] = useState<StorageTarget | null>(null)
  const [targetNewName, setTargetNewName] = useState('')
  const [editTargetLoading, setEditTargetLoading] = useState(false)

  // Delete Target Dialog
  const [deleteTargetDialogOpen, setDeleteTargetDialogOpen] = useState(false)
  const [targetToDelete, setTargetToDelete] = useState<StorageTarget | null>(null)

  // Bind Repo Dialog State
  const [bindDialogOpen, setBindDialogOpen] = useState(false)
  const [bindLoading, setBindLoading] = useState(false)
  const [bindResult, setBindResult] = useState<Repository | null>(null)
  const [bindForm, setBindForm] = useState({
    agent_id: '',
    storage_target_id: '',
  })

  // Unbind Repo Dialog
  const [unbindDialogOpen, setUnbindDialogOpen] = useState(false)
  const [repoToUnbind, setRepoToUnbind] = useState<Repository | null>(null)

  const onlineAgents = useMemo(() => agents.filter((a) => a.status === 'online' && !a.revoked), [agents])
  const offlineAgents = useMemo(() => agents.filter((a) => a.status !== 'online' || a.revoked), [agents])

  const loadTargets = async () => {
    setTargetsLoading(true)
    setTargetsError(null)
    try {
      const data = await apiGet<StorageTarget[]>('/storage-targets')
      setTargets(data)
    } catch (err: unknown) {
      const apiErr = err as ApiError
      setTargetsError(apiErr?.message || t('storage.loadTargetsFailed') || 'Failed to load targets')
    } finally {
      setTargetsLoading(false)
    }
  }

  const loadRepos = async () => {
    setReposLoading(true)
    setReposError(null)
    try {
      const data = await apiGet<Repository[]>('/repositories')
      setRepos(data)
    } catch (err: unknown) {
      const apiErr = err as ApiError
      setReposError(apiErr?.message || t('storage.loadReposFailed') || 'Failed to load repositories')
    } finally {
      setReposLoading(false)
    }
  }

  const loadAgents = async () => {
    try {
      const data = await apiGet<Agent[]>('/agents')
      setAgents(data)
    } catch {
      // Non-blocking for storage tab
    }
  }

  useEffect(() => {
    loadTargets()
    loadRepos()
    loadAgents()
  }, [])

  // Auto-fill validate agent
  useEffect(() => {
    if (onlineAgents.length > 0 && !importForm.validate_agent_id) {
      setImportForm((prev) => ({ ...prev, validate_agent_id: onlineAgents[0].id }))
    }
  }, [onlineAgents, importForm.validate_agent_id])

  const openImportDialog = () => {
    setImportForm({
      name: '',
      rclone_conf: '',
      validate_agent_id: onlineAgents[0]?.id || '',
      remote_name: '',
      remote_path: '',
    })
    setValidateResult(null)
    setImportDialogOpen(true)
  }

  const handleValidate = async () => {
    if (!importForm.rclone_conf || !importForm.remote_name || !importForm.validate_agent_id) return
    setValidateLoading(true)
    setValidateResult(null)
    try {
      const res = await apiPost<StorageTargetValidateResponse>('/storage-targets/validate', {
        rclone_conf: importForm.rclone_conf,
        remote_name: importForm.remote_name,
        validate_agent_id: importForm.validate_agent_id,
      })
      setValidateResult(res)
      toastSuccess(t('storage.importDialog.validationSuccess') || 'Configuration validated successfully')
    } catch (err: unknown) {
      const apiErr = err as ApiError
      toastError(apiErr?.message || t('storage.importDialog.validationFailed') || 'Validation failed')
    } finally {
      setValidateLoading(false)
    }
  }

  const handleImport = async () => {
    if (!importForm.name || !importForm.rclone_conf || !importForm.remote_name || !importForm.validate_agent_id) return
    setImportLoading(true)
    try {
      await apiPost('/storage-targets', {
        name: importForm.name.trim(),
        rclone_conf: importForm.rclone_conf,
        validate_agent_id: importForm.validate_agent_id,
        remote_name: importForm.remote_name.trim(),
        remote_path: importForm.remote_path.trim(),
      })
      toastSuccess(t('storage.importDialog.importedSuccessfully') || 'Storage target imported successfully')
      setImportDialogOpen(false)
      await loadTargets()
    } catch (err: unknown) {
      const apiErr = err as ApiError
      toastError(apiErr?.message || t('storage.importDialog.importFailed') || 'Import failed')
    } finally {
      setImportLoading(false)
    }
  }

  const openEditTargetDialog = (target: StorageTarget) => {
    setTargetToEdit(target)
    setTargetNewName(target.name)
    setEditTargetDialogOpen(true)
  }

  const handleRenameTarget = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!targetToEdit || !targetNewName.trim()) return
    setEditTargetLoading(true)
    try {
      await apiPatch(`/storage-targets/${targetToEdit.id}`, { name: targetNewName.trim() })
      toastSuccess(t('storage.editDialog.renamedSuccessfully') || 'Target renamed successfully')
      setEditTargetDialogOpen(false)
      await loadTargets()
    } catch (err: unknown) {
      const apiErr = err as ApiError
      toastError(apiErr?.message || t('storage.editDialog.renameFailed') || 'Failed to rename target')
    } finally {
      setEditTargetLoading(false)
    }
  }

  const openDeleteTarget = (target: StorageTarget) => {
    setTargetToDelete(target)
    setDeleteTargetDialogOpen(true)
  }

  const handleDeleteTargetConfirm = async () => {
    if (!targetToDelete) return
    try {
      await apiDelete(`/storage-targets/${targetToDelete.id}`)
      toastSuccess(t('storage.deletedSuccessfully') || 'Storage target deleted')
      await loadTargets()
    } catch (err: unknown) {
      const apiErr = err as ApiError
      toastError(apiErr?.message || t('storage.deleteFailed') || 'Failed to delete target')
    }
  }

  const openBindRepoDialog = () => {
    setBindForm({
      agent_id: onlineAgents[0]?.id || '',
      storage_target_id: targets[0]?.id || '',
    })
    setBindResult(null)
    setBindDialogOpen(true)
  }

  const handleBindRepo = async () => {
    if (!bindForm.agent_id || !bindForm.storage_target_id) return
    setBindLoading(true)
    try {
      const res = await apiPost<Repository>('/repositories', {
        agent_id: bindForm.agent_id,
        storage_target_id: bindForm.storage_target_id,
      })
      setBindResult(res)
      toastSuccess(t('storage.bindDialog.boundSuccessfully') || 'Repository bound successfully')
      await loadRepos()
    } catch (err: unknown) {
      const apiErr = err as ApiError
      toastError(apiErr?.message || t('storage.bindDialog.bindFailed') || 'Failed to bind repository')
    } finally {
      setBindLoading(false)
    }
  }

  const handleRetryRepo = async (repo: Repository) => {
    setRepoActionLoading((prev) => ({ ...prev, [repo.id]: true }))
    try {
      await apiPost(`/repositories/${repo.id}/retry`, {})
      toastSuccess(t('storage.repositoryDialog.retryDispatched') || 'Retry dispatched')
      await loadRepos()
    } catch (err: unknown) {
      const apiErr = err as ApiError
      toastError(apiErr?.message || t('storage.repositoryDialog.retryFailed') || 'Retry failed')
    } finally {
      setRepoActionLoading((prev) => ({ ...prev, [repo.id]: false }))
    }
  }

  const openUnbindRepo = (repo: Repository) => {
    setRepoToUnbind(repo)
    setUnbindDialogOpen(true)
  }

  const handleUnbindRepoConfirm = async () => {
    if (!repoToUnbind) return
    setRepoActionLoading((prev) => ({ ...prev, [repoToUnbind.id]: true }))
    try {
      await apiDelete(`/repositories/${repoToUnbind.id}`)
      toastSuccess(t('storage.repositoryDialog.unboundSuccessfully') || 'Repository unbound')
      await loadRepos()
    } catch (err: unknown) {
      const apiErr = err as ApiError
      toastError(apiErr?.message || t('storage.repositoryDialog.unbindFailed') || 'Unbind failed')
    } finally {
      setRepoActionLoading((prev) => ({ ...prev, [repoToUnbind.id]: false }))
    }
  }

  const getRepoStatusTone = (status: string): BadgeTone => {
    if (status === 'ready') return 'success'
    if (status === 'error') return 'destructive'
    return 'warning'
  }

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-xl font-bold tracking-tight text-foreground">
          {t('nav.storage')}
        </h2>
        <p className="text-xs text-muted-foreground">
          {t('storage.subtitle') || 'Manage rclone cloud targets and host repository bindings'}
        </p>
      </div>

      <Tabs value={activeTab} onValueChange={(val) => setActiveTab(val as 'targets' | 'repos')} className="w-full">
        <TabsList className="bg-muted/50 p-1 border border-border">
          <TabsTrigger value="targets" className="text-xs gap-2">
            <Folder className="h-3.5 w-3.5" />
            <span>{t('storage.tabs.targets')}</span>
          </TabsTrigger>
          <TabsTrigger value="repos" className="text-xs gap-2">
            <Link className="h-3.5 w-3.5" />
            <span>{t('storage.tabs.repositories')}</span>
          </TabsTrigger>
        </TabsList>

        {/* TARGETS TAB */}
        <TabsContent value="targets" className="space-y-4 pt-2">
          <div className="flex items-center justify-between">
            <span className="text-xs text-muted-foreground">
              {t('storage.targets_count', { count: targets.length }) || `Total targets: ${targets.length}`}
            </span>
            <div className="flex items-center gap-2">
              <Button size="sm" onClick={openImportDialog} className="h-8 text-xs gap-1.5 bg-primary text-primary-foreground">
                <Upload className="h-3.5 w-3.5" />
                {t('storage.importRcloneConfig')}
              </Button>
              <Button variant="outline" size="sm" onClick={loadTargets} className="h-8 text-xs gap-1.5">
                <RefreshCw className="h-3.5 w-3.5" />
                {t('common.refresh')}
              </Button>
            </div>
          </div>

          {targetsError ? (
            <AppErrorState
              title={t('storage.tabs.targets')}
              message={targetsError}
              onRetry={loadTargets}
            />
          ) : (
            <Card className="border-border bg-card/60 shadow-sm">
              <CardContent className="p-0">
                {targetsLoading ? (
                  <div className="flex h-48 items-center justify-center">
                    <Loader2 className="h-6 w-6 animate-spin text-primary" />
                  </div>
                ) : targets.length > 0 ? (
                  <div className="rounded-md overflow-hidden">
                    <Table>
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
                                  onClick={() => openEditTargetDialog(tgt)}
                                >
                                  <Edit2 className="h-3 w-3" />
                                  {t('common.edit')}
                                </Button>
                                <Button
                                  variant="ghost"
                                  size="sm"
                                  className="h-7 text-xs text-rose-400 hover:text-rose-300 gap-1"
                                  onClick={() => openDeleteTarget(tgt)}
                                >
                                  <Trash2 className="h-3 w-3" />
                                  {t('common.delete')}
                                </Button>
                              </div>
                            </TableCell>
                          </TableRow>
                        ))}
                      </TableBody>
                    </Table>
                  </div>
                ) : (
                  <div className="p-8">
                    <AppEmptyState
                      title={t('storage.emptyTargets') || 'No Storage Targets'}
                      description={
                        t('storage.emptyTargets_desc') ||
                        'Import an rclone.conf configuration to set up cloud storage targets.'
                      }
                    />
                  </div>
                )}
              </CardContent>
            </Card>
          )}
        </TabsContent>

        {/* REPOSITORIES TAB */}
        <TabsContent value="repos" className="space-y-4 pt-2">
          <div className="flex items-center justify-between">
            <span className="text-xs text-muted-foreground">
              {t('storage.repos_count', { count: repos.length }) || `Total repositories: ${repos.length}`}
            </span>
            <div className="flex items-center gap-2">
              <Button
                size="sm"
                onClick={openBindRepoDialog}
                disabled={targets.length === 0 || agents.length === 0}
                className="h-8 text-xs gap-1.5 bg-primary text-primary-foreground"
              >
                <Link className="h-3.5 w-3.5" />
                {t('storage.bindRepository')}
              </Button>
              <Button variant="outline" size="sm" onClick={loadRepos} className="h-8 text-xs gap-1.5">
                <RefreshCw className="h-3.5 w-3.5" />
                {t('common.refresh')}
              </Button>
            </div>
          </div>

          <Alert className="border-border/80 bg-muted/20 py-2.5 text-xs text-muted-foreground">
            <Info className="h-4 w-4 text-primary" />
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
              onRetry={loadRepos}
            />
          ) : (
            <Card className="border-border bg-card/60 shadow-sm">
              <CardContent className="p-0">
                {reposLoading ? (
                  <div className="flex h-48 items-center justify-center">
                    <Loader2 className="h-6 w-6 animate-spin text-primary" />
                  </div>
                ) : repos.length > 0 ? (
                  <div className="rounded-md overflow-hidden">
                    <Table>
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
                                      onClick={() => handleRetryRepo(repo)}
                                    >
                                      {actionLoading ? (
                                        <Loader2 className="h-3 w-3 animate-spin" />
                                      ) : (
                                        <RotateCcw className="h-3 w-3" />
                                      )}
                                      {t('common.retry')}
                                    </Button>
                                  )}
                                  <Button
                                    variant="ghost"
                                    size="sm"
                                    disabled={actionLoading}
                                    className="h-7 text-xs text-rose-400 hover:text-rose-300 gap-1"
                                    onClick={() => openUnbindRepo(repo)}
                                  >
                                    <Unlink className="h-3 w-3" />
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
                ) : (
                  <div className="p-8">
                    <AppEmptyState
                      title={t('storage.emptyRepositories') || 'No Repositories Bound'}
                      description={
                        t('storage.emptyRepositories_desc') ||
                        'Bind an agent to a storage target to initialize a restic repository.'
                      }
                    />
                  </div>
                )}
              </CardContent>
            </Card>
          )}
        </TabsContent>
      </Tabs>

      {/* Import Rclone Dialog */}
      <Dialog open={importDialogOpen} onOpenChange={setImportDialogOpen}>
        <DialogContent className="sm:max-w-xl max-h-[90vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle className="text-sm font-semibold">
              {t('storage.importRcloneConfig')}
            </DialogTitle>
            <DialogDescription className="text-xs">
              {t('storage.importDialog.description') || 'Provide rclone.conf content and target remote details'}
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4 pt-2">
            <div className="space-y-1.5">
              <Label className="text-xs">{t('storage.importDialog.name')} *</Label>
              <Input
                placeholder={t('storage.importDialog.namePlaceholder') || 'e.g. Google Drive Backup'}
                value={importForm.name}
                onChange={(e) => setImportForm({ ...importForm, name: e.target.value })}
                disabled={importLoading || validateLoading}
                className="h-9 text-xs"
              />
            </div>

            <div className="space-y-1.5">
              <Label className="text-xs">rclone.conf *</Label>
              <textarea
                rows={6}
                placeholder={`[remote]\ntype = drive\ntoken = ...`}
                value={importForm.rclone_conf}
                onChange={(e) => setImportForm({ ...importForm, rclone_conf: e.target.value })}
                disabled={importLoading || validateLoading}
                className="w-full rounded-md border border-input bg-transparent px-3 py-2 text-xs font-mono shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:opacity-50"
              />
            </div>

            <div className="space-y-1.5">
              <Label className="text-xs">{t('storage.importDialog.validationAgent')} *</Label>
              <Select
                value={importForm.validate_agent_id}
                onValueChange={(val) => setImportForm({ ...importForm, validate_agent_id: val })}
                disabled={onlineAgents.length === 0 || importLoading || validateLoading}
              >
                <SelectTrigger className="h-9 text-xs">
                  <SelectValue placeholder={t('storage.importDialog.validationAgentPlaceholder')} />
                </SelectTrigger>
                <SelectContent>
                  {onlineAgents.map((a) => (
                    <SelectItem key={a.id} value={a.id} className="text-xs">
                      {a.name} ({a.hostname})
                    </SelectItem>
                  ))}
                  {offlineAgents.map((a) => (
                    <SelectItem key={a.id} value={a.id} disabled className="text-xs">
                      {a.name} ({a.hostname}) [Offline]
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div className="space-y-1.5">
                <Label className="text-xs">{t('storage.importDialog.remoteName')} *</Label>
                <Input
                  placeholder={t('storage.importDialog.remoteNamePlaceholder') || 'e.g. gdrive'}
                  value={importForm.remote_name}
                  onChange={(e) => setImportForm({ ...importForm, remote_name: e.target.value })}
                  disabled={importLoading || validateLoading}
                  className="h-9 text-xs"
                />
              </div>
              <div className="space-y-1.5">
                <Label className="text-xs">{t('storage.importDialog.remotePath')}</Label>
                <Input
                  placeholder={t('storage.importDialog.remotePathPlaceholder') || 'e.g. backups/bmc'}
                  value={importForm.remote_path}
                  onChange={(e) => setImportForm({ ...importForm, remote_path: e.target.value })}
                  disabled={importLoading || validateLoading}
                  className="h-9 text-xs"
                />
              </div>
            </div>

            {validateResult && (
              <Alert className="border-emerald-500/30 bg-emerald-500/10 text-emerald-400 py-3 text-xs">
                <CheckCircle2 className="h-4 w-4 text-emerald-400" />
                <AlertTitle className="text-xs font-semibold">
                  remote_type: {validateResult.remote_type}
                </AlertTitle>
                <AlertDescription className="text-xs mt-1">
                  <span>
                    {t('storage.importDialog.lsdEntries')}:{' '}
                    <strong>{validateResult.lsd_entries.length}</strong>
                  </span>
                  {validateResult.lsd_entries.length > 0 && (
                    <div className="mt-2 max-h-36 overflow-y-auto rounded border border-border/60 bg-background/50 p-2 font-mono text-[11px]">
                      {validateResult.lsd_entries.slice(0, 10).map((entry, idx) => (
                        <div key={idx} className="flex items-center gap-1.5 text-muted-foreground py-0.5">
                          <FileCode className="h-3 w-3" />
                          <span>{entry.name}</span>
                          <span className="text-[10px] text-muted-foreground/60">
                            ({entry.is_dir ? 'dir' : 'file'})
                          </span>
                        </div>
                      ))}
                      {validateResult.lsd_entries.length > 10 && (
                        <span className="text-[10px] text-muted-foreground italic">
                          ... and {validateResult.lsd_entries.length - 10} more
                        </span>
                      )}
                    </div>
                  )}
                </AlertDescription>
              </Alert>
            )}

            <DialogFooter className="gap-2 pt-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={handleValidate}
                disabled={
                  validateLoading ||
                  importLoading ||
                  !importForm.rclone_conf ||
                  !importForm.remote_name ||
                  !importForm.validate_agent_id
                }
                className="h-8 text-xs gap-1.5"
              >
                {validateLoading ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                ) : (
                  <Search className="h-3.5 w-3.5" />
                )}
                {t('storage.importDialog.validateFirst')}
              </Button>
              <Button
                type="button"
                size="sm"
                onClick={handleImport}
                disabled={
                  importLoading ||
                  validateLoading ||
                  !importForm.name ||
                  !importForm.rclone_conf ||
                  !importForm.remote_name ||
                  !importForm.validate_agent_id
                }
                className="h-8 text-xs gap-1.5"
              >
                {importLoading && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
                {t('storage.importDialog.confirmImport')}
              </Button>
            </DialogFooter>
          </div>
        </DialogContent>
      </Dialog>

      {/* Edit Target Dialog */}
      <Dialog open={editTargetDialogOpen} onOpenChange={setEditTargetDialogOpen}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle className="text-sm font-semibold">
              {t('storage.editDialog.title')}
            </DialogTitle>
            <DialogDescription className="text-xs">
              {t('storage.editDialog.notice')}
            </DialogDescription>
          </DialogHeader>
          <form onSubmit={handleRenameTarget} className="space-y-4">
            <Input
              value={targetNewName}
              onChange={(e) => setTargetNewName(e.target.value)}
              disabled={editTargetLoading}
              className="h-9 text-xs"
              autoFocus
            />
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => setEditTargetDialogOpen(false)}
                disabled={editTargetLoading}
                className="h-8 text-xs gap-1.5"
              >
                <X className="h-3.5 w-3.5" />
                {t('common.cancel')}
              </Button>
              <Button
                type="submit"
                size="sm"
                disabled={editTargetLoading || !targetNewName.trim()}
                className="h-8 text-xs gap-1.5"
              >
                {editTargetLoading ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                ) : (
                  <Save className="h-3.5 w-3.5" />
                )}
                {t('common.save')}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      {/* Delete Target Dialog */}
      <ConfirmActionDialog
        open={deleteTargetDialogOpen}
        onOpenChange={setDeleteTargetDialogOpen}
        title={t('storage.deleteConfirmTitle') || 'Delete Storage Target?'}
        description={
          targetToDelete
            ? t('storage.deleteConfirmDesc', { name: targetToDelete.name }) ||
              `Are you sure you want to delete "${targetToDelete.name}"?`
            : ''
        }
        destructive
        onConfirm={handleDeleteTargetConfirm}
      />

      {/* Bind Repo Dialog */}
      <Dialog open={bindDialogOpen} onOpenChange={setBindDialogOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle className="text-sm font-semibold">
              {t('storage.bindRepository')}
            </DialogTitle>
            <DialogDescription className="text-xs">
              {t('storage.bindDialog.description') || 'Associate an agent with a target to initialize restic repository'}
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4 pt-2">
            <div className="space-y-1.5">
              <Label className="text-xs">{t('storage.bindDialog.agent')} *</Label>
              <Select
                value={bindForm.agent_id}
                onValueChange={(val) => setBindForm({ ...bindForm, agent_id: val })}
                disabled={bindLoading}
              >
                <SelectTrigger className="h-9 text-xs">
                  <SelectValue placeholder={t('storage.bindDialog.selectAgent')} />
                </SelectTrigger>
                <SelectContent>
                  {onlineAgents.map((a) => (
                    <SelectItem key={a.id} value={a.id} className="text-xs">
                      {a.name} ({a.hostname})
                    </SelectItem>
                  ))}
                  {offlineAgents.map((a) => (
                    <SelectItem key={a.id} value={a.id} disabled className="text-xs">
                      {a.name} ({a.hostname}) [Offline]
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-1.5">
              <Label className="text-xs">{t('storage.bindDialog.storageTarget')} *</Label>
              <Select
                value={bindForm.storage_target_id}
                onValueChange={(val) => setBindForm({ ...bindForm, storage_target_id: val })}
                disabled={bindLoading}
              >
                <SelectTrigger className="h-9 text-xs">
                  <SelectValue placeholder={t('storage.bindDialog.selectTarget')} />
                </SelectTrigger>
                <SelectContent>
                  {targets.map((tgt) => (
                    <SelectItem key={tgt.id} value={tgt.id} className="text-xs">
                      {tgt.name} ({tgt.remote_name}:{tgt.remote_path || '/'})
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            {bindResult && (
              <Alert className="border-emerald-500/30 bg-emerald-500/10 text-emerald-400 py-2.5 text-xs">
                <CheckCircle2 className="h-4 w-4 text-emerald-400" />
                <AlertTitle className="text-xs font-semibold">
                  {t('storage.bindDialog.boundSuccessfully')}
                </AlertTitle>
                <AlertDescription className="text-xs mt-1">
                  <span className="font-mono">{bindResult.repository_path}</span>
                </AlertDescription>
              </Alert>
            )}

            <DialogFooter className="pt-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => setBindDialogOpen(false)}
                disabled={bindLoading}
                className="h-8 text-xs gap-1.5"
              >
                <X className="h-3.5 w-3.5" />
                {t('common.close')}
              </Button>
              <Button
                type="button"
                size="sm"
                onClick={handleBindRepo}
                disabled={bindLoading || !bindForm.agent_id || !bindForm.storage_target_id}
                className="h-8 text-xs gap-1.5"
              >
                {bindLoading ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                ) : (
                  <Check className="h-3.5 w-3.5" />
                )}
                {t('storage.bindDialog.bindButton') || t('common.confirm')}
              </Button>
            </DialogFooter>
          </div>
        </DialogContent>
      </Dialog>

      {/* Unbind Repo Dialog */}
      <ConfirmActionDialog
        open={unbindDialogOpen}
        onOpenChange={setUnbindDialogOpen}
        title={t('storage.repositoryDialog.unbindConfirmTitle') || 'Unbind Repository?'}
        description={
          repoToUnbind
            ? t('storage.repositoryDialog.unbindConfirmDesc', {
                agent: repoToUnbind.agent_name || repoToUnbind.agent_id,
              }) || `Unbind repository for agent "${repoToUnbind.agent_name || repoToUnbind.agent_id}"?`
            : ''
        }
        destructive
        onConfirm={handleUnbindRepoConfirm}
      />
    </div>
  )
}
