import { createContext, useContext } from 'react'

// The public runtime config that cmd/api serves as /config.json (ADR 0021),
// read once before the app renders.

export type PublicConfig = { ingestUrl: string }

export const DEFAULT_INGEST_URL = 'http://localhost:8080'
export const DEFAULT_CONFIG: PublicConfig = { ingestUrl: DEFAULT_INGEST_URL }
export const CONFIG_TIMEOUT_MS = 3000

/** An http(s) URL without credentials, query or fragment, without a trailing slash; null otherwise. */
export function normalizeIngestUrl(v: unknown): string | null {
  if (typeof v !== 'string' || v.trim() === '') return null
  let u: URL
  try {
    u = new URL(v.trim())
  } catch {
    return null
  }
  if ((u.protocol !== 'http:' && u.protocol !== 'https:') || !u.hostname || u.username || u.password || u.search || u.hash || v.includes('?') || v.includes('#')) {
    return null
  }
  return `${u.protocol}//${u.host}${u.pathname}`.replace(/\/+$/, '')
}

/** Validates the shape: exactly an object with a usable ingestUrl. */
export function parseConfig(data: unknown): PublicConfig | null {
  if (!data || typeof data !== 'object' || Array.isArray(data)) return null
  const ingestUrl = normalizeIngestUrl((data as Record<string, unknown>).ingestUrl)
  return ingestUrl ? { ingestUrl } : null
}

type LoadOptions = { timeoutMs?: number; dev?: boolean; warn?: (msg: string) => void }

/**
 * Fetches /config.json with a short timeout. Only a JSON response with the
 * right shape counts: an HTML page (a fallback route, a proxy error page) is
 * never parsed as config. Anything else gives the defaults, with a console
 * warning in development only.
 */
export async function loadConfig({ timeoutMs = CONFIG_TIMEOUT_MS, dev = false, warn = console.warn }: LoadOptions = {}): Promise<PublicConfig> {
  const ac = new AbortController()
  const timer = setTimeout(() => ac.abort(), timeoutMs)
  let problem: string
  try {
    const res = await fetch('/config.json', { credentials: 'same-origin', headers: { Accept: 'application/json' }, signal: ac.signal, cache: 'no-store' })
    const type = res.headers.get('Content-Type') ?? ''
    if (!res.ok) problem = `status ${res.status}`
    else if (!type.toLowerCase().startsWith('application/json')) problem = `not JSON (${type || 'no content type'})`
    else {
      const parsed = parseConfig(await res.json())
      if (parsed) return parsed
      problem = 'unexpected shape'
    }
  } catch (err) {
    problem = ac.signal.aborted ? `no answer within ${timeoutMs} ms` : err instanceof Error ? err.message : 'request failed'
  } finally {
    clearTimeout(timer)
  }
  if (dev) warn(`LogSage: /config.json could not be used (${problem}); using ${DEFAULT_INGEST_URL}.`)
  return DEFAULT_CONFIG
}

export const ConfigContext = createContext<PublicConfig>(DEFAULT_CONFIG)
export const useConfig = () => useContext(ConfigContext)
