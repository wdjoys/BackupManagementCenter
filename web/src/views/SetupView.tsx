import React, { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import * as z from 'zod'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Progress } from '@/components/ui/progress'
import { ShieldAlert, Eye, EyeOff, Loader2 } from 'lucide-react'
import { LocaleSwitcher } from '@/components/LocaleSwitcher'
import { useAuthStore } from '@/stores/auth'
import { toastSuccess } from '@/lib/toast'
import type { ApiError } from '@/api/types'

function calculatePasswordStrength(pass: string): { score: number; label: string; color: string } {
  if (!pass) return { score: 0, label: '', color: 'bg-muted' }
  let score = 0
  if (pass.length >= 8) score += 25
  if (pass.length >= 12) score += 25
  if (/[a-z]/.test(pass) && /[A-Z]/.test(pass)) score += 25
  if (/[0-9]/.test(pass) && /[^a-zA-Z0-9]/.test(pass)) score += 25

  if (score <= 25) return { score, label: 'Weak', color: 'bg-rose-500' }
  if (score <= 50) return { score, label: 'Fair', color: 'bg-amber-500' }
  if (score <= 75) return { score, label: 'Good', color: 'bg-primary' }
  return { score: 100, label: 'Strong', color: 'bg-emerald-500' }
}

export const SetupView: React.FC = () => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { setup } = useAuthStore()

  const [showPassword, setShowPassword] = useState(false)
  const [serverError, setServerError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  const schema = z
    .object({
      username: z
        .string()
        .min(1, t('validation.username_required') || 'Username is required')
        .min(3, t('validation.username_min_length') || 'Username must be at least 3 characters'),
      password: z
        .string()
        .min(1, t('validation.password_required') || 'Password is required')
        .min(8, t('validation.password_min_length') || 'Password must be at least 8 characters'),
      confirmPassword: z
        .string()
        .min(1, t('validation.confirm_password_required') || 'Please confirm password'),
    })
    .refine((data) => data.password === data.confirmPassword, {
      message: t('validation.passwords_must_match') || 'Passwords do not match',
      path: ['confirmPassword'],
    })

  type FormValues = z.infer<typeof schema>

  const {
    register,
    handleSubmit,
    watch,
    formState: { errors },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: {
      username: '',
      password: '',
      confirmPassword: '',
    },
  })

  const passwordVal = watch('password')
  const strength = calculatePasswordStrength(passwordVal)

  const onSubmit = async (data: FormValues) => {
    setServerError(null)
    setLoading(true)
    try {
      await setup(data.username, data.password)
      toastSuccess(t('auth.setup_success') || 'Initial setup complete! Please sign in.')
      navigate('/login', { replace: true })
    } catch (err: unknown) {
      const apiErr = err as ApiError
      setServerError(apiErr?.message || t('auth.setup_failed') || 'Setup initialization failed')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="flex min-h-screen flex-col items-center justify-center bg-background px-4">
      <div className="absolute right-4 top-4">
        <LocaleSwitcher />
      </div>

      <div className="w-full max-w-sm space-y-6">
        <div className="flex flex-col items-center space-y-2 text-center">
          <div className="flex h-12 w-12 items-center justify-center rounded-xl bg-primary/15 text-primary shadow-inner">
            <ShieldAlert className="h-7 w-7" />
          </div>
          <h1 className="text-xl font-bold tracking-tight text-foreground">
            {t('auth.setup_title') || 'Initial System Setup'}
          </h1>
          <p className="text-xs text-muted-foreground">
            {t('auth.setup_subtitle') || 'Create the primary administrator account'}
          </p>
        </div>

        <Card className="border-border bg-card/60 shadow-xl backdrop-blur-sm">
          <CardHeader className="pb-4">
            <CardTitle className="text-sm font-semibold">{t('auth.setup')}</CardTitle>
            <CardDescription className="text-xs">
              {t('auth.setup_desc') || 'Configure master credentials'}
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
                    autoComplete="new-password"
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
                {passwordVal && (
                  <div className="space-y-1 pt-1">
                    <div className="flex justify-between text-[10px] text-muted-foreground">
                      <span>{t('auth.password_strength') || 'Strength'}</span>
                      <span className="font-medium">{strength.label}</span>
                    </div>
                    <Progress value={strength.score} className="h-1 bg-muted" />
                  </div>
                )}
                {errors.password && (
                  <p id="password-error" className="text-[11px] text-destructive">
                    {errors.password.message}
                  </p>
                )}
              </div>

              <div className="space-y-1.5">
                <Label htmlFor="confirmPassword" className="text-xs">
                  {t('auth.confirm_password')}
                </Label>
                <Input
                  id="confirmPassword"
                  type={showPassword ? 'text' : 'password'}
                  autoComplete="new-password"
                  disabled={loading}
                  aria-describedby={errors.confirmPassword ? 'confirm-password-error' : undefined}
                  className="h-9 text-xs"
                  {...register('confirmPassword')}
                />
                {errors.confirmPassword && (
                  <p id="confirm-password-error" className="text-[11px] text-destructive">
                    {errors.confirmPassword.message}
                  </p>
                )}
              </div>

              <Button type="submit" disabled={loading} className="w-full h-9 text-xs mt-2">
                {loading && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
                {t('auth.setup_button') || t('auth.setup')}
              </Button>
            </form>
          </CardContent>
        </Card>
      </div>
    </div>
  )
}
