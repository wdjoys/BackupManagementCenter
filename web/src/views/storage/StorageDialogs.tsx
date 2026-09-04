import React from 'react'
import { useTranslation } from 'react-i18next'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
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
import { ConfirmActionDialog } from '@/components/ConfirmActionDialog'
import type { StorageTarget, Repository, Agent, StorageTargetValidateResponse } from '@/api/types'
import {
  Search,
  Loader2,
  FileCode,
  CheckCircle2,
  X,
  Save,
  Check,
} from 'lucide-react'

export interface StorageDialogsProps {
  // Import Dialog
  importDialogOpen: boolean
  onImportDialogOpenChange: (open: boolean) => void
  importForm: {
    name: string
    rclone_conf: string
    validate_agent_id: string
    remote_name: string
    remote_path: string
  }
  onImportFormChange: (form: {
    name: string
    rclone_conf: string
    validate_agent_id: string
    remote_name: string
    remote_path: string
  }) => void
  onlineAgents: Agent[]
  offlineAgents: Agent[]
  importLoading: boolean
  validateLoading: boolean
  validateResult: StorageTargetValidateResponse | null
  onValidate: () => void
  onImport: () => void

  // Edit Target Dialog
  editTargetDialogOpen: boolean
  onEditTargetDialogOpenChange: (open: boolean) => void
  targetNewName: string
  onTargetNewNameChange: (name: string) => void
  editTargetLoading: boolean
  onRenameTarget: (e: React.FormEvent<HTMLFormElement>) => void

  // Delete Target Dialog
  deleteTargetDialogOpen: boolean
  onDeleteTargetDialogOpenChange: (open: boolean) => void
  targetToDelete: StorageTarget | null
  onDeleteTargetConfirm: () => void

  // Bind Repo Dialog
  bindDialogOpen: boolean
  onBindDialogOpenChange: (open: boolean) => void
  bindForm: {
    agent_id: string
    storage_target_id: string
  }
  onBindFormChange: (form: {
    agent_id: string
    storage_target_id: string
  }) => void
  targets: StorageTarget[]
  bindLoading: boolean
  bindResult: Repository | null
  onBindRepo: () => void

  // Unbind Repo Dialog
  unbindDialogOpen: boolean
  onUnbindDialogOpenChange: (open: boolean) => void
  repoToUnbind: Repository | null
  onUnbindRepoConfirm: () => void
}

export const StorageDialogs: React.FC<StorageDialogsProps> = ({
  importDialogOpen,
  onImportDialogOpenChange,
  importForm,
  onImportFormChange,
  onlineAgents,
  offlineAgents,
  importLoading,
  validateLoading,
  validateResult,
  onValidate,
  onImport,
  editTargetDialogOpen,
  onEditTargetDialogOpenChange,
  targetNewName,
  onTargetNewNameChange,
  editTargetLoading,
  onRenameTarget,
  deleteTargetDialogOpen,
  onDeleteTargetDialogOpenChange,
  targetToDelete,
  onDeleteTargetConfirm,
  bindDialogOpen,
  onBindDialogOpenChange,
  bindForm,
  onBindFormChange,
  targets,
  bindLoading,
  bindResult,
  onBindRepo,
  unbindDialogOpen,
  onUnbindDialogOpenChange,
  repoToUnbind,
  onUnbindRepoConfirm,
}) => {
  const { t } = useTranslation()

  return (
    <>
      {/* Import Rclone Dialog */}
      <Dialog open={importDialogOpen} onOpenChange={onImportDialogOpenChange}>
        <DialogContent className="sm:max-w-xl max-h-[90vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle className="text-sm font-semibold">
              {t('storage.importRcloneConfig')}
            </DialogTitle>
            <DialogDescription className="text-xs">
              {t('storage.importDialog.description')}
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4 pt-2">
            <div className="space-y-1.5">
              <Label className="text-xs">{t('storage.importDialog.name')} *</Label>
              <Input
                placeholder={t('storage.importDialog.namePlaceholder')}
                value={importForm.name}
                onChange={(e) => onImportFormChange({ ...importForm, name: e.target.value })}
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
                onChange={(e) => onImportFormChange({ ...importForm, rclone_conf: e.target.value })}
                disabled={importLoading || validateLoading}
                className="w-full rounded-md border border-input bg-transparent px-3 py-2 text-xs font-mono shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:opacity-50"
              />
            </div>

            <div className="space-y-1.5">
              <Label className="text-xs">{t('storage.importDialog.validationAgent')} *</Label>
              <Select
                value={importForm.validate_agent_id}
                onValueChange={(val) => onImportFormChange({ ...importForm, validate_agent_id: val })}
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
                  placeholder={t('storage.importDialog.remoteNamePlaceholder')}
                  value={importForm.remote_name}
                  onChange={(e) => onImportFormChange({ ...importForm, remote_name: e.target.value })}
                  disabled={importLoading || validateLoading}
                  className="h-9 text-xs"
                />
              </div>
              <div className="space-y-1.5">
                <Label className="text-xs">{t('storage.importDialog.remotePath')}</Label>
                <Input
                  placeholder={t('storage.importDialog.remotePathPlaceholder')}
                  value={importForm.remote_path}
                  onChange={(e) => onImportFormChange({ ...importForm, remote_path: e.target.value })}
                  disabled={importLoading || validateLoading}
                  className="h-9 text-xs"
                />
              </div>
            </div>

            {validateResult && (
              <Alert className="border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 py-3 text-xs">
                <CheckCircle2 className="h-4 w-4 text-emerald-600 dark:text-emerald-400" aria-hidden="true" />
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
                          <FileCode className="h-3 w-3" aria-hidden="true" />
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
                onClick={onValidate}
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
                  <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
                ) : (
                  <Search className="h-3.5 w-3.5" aria-hidden="true" />
                )}
                {t('storage.importDialog.validateFirst')}
              </Button>
              <Button
                type="button"
                size="sm"
                onClick={onImport}
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
                {importLoading && <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />}
                {t('storage.importDialog.confirmImport')}
              </Button>
            </DialogFooter>
          </div>
        </DialogContent>
      </Dialog>

      {/* Edit Target Dialog */}
      <Dialog open={editTargetDialogOpen} onOpenChange={onEditTargetDialogOpenChange}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle className="text-sm font-semibold">
              {t('storage.editDialog.title')}
            </DialogTitle>
            <DialogDescription className="text-xs">
              {t('storage.editDialog.notice')}
            </DialogDescription>
          </DialogHeader>
          <form onSubmit={onRenameTarget} className="space-y-4">
            <Input
              value={targetNewName}
              onChange={(e) => onTargetNewNameChange(e.target.value)}
              disabled={editTargetLoading}
              className="h-9 text-xs"
              autoFocus
            />
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => onEditTargetDialogOpenChange(false)}
                disabled={editTargetLoading}
                className="h-8 text-xs gap-1.5"
              >
                <X className="h-3.5 w-3.5" aria-hidden="true" />
                {t('common.cancel')}
              </Button>
              <Button
                type="submit"
                size="sm"
                disabled={editTargetLoading || !targetNewName.trim()}
                className="h-8 text-xs gap-1.5"
              >
                {editTargetLoading ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
                ) : (
                  <Save className="h-3.5 w-3.5" aria-hidden="true" />
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
        onOpenChange={onDeleteTargetDialogOpenChange}
        title={t('storage.deleteConfirmTitle')}
        description={
          targetToDelete
            ? t('storage.deleteConfirmDesc', { name: targetToDelete.name })
            : ''
        }
        destructive
        onConfirm={onDeleteTargetConfirm}
      />

      {/* Bind Repo Dialog */}
      <Dialog open={bindDialogOpen} onOpenChange={onBindDialogOpenChange}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle className="text-sm font-semibold">
              {t('storage.bindRepository')}
            </DialogTitle>
            <DialogDescription className="text-xs">
              {t('storage.bindDialog.description')}
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4 pt-2">
            <div className="space-y-1.5">
              <Label className="text-xs">{t('storage.bindDialog.agent')} *</Label>
              <Select
                value={bindForm.agent_id}
                onValueChange={(val) => onBindFormChange({ ...bindForm, agent_id: val })}
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
                onValueChange={(val) => onBindFormChange({ ...bindForm, storage_target_id: val })}
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
              <Alert className="border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 py-2.5 text-xs">
                <CheckCircle2 className="h-4 w-4 text-emerald-600 dark:text-emerald-400" aria-hidden="true" />
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
                onClick={() => onBindDialogOpenChange(false)}
                disabled={bindLoading}
                className="h-8 text-xs gap-1.5"
              >
                <X className="h-3.5 w-3.5" aria-hidden="true" />
                {t('common.close')}
              </Button>
              <Button
                type="button"
                size="sm"
                onClick={onBindRepo}
                disabled={bindLoading || !bindForm.agent_id || !bindForm.storage_target_id}
                className="h-8 text-xs gap-1.5"
              >
                {bindLoading ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
                ) : (
                  <Check className="h-3.5 w-3.5" aria-hidden="true" />
                )}
                {t('common.confirm')}
              </Button>
            </DialogFooter>
          </div>
        </DialogContent>
      </Dialog>

      {/* Unbind Repo Dialog */}
      <ConfirmActionDialog
        open={unbindDialogOpen}
        onOpenChange={onUnbindDialogOpenChange}
        title={t('storage.repositoryDialog.unbindConfirmTitle')}
        description={
          repoToUnbind
            ? t('storage.repositoryDialog.unbindConfirmDesc', {
                agent: repoToUnbind.agent_name || repoToUnbind.agent_id,
              })
            : ''
        }
        destructive
        onConfirm={onUnbindRepoConfirm}
      />
    </>
  )
}
