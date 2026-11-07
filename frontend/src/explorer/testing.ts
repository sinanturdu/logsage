import { vi } from 'vitest'
import { alice, json, project } from '../test/util'

// Shared fetch mock for the explorer tests: routed by path, because the
// explorer endpoints carry query strings.

export type Req = { path: string; params: URLSearchParams; signal: AbortSignal | undefined }

export function event(i: number, extra: Partial<Record<string, string>> = {}) {
  return {
    event_id: `${1000 + i}-0`,
    timestamp: new Date(Date.UTC(2026, 10, 7, 2, 16, 58, 412) - i * 1000).toISOString(),
    level: 'error',
    service: 'payments',
    message: `payment gateway timeout after 5000 ms (psp-eu-2) #${i}`,
    trace_id: '',
    attributes: {},
    ...extra,
  }
}
export const facets = (total: number) => ({
  total,
  levels: { debug: 1, info: 2, warn: 3, error: total, fatal: 0 },
  services: [
    { name: 'payments', count: 40 },
    { name: 'checkout', count: 20 },
    { name: 'other', count: 5, other: true },
  ],
  took_ms: 3,
})
export const histogram = {
  bucket_seconds: 10,
  buckets: [
    { t: '2026-11-07T02:16:30Z', debug: 0, info: 10, warn: 0, error: 0, fatal: 0 },
    { t: '2026-11-07T02:16:40Z', debug: 0, info: 10, warn: 2, error: 30, fatal: 1 },
  ],
  took_ms: 2,
}

export type Handler = (r: Req) => Response | Promise<Response>

/** fetch mock routed by path; the explorer endpoints carry query strings. */
export function mockExplorer(overrides: Partial<Record<'logs' | 'facets' | 'histogram' | 'projects', Handler>> = {}) {
  const requests: Req[] = []
  const routes: Record<string, Handler> = {
    '/api/auth/me': () => json(200, alice),
    '/api/projects': () => json(200, { projects: [project('shop', 'Shop')] }),
    '/api/projects/shop/histogram': () => json(200, histogram),
    '/api/projects/shop/facets': () => json(200, facets(3)),
    '/api/projects/shop/logs': () => json(200, { events: [event(0), event(1), event(2)], next_cursor: null, took_ms: 4 }),
  }
  for (const [k, h] of Object.entries(overrides)) routes[k === 'projects' ? '/api/projects' : `/api/projects/shop/${k}`] = h
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      const u = new URL(url, 'http://localhost')
      const r = { path: u.pathname, params: u.searchParams, signal: init?.signal ?? undefined }
      requests.push(r)
      const h = routes[u.pathname]
      return h ? h(r) : json(404, { error: 'not found' })
    }),
  )
  return { requests, routes }
}

