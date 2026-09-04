import { createBrowserRouter, Navigate } from 'react-router-dom'
import { AppGuard } from '@/router/AppGuard'
import { MainLayout } from '@/layouts/MainLayout'
import { PageLoadingState } from '@/components/PageLoadingState'
// Route-level code splitting: pages are loaded dynamically via route `lazy` to optimize initial bundle size.
export const router = createBrowserRouter([
  {
    element: <AppGuard />,
    HydrateFallback: PageLoadingState,
    children: [
      {
        path: '/login',
        lazy: async () => {
          const { LoginView } = await import('@/views/LoginView')
          return { Component: LoginView }
        },
      },
      {
        path: '/setup',
        lazy: async () => {
          const { SetupView } = await import('@/views/SetupView')
          return { Component: SetupView }
        },
      },
      {
        element: <MainLayout />,
        HydrateFallback: PageLoadingState,
        children: [
          {
            path: '/',
            element: <Navigate to="/dashboard" replace />,
          },
          {
            path: '/dashboard',
            lazy: async () => {
              const { DashboardView } = await import('@/views/DashboardView')
              return { Component: DashboardView }
            },
          },
          {
            path: '/agents',
            lazy: async () => {
              const { AgentsView } = await import('@/views/AgentsView')
              return { Component: AgentsView }
            },
          },
          {
            path: '/storage',
            lazy: async () => {
              const { StorageView } = await import('@/views/storage/StorageView')
              return { Component: StorageView }
            },
          },
          {
            path: '/plans',
            lazy: async () => {
              const { PlansView } = await import('@/views/plans/PlansView')
              return { Component: PlansView }
            },
          },
          {
            path: '/runs',
            lazy: async () => {
              const { RunsView } = await import('@/views/runs/RunsView')
              return { Component: RunsView }
            },
          },
          {
            path: '/runs/:id',
            lazy: async () => {
              const { RunDetailView } = await import('@/views/runs/RunDetailView')
              return { Component: RunDetailView }
            },
          },
          {
            path: '/snapshots',
            lazy: async () => {
              const { SnapshotsView } = await import('@/views/snapshots/SnapshotsView')
              return { Component: SnapshotsView }
            },
          },
          {
            path: '/logs',
            lazy: async () => {
              const { LogsView } = await import('@/views/LogsView')
              return { Component: LogsView }
            },
          },
          {
            path: '/settings',
            lazy: async () => {
              const { SettingsView } = await import('@/views/settings/SettingsView')
              return { Component: SettingsView }
            },
          },
        ],
      },
      {
        path: '*',
        element: <Navigate to="/dashboard" replace />,
      },
    ],
  },
])
