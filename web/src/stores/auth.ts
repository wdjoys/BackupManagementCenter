import { create } from 'zustand'
import { apiGet, apiPost, isApiClientError } from '@/api/client'
import type { AuthUser } from '@/api/types'

interface AuthState {
  me: AuthUser | null
  loading: boolean
  initialized: boolean
  isLoggedIn: boolean
  fetchMe: () => Promise<boolean>
  login: (username: string, password: string) => Promise<void>
  logout: () => Promise<void>
  setup: (username: string, password: string) => Promise<void>
}

export const useAuthStore = create<AuthState>((set) => ({
  me: null,
  loading: true,
  initialized: false,
  isLoggedIn: false,

  fetchMe: async () => {
    try {
      const user = await apiGet<AuthUser>('/auth/me')
      set({ me: user, loading: false, initialized: true, isLoggedIn: true })
      return true
    } catch (err: unknown) {
      if (isApiClientError(err) && err.status === 401) {
        set({ me: null, loading: false, initialized: true, isLoggedIn: false })
        return false
      }
      set({ me: null, loading: false, initialized: true, isLoggedIn: false })
      return false
    }
  },

  login: async (username: string, password: string) => {
    await apiPost<AuthUser>('/auth/login', { username, password })
    set({ me: { username }, isLoggedIn: true })
  },

  logout: async () => {
    await apiPost('/auth/logout')
    set({ me: null, isLoggedIn: false })
  },

  setup: async (username: string, password: string) => {
    await apiPost('/setup', { username, password })
  }
}))
