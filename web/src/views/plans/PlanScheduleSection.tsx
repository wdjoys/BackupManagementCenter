import React from 'react'
import { useTranslation } from 'react-i18next'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { IANA_TIMEZONES } from './Constants'
import type { PlanFormModel } from './Types'

export interface PlanScheduleSectionProps {
  schedule: string
  timezone: string
  timeoutSeconds: number
  errors: Record<string, string>
  submitting: boolean
  cronPresets: { value: string; label: string }[]
  onUpdateModel: (partial: Partial<PlanFormModel>) => void
}

export const PlanScheduleSection: React.FC<PlanScheduleSectionProps> = ({
  schedule,
  timezone,
  timeoutSeconds,
  errors,
  submitting,
  cronPresets,
  onUpdateModel,
}) => {
  const { t } = useTranslation()

  return (
    <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
      {/* Schedule */}
      <div className="space-y-1.5 sm:col-span-1">
        <Label htmlFor="plan-schedule" className="text-xs">
          {t('plans.form.schedule')} *
        </Label>
        <Input
          id="plan-schedule"
          value={schedule}
          onChange={(e) => onUpdateModel({ schedule: e.target.value })}
          placeholder={t('plans.form.schedulePlaceholder')}
          disabled={submitting}
          aria-describedby={errors.schedule ? 'plan-schedule-error' : undefined}
          className="h-9 text-xs font-mono"
        />
        <div className="flex flex-wrap items-center gap-1.5 pt-1">
          <span className="text-xs text-muted-foreground">
            {t('plans.form.presets')}:
          </span>
          {cronPresets.map((p) => (
            <Badge
              key={p.value}
              variant="outline"
              onClick={() => onUpdateModel({ schedule: p.value })}
              className="cursor-pointer text-xs px-2 py-0.5 hover:bg-primary/20 hover:text-primary transition-colors font-mono"
            >
              {p.label}
            </Badge>
          ))}
        </div>
        {errors.schedule && (
          <p id="plan-schedule-error" className="text-[11px] text-destructive">
            {errors.schedule}
          </p>
        )}
      </div>

      {/* Timezone */}
      <div className="space-y-1.5 sm:col-span-1">
        <Label className="text-xs">{t('plans.form.timezone')} *</Label>
        <Select
          value={timezone}
          onValueChange={(val) => onUpdateModel({ timezone: val })}
          disabled={submitting}
        >
          <SelectTrigger className="h-9 text-xs">
            <SelectValue placeholder={t('plans.form.timezonePlaceholder')} />
          </SelectTrigger>
          <SelectContent className="max-h-60">
            {IANA_TIMEZONES.map((tz) => (
              <SelectItem key={tz} value={tz} className="text-xs font-mono">
                {tz}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {errors.timezone && <p className="text-[11px] text-destructive">{errors.timezone}</p>}
      </div>

      {/* Timeout Seconds */}
      <div className="space-y-1.5 sm:col-span-1">
        <Label htmlFor="plan-timeout" className="text-xs">
          {t('plans.form.timeoutSeconds')} *
        </Label>
        <Input
          id="plan-timeout"
          type="number"
          min={1}
          step={60}
          value={timeoutSeconds}
          onChange={(e) => onUpdateModel({ timeout_seconds: Number(e.target.value) || 3600 })}
          disabled={submitting}
          aria-describedby={errors.timeout_seconds ? 'plan-timeout-error' : undefined}
          className="h-9 text-xs"
        />
        {errors.timeout_seconds && (
          <p id="plan-timeout-error" className="text-[11px] text-destructive">
            {errors.timeout_seconds}
          </p>
        )}
      </div>
    </div>
  )
}
