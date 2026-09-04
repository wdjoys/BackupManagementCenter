import React from 'react'
import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'

export interface PageLoadingStateProps {
  label?: string
  compact?: boolean
  className?: string
}

export const PageLoadingState: React.FC<PageLoadingStateProps> = ({
  label = 'Loading...',
  compact = false,
  className,
}) => {
  if (compact) {
    return (
      <div
        role="status"
        aria-live="polite"
        className={cn('w-full space-y-3 p-4', className)}
      >
        <span className="sr-only">{label}</span>
        <Skeleton className="h-8 w-1/3" />
        <Skeleton className="h-20 w-full" />
        <Skeleton className="h-20 w-full" />
      </div>
    )
  }

  return (
    <div
      role="status"
      aria-live="polite"
      className={cn('w-full space-y-6 p-6', className)}
    >
      <span className="sr-only">{label}</span>
      <div className="flex items-center justify-between">
        <div className="space-y-2">
          <Skeleton className="h-8 w-48" />
          <Skeleton className="h-4 w-72" />
        </div>
        <Skeleton className="h-9 w-28" />
      </div>
      <div className="grid gap-4 md:grid-cols-3">
        <Skeleton className="h-28 w-full" />
        <Skeleton className="h-28 w-full" />
        <Skeleton className="h-28 w-full" />
      </div>
      <div className="space-y-3">
        <Skeleton className="h-10 w-full" />
        <Skeleton className="h-16 w-full" />
        <Skeleton className="h-16 w-full" />
        <Skeleton className="h-16 w-full" />
      </div>
    </div>
  )
}
