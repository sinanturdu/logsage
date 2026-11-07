import { useEffect, useRef } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Navigate, useLocation, useNavigate } from 'react-router'
import { ApiError, api, onSessionEnded } from '../api/client'

export const meKey = ['me'] as const

export function useMe() {
  return useQuery({ queryKey: meKey, queryFn: ({ signal }) => api.me(signal), staleTime: 60_000 })
}

/** Only same-app paths are accepted as a return target (no open redirect). */
export function safeNext(raw: string | null): string {
  // "//host" and "/\host" are protocol-relative to browsers: never follow them.
  if (!raw || !raw.startsWith('/') || raw.startsWith('//') || raw.startsWith('/\\') || raw.startsWith('/login')) {
    return '/'
  }
  return raw
}

export function loginPath(next: string, expired: boolean): string {
  const p = new URLSearchParams({ next })
  if (expired) p.set('expired', '1')
  return `/login?${p.toString()}`
}

/**
 * When a request made with a session gets 401 after the user was signed in,
 * the session ended (expiry, logout elsewhere, revoked): drop all cached data
 * and go to sign-in, returning to the same URL afterwards.
 */
export function SessionWatcher() {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const location = useLocation()
  // Several requests can fail with 401 at once; only the first one may
  // redirect (a later one would see the cleared cache and drop the
  // "session ended" notice). The refs avoid acting on a stale location.
  const current = useRef(location)
  const redirected = useRef(false)
  useEffect(() => {
    current.current = location
    if (location.pathname !== '/login') redirected.current = false
  }, [location])
  useEffect(
    () =>
      onSessionEnded(() => {
        const at = current.current
        if (at.pathname === '/login' || redirected.current) return
        redirected.current = true
        const wasSignedIn = qc.getQueryData(meKey) !== undefined
        qc.clear()
        navigate(loginPath(at.pathname + at.search, wasSignedIn), { replace: true })
      }),
    [qc, navigate],
  )
  return null
}

/** Renders children for a signed-in user; otherwise sends them to sign-in. */
export function RequireAuth({ children }: { children: React.ReactNode }) {
  const me = useMe()
  const location = useLocation()
  if (me.isPending) return <p className="shell-main" role="status">Loading…</p>
  if (me.error instanceof ApiError && me.error.kind === 'unauthorized') {
    return <Navigate to={loginPath(location.pathname + location.search, false)} replace />
  }
  if (me.isError) {
    return (
      <div className="shell-main panel" role="alert">
        <h1>LogSage is not reachable</h1>
        <p>Cannot load your account. Check that the API is running, then reload the page.</p>
        <button type="button" className="button" onClick={() => void me.refetch()}>
          Try again
        </button>
      </div>
    )
  }
  return <>{children}</>
}
