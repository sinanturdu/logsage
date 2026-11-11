import { vi } from 'vitest'
import { csrfGate } from '../test/csrf'
import { facets } from '../explorer/testing'
import { alice, json, project } from '../test/util'
import type { Detail, Incident, LinkedAlert, LinkedGroup, Member, Severity, Suggestion, TimelineEvent } from './api'

// A small in-memory incidents API for the tests. It applies the server's
// rules (ADR 0026, internal/api/incidents.go): the CSRF rule, the role
// matrix (viewers read), the validation limits and messages, version
// checks with the same 409 messages, the status workflow, assignees from
// the members, the event cap. A test that breaks one of these fails here
// as it would against cmd/api.

export const XSS = '<img src=x onerror="window.__xss=1"><script>window.__xss=1</script>'
export const SLUG = 'shop'
export const B = `/api/projects/${SLUG}/incidents`
export const ME = 'u1' // the signed-in user of test/util (alice)
export const MEMBERS: Member[] = [
  { user_id: ME, email: 'alice@example.com', role: 'owner' },
  { user_id: 'u-bob', email: 'bob@example.com', role: 'admin' },
]

export const incident = (n: number, extra: Partial<Incident> = {}): Incident => ({
  id: `00000000-0000-0000-0000-${String(n).padStart(12, '0')}`,
  number: n,
  title: `Incident ${n}`,
  severity: 'high',
  status: 'open',
  source: 'auto',
  assignee: null,
  summary: '',
  services: ['payments'],
  opened_at: '2026-11-10T12:00:00Z',
  acknowledged_at: null,
  acknowledged_by: null,
  resolved_at: null,
  resolved_by: null,
  last_activity_at: '2026-11-10T12:30:00Z',
  recurrence_of: null,
  all_alerts_resolved: false,
  events_dropped: 0,
  version: 1,
  alert_count: 0,
  group_count: 0,
  ...extra,
})

export const ev = (id: number, type: string, payload: Record<string, unknown> = {}, extra: Partial<TimelineEvent> = {}): TimelineEvent => ({
  id,
  type,
  actor: null,
  at: '2026-11-10T12:00:00Z',
  last_at: '2026-11-10T12:00:00Z',
  count: 1,
  payload,
  body: null,
  ...extra,
})

export const linkedAlert = (extra: Partial<LinkedAlert> = {}): LinkedAlert => ({
  rule_id: 'r1',
  rule_name: 'Payment errors',
  state_key: 'rule',
  services: ['payments'],
  linked_at: '2026-11-10T12:00:00Z',
  closed_at: null,
  close_reason: '',
  state: 'firing',
  ...extra,
})

export const linkedGroup = (fp: string, extra: Partial<LinkedGroup> = {}): LinkedGroup => ({
  fingerprint: fp,
  template: 'gateway timeout after <n> ms',
  source: 'auto',
  linked_at: '2026-11-10T12:00:00Z',
  linked_by: null,
  ...extra,
})

export const suggestion = (fp: string, extra: Partial<Suggestion> = {}): Suggestion => ({
  fingerprint: fp,
  title: 'card declined <id>',
  services: ['payments'],
  levels: ['error'],
  count: 120,
  is_new: false,
  regressed: false,
  first_seen: '2026-11-10T11:00:00Z',
  last_seen: '2026-11-10T12:20:00Z',
  ...extra,
})

export type Req = { path: string; params: URLSearchParams; method: string; body: Record<string, unknown> | undefined }
export type Handler = (r: Req) => Response | Promise<Response>

type State = {
  incidents: Incident[]
  details: Map<number, Omit<Detail, 'incident' | 'events' | 'next_before'>>
  events: Map<number, TimelineEvent[]> // newest first
  eventCap: Set<number>
  nextEvent: number
}

const err = (status: number, error: string) => json(status, { error })
const runes = (s: string) => Array.from(s).length

function text(name: string, v: unknown, lo: number, hi: number): string | Response {
  if (typeof v !== 'string') return lo > 0 ? err(400, `${name}: ${lo} to ${hi} characters`) : ''
  const t = v.trim()
  if (runes(t) < lo || runes(t) > hi) return err(400, lo > 0 ? `${name}: ${lo} to ${hi} characters` : `${name}: at most ${hi} characters`)
  return t
}
const SEVS = ['critical', 'high', 'medium', 'low']

/**
 * The incidents API, plus the project, members and the endpoints the pages
 * touch. role: the signed-in user's role in "shop".
 */
export function mockIncidents(
  opts: { incidents?: Incident[]; detail?: Partial<Omit<Detail, 'incident'>> & { number?: number }; role?: 'owner' | 'admin' | 'viewer'; routes?: Record<string, Handler> } = {},
) {
  const role = opts.role ?? 'owner'
  const st: State = { incidents: [...(opts.incidents ?? [])], details: new Map(), events: new Map(), eventCap: new Set(), nextEvent: 1000 }
  for (const i of st.incidents) {
    st.details.set(i.number, { alerts: [], groups: [], suggested_groups: [] })
    st.events.set(i.number, [])
  }
  if (opts.detail) {
    const n = opts.detail.number ?? st.incidents[0]?.number ?? 1
    const d = st.details.get(n) ?? { alerts: [], groups: [], suggested_groups: [] }
    st.details.set(n, {
      alerts: opts.detail.alerts ?? d.alerts,
      groups: opts.detail.groups ?? d.groups,
      suggested_groups: opts.detail.suggested_groups === undefined ? d.suggested_groups : opts.detail.suggested_groups,
      ...(opts.detail.suggested_groups_error !== undefined && { suggested_groups_error: opts.detail.suggested_groups_error }),
    })
    st.events.set(n, opts.detail.events ?? [])
  }
  const requests: Req[] = []
  const find = (n: number) => st.incidents.find((i) => i.number === n)
  const writable = role !== 'viewer'
  const write = (fn: () => Response): Response => (writable ? fn() : err(403, 'forbidden'))
  const addEvent = (n: number, type: string, payload: Record<string, unknown>, body: string | null = null): TimelineEvent | null => {
    if (st.eventCap.has(n)) return null
    const e = ev(++st.nextEvent, type, payload, { actor: ME, actor_email: 'alice@example.com', body, at: new Date().toISOString(), last_at: new Date().toISOString() })
    st.events.set(n, [e, ...(st.events.get(n) ?? [])])
    return e
  }
  const versioned = (r: Req, n: number, fn: (i: Incident) => Response | null): Response => {
    const i = find(n)
    if (!i) return err(404, 'not found')
    const v = r.body?.version
    if (typeof v !== 'number' || v < 1) return err(400, 'version: required (from the incident you change)')
    if (v !== i.version) return err(409, 'version: the incident changed; reload it and try again')
    const out = fn(i)
    if (out) return out
    i.version++
    return json(200, { incident: i })
  }

  const routes: Record<string, Handler> = {
    'GET /api/auth/me': () => json(200, alice),
    'GET /api/projects': () => json(200, { projects: [{ ...project(SLUG, 'Shop'), role }] }),
    'GET /api/projects/shop/facets': () => json(200, facets(3)),
    'GET /api/projects/shop/alerts/rules': () => json(200, { rules: [] }),
    [`GET /api/projects/${SLUG}/members`]: () => json(200, { members: MEMBERS }),
    [`GET ${B}/summary`]: () =>
      json(200, {
        open: st.incidents.filter((i) => i.status === 'open').length,
        acknowledged: st.incidents.filter((i) => i.status === 'acknowledged').length,
        resolved: st.incidents.filter((i) => i.status === 'resolved').length,
      }),
    [`GET ${B}`]: (r) => {
      const status = (r.params.get('status') ?? '').split(',').filter(Boolean)
      if (status.some((s) => !['open', 'acknowledged', 'resolved'].includes(s))) return err(400, 'status: must be open, acknowledged or resolved')
      const limit = Number(r.params.get('limit') ?? 50)
      if (!(limit >= 1 && limit <= 100)) return err(400, 'limit: must be between 1 and 100')
      let rows = st.incidents.filter((i) => status.length === 0 || status.includes(i.status))
      const sev = (r.params.get('severity') ?? '').split(',').filter(Boolean)
      if (sev.length) rows = rows.filter((i) => sev.includes(i.severity))
      const q = r.params.get('q')
      if (q) rows = rows.filter((i) => i.title.toLowerCase().includes(q.toLowerCase()))
      const cursor = Number(r.params.get('cursor') || 0)
      const page = rows.slice(cursor, cursor + limit)
      return json(200, { incidents: page, next_cursor: page.length === limit ? String(cursor + limit) : null })
    },
    [`POST ${B}`]: (r) =>
      write(() => {
        const title = text('title', r.body?.title, 1, 200)
        if (title instanceof Response) return title
        const summary = text('summary', r.body?.summary ?? '', 0, 2000)
        if (summary instanceof Response) return summary
        const severity = (r.body?.severity as string) || 'high'
        if (!SEVS.includes(severity)) return err(400, 'severity: must be critical, high, medium or low')
        const n = st.incidents.reduce((m, i) => Math.max(m, i.number), 0) + 1
        const i = incident(n, { title, summary, severity: severity as Severity, source: 'manual', services: [] })
        st.incidents.unshift(i)
        st.details.set(n, { alerts: [], groups: [], suggested_groups: [] })
        st.events.set(n, [])
        addEvent(n, 'opened', { source: 'manual' })
        return json(201, { incident: i })
      }),
    ...opts.routes,
  }
  const dynamic = (r: Req): Response | null => {
    const m = /^\/api\/projects\/shop\/incidents\/([^/]+)(?:\/(.*))?$/.exec(r.path)
    if (!m || m[1] === 'summary') return null
    if (!/^[1-9][0-9]*$/.test(m[1] ?? '')) return err(404, 'not found')
    const n = Number(m[1])
    const sub = m[2] ?? ''
    const i = find(n)
    if (r.method === 'GET' && sub === '') {
      if (!i) return err(404, 'not found')
      const evs = st.events.get(n) ?? []
      return json(200, { incident: i, ...st.details.get(n), events: evs.slice(0, 50), next_before: evs.length > 50 ? (evs[49]?.id ?? null) : null })
    }
    if (r.method === 'GET' && sub === 'events') {
      if (!i) return err(404, 'not found')
      const before = Number(r.params.get('before') ?? 0)
      const evs = (st.events.get(n) ?? []).filter((e) => !before || e.id < before)
      return json(200, { events: evs.slice(0, 50), next_before: evs.length > 50 ? (evs[49]?.id ?? null) : null })
    }
    if (!writable) return err(403, 'forbidden')
    if (!i) return err(404, 'not found')
    if (r.method === 'PATCH' && sub === '') {
      return versioned(r, n, (x) => {
        const b = r.body ?? {}
        if (b.title !== undefined) {
          const t = text('title', b.title, 1, 200)
          if (t instanceof Response) return t
          x.title = t
        }
        if (b.summary !== undefined) {
          const s = text('summary', b.summary, 0, 2000)
          if (s instanceof Response) return s
          x.summary = s
        }
        if (b.severity !== undefined) {
          if (!SEVS.includes(String(b.severity))) return err(400, 'severity: must be critical, high, medium or low')
          if (x.severity !== b.severity) addEvent(n, 'severity_changed', { from: x.severity, to: b.severity })
          x.severity = b.severity as Severity
        }
        return null
      })
    }
    const transitions: Record<string, [string[], Incident['status']]> = {
      acknowledge: [['open'], 'acknowledged'],
      resolve: [['open', 'acknowledged'], 'resolved'],
      reopen: [['resolved'], 'open'],
    }
    if (r.method === 'POST' && sub in transitions) {
      return versioned(r, n, (x) => {
        const note = text('note', r.body?.note ?? '', 0, 2000)
        if (note instanceof Response) return note
        const [from, to] = transitions[sub] as [string[], Incident['status']]
        if (!from.includes(x.status)) return err(409, `status: ${sub} from ${x.status} is not allowed`)
        addEvent(n, sub === 'reopen' ? 'reopened' : 'status_changed', sub === 'reopen' ? {} : { from: x.status, to })
        x.status = to
        if (note) addEvent(n, 'comment', {}, note)
        return null
      })
    }
    if (r.method === 'POST' && (sub === 'assign' || sub === 'unassign')) {
      const uid = sub === 'assign' ? String(r.body?.user_id ?? '') : ''
      if (sub === 'assign' && !MEMBERS.some((mm) => mm.user_id === uid)) return err(400, 'assignee: not a member of this project')
      i.assignee = uid || null
      i.assignee_email = MEMBERS.find((mm) => mm.user_id === uid)?.email ?? ''
      i.version++
      addEvent(n, 'assigned', uid ? { user_id: uid } : {})
      return json(200, { incident: i })
    }
    if (r.method === 'POST' && sub === 'comments') {
      const body = text('body', r.body?.body, 1, 2000)
      if (body instanceof Response) return body
      const e = addEvent(n, 'comment', {}, body)
      if (!e) return err(409, 'events: this incident has reached 1000 timeline events')
      return json(201, { event: e })
    }
    const cm = /^comments\/([0-9]+)$/.exec(sub)
    if (r.method === 'DELETE' && cm) {
      const id = Number(cm[1])
      const evs = st.events.get(n) ?? []
      const e = evs.find((x) => x.id === id && x.type === 'comment' && x.body !== null)
      if (!e) return err(404, 'not found')
      e.body = null
      addEvent(n, 'comment_deleted', { comment_id: id })
      return json(204)
    }
    const gm = /^groups\/([0-9a-f]{16})$/.exec(sub)
    if (gm) {
      const fp = gm[1] as string
      const d = st.details.get(n)
      if (!d) return err(404, 'not found')
      if (r.method === 'POST') {
        if (d.groups.some((g) => g.fingerprint === fp)) return err(409, 'conflict')
        d.groups = [...d.groups, linkedGroup(fp, { source: 'manual', template: '', linked_by: ME })]
        if (d.suggested_groups) d.suggested_groups = d.suggested_groups.filter((s) => s.fingerprint !== fp)
        addEvent(n, 'group_linked', { fingerprint: fp, source: 'manual' })
      } else {
        if (!d.groups.some((g) => g.fingerprint === fp)) return err(404, 'not found')
        d.groups = d.groups.filter((g) => g.fingerprint !== fp)
        addEvent(n, 'group_unlinked', { fingerprint: fp, source: 'manual' })
      }
      i.group_count = d.groups.length
      return json(200, { incident: i })
    }
    return err(404, 'not found')
  }

  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      const u = new URL(url, 'http://localhost')
      const method = init?.method ?? 'GET'
      const r: Req = { path: u.pathname, params: u.searchParams, method, body: init?.body ? JSON.parse(String(init.body)) : undefined }
      requests.push(r)
      const blocked = csrfGate(url, init)
      if (blocked) return blocked
      const h = routes[`${method} ${u.pathname}`]
      if (h) return h(r)
      return dynamic(r) ?? err(404, 'not found')
    }),
  )
  return { requests, state: st, routes }
}
