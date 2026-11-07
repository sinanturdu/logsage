import { useQuery } from '@tanstack/react-query'
import { request } from '../api/client'

// Project API keys (ADR 0013). The list never contains a secret. The
// plaintext of a new key is returned by createKey only: callers keep it in
// component state and never pass it to React Query, the URL or storage.

export type KeyKind = 'ingest' | 'query'
export type KeyStatus = 'active' | 'revoked' | 'expired'
export type KeyInfo = {
  prefix: string
  kind: KeyKind
  name: string
  created_at: string
  expires_at: string | null
  revoked_at: string | null
  last_used_at: string | null
  status: KeyStatus
}

/** The server's name rule (postgres.ValidateName). */
export const MAX_KEY_NAME = 200

/** Expiry choices, as the server's expires_in (a Go duration). */
export const EXPIRY = [
  { id: 'never', label: 'Never', expiresIn: '' },
  { id: '30d', label: '30 days', expiresIn: '720h' },
  { id: '90d', label: '90 days', expiresIn: '2160h' },
  { id: '1y', label: '1 year', expiresIn: '8760h' },
] as const

const base = (slug: string) => `/api/projects/${encodeURIComponent(slug)}/keys`
export const keysKey = (slug: string) => ['keys', slug] as const

const STATUS_ORDER: Record<KeyStatus, number> = { active: 0, expired: 1, revoked: 2 }

/** Active keys first, then newest first. */
export function sortKeys(keys: readonly KeyInfo[]): KeyInfo[] {
  return [...keys].sort((a, b) => STATUS_ORDER[a.status] - STATUS_ORDER[b.status] || b.created_at.localeCompare(a.created_at))
}

export function useKeys(slug: string) {
  return useQuery({
    queryKey: keysKey(slug),
    queryFn: ({ signal }) => request<{ keys: KeyInfo[] }>('GET', base(slug), { signal }),
    select: (d) => sortKeys(d.keys),
  })
}

/** Creates a key; the result holds the plaintext, shown once. */
export function createKey(slug: string, kind: KeyKind, name: string, expiresIn: string) {
  return request<{ key: string; info: KeyInfo }>('POST', base(slug), { body: { kind, name, ...(expiresIn && { expires_in: expiresIn }) } })
}

export function revokeKey(slug: string, prefix: string) {
  return request<{ info: KeyInfo }>('POST', `${base(slug)}/${encodeURIComponent(prefix)}/revoke`, { body: {} })
}
