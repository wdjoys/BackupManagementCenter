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
      ],
    },
    {
      path: '/:pathMatch(.*)*',
      redirect: '/dashboard',
    },
  ],
})

let appInitialized = false

router.beforeEach(async (to, _from, next) => {
  // First-ever navigation: check setup status and auth
  if (!appInitialized) {
    appInitialized = true

    // Try to fetch auth state
    const auth = useAuthStore()
    await auth.fetchMe()

    // Check setup status
    try {
      const status = await apiGet<SetupStatus>('/setup/status')
      if (!status.initialized) {
        if (to.path !== '/setup') {
          return next('/setup')
        }
        return next()
      }
    } catch {
      // If we can't reach the server, still allow navigation
      // The individual pages will handle errors
    }

    // If already initialized and user is on /setup, redirect to dashboard
    if (to.path === '/setup') {
      return next('/dashboard')
    }

    // If not logged in and going to a protected page, redirect to login
    if (!auth.isLoggedIn && to.path !== '/login') {
      return next('/login')
    }

    // If logged in and going to login, redirect to dashboard
    if (auth.isLoggedIn && to.path === '/login') {
      return next('/dashboard')
    }

    return next()
  }

  const auth = useAuthStore()

  // Check setup status for non-setup routes
  if (to.path !== '/setup') {
    try {
      const status = await apiGet<SetupStatus>('/setup/status')
      if (!status.initialized) {
        return next('/setup')
      }
    } catch {
      // Server unreachable — allow through, pages handle errors
    }
  }

  // Already initialized visiting /setup → redirect
  if (to.path === '/setup') {
    return next('/dashboard')
  }

  // Protected routes: check auth
  if (to.path !== '/login' && !auth.isLoggedIn) {
    // Refresh auth in case session expired
    await auth.fetchMe()
    if (!auth.isLoggedIn) {
      return next('/login')
    }
  }

  // Logged in going to login → dashboard
  if (to.path === '/login' && auth.isLoggedIn) {
    return next('/dashboard')
  }

  next()
})

export default router