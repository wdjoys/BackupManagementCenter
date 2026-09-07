import React from 'react'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

export type BadgeTone = 'default' | 'success' | 'warning' | 'destructive' | 'secondary' | 'outline'

interface StatusBadgeProps {
  tone?: BadgeTone
  children: React.ReactNode
  className?: string
  dot?: boolean
}

export const StatusBadge: React.FC<StatusBadgeProps> = ({
  tone = 'default',
  children,
  className = '',
  dot = false,
}) => {
  const toneStyles: Record<BadgeTone, string> = {
    default: 'bg-primary/15 text-primary border-primary/25 dark:bg-primary/20 dark:text-primary',
    success: 'bg-emerald-500/15 text-emerald-700 border-emerald-500/30 dark:bg-emerald-500/15 dark:text-emerald-400 dark:border-emerald-500/30',
    warning: 'bg-amber-500/15 text-amber-700 border-amber-500/30 dark:bg-amber-500/15 dark:text-amber-400 dark:border-amber-500/30',
    destructive: 'bg-rose-500/15 text-rose-700 border-rose-500/30 dark:bg-rose-500/15 dark:text-rose-400 dark:border-rose-500/30',
    secondary: 'bg-secondary text-secondary-foreground border-border',
    outline: 'bg-transparent text-muted-foreground border-border',
  }

  const dotStyles: Record<BadgeTone, string> = {
    default: 'bg-primary',
    success: 'bg-emerald-600 dark:bg-emerald-400',
    warning: 'bg-amber-600 dark:bg-amber-400',
    destructive: 'bg-rose-600 dark:bg-rose-400',
    secondary: 'bg-muted-foreground',
    outline: 'bg-muted-foreground',
  }

  return (
    <Badge
      variant="outline"
      className={cn(
        'font-medium text-xs px-2 py-0.5 inline-flex items-center gap-1.5 transition-colors',
        toneStyles[tone],
        className
      )}
    >
      {dot && (
        <span
          className={cn('h-1.5 w-1.5 rounded-full inline-block animate-pulse', dotStyles[tone])}
        />
      )}
      {children}
    </Badge>
  )
}
