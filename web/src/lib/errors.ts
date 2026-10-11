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
