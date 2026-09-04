import React, { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { apiGet, apiPut, apiDelete, isApiClientError } from '@/api/client'
import type { TelegramSettings, TelegramSettingsUpdate } from '@/api/types'
import { toastSuccess, toastError } from '@/lib/toast'
import { AppErrorState } from '@/components/AppErrorState'
import { PageLoadingState } from '@/components/PageLoadingState'
import { ConfirmActionDialog } from '@/components/ConfirmActionDialog'
import { Send, Trash2, CheckCircle2, Loader2, KeyRound, Save } from 'lucide-react'

export const SettingsView: React.FC = () => {
  const { t } = useTranslation()

  const [settings, setSettings] = useState<TelegramSettings | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [fetchError, setFetchError] = useState<string | null>(null)

  const [botToken, setBotToken] = useState('')
  const [chatId, setChatId] = useState('')
  const [validationError, setValidationError] = useState<string | null>(null)

  const [clearDialogOpen, setClearDialogOpen] = useState(false)

  const loadSettings = async () => {
    setLoading(true)
    setFetchError(null)
    try {
      const data = await apiGet<TelegramSettings>('/settings/telegram')
      setSettings(data)
      setChatId(data.chat_id || '')
      setBotToken('')
    } catch (err: unknown) {
      setFetchError(isApiClientError(err) ? err.message : t('settings.load_failed'))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    loadSettings()
  }, [])

  const handleSave = async (e: React.SyntheticEvent) => {
    e.preventDefault()
    setValidationError(null)

    if (!botToken.trim()) {
      setValidationError(t('validation.bot_token_required'))
      return
    }
    if (!chatId.trim()) {
      setValidationError(t('validation.chat_id_required'))
      return
    }

    setSaving(true)
    try {
      const payload: TelegramSettingsUpdate = {
        bot_token: botToken.trim(),
        chat_id: chatId.trim(),
      }
      await apiPut('/settings/telegram', payload)
      toastSuccess(t('settings.saved_success'))
      await loadSettings()
    } catch (err: unknown) {
      toastError(isApiClientError(err) ? err.message : t('settings.save_failed'))
    } finally {
      setSaving(false)
    }
  }

  const handleClear = async () => {
    try {
      await apiDelete('/settings/telegram')
      toastSuccess(t('settings.cleared_success'))
      await loadSettings()
    } catch (err: unknown) {
      toastError(isApiClientError(err) ? err.message : t('settings.clear_failed'))
    }
  }

  if (loading) {
    return <PageLoadingState />
  }

  if (fetchError) {
    return (
      <AppErrorState
        title={t('settings.load_failed')}
        message={fetchError}
        onRetry={loadSettings}
      />
    )
  }

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-xl font-bold tracking-tight text-foreground">
          {t('settings.title')}
        </h2>
        <p className="text-xs text-muted-foreground">
          {t('settings.subtitle')}
        </p>
      </div>

      <Card className="border-border bg-card/60 shadow-md">
        <CardHeader>
          <div className="flex items-center gap-2">
            <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-sky-500/15 text-sky-600 dark:text-sky-400">
              <Send className="h-4 w-4" aria-hidden="true" />
            </div>
            <div>
              <CardTitle className="text-sm font-semibold">
                {t('settings.telegram_title')}
              </CardTitle>
              <CardDescription className="text-xs">
                {t('settings.telegram_desc')}
              </CardDescription>
            </div>
          </div>
        </CardHeader>
        <CardContent className="space-y-6">
          {settings?.configured && (
            <div className="flex items-center justify-between rounded-md border border-emerald-500/30 bg-emerald-500/10 p-3 text-xs text-emerald-600 dark:text-emerald-400 font-medium">
              <div className="flex items-center gap-2">
                <CheckCircle2 className="h-4 w-4 text-emerald-600 dark:text-emerald-400" aria-hidden="true" />
                <span>
                  {t('settings.status_configured')}
                </span>
              </div>
              <Button
                variant="destructive"
                size="sm"
                className="h-7 text-xs"
                onClick={() => setClearDialogOpen(true)}
              >
                <Trash2 className="mr-1.5 h-3.5 w-3.5" aria-hidden="true" />
                {t('settings.clear_config')}
              </Button>
            </div>
          )}

          <form onSubmit={handleSave} className="space-y-4 max-w-lg">
            {validationError && (
              <Alert variant="destructive" className="py-2 text-xs">
                <AlertDescription>{validationError}</AlertDescription>
              </Alert>
            )}

            <div className="space-y-1.5">
              <Label htmlFor="botToken" className="text-xs flex items-center gap-1.5">
                <KeyRound className="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
                {t('settings.bot_token')}
              </Label>
              <Input
                id="botToken"
                type="password"
                autoComplete="new-password"
                placeholder={settings?.configured ? '••••••••••••••••' : '123456789:ABCdefGHIjklMNO...'}
                value={botToken}
                onChange={(e) => setBotToken(e.target.value)}
                disabled={saving}
                className="h-9 text-xs"
              />
              <p className="text-[11px] text-muted-foreground">
                {t('settings.bot_token_help')}
              </p>
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="chatId" className="text-xs">
                {t('settings.chat_id')}
              </Label>
              <Input
                id="chatId"
                type="text"
                placeholder="-1001234567890"
                value={chatId}
                onChange={(e) => setChatId(e.target.value)}
                disabled={saving}
                className="h-9 text-xs"
              />
              <p className="text-[11px] text-muted-foreground">
                {t('settings.chat_id_help')}
              </p>
            </div>

            <div className="flex items-center gap-2 pt-2">
              <Button type="submit" disabled={saving} className="h-9 text-xs gap-1.5">
                {saving ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
                ) : (
                  <Save className="h-3.5 w-3.5" aria-hidden="true" />
                )}
                {t('common.save')}
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>

      <ConfirmActionDialog
        open={clearDialogOpen}
        onOpenChange={setClearDialogOpen}
        title={t('settings.clear_confirm_title')}
        description={t('settings.clear_confirm_desc')}
        destructive
        onConfirm={handleClear}
      />
    </div>
  )
}
