import { vi } from 'vitest'
import { csrfGate } from '../test/csrf'
import { facets } from '../explorer/testing'
import { alice, json, project } from '../test/util'
import type { AlertEvent, Channel, Delivery, Rule, StateRecord } from './api'

// Shared fixtures for the alerts tests.

export const XSS = '<img src=x onerror="window.__xss=1"><script>window.__xss=1</script>'

export const rule = (id: string, extra: Partial<Rule> = {}): Rule => ({
  id,
  name: `rule ${id}`,
  note: '',
  type: 'error_rate',
  params: { threshold_percent: 20, min_logs: 50 },
  interval: '1m0s',
  window: '5m0s',
  for: '2m0s',
  clear_for: '1m0s',
  repeat: '0s',
  enabled: true,
  channels: ['c1'],
  version: 1,
  next_eval_at: '2026-11-09T12:00:00Z',
  state: 'ok',
  firing: 0,
  pending: 0,
  last_change_at: null,
  ...extra,
})

export const channel = (id: string, extra: Partial<Channel> = {}): Channel => ({
  id,
  name: `ops ${id}`,
  kind: 'webhook',
  url_host: 'https://hooks.example.com',
  include_sample: false,
  created_at: '2026-11-09T10:00:00Z',
  updated_at: '2026-11-09T10:00:00Z',
  ...extra,
})

export const event = (id: number, extra: Partial<AlertEvent> = {}): AlertEvent => ({
  id,
  rule_id: 'r1',
  rule_name: 'rule r1',
  rule_type: 'error_rate',
  dedup_key: 'rule',
  from: 'pending',
  to: 'firing',
  value: 56.52,
  threshold: 20,
  window_start: '2026-11-09T11:55:00Z',
  window_end: '2026-11-09T12:00:00Z',
  fingerprint: '',
  template: '',
  reason: '',
  at: '2026-11-09T12:00:00Z',
  ...extra,
})

export const delivery = (id: number, extra: Partial<Delivery> = {}): Delivery => ({
  id,
  channel_id: 'c1',
  channel_name: 'ops c1',
  rule_id: 'r1',
  rule_name: 'rule r1',
  event_id: 1,
  event: 'alert.firing',
  status: 'sent',
  attempts: 1,
  response_status: 204,
  last_error: '',
  test: false,
  idempotency_key: `k${id}`,
  created_at: '2026-11-09T12:00:01Z',
  next_attempt_at: '2026-11-09T12:00:01Z',
  sent_at: '2026-11-09T12:00:02Z',
  ...extra,
})

export const firingState = (ruleId: string, extra: Partial<StateRecord> = {}): StateRecord => ({
  rule_id: ruleId,
  rule_name: `rule ${ruleId}`,
  dedup_key: 'rule',
  state: 'firing',
  since: '2026-11-09T12:00:00Z',
  value: 56.52,
  fired_at: '2026-11-09T12:00:00Z',
  fingerprint: '',
  template: '',
  ...extra,
})

export type AReq = { path: string; params: URLSearchParams; method: string; body: unknown }
export type AHandler = (r: AReq) => Response | Promise<Response>

export const OFF = () => json(503, { error: 'alerts are disabled: set ALERTS_ENABLED=true and ALERTS_SECRET_KEY' })

/**
 * fetch mock for the alerts pages, routed by "METHOD path". Defaults: one
 * channel, no rules, no states, events or deliveries. Unrouted: 404.
 */
export function mockAlerts(routes: Record<string, AHandler>, role: 'owner' | 'admin' | 'viewer' = 'owner') {
  const requests: AReq[] = []
  const b = '/api/projects/shop/alerts'
  const all: Record<string, AHandler> = {
    'GET /api/auth/me': () => json(200, alice),
    'GET /api/projects': () => json(200, { projects: [{ ...project('shop', 'Shop'), role }] }),
    'GET /api/projects/shop/facets': () => json(200, facets(3)),
    [`GET ${b}/rules`]: () => json(200, { rules: [] }),
    [`GET ${b}/channels`]: () => json(200, { channels: [channel('c1')] }),
    [`GET ${b}/states`]: () => json(200, { states: [] }),
    [`GET ${b}/events`]: () => json(200, { events: [], next_before: null }),
    [`GET ${b}/notifications`]: () => json(200, { notifications: [], next_before: null }),
    ...routes,
  }
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      const u = new URL(url, 'http://localhost')
      const method = init?.method ?? 'GET'
      const r = { path: u.pathname, params: u.searchParams, method, body: init?.body ? JSON.parse(String(init.body)) : undefined }
      requests.push(r)
      const blocked = csrfGate(url, init)
      if (blocked) return blocked
      const h = all[`${method} ${u.pathname}`]
      return h ? h(r) : json(404, { error: 'not found' })
    }),
  )
  return { requests, routes: all, base: b }
}
