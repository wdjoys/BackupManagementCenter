import { createI18n } from 'vue-i18n'
import enUS from './locales/en-US'
import zhCN from './locales/zh-CN'

export type SupportedLocale = 'zh-CN' | 'en-US'

export const SUPPORTED_LOCALES: readonly SupportedLocale[] = ['zh-CN', 'en-US']

export const LOCALE_STORAGE_KEY = 'bmc_locale'

export const DEFAULT_LOCALE: SupportedLocale = 'en-US'

const DEFAULT_DATETIME_OPTIONS: Intl.DateTimeFormatOptions = {
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
}

export const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: DEFAULT_LOCALE,
  fallbackLocale: DEFAULT_LOCALE,
  messages: {
    'en-US': enUS,
    'zh-CN': zhCN,
  },
})

function isSupportedLocale(value: unknown): value is SupportedLocale {
  return typeof value === 'string' && (SUPPORTED_LOCALES as readonly string[]).includes(value)
}

function detectBrowserLocale(): SupportedLocale {
  if (typeof navigator === 'undefined') return DEFAULT_LOCALE
  const candidates = [...(navigator.languages ?? []), navigator.language]
  for (const candidate of candidates) {
    if (candidate && candidate.toLowerCase().startsWith('zh')) return 'zh-CN'
  }
  return 'en-US'
}

function applyLocale(locale: SupportedLocale): void {
  i18n.global.locale.value = locale
  if (typeof document !== 'undefined') {
    document.documentElement.lang = locale
  }
}

/** Detects the initial locale: stored choice first, else browser language. */
export function initializeLocale(): SupportedLocale {
  let stored: string | null = null
  try {
    stored = localStorage.getItem(LOCALE_STORAGE_KEY)
  } catch {
    // Storage unavailable — fall through to browser detection.
  }
  const initial = isSupportedLocale(stored) ? stored : detectBrowserLocale()
  applyLocale(initial)
  return initial
}

/** Switches language for the session and persists it when storage allows. */
export function setLocale(locale: SupportedLocale): void {
  applyLocale(locale)
  try {
    localStorage.setItem(LOCALE_STORAGE_KEY, locale)
  } catch {
    // Persistence is best-effort; keep the session switch.
  }
}

export function currentLocale(): SupportedLocale {
  return i18n.global.locale.value
}

/** Formats a timestamp using the active UI locale. Invalid input is returned as-is. */
export function formatDateTime(
  value: string | number | Date | null | undefined,
  options?: Intl.DateTimeFormatOptions,
): string {
  if (value === null || value === undefined || value === '') return ''
  try {
    const date = value instanceof Date ? value : new Date(value)
    if (Number.isNaN(date.getTime())) return String(value)
    return date.toLocaleString(currentLocale(), { ...DEFAULT_DATETIME_OPTIONS, ...options })
  } catch {
    return String(value)
  }
}

/** Translates a dynamic enum-like value (status, phase, …) under `prefix`,
    falling back to the raw API value when no translation exists. */
export function translateEnum(prefix: string, value: string): string {
  const key = `${prefix}.${value}`
  return i18n.global.te(key) ? i18n.global.t(key) : value
}
