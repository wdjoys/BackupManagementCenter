import React from 'react'
import { Inbox } from 'lucide-react'

interface AppEmptyStateProps {
  title?: string
  description?: string
  action?: React.ReactNode
  icon?: React.ReactNode
  className?: string
}

export const AppEmptyState: React.FC<AppEmptyStateProps> = ({
  title = 'No data',
  description,
  action,
  icon,
  className = '',
}) => {
  return (
    <div
      className={`flex min-h-[240px] flex-col items-center justify-center rounded-md border border-dashed border-border/60 p-8 text-center animate-in fade-in-50 ${className}`}
    >
      <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-muted/40 text-muted-foreground">
        {icon || <Inbox className="h-6 w-6" />}
      </div>
      <h3 className="mt-4 text-sm font-semibold text-foreground">{title}</h3>
      {description && (
        <p className="mt-1 text-xs text-muted-foreground max-w-sm">{description}</p>
      )}
      {action && <div className="mt-4">{action}</div>}
    </div>
  )
}
