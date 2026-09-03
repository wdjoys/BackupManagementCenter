import React, { useState } from 'react'
import { Outlet, NavLink, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import {
  LayoutDashboard,
  Server,
  FileText,
  HardDrive,
  Calendar,
  PlayCircle,
  Camera,
  Settings,
  LogOut,
  User,
  Menu,
  ShieldCheck,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetTrigger } from '@/components/ui/sheet'
import { LocaleSwitcher } from '@/components/LocaleSwitcher'
import { useAuthStore } from '@/stores/auth'
import { toastSuccess, toastError } from '@/lib/toast'
import { cn } from '@/lib/utils'

interface NavItem {
  titleKey: string
  path: string
  icon: React.ComponentType<{ className?: string }>
}

const NAV_ITEMS: NavItem[] = [
  { titleKey: 'nav.dashboard', path: '/dashboard', icon: LayoutDashboard },
  { titleKey: 'nav.agents', path: '/agents', icon: Server },
  { titleKey: 'nav.storage', path: '/storage', icon: HardDrive },
  { titleKey: 'nav.plans', path: '/plans', icon: Calendar },
  { titleKey: 'nav.runs', path: '/runs', icon: PlayCircle },
  { titleKey: 'nav.snapshots', path: '/snapshots', icon: Camera },
  { titleKey: 'nav.logs', path: '/logs', icon: FileText },
  { titleKey: 'nav.settings', path: '/settings', icon: Settings },
]

export const MainLayout: React.FC = () => {
  const { t } = useTranslation()
  const location = useLocation()
  const navigate = useNavigate()
  const { me, logout } = useAuthStore()
  const [mobileOpen, setMobileOpen] = useState(false)

  const handleLogout = async () => {
    try {
      await logout()
      toastSuccess(t('auth.logout_success') || 'Logged out successfully')
      navigate('/login')
    } catch {
      toastError(t('auth.logout_failed') || 'Logout failed')
    }
  }

  const currentNav = NAV_ITEMS.find((item) => location.pathname.startsWith(item.path))
  const pageTitle = currentNav ? t(currentNav.titleKey) : ''

  return (
    <div className="flex min-h-screen bg-background text-foreground">
      {/* Desktop Sidebar */}
      <aside className="hidden md:flex w-60 flex-col border-r border-border bg-card/40 backdrop-blur-md">
        <div className="flex h-14 items-center gap-2.5 px-4 border-b border-border">
          <div className="flex h-8 w-8 items-center justify-center rounded-md bg-primary/20 text-primary">
            <ShieldCheck className="h-5 w-5" />
          </div>
          <div className="flex flex-col">
            <span className="font-semibold text-sm tracking-tight">BMC Console</span>
            <span className="text-[10px] text-muted-foreground uppercase tracking-wider">
              Backup Center
            </span>
          </div>
        </div>

        <nav className="flex-1 space-y-1 p-2">
          {NAV_ITEMS.map((item) => {
            const Icon = item.icon
            const active = location.pathname.startsWith(item.path)
            return (
              <NavLink
                key={item.path}
                to={item.path}
                className={cn(
                  'group flex items-center gap-3 rounded-md px-3 py-2 text-xs font-medium transition-colors relative',
                  active
                    ? 'bg-primary/10 text-primary font-semibold'
                    : 'text-muted-foreground hover:bg-muted/50 hover:text-foreground'
                )}
              >
                {active && (
                  <span className="absolute left-0 top-1/2 -translate-y-1/2 h-4 w-1 rounded-r-full bg-primary" />
                )}
                <Icon
                  className={cn(
                    'h-4 w-4 shrink-0 transition-colors',
                    active ? 'text-primary' : 'text-muted-foreground group-hover:text-foreground'
                  )}
                />
                <span>{t(item.titleKey)}</span>
              </NavLink>
            )
          })}
        </nav>

        <div className="p-3 border-t border-border flex items-center justify-between">
          <span className="text-[11px] text-muted-foreground">v0.1.0</span>
          <LocaleSwitcher compact />
        </div>
      </aside>

      {/* Main Content Area */}
      <div className="flex flex-1 flex-col min-w-0">
        {/* Top Navigation Bar */}
        <header className="sticky top-0 z-30 flex h-14 items-center justify-between gap-4 border-b border-border bg-background/80 px-4 sm:px-6 backdrop-blur-md">
          <div className="flex items-center gap-3">
            {/* Mobile Sheet Trigger */}
            <Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
              <SheetTrigger asChild>
                <Button variant="ghost" size="icon" className="md:hidden">
                  <Menu className="h-5 w-5" />
                </Button>
              </SheetTrigger>
              <SheetContent side="left" className="w-64 p-0 bg-card border-r border-border">
                <SheetHeader className="p-4 border-b border-border flex flex-row items-center gap-2 space-y-0">
                  <ShieldCheck className="h-5 w-5 text-primary" />
                  <SheetTitle className="text-sm font-semibold">BMC Console</SheetTitle>
                </SheetHeader>
                <div className="flex flex-col h-[calc(100%-60px)] justify-between">
                  <nav className="space-y-1 p-2">
                    {NAV_ITEMS.map((item) => {
                      const Icon = item.icon
                      const active = location.pathname.startsWith(item.path)
                      return (
                        <NavLink
                          key={item.path}
                          to={item.path}
                          onClick={() => setMobileOpen(false)}
                          className={cn(
                            'flex items-center gap-3 rounded-md px-3 py-2.5 text-xs font-medium transition-colors',
                            active
                              ? 'bg-primary/15 text-primary font-semibold'
                              : 'text-muted-foreground hover:bg-muted/50 hover:text-foreground'
                          )}
                        >
                          <Icon className="h-4 w-4" />
                          <span>{t(item.titleKey)}</span>
                        </NavLink>
                      )
                    })}
                  </nav>
                  <div className="p-4 border-t border-border flex items-center justify-between">
                    <span className="text-xs text-muted-foreground">Language</span>
                    <LocaleSwitcher compact />
                  </div>
                </div>
              </SheetContent>
            </Sheet>

            <h1 className="text-base font-semibold tracking-tight text-foreground">
              {pageTitle}
            </h1>
          </div>

          <div className="flex items-center gap-2">
            <div className="hidden sm:block">
              <LocaleSwitcher />
            </div>

            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="sm" className="gap-2 text-xs">
                  <div className="flex h-6 w-6 items-center justify-center rounded-full bg-secondary text-foreground">
                    <User className="h-3.5 w-3.5" />
                  </div>
                  <span className="hidden sm:inline font-medium">{me?.username || 'User'}</span>
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-48">
                <DropdownMenuLabel className="text-xs font-normal text-muted-foreground">
                  {t('auth.logged_in_as') || 'Logged in as'} <span className="font-semibold text-foreground">{me?.username}</span>
                </DropdownMenuLabel>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  onClick={() => navigate('/settings')}
                  className="cursor-pointer text-xs"
                >
                  <Settings className="mr-2 h-4 w-4" />
                  <span>{t('nav.settings')}</span>
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  onClick={handleLogout}
                  className="cursor-pointer text-xs text-destructive focus:text-destructive"
                >
                  <LogOut className="mr-2 h-4 w-4" />
                  <span>{t('auth.logout') || 'Logout'}</span>
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </header>

        {/* View Outlet Container */}
        <main className="flex-1 p-4 sm:p-6 lg:p-8 max-w-7xl w-full mx-auto">
          <Outlet />
        </main>
      </div>
    </div>
  )
}
