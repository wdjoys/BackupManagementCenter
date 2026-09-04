import React, { useState } from 'react'
import { useNavigate, useLocation } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import * as z from 'zod'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { ShieldCheck, Eye, EyeOff, Loader2, LogIn } from 'lucide-react'
import { LocaleSwitcher } from '@/components/LocaleSwitcher'
import { ThemeToggle } from '@/components/ThemeToggle'
import { useAuthStore } from '@/stores/auth'
import { toastSuccess } from '@/lib/toast'
import type { ApiError } from '@/api/types'

export const LoginView: React.FC = () => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const location = useLocation()
  const { login } = useAuthStore()

  const [showPassword, setShowPassword] = useState(false)
  const [serverError, setServerError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  const schema = z.object({
    username: z.string().min(1, t('validation.username_required') || 'Username is required'),
    password: z.string().min(1, t('validation.password_required') || 'Password is required'),
  })

  type FormValues = z.infer<typeof schema>

  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: {
      username: '',
      password: '',
    },
  })

  const onSubmit = async (data: FormValues) => {
    setServerError(null)
    setLoading(true)
    try {
      await login(data.username, data.password)
      toastSuccess(t('auth.login_success') || 'Signed in successfully')
      let from = '/dashboard'
      if (location.state && typeof location.state === 'object' && 'from' in location.state) {
        const stateFrom = location.state.from
        if (stateFrom && typeof stateFrom === 'object' && 'pathname' in stateFrom && typeof stateFrom.pathname === 'string') {
          from = stateFrom.pathname
        }
      }
      navigate(from, { replace: true })
    } catch (err: unknown) {
      const apiErr = err as ApiError
      setServerError(apiErr?.message || t('auth.login_failed') || 'Invalid username or password')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="flex min-h-screen flex-col items-center justify-center bg-background px-4">
      <div className="absolute right-4 top-4 flex items-center gap-1">
        <ThemeToggle compact />
        <LocaleSwitcher />
      </div>

      <div className="w-full max-w-sm space-y-6">
        <div className="flex flex-col items-center space-y-2 text-center">
          <div className="flex h-12 w-12 items-center justify-center rounded-xl bg-primary/15 text-primary shadow-inner">
            <ShieldCheck className="h-7 w-7" />
          </div>
          <h1 className="text-xl font-bold tracking-tight text-foreground">
            Backup Management Center
          </h1>
          <p className="text-xs text-muted-foreground">
            {t('auth.login_subtitle') || 'Enter your credentials to access the console'}
          </p>
        </div>

        <Card className="border-border bg-card/60 shadow-xl backdrop-blur-sm">
          <CardHeader className="pb-4">
            <CardTitle className="text-sm font-semibold">{t('auth.login')}</CardTitle>
            <CardDescription className="text-xs">
              {t('auth.login_desc') || 'System administrator sign-in'}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
              {serverError && (
                <Alert variant="destructive" className="py-2.5 text-xs">
                  <AlertDescription>{serverError}</AlertDescription>
                </Alert>
              )}

              <div className="space-y-1.5">
                <Label htmlFor="username" className="text-xs">
                  {t('auth.username')}
                </Label>
                <Input
                  id="username"
                  type="text"
                  autoComplete="username"
                  placeholder="admin"
                  disabled={loading}
                  aria-describedby={errors.username ? 'username-error' : undefined}
                  className="h-9 text-xs"
                  {...register('username')}
                />
                {errors.username && (
                  <p id="username-error" className="text-[11px] text-destructive">
                    {errors.username.message}
                  </p>
                )}
              </div>

              <div className="space-y-1.5">
                <Label htmlFor="password" className="text-xs">
                  {t('auth.password')}
                </Label>
                <div className="relative">
                  <Input
                    id="password"
                    type={showPassword ? 'text' : 'password'}
                    autoComplete="current-password"
                    disabled={loading}
                    aria-describedby={errors.password ? 'password-error' : undefined}
                    className="h-9 pr-9 text-xs"
                    {...register('password')}
                  />
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    className="absolute right-0.5 top-0.5 h-8 w-8 text-muted-foreground hover:text-foreground"
                    onClick={() => setShowPassword(!showPassword)}
                  >
                    {showPassword ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                  </Button>
                </div>
                {errors.password && (
                  <p id="password-error" className="text-[11px] text-destructive">
                    {errors.password.message}
                  </p>
                )}
              </div>

              <Button type="submit" disabled={loading} className="w-full h-9 text-xs mt-2 gap-2">
                {loading ? (
                  <Loader2 className="h-4 w-4 animate-spin" />
                ) : (
                  <LogIn className="h-4 w-4" />
                )}
                {t('auth.login_button') || t('auth.login')}
              </Button>
            </form>
          </CardContent>
        </Card>
      </div>
    </div>
  )
}
