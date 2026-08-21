import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { apiGet, apiPost } from '@/api/client'
import type { AuthUser } from '@/api/types'

export const useAuthStore = defineStore('auth', () => {
  const me = ref<AuthUser | null>(null)
  const loading = ref(true)

  const initialized = computed(() => {
    // If me is null and loading is done, we've confirmed no session
    return !loading.value
  })

  const isLoggedIn = computed(() => me.value !== null)

  async function fetchMe(): Promise<boolean> {
    try {
      me.value = await apiGet<AuthUser>('/auth/me')
      return true
    } catch (err: any) {
      if (err.status === 401) {
        me.value = null
        return false
      }
      // Network error or other — keep loading false, treat as not logged in
      me.value = null
      return false
    } finally {
      loading.value = false
    }
  }

  async function login(username: string, password: string): Promise<void> {
    await apiPost<AuthUser>('/auth/login', { username, password })
    me.value = { username }
  }

  async function logout(): Promise<void> {
    await apiPost('/auth/logout')
    me.value = null
  }

  async function setup(username: string, password: string): Promise<void> {
    await apiPost('/setup', { username, password })
  }

  return { me, loading, initialized, isLoggedIn, fetchMe, login, logout, setup }
})