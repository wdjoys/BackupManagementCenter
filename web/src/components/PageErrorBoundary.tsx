import React from 'react'
import { AppErrorState } from '@/components/AppErrorState'
import i18n from '@/i18n'

interface PageErrorBoundaryProps {
  children: React.ReactNode
  /** 路由标识（通常为 pathname）：变化时自动清空错误，使新页面正常渲染。 */
  resetKey?: string
}

interface PageErrorBoundaryState {
  error: Error | null
}

/**
 * 页面级错误边界。
 *
 * 单个页面渲染时抛错（典型场景：服务端返回畸形数据，如列表接口返回对象而非数组）
 * 只替换页面内容，保留外层导航/头部；否则 React Router 的默认 ErrorBoundary 会接管
 * 整个路由树，导航一并消失，等同于整站白屏。
 *
 * 「重试」清空错误并重挂载子树，从而重新触发数据拉取。
 */
export class PageErrorBoundary extends React.Component<
  PageErrorBoundaryProps,
  PageErrorBoundaryState
> {
  state: PageErrorBoundaryState = { error: null }

  static getDerivedStateFromError(error: Error): PageErrorBoundaryState {
    return { error }
  }

  componentDidCatch(error: Error) {
    console.error('[PageErrorBoundary]', error)
  }

  componentDidUpdate(prev: PageErrorBoundaryProps) {
    if (this.state.error && prev.resetKey !== this.props.resetKey) {
      this.setState({ error: null })
    }
  }

  private reset = () => this.setState({ error: null })

  render() {
    if (this.state.error) {
      return <AppErrorState message={i18n.t('common.page_crashed')} onRetry={this.reset} />
    }
    return this.props.children
  }
}
