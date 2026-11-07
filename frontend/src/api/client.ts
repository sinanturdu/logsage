// Typed client for the management API (cmd/api, ADR 0013/0014). The session
// is an HttpOnly cookie on the same origin; nothing here stores tokens or
// user data, and passwords only pass through to fetch.

export type User = { id: string; email: string }
export type Project = {
  id: string
  slug: string
  name: string
  role: 'owner' | 'admin' | 'viewer'
  created_at: string
  disabled: boolean
}

export type ApiErrorKind =
  | 'unauthorized'
  | 'forbidden'
  | 'not_found'
  | 'conflict'
  | 'invalid'
  | 'rate_limited'
  | 'server'
  | 'network'

export class ApiError extends Error {
  readonly kind: ApiErrorKind
  readonly status: number
  /** Seconds from Retry-After, for 429. */
  readonly retryAfter: number | undefined
  /** The server's error text for 400 (fixed, safe strings from the API). */
  readonly serverMessage: string | undefined

  constructor(kind: ApiErrorKind, status: number, opts: { retryAfter?: number; serverMessage?: string } = {}) {
    super(`${kind} (${status})`)
    this.name = 'ApiError'
    this.kind = kind
    this.status = status
    this.retryAfter = opts.retryAfter
    this.serverMessage = opts.serverMessage
  }
}

// Called when a request made with a session gets 401: the session ended.
type Listener = () => void
const sessionEndedListeners = new Set<Listener>()
export function onSessionEnded(fn: Listener): () => void {
  sessionEndedListeners.add(fn)
  return () => sessionEndedListeners.delete(fn)
}

function retryAfterSeconds(h: string | null): number | undefined {
  if (!h) return undefined
  const n = Number.parseInt(h, 10)
  return Number.isFinite(n) && n >= 0 ? n : undefined
}

function kindFor(status: number): ApiErrorKind {
  switch (status) {
    case 400:
    case 413:
      return 'invalid'
    case 401:
      return 'unauthorized'
    case 403:
      return 'forbidden'
    case 404:
      return 'not_found'
    case 409:
      return 'conflict'
    case 429:
      return 'rate_limited'
    default:
      return 'server'
  }
}

type RequestOptions = {
  body?: unknown
  signal?: AbortSignal | undefined
  /** The login request: a 401 means wrong credentials, not an ended session. */
  anonymous?: boolean
}

export async function request<T>(method: 'GET' | 'POST', path: string, opts: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  const init: RequestInit = { method, credentials: 'same-origin', headers }
  if (opts.signal) init.signal = opts.signal
  if (opts.body !== undefined) {
    headers['Content-Type'] = 'application/json'
    init.body = JSON.stringify(opts.body)
  }
  let res: Response
  try {
    res = await fetch(path, init)
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') throw err
    throw new ApiError('network', 0)
  }
  if (res.status === 204) return undefined as T

  let data: unknown
  try {
    data = await res.json()
  } catch {
    data = undefined
  }
  if (res.ok) return data as T

  const kind = kindFor(res.status)
  const serverMessage =
    kind === 'invalid' && data && typeof data === 'object' && 'error' in data && typeof data.error === 'string'
      ? data.error
      : undefined
  const err = new ApiError(kind, res.status, {
    ...(serverMessage !== undefined && { serverMessage }),
    ...(kind === 'rate_limited' && { retryAfter: retryAfterSeconds(res.headers.get('Retry-After')) ?? 1 }),
  })
  if (kind === 'unauthorized' && !opts.anonymous) sessionEndedListeners.forEach((fn) => fn())
  throw err
}

export const api = {
  login: (email: string, password: string) =>
    request<{ user: User }>('POST', '/api/auth/login', { body: { email, password }, anonymous: true }),
  logout: () => request<undefined>('POST', '/api/auth/logout', { body: {} }),
  me: (signal?: AbortSignal) => request<{ user: User }>('GET', '/api/auth/me', { signal }),
  listProjects: (signal?: AbortSignal) => request<{ projects: Project[] }>('GET', '/api/projects', { signal }),
  createProject: (name: string, slug: string) =>
    request<{ project: Project }>('POST', '/api/projects', { body: { name, slug } }),
}

/** The server's slug rule (postgres.ValidateSlug). */
export const SLUG_PATTERN = /^[a-z0-9-]{3,40}$/

function sentence(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1) + (/[.!?]$/.test(s) ? '' : '.')
}

/** A plain-language message for an error: what happened and how to fix it. */
export function errorMessage(err: unknown): string {
  if (!(err instanceof ApiError)) return 'Something unexpected happened. Reload the page and try again.'
  switch (err.kind) {
    case 'network':
      return 'Cannot reach the LogSage API. Check your connection and that the API is running, then try again.'
    case 'unauthorized':
      return 'Your session ended. Sign in again to continue.'
    case 'forbidden':
      return 'You do not have permission to do this. Ask a project owner for access.'
    case 'not_found':
      return 'This was not found. It may have been removed, or you are not a member of the project.'
    case 'conflict':
      return 'This conflicts with something that already exists. Change it and try again.'
    case 'invalid':
      return err.serverMessage ? sentence(err.serverMessage) : 'The request was not valid. Check the fields and try again.'
    case 'rate_limited':
      return `Too many requests. Try again in ${err.retryAfter ?? 1} seconds.`
    case 'server':
      return `The LogSage API failed (status ${err.status}). Try again in a minute.`
  }
}
