import React from 'react'
import { RouterProvider } from 'react-router-dom'
import { router } from './router'
import { Toaster } from '@/components/ui/sonner'
import { TooltipProvider } from '@/components/ui/tooltip'

export const App: React.FC = () => {
  return (
    <TooltipProvider delayDuration={200}>
      <RouterProvider router={router} />
      <Toaster position="top-right" richColors />
    </TooltipProvider>
  )
}
