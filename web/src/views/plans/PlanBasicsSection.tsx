import React from 'react'
import { useTranslation } from 'react-i18next'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { translateEnum } from '@/i18n'
import type { Agent, Repository } from '@/api/types'
import type { PlanFormModel, PlanKind } from './Types'

export interface PlanBasicsSectionProps {
  model: PlanFormModel
  errors: Record<string, string>
  agents: Agent[]
  filteredRepos: Repository[]
  planKinds: { value: PlanKind; label: string }[]
  submitting: boolean
  onUpdateModel: (partial: Partial<PlanFormModel>) => void
  onKindChange: (kind: PlanKind) => void
  onAgentChange: (agentId: string) => void
}

export const PlanBasicsSection: React.FC<PlanBasicsSectionProps> = ({
  model,
  errors,
  agents,
  filteredRepos,
  planKinds,
  submitting,
  onUpdateModel,
  onKindChange,
  onAgentChange,
}) => {
  const { t } = useTranslation()

  return (
    <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
      {/* Name */}
      <div className="space-y-1.5">
        <Label htmlFor="plan-name" className="text-xs">
          {t('plans.form.name')} *
        </Label>
        <Input
          id="plan-name"
          value={model.name}
          onChange={(e) => onUpdateModel({ name: e.target.value })}
          placeholder={t('plans.form.namePlaceholder')}
          disabled={submitting}
          aria-describedby={errors.name ? 'plan-name-error' : undefined}
          className="h-9 text-xs"
        />
        {errors.name && (
          <p id="plan-name-error" className="text-[11px] text-destructive">
            {errors.name}
          </p>
        )}
      </div>

      {/* Kind */}
      <div className="space-y-1.5">
        <Label className="text-xs">{t('plans.form.kind')} *</Label>
        <Select
          value={model.kind}
          onValueChange={(val) => onKindChange(val as PlanKind)}
          disabled={submitting}
        >
          <SelectTrigger className="h-9 text-xs">
            <SelectValue placeholder={t('plans.form.kindPlaceholder')} />
          </SelectTrigger>
          <SelectContent>
            {planKinds.map((k) => (
              <SelectItem key={k.value} value={k.value} className="text-xs">
                {k.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {errors.kind && <p className="text-[11px] text-destructive">{errors.kind}</p>}
      </div>

      {/* Agent */}
      <div className="space-y-1.5">
        <Label className="text-xs">{t('plans.form.agent')} *</Label>
        <Select
          value={model.agent_id}
          onValueChange={onAgentChange}
          disabled={submitting}
        >
          <SelectTrigger className="h-9 text-xs">
            <SelectValue placeholder={t('plans.form.agent')} />
          </SelectTrigger>
          <SelectContent>
            {agents.map((a) => (
              <SelectItem key={a.id} value={a.id} className="text-xs">
                <span>{a.name}</span>
                <span className="ml-2 text-[10px] text-muted-foreground">
                  ({translateEnum('status', a.status)})
                </span>
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {errors.agent_id && <p className="text-[11px] text-destructive">{errors.agent_id}</p>}
      </div>

      {/* Repository */}
      <div className="space-y-1.5">
        <Label className="text-xs">{t('plans.form.repository')} *</Label>
        <Select
          value={model.repository_id}
          onValueChange={(val) => onUpdateModel({ repository_id: val })}
          disabled={submitting || filteredRepos.length === 0}
        >
          <SelectTrigger className="h-9 text-xs">
            <SelectValue placeholder={t('plans.form.repository')} />
          </SelectTrigger>
          <SelectContent>
            {filteredRepos.map((r) => (
              <SelectItem key={r.id} value={r.id} className="text-xs">
                {r.storage_target_name} @ {r.agent_name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {errors.repository_id && (
          <p className="text-[11px] text-destructive">{errors.repository_id}</p>
        )}
      </div>
    </div>
  )
}
