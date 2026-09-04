import React, { useEffect, useState, useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { apiGet, apiPost, apiDelete, apiPatch, isApiClientError } from '@/api/client'
import type {
  StorageTarget,
  Repository,
  Agent,
  StorageTargetValidateResponse,
} from '@/api/types'
import { toastSuccess, toastError } from '@/lib/toast'
import { Folder, Link } from 'lucide-react'
import { StorageTargetsPanel } from './StorageTargetsPanel'
import { RepositoriesPanel } from './RepositoriesPanel'
import { StorageDialogs } from './StorageDialogs'

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
      setTargetsError(isApiClientError(err) ? err.message : t('storage.loadTargetsFailed'))
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
      setReposError(isApiClientError(err) ? err.message : t('storage.loadReposFailed'))
    } finally {
      setReposLoading(false)
    }
  }

  const loadAgents = async () => {
    try {
      const data = await apiGet<Agent[]>('/agents')
      setAgents(data)
    } catch {
      // Non-blocking
    }
  }

  useEffect(() => {
    loadTargets()
    loadRepos()
    loadAgents()
  }, [])

  // Import flow
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
    if (!importForm.rclone_conf || !importForm.remote_name || !importForm.validate_agent_id) {
      return
    }
    setValidateLoading(true)
    setValidateResult(null)
    try {
      const res = await apiPost<StorageTargetValidateResponse>('/storage-targets/validate', {
        agent_id: importForm.validate_agent_id,
        rclone_conf: importForm.rclone_conf,
        remote_name: importForm.remote_name,
        remote_path: importForm.remote_path,
      })
      setValidateResult(res)
      toastSuccess(t('storage.importDialog.validationSuccess'))
    } catch (err: unknown) {
      toastError(isApiClientError(err) ? err.message : t('storage.importDialog.validationFailed'))
    } finally {
      setValidateLoading(false)
    }
  }

  const handleImport = async () => {
    if (!importForm.name || !importForm.rclone_conf || !importForm.remote_name || !importForm.validate_agent_id) {
      return
    }
    setImportLoading(true)
    try {
      await apiPost<StorageTarget>('/storage-targets', {
        name: importForm.name,
        rclone_conf: importForm.rclone_conf,
        remote_name: importForm.remote_name,
        remote_path: importForm.remote_path,
        validate_agent_id: importForm.validate_agent_id,
      })
      toastSuccess(t('storage.importDialog.importSuccess'))
      setImportDialogOpen(false)
      await loadTargets()
    } catch (err: unknown) {
      toastError(isApiClientError(err) ? err.message : t('storage.importDialog.importFailed'))
    } finally {
      setImportLoading(false)
    }
  }

  // Edit target flow
  const openEditTargetDialog = (target: StorageTarget) => {
    setTargetToEdit(target)
    setTargetNewName(target.name)
    setEditTargetDialogOpen(true)
  }

  const handleRenameTarget = async (e: React.FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    if (!targetToEdit || !targetNewName.trim()) return
    setEditTargetLoading(true)
    try {
      await apiPatch<StorageTarget>(`/storage-targets/${targetToEdit.id}`, {
        name: targetNewName.trim(),
      })
      toastSuccess(t('storage.editDialog.renameSuccess'))
      setEditTargetDialogOpen(false)
      await loadTargets()
    } catch (err: unknown) {
      toastError(isApiClientError(err) ? err.message : t('storage.editDialog.renameFailed'))
    } finally {
      setEditTargetLoading(false)
    }
  }

  // Delete target flow
  const openDeleteTarget = (target: StorageTarget) => {
    setTargetToDelete(target)
    setDeleteTargetDialogOpen(true)
  }

  const handleDeleteTargetConfirm = async () => {
    if (!targetToDelete) return
    try {
      await apiDelete(`/storage-targets/${targetToDelete.id}`)
      toastSuccess(t('storage.targetDeleted'))
      await loadTargets()
    } catch (err: unknown) {
      toastError(isApiClientError(err) ? err.message : t('storage.deleteFailed'))
    }
  }

  // Bind repo flow
  const openBindRepoDialog = () => {
    setBindForm({
      agent_id: onlineAgents[0]?.id || agents[0]?.id || '',
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
      toastSuccess(t('storage.bindDialog.bindSuccess'))
      await loadRepos()
    } catch (err: unknown) {
      toastError(isApiClientError(err) ? err.message : t('storage.bindDialog.bindFailed'))
    } finally {
      setBindLoading(false)
    }
  }

  const handleRetryRepo = async (repo: Repository) => {
    setRepoActionLoading((prev) => ({ ...prev, [repo.id]: true }))
    try {
      await apiPost(`/repositories/${repo.id}/retry`, {})
      toastSuccess(t('storage.repositoryDialog.retryDispatched'))
      await loadRepos()
    } catch (err: unknown) {
      toastError(isApiClientError(err) ? err.message : t('storage.repositoryDialog.retryFailed'))
    } finally {
      setRepoActionLoading((prev) => ({ ...prev, [repo.id]: false }))
    }
  }

  // Unbind repo flow
  const openUnbindRepo = (repo: Repository) => {
    setRepoToUnbind(repo)
    setUnbindDialogOpen(true)
  }

  const handleUnbindRepoConfirm = async () => {
    if (!repoToUnbind) return
    setRepoActionLoading((prev) => ({ ...prev, [repoToUnbind.id]: true }))
    try {
      await apiDelete(`/repositories/${repoToUnbind.id}`)
      toastSuccess(t('storage.repositoryDialog.unbindSuccess'))
      await loadRepos()
    } catch (err: unknown) {
      toastError(isApiClientError(err) ? err.message : t('storage.repositoryDialog.unbindFailed'))
    } finally {
      setRepoActionLoading((prev) => ({ ...prev, [repoToUnbind.id]: false }))
    }
  }

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-xl font-bold tracking-tight text-foreground">
          {t('nav.storage')}
        </h2>
        <p className="text-xs text-muted-foreground">
          {t('storage.subtitle')}
        </p>
      </div>

      <Tabs value={activeTab} onValueChange={(val) => setActiveTab(val as 'targets' | 'repos')} className="w-full">
        <TabsList className="bg-muted/50 p-1 border border-border">
          <TabsTrigger value="targets" className="text-xs gap-2">
            <Folder className="h-3.5 w-3.5" aria-hidden="true" />
            <span>{t('storage.tabs.targets')}</span>
          </TabsTrigger>
          <TabsTrigger value="repos" className="text-xs gap-2">
            <Link className="h-3.5 w-3.5" aria-hidden="true" />
            <span>{t('storage.tabs.repositories')}</span>
          </TabsTrigger>
        </TabsList>

        <TabsContent value="targets">
          <StorageTargetsPanel
            targets={targets}
            targetsLoading={targetsLoading}
            targetsError={targetsError}
            onLoadTargets={loadTargets}
            onOpenImport={openImportDialog}
            onOpenEdit={openEditTargetDialog}
            onOpenDelete={openDeleteTarget}
          />
        </TabsContent>

        <TabsContent value="repos">
          <RepositoriesPanel
            repos={repos}
            reposLoading={reposLoading}
            reposError={reposError}
            repoActionLoading={repoActionLoading}
            canBind={targets.length > 0 && agents.length > 0}
            onLoadRepos={loadRepos}
            onOpenBind={openBindRepoDialog}
            onRetryRepo={handleRetryRepo}
            onOpenUnbind={openUnbindRepo}
          />
        </TabsContent>
      </Tabs>

      <StorageDialogs
        importDialogOpen={importDialogOpen}
        onImportDialogOpenChange={setImportDialogOpen}
        importForm={importForm}
        onImportFormChange={setImportForm}
        onlineAgents={onlineAgents}
        offlineAgents={offlineAgents}
        importLoading={importLoading}
        validateLoading={validateLoading}
        validateResult={validateResult}
        onValidate={handleValidate}
        onImport={handleImport}
        editTargetDialogOpen={editTargetDialogOpen}
        onEditTargetDialogOpenChange={setEditTargetDialogOpen}
        targetNewName={targetNewName}
        onTargetNewNameChange={setTargetNewName}
        editTargetLoading={editTargetLoading}
        onRenameTarget={handleRenameTarget}
        deleteTargetDialogOpen={deleteTargetDialogOpen}
        onDeleteTargetDialogOpenChange={setDeleteTargetDialogOpen}
        targetToDelete={targetToDelete}
        onDeleteTargetConfirm={handleDeleteTargetConfirm}
        bindDialogOpen={bindDialogOpen}
        onBindDialogOpenChange={setBindDialogOpen}
        bindForm={bindForm}
        onBindFormChange={setBindForm}
        targets={targets}
        bindLoading={bindLoading}
        bindResult={bindResult}
        onBindRepo={handleBindRepo}
        unbindDialogOpen={unbindDialogOpen}
        onUnbindDialogOpenChange={setUnbindDialogOpen}
        repoToUnbind={repoToUnbind}
        onUnbindRepoConfirm={handleUnbindRepoConfirm}
      />
    </div>
  )
}
