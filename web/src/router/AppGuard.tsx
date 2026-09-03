import React, { useEffect, useState } from 'react'
import { Navigate, Outlet, useLocation } from 'react-router-dom'
import { useAuthStore } from '@/stores/auth'
import { apiGet } from '@/api/client'
import type { SetupStatus } from '@/api/types'
import { Loader2 } from 'lucide-react'

export const AppGuard: React.FC = () => {
  const { initialized, isLoggedIn, fetchMe } = useAuthStore()
  const [setupChecked, setSetupChecked] = useState(false)
  const [isSetupNeeded, setIsSetupNeeded] = useState(false)
  const location = useLocation()

  useEffect(() => {
    let active = true

    const checkState = async () => {
      try {
        const res = await apiGet<SetupStatus>('/setup/status')
        if (active) {
          if (!res.initialized) {
            setIsSetupNeeded(true)
            setSetupChecked(true)
            return
          }
          setIsSetupNeeded(false)
          setSetupChecked(true)
        }
      } catch {
        if (active) {
          setSetupChecked(true)
        }
      }

      if (active) {
        await fetchMe()
      }
    }

    checkState()

    return () => {
      active = false
    }
  }, [fetchMe])

  if (!setupChecked || (!isSetupNeeded && !initialized)) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background text-foreground">
        <div className="flex flex-col items-center gap-3">
          <Loader2 className="h-8 w-8 animate-spin text-primary" />
          <span className="text-xs text-muted-foreground">Initializing BMC Console...</span>
        </div>
      </div>
    )
  }

  // Setup needed
  if (isSetupNeeded && location.pathname !== '/setup') {
    return <Navigate to="/setup" replace />
  }

  // Setup completed, avoid visiting /setup
  if (!isSetupNeeded && location.pathname === '/setup') {
    return <Navigate to="/login" replace />
  }

  // Unauthenticated user attempting to access protected route
  const isPublicRoute = location.pathname === '/login' || location.pathname === '/setup'
  if (!isLoggedIn && !isPublicRoute) {
    return <Navigate to="/login" state={{ from: location }} replace />
  }

  // Authenticated user attempting to access login page
  if (isLoggedIn && location.pathname === '/login') {
    return <Navigate to="/dashboard" replace />
  }

  return <Outlet />
}
