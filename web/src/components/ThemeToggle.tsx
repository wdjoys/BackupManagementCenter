import React from 'react'
import { useTheme } from 'next-themes'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Button } from '@/components/ui/button'
import { Sun, Moon, Laptop, Check } from 'lucide-react'
import { useTranslation } from 'react-i18next'

interface ThemeToggleProps {
  compact?: boolean
}

export const ThemeToggle: React.FC<ThemeToggleProps> = ({ compact = false }) => {
  const { t } = useTranslation()
  const { theme, setTheme } = useTheme()

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size={compact ? 'icon' : 'sm'}
          className="text-muted-foreground hover:text-foreground relative"
          title={t('theme.toggle')}
          aria-label={t('theme.toggle')}
        >
          <Sun className="h-4 w-4 rotate-0 scale-100 transition-all dark:-rotate-90 dark:scale-0" aria-hidden="true" />
          <Moon className="absolute h-4 w-4 rotate-90 scale-0 transition-all dark:rotate-0 dark:scale-100" aria-hidden="true" />
          <span className="sr-only">{t('theme.toggle')}</span>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-32">
        <DropdownMenuItem
          onClick={() => setTheme('light')}
          className="cursor-pointer text-xs justify-between"
        >
          <div className="flex items-center gap-2">
            <Sun className="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
            <span>{t('theme.light')}</span>
          </div>
          {theme === 'light' && (
            <Check className="h-3.5 w-3.5 text-primary" aria-hidden="true" />
          )}
        </DropdownMenuItem>
        <DropdownMenuItem
          onClick={() => setTheme('dark')}
          className="cursor-pointer text-xs justify-between"
        >
          <div className="flex items-center gap-2">
            <Moon className="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
            <span>{t('theme.dark')}</span>
          </div>
          {theme === 'dark' && (
            <Check className="h-3.5 w-3.5 text-primary" aria-hidden="true" />
          )}
        </DropdownMenuItem>
        <DropdownMenuItem
          onClick={() => setTheme('system')}
          className="cursor-pointer text-xs justify-between"
        >
          <div className="flex items-center gap-2">
            <Laptop className="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
            <span>{t('theme.system')}</span>
          </div>
          {theme === 'system' && (
            <Check className="h-3.5 w-3.5 text-primary" aria-hidden="true" />
          )}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
