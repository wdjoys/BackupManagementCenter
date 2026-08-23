import type { ApiError } from './types'

const BASE_URL = '/api/v1'

/**
 * Parse a cookie value by name from document.cookie.
 */
export function getCookie(name: string): string | null {
  const match = document.cookie.match(new RegExp(`(?:^|;\\s*)${name}=([^;]*)`))
  return match ? decodeURIComponent(match[1]) : null
}

/**
 * Thin fetch wrapper with automatic JSON parsing, credentials, and CSRF.
 */
export async function api<T = unknown>(
  path: string,
  options: RequestInit = {},
): Promise<T> {
  const url = `${BASE_URL}${path}`
  const headers = new Headers(options.headers)

  // Always send cookies
  const fetchOptions: RequestInit = {
    ...options,
    credentials: 'include',
    headers,
  }

  // Non-GET: add Content-Type and CSRF token
  const method = (options.method || 'GET').toUpperCase()
  if (method !== 'GET') {
    if (!headers.has('Content-Type')) {
      headers.set('Content-Type', 'application/json')
    }
    const csrfToken = getCookie('bmc_csrf')
    if (csrfToken) {
      headers.set('X-CSRF-Token', csrfToken)
    }
  }

  const response = await fetch(url, fetchOptions)

  // 204 No Content
  if (response.status === 204) {
    return undefined as T
  }

  // Try JSON
  const contentType = response.headers.get('content-type') || ''
  if (contentType.includes('application/json')) {
    const body = await response.json()
    if (!response.ok) {
      const err = body.error as ApiError | undefined
      throw Object.assign(new Error(err?.message || response.statusText), {
        code: err?.code || 'unknown',
        status: response.status,
      })
    }
    return body as T
  }

  // Non-JSON success
  if (!response.ok) {
    throw Object.assign(new Error(response.statusText), {
      code: 'unknown',
      status: response.status,
    })
  }

  const text = await response.text()
  return text as unknown as T
}

// Convenience helpers
export const apiGet = <T>(path: string, params?: Record<string, string | number | undefined>) => {
  let url = path
  if (params) {
    const qs = Object.entries(params)
      .filter(([, v]) => v !== undefined && v !== null)
      .map(([k, v]) => `${encodeURIComponent(k)}=${encodeURIComponent(String(v))}`)
      .join('&')
    if (qs) url += `?${qs}`
  }
  return api<T>(url)
}

export const apiPost = <T>(path: string, body?: unknown) =>
  api<T>(path, { method: 'POST', body: body !== undefined ? JSON.stringify(body) : undefined })

export const apiPut = <T>(path: string, body: unknown) =>
  api<T>(path, { method: 'PUT', body: JSON.stringify(body) })
export const apiPatch = <T>(path: string, body: unknown) =>
  api<T>(path, { method: 'PATCH', body: JSON.stringify(body) })

export const apiDelete = (path: string) =>
  api<void>(path, { method: 'DELETE' })