import { createBrowserRouter, Navigate } from 'react-router-dom'
import { AppGuard } from '@/router/AppGuard'
import { MainLayout } from '@/layouts/MainLayout'
import { LoginView } from '@/views/LoginView'
import { SetupView } from '@/views/SetupView'
import { DashboardView } from '@/views/DashboardView'
import { AgentsView } from '@/views/AgentsView'
import { StorageView } from '@/views/storage/StorageView'
import { PlansView } from '@/views/plans/PlansView'
import { RunsView } from '@/views/runs/RunsView'
import { RunDetailView } from '@/views/runs/RunDetailView'
import { SnapshotsView } from '@/views/snapshots/SnapshotsView'
import { LogsView } from '@/views/LogsView'
import { SettingsView } from '@/views/settings/SettingsView'

export const router = createBrowserRouter([
  {
    element: <AppGuard />,
    children: [
      {
        path: '/login',
        element: <LoginView />,
      },
      {
        path: '/setup',
        element: <SetupView />,
      },
      {
        element: <MainLayout />,
        children: [
          {
            path: '/',
            element: <Navigate to="/dashboard" replace />,
          },
          {
            path: '/dashboard',
            element: <DashboardView />,
          },
          {
            path: '/agents',
            element: <AgentsView />,
          },
          {
            path: '/storage',
            element: <StorageView />,
          },
          {
            path: '/plans',
            element: <PlansView />,
          },
          {
            path: '/runs',
            element: <RunsView />,
          },
          {
            path: '/runs/:id',
            element: <RunDetailView />,
          },
          {
            path: '/snapshots',
            element: <SnapshotsView />,
          },
          {
            path: '/logs',
            element: <LogsView />,
          },
          {
            path: '/settings',
            element: <SettingsView />,
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
