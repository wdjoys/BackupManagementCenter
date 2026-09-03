import React, { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { apiGet, apiPut, apiDelete } from '@/api/client'
import type { TelegramSettings, TelegramSettingsUpdate, ApiError } from '@/api/types'
import { toastSuccess, toastError } from '@/lib/toast'
import { AppErrorState } from '@/components/AppErrorState'
import { ConfirmActionDialog } from '@/components/ConfirmActionDialog'
import { Send, Trash2, CheckCircle2, Loader2, KeyRound } from 'lucide-react'

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
      const apiErr = err as ApiError
      setFetchError(apiErr?.message || t('settings.load_failed') || 'Failed to load settings')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    loadSettings()
  }, [])

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault()
    setValidationError(null)

    if (!botToken.trim()) {
      setValidationError(t('validation.bot_token_required') || 'Bot Token is required')
      return
    }
    if (!chatId.trim()) {
      setValidationError(t('validation.chat_id_required') || 'Chat ID is required')
      return
    }

    setSaving(true)
    try {
      const payload: TelegramSettingsUpdate = {
        bot_token: botToken.trim(),
        chat_id: chatId.trim(),
      }
      await apiPut('/settings/telegram', payload)
      toastSuccess(t('settings.saved_success') || 'Telegram notification settings saved')
      await loadSettings()
    } catch (err: unknown) {
      const apiErr = err as ApiError
      toastError(apiErr?.message || t('settings.save_failed') || 'Failed to save settings')
    } finally {
      setSaving(false)
    }
  }

  const handleClear = async () => {
    try {
      await apiDelete('/settings/telegram')
      toastSuccess(t('settings.cleared_success') || 'Settings cleared successfully')
      await loadSettings()
    } catch (err: unknown) {
      const apiErr = err as ApiError
      toastError(apiErr?.message || t('settings.clear_failed') || 'Failed to clear settings')
    }
  }

  if (loading) {
    return (
      <div className="flex h-64 items-center justify-center">
        <Loader2 className="h-8 w-8 animate-spin text-primary" />
      </div>
    )
  }

  if (fetchError) {
    return (
      <AppErrorState
        title={t('settings.load_failed') || 'Error Loading Settings'}
        message={fetchError}
        onRetry={loadSettings}
      />
    )
  }

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-xl font-bold tracking-tight text-foreground">
          {t('settings.title') || 'Settings'}
        </h2>
        <p className="text-xs text-muted-foreground">
          {t('settings.subtitle') || 'Configure alert integrations and notifications'}
        </p>
      </div>

      <Card className="border-border bg-card/60 shadow-md">
        <CardHeader>
          <div className="flex items-center gap-2">
            <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-sky-500/15 text-sky-400">
              <Send className="h-4 w-4" />
            </div>
            <div>
              <CardTitle className="text-sm font-semibold">
                {t('settings.telegram_title') || 'Telegram Bot Notifications'}
              </CardTitle>
              <CardDescription className="text-xs">
                {t('settings.telegram_desc') || 'Receive backup execution summaries and failure alerts'}
              </CardDescription>
            </div>
          </div>
        </CardHeader>
        <CardContent className="space-y-6">
          {settings?.configured && (
            <div className="flex items-center justify-between rounded-md border border-emerald-500/30 bg-emerald-500/10 p-3 text-xs text-emerald-400">
              <div className="flex items-center gap-2">
                <CheckCircle2 className="h-4 w-4 text-emerald-400" />
                <span>
                  {t('settings.status_configured') || 'Telegram notifications are currently active'}
                </span>
              </div>
              <Button
                variant="destructive"
                size="sm"
                className="h-7 text-xs"
                onClick={() => setClearDialogOpen(true)}
              >
                <Trash2 className="mr-1.5 h-3.5 w-3.5" />
                {t('settings.clear_config') || 'Clear'}
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
                <KeyRound className="h-3.5 w-3.5 text-muted-foreground" />
                {t('settings.bot_token') || 'Bot Token'}
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
                {t('settings.bot_token_help') || 'Provided by @BotFather upon bot creation'}
              </p>
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="chatId" className="text-xs">
                {t('settings.chat_id') || 'Chat ID'}
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
                {t('settings.chat_id_help') || 'Target group or channel conversation identifier'}
              </p>
            </div>

            <div className="flex items-center gap-2 pt-2">
              <Button type="submit" disabled={saving} className="h-9 text-xs">
                {saving && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
                {t('common.save') || 'Save Settings'}
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>

      <ConfirmActionDialog
        open={clearDialogOpen}
        onOpenChange={setClearDialogOpen}
        title={t('settings.clear_confirm_title') || 'Clear Telegram Settings?'}
        description={
          t('settings.clear_confirm_desc') ||
          'Alert notifications will be disabled until reconfigured.'
        }
        destructive
        onConfirm={handleClear}
      />
    </div>
  )
}
