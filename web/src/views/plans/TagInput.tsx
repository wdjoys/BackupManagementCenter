import React, { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { X } from 'lucide-react'

interface TagInputProps {
  modelValue?: string[]
  onChange: (value: string[]) => void
  placeholder?: string
  disabled?: boolean
}

export const TagInput: React.FC<TagInputProps> = ({
  modelValue = [],
  onChange,
  placeholder,
  disabled = false,
}) => {
  const { t } = useTranslation()
  const [draft, setDraft] = useState('')

  const placeholderText = placeholder ?? t('common.tagInputPlaceholder')

  const add = () => {
    const trimmed = draft.trim()
    setDraft('')
    if (!trimmed) return
    if (modelValue.includes(trimmed)) return
    onChange([...modelValue, trimmed])
  }

  const removeAt = (index: number) => {
    const next = [...modelValue]
    next.splice(index, 1)
    onChange(next)
  }

  const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      add()
    }
  }

  return (
    <div className="w-full space-y-2">
      {modelValue.length > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {modelValue.map((item, index) => (
            <Badge
              key={`${index}-${item}`}
              variant="secondary"
              className="text-xs px-2 py-0.5 font-mono gap-1"
            >
              <span>{item}</span>
              {!disabled && (
                <button
                  type="button"
                  onClick={() => removeAt(index)}
                  className="rounded-full hover:bg-muted p-0.5 transition-colors"
                  aria-label={t('common.removeTag', { tag: item })}
                >
                  <X className="h-3 w-3 text-muted-foreground hover:text-foreground" aria-hidden="true" />
                </button>
              )}
            </Badge>
          ))}
        </div>
      )}
      <Input
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={handleKeyDown}
        onBlur={add}
        placeholder={placeholderText}
        disabled={disabled}
        className="h-8 text-xs font-mono"
      />
    </div>
  )
}
