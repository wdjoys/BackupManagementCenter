import React from 'react'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Button } from '@/components/ui/button'
import { Globe } from 'lucide-react'
import {
  SUPPORTED_LOCALES,
  currentLocale,
  setLocale,
  type SupportedLocale,
} from '@/i18n'
import { useTranslation } from 'react-i18next'

interface LocaleSwitcherProps {
  compact?: boolean
}

const LOCALE_LABELS: Record<SupportedLocale, string> = {
  'en-US': 'English',
  'zh-CN': '简体中文',
}

export const LocaleSwitcher: React.FC<LocaleSwitcherProps> = ({ compact = false }) => {
  const { t } = useTranslation()
  const activeLocale = currentLocale()

  const handleSelect = (locale: SupportedLocale) => {
    if (locale !== activeLocale) {
      setLocale(locale)
    }
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size={compact ? 'icon' : 'sm'}
          className="text-muted-foreground hover:text-foreground"
          title={t('common.switch_language')}
        >
          <Globe className="h-4 w-4" />
          {!compact && (
            <span className="ml-2 text-xs font-normal">
              {LOCALE_LABELS[activeLocale]}
            </span>
          )}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-36">
        {SUPPORTED_LOCALES.map((locale) => (
          <DropdownMenuItem
            key={locale}
            disabled={locale === activeLocale}
            onClick={() => handleSelect(locale)}
            className="cursor-pointer text-xs justify-between"
          >
            <span>{LOCALE_LABELS[locale]}</span>
            {locale === activeLocale && (
              <span className="text-[10px] text-primary font-medium">✓</span>
            )}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
