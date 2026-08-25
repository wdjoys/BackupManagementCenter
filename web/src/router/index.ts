import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { apiGet } from '@/api/client'
import type { SetupStatus } from '@/api/types'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    {
      path: '/login',
      name: 'Login',
      component: () => import('@/views/LoginView.vue'),
    },
    {
      path: '/setup',
      name: 'Setup',
      component: () => import('@/views/SetupView.vue'),
    },
    {
      path: '/',
      component: () => import('@/layouts/MainLayout.vue'),
      redirect: '/dashboard',
      children: [
        {
          path: 'dashboard',
          name: 'Dashboard',
          component: () => import('@/views/DashboardView.vue'),
          meta: { title: 'nav.dashboard' },
        },
        {
          path: 'agents',
          name: 'Agents',
          component: () => import('@/views/AgentsView.vue'),
          meta: { title: 'nav.agents' },
        },
        {
          path: 'logs',
          name: 'Logs',
          component: () => import('@/views/LogsView.vue'),
          meta: { title: 'nav.logs' },
        },
        {
          path: 'storage',
          name: 'Storage',
          component: () => import('@/views/storage/StorageView.vue'),
          meta: { title: 'nav.storage' },
        },
        {
          path: 'plans',
          name: 'Plans',
          component: () => import('@/views/plans/PlansView.vue'),
          meta: { title: 'nav.plans' },
        },
        {
          path: 'runs',
          name: 'Runs',
          component: () => import('@/views/runs/RunsView.vue'),
          meta: { title: 'nav.runs' },
        },
        {
          path: 'runs/:id',
          name: 'RunDetail',
          component: () => import('@/views/runs/RunDetailView.vue'),
          meta: { title: 'nav.runDetail' },
        },
        {
          path: 'snapshots',
          name: 'Snapshots',
          component: () => import('@/views/snapshots/SnapshotsView.vue'),
          meta: { title: 'nav.snapshots' },
        },
        {
          path: 'settings',
          name: 'Settings',
          component: () => import('@/views/settings/SettingsView.vue'),
          meta: { title: 'nav.settings' },
        },
      ],
    },
    {
      path: '/:pathMatch(.*)*',
      redirect: '/dashboard',
    },
  ],
})

let appInitialized = false
let setupInitialized: boolean | null = null

router.beforeEach(async (to) => {
  const auth = useAuthStore()
  if (!appInitialized) {
    appInitialized = true
    await auth.fetchMe()
  }

  // Cache the first setup result for the redirect into /setup; refresh it
  // before every non-setup navigation so completing setup takes effect.
  if (setupInitialized === null || to.path !== '/setup') {
    try {
      const status = await apiGet<SetupStatus>('/setup/status')
      setupInitialized = status.initialized
    } catch {
      // Server unreachable — individual pages surface their own error.
    }
  }

  if (setupInitialized === false) {
    if (to.path !== '/setup') {
      return '/setup'
    }
    return true
  }
  if (setupInitialized === true && to.path === '/setup') {
    return '/dashboard'
  }

  // Setup itself is public. All other non-login pages require a session.
  if (to.path !== '/login' && to.path !== '/setup' && !auth.isLoggedIn) {
    await auth.fetchMe()
    if (!auth.isLoggedIn) {
      return '/login'
    }
  }
  if (to.path === '/login' && auth.isLoggedIn) {
    return '/dashboard'
  }

  return true
})

export default router
