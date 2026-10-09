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
import { Checkbox } from '@/components/ui/checkbox'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import type { Agent, Snapshot } from '@/api/types'
import type { DryRunResult } from './Types'
import {
  Download,
  Search,
  AlertTriangle,
  Loader2,
  Trash2,
  X,
} from 'lucide-react'

export interface DatabaseRestoreFormState {
  host: string
  port: string
  username: string
  database: string
  authSource: string
  password: string
  overwrite: boolean
}

export interface SnapshotRestoreDialogsProps {
  // Restore Wizard
  restoreDialogOpen: boolean
  onRestoreDialogOpenChange: (open: boolean) => void
  restoreTargetPath: string
  onRestoreTargetPathChange: (path: string) => void
  restoreTargetValidationMessage: string | null
  restoreHostRoots: string[]
  overwriteMode: 'never' | 'if-changed' | 'always'
  onOverwriteModeChange: (mode: 'never' | 'if-changed' | 'always') => void
  selectedIncludePaths: string[]
  dryRunResult: DryRunResult | null
  dryRunLoading: boolean
  restoreTargetValid: boolean
  restoreLoading: boolean
  onDryRun: () => void
  onOpenConfirmPrompt: () => void

  // Target agent selection (cross-agent restore)
  restoreKind: 'filesystem' | 'postgresql' | 'mysql' | 'mongodb' | 'sqlite'
  targetAgents: Agent[]
  targetAgentId: string
  onTargetAgentIdChange: (id: string) => void
  sourceAgentId: string
  sourceAgentOnline: boolean
  databaseForm: DatabaseRestoreFormState
  onDatabaseFormChange: (patch: Partial<DatabaseRestoreFormState>) => void
  databaseFormError: string | null
  canDryRun: boolean

  // Confirmation Prompt
  confirmPromptOpen: boolean
  onConfirmPromptOpenChange: (open: boolean) => void
  selectedSnapshot: Snapshot | null
  confirmationInput: string
  onConfirmationInputChange: (input: string) => void
  onExecuteRestore: () => void

  // Delete Prompt
  deletePromptOpen: boolean
  onDeletePromptOpenChange: (open: boolean) => void
  snapshotToDelete: Snapshot | null
  deleteConfirmInput: string
  onDeleteConfirmInputChange: (input: string) => void
  deletingSnapshot: boolean
  onDeleteConfirm: () => void
}

export const SnapshotRestoreDialogs: React.FC<SnapshotRestoreDialogsProps> = ({
  restoreDialogOpen,
  onRestoreDialogOpenChange,
  restoreTargetPath,
  onRestoreTargetPathChange,
  restoreTargetValidationMessage,
  restoreHostRoots,
  overwriteMode,
  onOverwriteModeChange,
  selectedIncludePaths,
  dryRunResult,
  dryRunLoading,
  restoreTargetValid,
  restoreLoading,
  onDryRun,
  onOpenConfirmPrompt,
  restoreKind,
  targetAgents,
  targetAgentId,
  onTargetAgentIdChange,
  sourceAgentId,
  sourceAgentOnline,
  databaseForm,
  onDatabaseFormChange,
  databaseFormError,
  canDryRun,
  confirmPromptOpen,
  onConfirmPromptOpenChange,
  selectedSnapshot,
  confirmationInput,
  onConfirmationInputChange,
  onExecuteRestore,
  deletePromptOpen,
  onDeletePromptOpenChange,
  snapshotToDelete,
  deleteConfirmInput,
  onDeleteConfirmInputChange,
  deletingSnapshot,
  onDeleteConfirm,
}) => {
  const { t } = useTranslation()

  return (
    <>
      {/* Restore Wizard Dialog */}
      <Dialog open={restoreDialogOpen} onOpenChange={onRestoreDialogOpenChange}>
        <DialogContent className="sm:max-w-lg max-h-[90vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle className="text-sm font-semibold">
              {restoreKind === 'filesystem'
                ? t('snapshots.restoreDialog.title')
                : t('snapshots.restoreDialog.databaseTitle')}
            </DialogTitle>
            <DialogDescription className="text-xs">
              {t('snapshots.restoreDialog.subtitle')}
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4 pt-2">
            <div className="space-y-1.5">
              <Label className="text-xs">{t('snapshots.restoreDialog.targetAgent')} *</Label>
              <Select value={targetAgentId} onValueChange={onTargetAgentIdChange}>
                <SelectTrigger className="h-9 text-xs">
                  <SelectValue placeholder={t('snapshots.restoreDialog.targetAgentPlaceholder')} />
                </SelectTrigger>
                <SelectContent>
                  {targetAgents.map((agent) => (
                    <SelectItem key={agent.id} value={agent.id} className="text-xs">
                      {agent.name} ({agent.hostname}){agent.id === sourceAgentId ? ` · ${t('snapshots.restoreDialog.sourceAgentBadge')}` : ''}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {!sourceAgentOnline && (
                <p className="text-[11px] text-muted-foreground">
                  {t('snapshots.restoreDialog.sourceOfflineHint')}
                </p>
              )}
              {targetAgentId && targetAgentId !== sourceAgentId && (
                <p className="text-[11px] text-amber-600 dark:text-amber-400">
                  {t('snapshots.restoreDialog.credentialWarning')}
                </p>
              )}
            </div>

            {restoreKind === 'filesystem' ? (
              <>
                <div className="space-y-1.5">
                  <Label className="text-xs">{t('snapshots.restoreDialog.targetPath')} *</Label>
                  <Input
                    placeholder="/restore/target/directory"
                    value={restoreTargetPath}
                    onChange={(e) => onRestoreTargetPathChange(e.target.value)}
                    className="h-9 text-xs font-mono"
                  />
                  {restoreTargetValidationMessage && (
                    <p className="text-[11px] text-destructive">{restoreTargetValidationMessage}</p>
                  )}
                  {restoreHostRoots.length > 0 ? (
                    <div className="text-[11px] text-muted-foreground flex flex-wrap items-center gap-1.5 pt-1">
                      <span>{t('snapshots.restoreDialog.availableHostPaths')}</span>
                      {restoreHostRoots.map((r) => (
                        <span key={r} className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px]">
                          {r}
                        </span>
                      ))}
                    </div>
                  ) : null}
                </div>

                <div className="space-y-1.5">
                  <Label className="text-xs">{t('snapshots.restoreDialog.overwriteMode')} *</Label>
                  <RadioGroup
                    value={overwriteMode}
                    onValueChange={(val) => onOverwriteModeChange(val as 'never' | 'if-changed' | 'always')}
                    className="flex gap-4 pt-1"
                  >
                    <div className="flex items-center space-x-2">
                      <RadioGroupItem value="never" id="ow-never" />
                      <label htmlFor="ow-never" className="text-xs cursor-pointer">
                        {t('snapshots.restoreDialog.never')}
                      </label>
                    </div>
                    <div className="flex items-center space-x-2">
                      <RadioGroupItem value="if-changed" id="ow-changed" />
                      <label htmlFor="ow-changed" className="text-xs cursor-pointer">
                        {t('snapshots.restoreDialog.ifChanged')}
                      </label>
                    </div>
                    <div className="flex items-center space-x-2">
                      <RadioGroupItem value="always" id="ow-always" />
                      <label htmlFor="ow-always" className="text-xs cursor-pointer">
                        {t('snapshots.restoreDialog.always')}
                      </label>
                    </div>
                  </RadioGroup>
                </div>
              </>
            ) : (
              <>
                <Alert className="border-amber-500/40 bg-amber-500/10 text-amber-700 dark:text-amber-300 py-2.5 text-xs">
                  <AlertTriangle className="h-4 w-4" aria-hidden="true" />
                  <AlertTitle className="text-xs font-semibold">
                    {t('snapshots.restoreDialog.maintenanceTitle')}
                  </AlertTitle>
                  <AlertDescription className="text-[11px] pt-1">
                    {restoreKind === 'sqlite'
                      ? t('snapshots.restoreDialog.sqliteMaintenance')
                      : t('snapshots.restoreDialog.databaseMaintenance')}
                  </AlertDescription>
                </Alert>

                {restoreKind === 'sqlite' ? (
                  <div className="space-y-1.5">
                    <Label className="text-xs">{t('snapshots.restoreDialog.sqlitePath')} *</Label>
                    <Input
                      placeholder="/backup-restore/target.sqlite"
                      value={databaseForm.database}
                      onChange={(e) => onDatabaseFormChange({ database: e.target.value })}
                      className="h-9 text-xs font-mono"
                    />
                  </div>
                ) : (
                  <>
                    <div className="grid grid-cols-2 gap-3">
                      <div className="space-y-1.5">
                        <Label className="text-xs">{t('snapshots.restoreDialog.host')} *</Label>
                        <Input
                          value={databaseForm.host}
                          onChange={(e) => onDatabaseFormChange({ host: e.target.value })}
                          className="h-9 text-xs font-mono"
                        />
                      </div>
                      <div className="space-y-1.5">
                        <Label className="text-xs">{t('snapshots.restoreDialog.port')} *</Label>
                        <Input
                          value={databaseForm.port}
                          onChange={(e) => onDatabaseFormChange({ port: e.target.value })}
                          className="h-9 text-xs font-mono"
                        />
                      </div>
                      <div className="space-y-1.5">
                        <Label className="text-xs">{t('snapshots.restoreDialog.username')} *</Label>
                        <Input
                          value={databaseForm.username}
                          onChange={(e) => onDatabaseFormChange({ username: e.target.value })}
                          className="h-9 text-xs font-mono"
                        />
                      </div>
                      <div className="space-y-1.5">
                        <Label className="text-xs">{t('snapshots.restoreDialog.database')} *</Label>
                        <Input
                          value={databaseForm.database}
                          onChange={(e) => onDatabaseFormChange({ database: e.target.value })}
                          className="h-9 text-xs font-mono"
                        />
                      </div>
                    </div>
                    {restoreKind === 'mongodb' && (
                      <div className="space-y-1.5">
                        <Label className="text-xs">{t('snapshots.restoreDialog.authSource')}</Label>
                        <Input
                          value={databaseForm.authSource}
                          onChange={(e) => onDatabaseFormChange({ authSource: e.target.value })}
                          className="h-9 text-xs font-mono"
                        />
                      </div>
                    )}
                    {databaseForm.database.trim().toLowerCase() === 'all' && (
                      <p className="text-[11px] text-destructive">
                        {t('snapshots.restoreDialog.allNotAllowed')}
                      </p>
                    )}
                  </>
                )}

                <div className="space-y-1.5">
                  <Label className="text-xs">{t('snapshots.restoreDialog.targetPassword')} *</Label>
                  <Input
                    type="password"
                    autoComplete="off"
                    value={databaseForm.password}
                    onChange={(e) => onDatabaseFormChange({ password: e.target.value })}
                    className="h-9 text-xs font-mono"
                  />
                  <p className="text-[11px] text-muted-foreground">
                    {t('snapshots.restoreDialog.targetPasswordHint')}
                  </p>
                </div>

                <div className="flex items-center space-x-2">
                  <Checkbox
                    id="db-overwrite"
                    checked={databaseForm.overwrite}
                    onCheckedChange={(checked) => onDatabaseFormChange({ overwrite: checked === true })}
                  />
                  <label htmlFor="db-overwrite" className="text-xs cursor-pointer">
                    {t('snapshots.restoreDialog.overwriteExisting')}
                  </label>
                </div>
                {databaseFormError && <p className="text-[11px] text-destructive">{databaseFormError}</p>}
              </>
            )}

            {restoreKind === 'filesystem' && selectedIncludePaths.length > 0 && (
              <div className="space-y-1">
                <Label className="text-xs">
                  {t('snapshots.selectedItems', { count: selectedIncludePaths.length })}
                </Label>
                <div className="max-h-24 overflow-y-auto rounded border border-border bg-muted/20 p-1.5 font-mono text-[11px]">
                  {selectedIncludePaths.map((p) => (
                    <div key={p} className="truncate text-muted-foreground">
                      {p}
                    </div>
                  ))}
                </div>
              </div>
            )}

            {restoreKind === 'filesystem' && dryRunResult && (
              <Alert className="border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 py-2.5 text-xs">
                <AlertTitle className="text-xs font-semibold">
                  {t('snapshots.restoreDialog.dryRunResult')}
                </AlertTitle>
                <AlertDescription className="text-xs grid grid-cols-4 gap-2 pt-2">
                  <div>
                    <span className="text-[11px] text-muted-foreground block">{t('snapshots.restoreDialog.add')}</span>
                    <span className="font-mono font-bold text-xs">{dryRunResult.add}</span>
                  </div>
                  <div>
                    <span className="text-[11px] text-muted-foreground block">{t('snapshots.restoreDialog.changed')}</span>
                    <span className="font-mono font-bold text-xs">{dryRunResult.changed}</span>
                  </div>
                  <div>
                    <span className="text-[11px] text-muted-foreground block">{t('snapshots.restoreDialog.skipped')}</span>
                    <span className="font-mono font-bold text-xs">{dryRunResult.skipped}</span>
                  </div>
                  <div>
                    <span className="text-[11px] text-muted-foreground block">{t('snapshots.restoreDialog.deleteStat')}</span>
                    <span className="font-mono font-bold text-xs">{dryRunResult.delete}</span>
                  </div>
                </AlertDescription>
              </Alert>
            )}

            <DialogFooter className="gap-2 pt-2">
              {restoreKind === 'filesystem' && (
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={onDryRun}
                disabled={dryRunLoading || !canDryRun}
                className="h-8 text-xs gap-1.5"
              >
                {dryRunLoading ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
                ) : (
                  <Search className="h-3.5 w-3.5" aria-hidden="true" />
                )}
                {t('snapshots.restoreDialog.dryRunButton')}
              </Button>
              )}
              <Button
                type="button"
                size="sm"
                onClick={onOpenConfirmPrompt}
                disabled={restoreKind === 'filesystem' ? (!restoreTargetValid || !dryRunResult || restoreLoading) : (restoreLoading || Boolean(databaseFormError))}
                className="h-8 text-xs gap-1.5 bg-primary text-primary-foreground"
              >
                <Download className="h-3.5 w-3.5" aria-hidden="true" />
                {t('snapshots.restoreDialog.confirmExecute')}
              </Button>
            </DialogFooter>
          </div>
        </DialogContent>
      </Dialog>

      {/* Confirmation String Dialog for Restore */}
      <Dialog open={confirmPromptOpen} onOpenChange={onConfirmPromptOpenChange}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle className="text-sm font-semibold">
              {t('snapshots.prompt.title')}
            </DialogTitle>
            <DialogDescription className="text-xs">
              {t('snapshots.prompt.message')}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 pt-2">
            <Input
              placeholder={t('snapshots.prompt.inputPlaceholder', {
                example: selectedSnapshot?.id.slice(0, 8),
              })}
              value={confirmationInput}
              onChange={(e) => onConfirmationInputChange(e.target.value)}
              disabled={restoreLoading}
              className="h-9 text-xs font-mono"
              autoFocus
            />
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => onConfirmPromptOpenChange(false)}
                disabled={restoreLoading}
                className="h-8 text-xs gap-1.5"
              >
                <X className="h-3.5 w-3.5" aria-hidden="true" />
                {t('common.cancel')}
              </Button>
              <Button
                type="button"
                size="sm"
                onClick={onExecuteRestore}
                disabled={restoreLoading || !confirmationInput.trim()}
                className="h-8 text-xs gap-1.5"
              >
                {restoreLoading ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
                ) : (
                  <Download className="h-3.5 w-3.5" aria-hidden="true" />
                )}
                {t('snapshots.prompt.execute')}
              </Button>
            </DialogFooter>
          </div>
        </DialogContent>
      </Dialog>

      {/* Delete Snapshot Verification Dialog */}
      <Dialog open={deletePromptOpen} onOpenChange={onDeletePromptOpenChange}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle className="text-sm font-semibold text-destructive flex items-center gap-2">
              <AlertTriangle className="h-4 w-4 text-destructive" aria-hidden="true" />
              <span>{t('snapshots.delete.title')}</span>
            </DialogTitle>
            <DialogDescription className="text-xs">
              {t('snapshots.delete.message')}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 pt-2 min-w-0">
            <div className="space-y-1 min-w-0">
              <span className="text-xs font-mono text-muted-foreground block break-all">
                Target ID: <strong className="text-foreground select-all break-all">{snapshotToDelete?.id}</strong>
              </span>
              <p className="text-[11px] text-muted-foreground">
                Type the full snapshot ID to confirm deletion:
              </p>
            </div>
            <Input
              placeholder={snapshotToDelete?.id}
              value={deleteConfirmInput}
              onChange={(e) => onDeleteConfirmInputChange(e.target.value)}
              disabled={deletingSnapshot}
              className="h-9 text-xs font-mono"
            />
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => onDeletePromptOpenChange(false)}
                disabled={deletingSnapshot}
                className="h-8 text-xs gap-1.5"
              >
                <X className="h-3.5 w-3.5" aria-hidden="true" />
                {t('common.cancel')}
              </Button>
              <Button
                type="button"
                variant="destructive"
                size="sm"
                onClick={onDeleteConfirm}
                disabled={deletingSnapshot || deleteConfirmInput.trim() !== snapshotToDelete?.id}
                className="h-8 text-xs gap-1.5"
              >
                {deletingSnapshot ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
                ) : (
                  <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
                )}
                {t('snapshots.delete.confirm')}
              </Button>
            </DialogFooter>
          </div>
        </DialogContent>
      </Dialog>
    </>
  )
}
