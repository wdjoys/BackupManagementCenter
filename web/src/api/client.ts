import type { ApiErrorPayload } from './types'

const BASE_URL = '/api/v1'

export class ApiClientError extends Error {
  readonly code: string
  readonly status: number

  constructor(message: string, code: string, status: number) {
    super(message)
    this.name = 'ApiClientError'
    this.code = code
    this.status = status
    Object.setPrototypeOf(this, new.target.prototype)
  }
}

export function isApiClientError(value: unknown): value is ApiClientError {
  return value instanceof ApiClientError
}

export function isAbortError(value: unknown): boolean {
  return typeof DOMException !== 'undefined' && value instanceof DOMException && value.name === 'AbortError'
}

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
export interface ApiResponseMeta {
  cache: string | null
  verifiedAt: string | null
}

export async function api<T = unknown>(
  path: string,
  options: RequestInit = {},
  onMeta?: (meta: ApiResponseMeta) => void,
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
  onMeta?.({
    cache: response.headers.get('X-BMC-Cache'),
    verifiedAt: response.headers.get('X-BMC-Verified-At'),
  })

  // 204 No Content
  if (response.status === 204) {
    return undefined as T
  }

  // Try JSON
  const contentType = response.headers.get('content-type') || ''
  if (contentType.includes('application/json')) {
    const body = await response.json()
    if (!response.ok) {
      const err = body?.error as ApiErrorPayload | undefined
      throw new ApiClientError(
        err?.message || response.statusText || 'Request failed',
        err?.code || 'unknown',
        response.status,
      )
    }
    return body as T
  }

  // Non-JSON failure
  if (!response.ok) {
    throw new ApiClientError(
      response.statusText || 'Request failed',
      'unknown',
      response.status,
    )
  }

  const text = await response.text()
  return text as unknown as T
}

// Convenience helpers
export const apiGet = <T>(
  path: string,
  params?: Record<string, string | number | undefined>,
  options?: Omit<RequestInit, 'method' | 'body'>,
): Promise<T> => {
  let url = path
  if (params) {
    const qs = Object.entries(params)
      .filter(([, v]) => v !== undefined && v !== null)
      .map(([k, v]) => `${encodeURIComponent(k)}=${encodeURIComponent(String(v))}`)
      .join('&')
    if (qs) url += `?${qs}`
  }
  return api<T>(url, { ...options, method: 'GET' })
}

export const apiGetWithMeta = async <T>(
  path: string,
  params?: Record<string, string | number | undefined>,
  options?: Omit<RequestInit, 'method' | 'body'>,
): Promise<{ data: T; meta: ApiResponseMeta }> => {
  let url = path
  if (params) {
    const qs = Object.entries(params)
      .filter(([, v]) => v !== undefined && v !== null)
      .map(([k, v]) => `${encodeURIComponent(k)}=${encodeURIComponent(String(v))}`)
      .join('&')
    if (qs) url += `?${qs}`
  }
  let meta: ApiResponseMeta = { cache: null, verifiedAt: null }
  const data = await api<T>(url, { ...options, method: 'GET' }, (responseMeta) => {
    meta = responseMeta
  })
  return { data, meta }
}

export const apiPost = <T>(path: string, body?: unknown) =>
  api<T>(path, { method: 'POST', body: body !== undefined ? JSON.stringify(body) : undefined })

export const apiPut = <T>(path: string, body: unknown) =>
  api<T>(path, { method: 'PUT', body: JSON.stringify(body) })

export const apiPatch = <T>(path: string, body: unknown) =>
  api<T>(path, { method: 'PATCH', body: JSON.stringify(body) })

export const apiDelete = <T = void>(path: string) =>
  api<T>(path, { method: 'DELETE' })
