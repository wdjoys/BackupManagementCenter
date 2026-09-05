import React from 'react'
import { useTranslation } from 'react-i18next'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import type { Retention } from '@/api/types'

export interface PlanRetentionSectionProps {
  retention: Retention
  submitting: boolean
  onUpdateRetention: (partial: Partial<Retention>) => void
}

export const PlanRetentionSection: React.FC<PlanRetentionSectionProps> = ({
  retention,
  submitting,
  onUpdateRetention,
}) => {
  const { t } = useTranslation()

  return (
    <div className="space-y-2.5 rounded-md border border-border bg-card/40 p-3.5">
      <Label className="text-sm font-semibold">{t('plans.form.retention')}</Label>
      <p className="text-xs text-muted-foreground">{t('plans.form.retentionHint')}</p>
      <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 pt-1">
        <div className="space-y-1.5">
          <span className="text-xs text-muted-foreground font-mono">keep_last</span>
          <Input
            type="number"
            min={0}
            value={retention.keep_last}
            onChange={(e) => onUpdateRetention({ keep_last: Number(e.target.value) || 0 })}
            disabled={submitting}
            className="h-8 text-xs font-mono"
          />
        </div>
        <div className="space-y-1.5">
          <span className="text-xs text-muted-foreground font-mono">keep_daily</span>
          <Input
            type="number"
            min={0}
            value={retention.keep_daily}
            onChange={(e) => onUpdateRetention({ keep_daily: Number(e.target.value) || 0 })}
            disabled={submitting}
            className="h-8 text-xs font-mono"
          />
        </div>
        <div className="space-y-1.5">
          <span className="text-xs text-muted-foreground font-mono">keep_weekly</span>
          <Input
            type="number"
            min={0}
            value={retention.keep_weekly}
            onChange={(e) => onUpdateRetention({ keep_weekly: Number(e.target.value) || 0 })}
            disabled={submitting}
            className="h-8 text-xs font-mono"
          />
        </div>
        <div className="space-y-1.5">
          <span className="text-xs text-muted-foreground font-mono">keep_monthly</span>
          <Input
            type="number"
            min={0}
            value={retention.keep_monthly}
            onChange={(e) => onUpdateRetention({ keep_monthly: Number(e.target.value) || 0 })}
            disabled={submitting}
            className="h-8 text-xs font-mono"
          />
        </div>
      </div>
    </div>
  )
}
