import * as React from 'react'

/**
 * Radix 的 Dialog/AlertDialog/Sheet 在未使用 DialogTrigger 时（本项目全部由
 * setOpen(true) 打开），关闭后会把焦点丢到 <body>，键盘用户因此失去位置。
 *
 * Radix 在自动聚焦进对话框之前派发 `onOpenAutoFocus`，此刻 document.activeElement
 * 仍是打开对话框的那个元素；把它记下来，关闭时归还。
 * 关闭回调里 `preventDefault()` 用于跳过 Radix 内部「聚焦 triggerRef」的兜底
 * （本项目没有 triggerRef），避免焦点被丢到 body。
 */
export function useRestoreFocusOnClose() {
  const previouslyFocused = React.useRef<HTMLElement | null>(null)

  const rememberFocus = React.useCallback((event: Event) => {
    if (event.defaultPrevented) return
    previouslyFocused.current = document.activeElement as HTMLElement | null
  }, [])

  const restoreFocus = React.useCallback((event: Event) => {
    event.preventDefault()
    const el = previouslyFocused.current
    previouslyFocused.current = null
    if (el && el.isConnected && typeof el.focus === 'function') el.focus()
  }, [])

  return { rememberFocus, restoreFocus }
}
