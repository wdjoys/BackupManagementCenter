import React from 'react'
import { AlertCircle } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useTranslation } from 'react-i18next'

interface AppErrorStateProps {
  title?: string
  message?: string
  onRetry?: () => void
  className?: string
}

export const AppErrorState: React.FC<AppErrorStateProps> = ({
  title,
  message,
  onRetry,
  className = '',
}) => {
  const { t } = useTranslation()

  return (
    <div
      className={`flex min-h-[240px] flex-col items-center justify-center rounded-md border border-destructive/20 bg-destructive/5 p-8 text-center animate-in fade-in-50 ${className}`}
    >
      <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-destructive/10 text-destructive">
        <AlertCircle className="h-6 w-6" />
      </div>
      <h3 className="mt-4 text-sm font-semibold text-foreground">
        {title || t('common.error_occurred')}
      </h3>
      {message && (
        <p className="mt-1 text-xs text-muted-foreground max-w-sm">{message}</p>
      )}
      {onRetry && (
        <div className="mt-4">
          <Button variant="outline" size="sm" onClick={onRetry}>
            {t('common.retry')}
          </Button>
        </div>
      )}
    </div>
  )
}
