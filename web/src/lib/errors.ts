import i18n from '@/i18n'

/**
 * 稳定错误码 → 本地化文案。
 *
 * 约定：文案键为 `errors.<code>`（zh-CN / en-US 同步维护）。未映射的码返回 null，
 * 调用方必须沿用服务端 message —— 绝不回退成裸 code 或空白，否则中文界面会直接
 * 暴露服务端英文原文（如 "a storage target with this name already exists"）。
 */
export function localizeErrorCode(code: string | null | undefined): string | null {
  if (!code) return null
  const key = `errors.${code}`
  return i18n.exists(key) ? i18n.t(key) : null
}

/**
 * 表单提交失败时的可读文案：已映射的错误码（如 validation_failed）在 message 里只剩
 * 本地化通用文案，被拒的具体原因（"name must be 1..200 chars"、
 * "schedule interval (5m0s) is shorter than timeout_seconds"…）只存在于 serverMessage。
 * 两者一起展示，否则用户只看到"请求参数校验失败"却无从修正。
 */
export function apiErrorMessage(err: unknown, fallback: string): string {
  const e = err as { message?: unknown; serverMessage?: unknown } | null
  if (!e || typeof e.message !== 'string') return fallback
  const detail = typeof e.serverMessage === 'string' ? e.serverMessage : ''
  return detail && detail !== e.message
    ? `${e.message}${i18n.t('common.detailSeparator')}${detail}`
    : e.message
}
