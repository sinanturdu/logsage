import { useId, useState, type FormEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useNavigate, useSearchParams } from 'react-router'
import { ApiError, api, errorMessage } from '../api/client'
import { meKey, safeNext } from '../auth/session'

type FieldErrors = { email?: string; password?: string }

function validate(email: string, password: string): FieldErrors {
  const errors: FieldErrors = {}
  if (!email.trim()) errors.email = 'Enter your email address.'
  else if (!/^[^\s@]+@[^\s@]+$/.test(email.trim())) errors.email = 'Enter an email address such as you@example.com.'
  if (!password) errors.password = 'Enter your password.'
  return errors
}

function loginError(err: unknown): string {
  if (err instanceof ApiError) {
    // Never say which of the two was wrong.
    if (err.kind === 'unauthorized') return 'Email or password is wrong. Check both and try again.'
    if (err.kind === 'rate_limited') return `Too many sign-in attempts. Try again in ${err.retryAfter ?? 1} seconds.`
  }
  return errorMessage(err)
}

export function LoginPage() {
  const [params] = useSearchParams()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const id = useId()
  const [email, setEmail] = useState('')
  // The password lives only in this component's state while the form is open.
  const [password, setPassword] = useState('')
  const [fieldErrors, setFieldErrors] = useState<FieldErrors>({})
  const [formError, setFormError] = useState('')
  const [busy, setBusy] = useState(false)
  const expired = params.get('expired') === '1'

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    setFormError('')
    const errors = validate(email, password)
    setFieldErrors(errors)
    if (errors.email || errors.password) return
    setBusy(true)
    try {
      const { user } = await api.login(email.trim(), password)
      setPassword('')
      qc.setQueryData(meKey, { user })
      navigate(safeNext(params.get('next')), { replace: true })
    } catch (err) {
      setPassword('')
      setFormError(loginError(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <main className="login">
      <div className="login-card">
        <p className="mark">LogSage</p>
        <h1>Sign in</h1>
        <p className="lead">Use the account an operator created for you.</p>
        {expired && (
          <p className="notice" role="status">
            Your session ended. Sign in again to continue.
          </p>
        )}
        {formError && (
          <p className="form-error" role="alert">
            {formError}
          </p>
        )}
        <form onSubmit={(e) => void onSubmit(e)} noValidate aria-label="Sign in">
          <div className="field">
            <label htmlFor={`${id}-email`}>Email</label>
            <input
              id={`${id}-email`}
              type="email"
              name="email"
              autoComplete="username"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              aria-invalid={fieldErrors.email ? true : undefined}
              aria-describedby={fieldErrors.email ? `${id}-email-error` : undefined}
              autoFocus
            />
            {fieldErrors.email && (
              <p className="field-error" id={`${id}-email-error`}>
                {fieldErrors.email}
              </p>
            )}
          </div>
          <div className="field">
            <label htmlFor={`${id}-password`}>Password</label>
            <input
              id={`${id}-password`}
              type="password"
              name="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              aria-invalid={fieldErrors.password ? true : undefined}
              aria-describedby={fieldErrors.password ? `${id}-password-error` : undefined}
            />
            {fieldErrors.password && (
              <p className="field-error" id={`${id}-password-error`}>
                {fieldErrors.password}
              </p>
            )}
          </div>
          <button type="submit" className="button button-primary" disabled={busy}>
            {busy ? 'Signing in…' : 'Sign in'}
          </button>
        </form>
      </div>
    </main>
  )
}
