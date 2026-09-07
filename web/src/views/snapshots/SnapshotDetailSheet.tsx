import React from 'react'
import { useTranslation } from 'react-i18next'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { PageLoadingState } from '@/components/PageLoadingState'
import { formatDateTime } from '@/i18n'
import type { TreeEntry } from '@/api/types'
import { formatSize, type BreadcrumbPart, type SnapshotView } from './Types'
import {
  Folder,
  FileCode,
  Download,
  Copy,
  Check,
  ChevronRight,
  ExternalLink,
} from 'lucide-react'

export interface SnapshotDetailSheetProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  selectedSnapshotView: SnapshotView | null
  canRestore: boolean
  copiedId: boolean
  treeLoading: boolean
  treeEntries: TreeEntry[]
  treePath: string
  breadcrumbs: BreadcrumbPart[]
  treeSelectedPaths: string[]
  onViewRun: (runId: string) => void
  onOpenRestore: () => void
  onCopySnapshotId: () => void
  onNavigateBreadcrumb: (path: string) => void
  onNavigateDir: (path: string) => void
  onToggleTreeSelection: (entryName: string) => void
}

export const SnapshotDetailSheet: React.FC<SnapshotDetailSheetProps> = ({
  open,
  onOpenChange,
  selectedSnapshotView,
  canRestore,
  copiedId,
  treeLoading,
  treeEntries,
  treePath,
  breadcrumbs,
  treeSelectedPaths,
  onViewRun,
  onOpenRestore,
  onCopySnapshotId,
  onNavigateBreadcrumb,
  onNavigateDir,
  onToggleTreeSelection,
}) => {
  const { t } = useTranslation()

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="w-full sm:max-w-2xl p-0 bg-card border-l border-border flex flex-col">
        {selectedSnapshotView && (
          <>
            <SheetHeader className="p-4 border-b border-border bg-muted/20">
              <div className="flex items-center justify-between">
                <div className="space-y-0.5">
                  <SheetTitle className="text-sm font-semibold">
                    {selectedSnapshotView.planName}
                  </SheetTitle>
                  <SheetDescription className="text-xs font-mono">
                    {selectedSnapshotView.kindLabel} · {formatDateTime(selectedSnapshotView.raw.time)}
                  </SheetDescription>
                </div>
                <div className="flex items-center gap-2">
                  {selectedSnapshotView.runID && (
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => onViewRun(selectedSnapshotView.runID)}
                      className="h-7 text-xs gap-1"
                    >
                      <ExternalLink className="h-3 w-3" aria-hidden="true" />
                      {t('snapshots.viewRun')}
                    </Button>
                  )}
                  <Button
                    size="sm"
                    disabled={!canRestore}
                    onClick={onOpenRestore}
                    className="h-7 text-xs gap-1.5 bg-primary text-primary-foreground"
                  >
                    <Download className="h-3.5 w-3.5" aria-hidden="true" />
                    {t('snapshots.restoreThisSnapshot')}
                  </Button>
                </div>
              </div>
            </SheetHeader>

            {/* Snapshot Info summary */}
            <div className="p-4 border-b border-border bg-card/40 space-y-2 text-xs">
              <div className="flex items-center justify-between font-mono bg-muted/30 p-2 rounded">
                <span className="text-muted-foreground">{t('snapshots.snapshotId')}:</span>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  onClick={onCopySnapshotId}
                  className="h-6 p-1 font-mono text-xs text-primary hover:underline gap-1.5"
                  aria-label={t('snapshots.copySnapshotId')}
                >
                  <span>{selectedSnapshotView.raw.id}</span>
                  {copiedId ? (
                    <Check className="h-3.5 w-3.5 text-emerald-600 dark:text-emerald-400" aria-hidden="true" />
                  ) : (
                    <Copy className="h-3.5 w-3.5" aria-hidden="true" />
                  )}
                </Button>
              </div>
              <div className="flex items-center justify-between text-muted-foreground">
                <span>{t('snapshots.browseTable.paths')}:</span>
                <span className="font-mono text-foreground">{selectedSnapshotView.raw.paths.join(', ') || '—'}</span>
              </div>
            </div>

            {/* Breadcrumbs */}
            <div className="px-4 py-2 border-b border-border/80 bg-muted/10 flex items-center gap-1.5 text-xs font-mono overflow-x-auto">
              <span className="text-muted-foreground">{t('snapshots.browseTable.path')}:</span>
              {breadcrumbs.map((b, idx) => {
                const isCurrent = b.path === treePath
                return (
                  <React.Fragment key={b.path}>
                    {idx > 0 && <ChevronRight className="h-3 w-3 text-muted-foreground shrink-0" aria-hidden="true" />}
                    <button
                      type="button"
                      onClick={() => onNavigateBreadcrumb(b.path)}
                      className={`hover:underline cursor-pointer truncate ${
                        isCurrent ? 'text-primary font-bold' : 'text-muted-foreground'
                      }`}
                      aria-current={isCurrent ? 'page' : undefined}
                    >
                      {b.label}
                    </button>
                  </React.Fragment>
                )
              })}
            </div>

            {/* Directory Tree Entries */}
            <div className="flex-1 overflow-y-auto p-2">
              {treeLoading ? (
                <PageLoadingState compact />
              ) : treeEntries.length > 0 ? (
                <Table>
                  <TableHeader>
                    <TableRow className="border-border hover:bg-transparent">
                      <TableHead className="w-8"></TableHead>
                      <TableHead className="text-xs font-medium">{t('common.name')}</TableHead>
                      <TableHead className="text-xs font-medium w-16">{t('snapshots.browseTable.type')}</TableHead>
                      <TableHead className="text-xs font-medium w-24 text-right">
                        {t('snapshots.browseTable.size')}
                      </TableHead>
                      <TableHead className="text-xs font-medium w-36 text-right">
                        {t('snapshots.browseTable.modified')}
                      </TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {treeEntries.map((entry) => {
                      const fullPath = treePath === '/' ? `/${entry.name}` : `${treePath}/${entry.name}`
                      const isSelected = treeSelectedPaths.includes(fullPath)
                      const isDir = entry.type === 'dir'
                      return (
                        <TableRow
                          key={entry.name}
                          className="border-border hover:bg-muted/30 text-xs"
                        >
                          <TableCell className="p-2">
                            <Checkbox
                              checked={isSelected}
                              onCheckedChange={() => onToggleTreeSelection(entry.name)}
                              aria-label={fullPath}
                            />
                          </TableCell>
                          <TableCell className="font-medium font-mono text-foreground">
                            {isDir ? (
                              <button
                                type="button"
                                onClick={() => onNavigateDir(fullPath)}
                                className="flex items-center gap-1.5 text-left hover:text-primary hover:underline"
                              >
                                <Folder className="h-3.5 w-3.5 text-amber-500 dark:text-amber-400 shrink-0" aria-hidden="true" />
                                <span className="truncate">{entry.name}</span>
                              </button>
                            ) : (
                              <div className="flex items-center gap-1.5">
                                <FileCode className="h-3.5 w-3.5 text-muted-foreground shrink-0" aria-hidden="true" />
                                <span className="truncate">{entry.name}</span>
                              </div>
                            )}
                          </TableCell>
                          <TableCell className="text-[11px] text-muted-foreground font-mono">
                            {entry.type}
                          </TableCell>
                          <TableCell className="text-xs text-muted-foreground font-mono text-right">
                            {formatSize(entry.size)}
                          </TableCell>
                          <TableCell className="text-xs text-muted-foreground font-mono text-right">
                            {formatDateTime(entry.mtime)}
                          </TableCell>
                        </TableRow>
                      )
                    })}
                  </TableBody>
                </Table>
              ) : (
                <div className="p-12 text-center text-xs text-muted-foreground">
                  {t('snapshots.emptyDirectory') || 'Empty directory.'}
                </div>
              )}
            </div>
          </>
        )}
      </SheetContent>
    </Sheet>
  )
}
