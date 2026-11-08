import { vi } from 'vitest'
import { facets, histogram } from '../explorer/testing'
import { alice, json, project } from '../test/util'
import type { Group } from './api'

// Shared fixtures for the groups tests.

export const group = (fp: string, extra: Partial<Group> = {}): Group => ({
  fingerprint: fp,
  title: 'payment gateway timeout after <n> ms',
  sample: 'payment gateway timeout after 5000 ms',
  error_type: '',
  levels: ['error'],
  services: ['payments'],
  count: 10,
  first_seen: '2026-11-07T20:00:00.000Z',
  last_seen: '2026-11-07T20:10:00.000Z',
  sparkline: [1, 2, 3],
  is_new: false,
  status: 'open',
  regressed: false,
  resolved_at: null,
  ...extra,
})


export type GReq = { path: string; params: URLSearchParams; method: string; body: unknown }
export type GHandler = (r: GReq) => Response | Promise<Response>

/** The fixed "now" of the groups tests: 2026-11-07 20:15 UTC. */
export const NOW = Date.UTC(2026, 10, 7, 20, 15)

/**
 * fetch mock for the groups pages, routed by "METHOD path" (paths carry
 * query strings). Unrouted requests answer 404.
 */
export function mockGroups(routes: Record<string, GHandler>, role: 'owner' | 'admin' | 'viewer' = 'owner') {
  const requests: GReq[] = []
  const all: Record<string, GHandler> = {
    'GET /api/auth/me': () => json(200, alice),
    'GET /api/projects': () => json(200, { projects: [{ ...project('shop', 'Shop'), role }] }),
    'GET /api/projects/shop/facets': () => json(200, facets(3)),
    'GET /api/projects/shop/histogram': () => json(200, histogram),
    ...routes,
  }
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      const u = new URL(url, 'http://localhost')
      const method = init?.method ?? 'GET'
      const r = { path: u.pathname, params: u.searchParams, method, body: init?.body ? JSON.parse(String(init.body)) : undefined }
      requests.push(r)
      const h = all[`${method} ${u.pathname}`]
      return h ? h(r) : json(404, { error: 'not found' })
    }),
  )
  return { requests, routes: all }
}

export const page = (groups: Group[], next: string | null = null) => json(200, { groups, next_cursor: next, sparkline_bucket_seconds: 60, took_ms: 3 })
