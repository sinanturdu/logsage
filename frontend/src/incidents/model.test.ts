import { describe, expect, it } from 'vitest'
import { errorFromResponse } from '../api/client'
import { json } from '../test/util'
import { EVENT_CAP, STALE, TRANSITION, fieldOf, isEventCap, isStale, listParams, mergeIncidents, readProblem, writeError } from './api'
import { DEFAULT_FILTERS, STATUS_SHAPE, actorName, eventSentence, logsLink, parseFilters, parseNumber, serializeFilters, type Ctx } from './model'
import { ev, incident, linkedAlert, MEMBERS } from './testing'
import { STATE_SHAPE } from '../alerts/parts'

const ctx: Ctx = { alerts: [linkedAlert({ rule_id: 'r1', rule_name: 'Payment errors' })], members: MEMBERS }

describe('URL state', () => {
  it('parses defensively and leaves defaults out', () => {
    expect(parseFilters(new URLSearchParams(''))).toEqual(DEFAULT_FILTERS)
    const f = parseFilters(new URLSearchParams('status=resolved&severity=low,critical,bogus&assignee=me&service=payments&q=gateway&range=7d'))
    expect(f).toEqual({ status: 'resolved', severity: ['critical', 'low'], assignee: 'me', service: 'payments', q: 'gateway', range: '7d' })
    expect(serializeFilters(f).toString()).toBe('status=resolved&severity=critical%2Clow&assignee=me&service=payments&q=gateway&range=7d')
    expect(parseFilters(new URLSearchParams('status=closed&assignee=bob&range=1y&q=' + 'x'.repeat(201)))).toEqual(DEFAULT_FILTERS)
    expect(serializeFilters(DEFAULT_FILTERS).toString()).toBe('')
  })

  it('maps the filters to the API names', () => {
    const now = Date.UTC(2026, 10, 10, 12)
    expect(listParams(DEFAULT_FILTERS, now).toString()).toBe('status=open%2Cacknowledged&limit=50')
    expect(listParams({ ...DEFAULT_FILTERS, status: 'all', assignee: 'unassigned', range: '24h' }, now).toString()).toBe('assignee=none&from=2026-11-09T12%3A00%3A00Z&limit=50')
  })

  it('accepts only positive integer numbers in the route', () => {
    expect(parseNumber('12')).toBe(12)
    for (const v of ['0', '-1', '1.5', 'abc', '01', '', undefined, '1e3', '9999999999']) expect(parseNumber(v)).toBeNull()
  })
})

describe('marks', () => {
  it('status shapes differ from the alert states, the levels and the group statuses', () => {
    const taken = [...Object.values(STATE_SHAPE), '○', '●', '▲', '■', '◆', '□', '–', '✓']
    for (const s of Object.values(STATUS_SHAPE)) expect(taken).not.toContain(s)
    expect(new Set(Object.values(STATUS_SHAPE)).size).toBe(3)
  })
})

describe('timeline sentences', () => {
  const s = (type: string, payload: Record<string, unknown> = {}, extra: Parameters<typeof ev>[3] = {}) => eventSentence(ev(1, type, payload, extra), ctx)
  const alice = { actor: 'u-alice', actor_email: 'alice@example.com' }
  it('has one sentence per closed event type, from the payload fields only', () => {
    expect(s('opened', { source: 'auto', rule_name: 'Payment errors' })).toBe('Rule “Payment errors” opened this incident.')
    expect(s('opened', { source: 'manual' }, alice)).toBe('alice@example.com opened this incident.')
    expect(s('opened', { source: 'auto', rule_name: 'X', recurrence_of: 7 })).toBe('Rule “X” opened this incident. It is a recurrence of #7.')
    expect(s('alert_firing', { rule_name: 'Payment errors', template: 'gateway timeout <n>' })).toBe('Rule “Payment errors” fired for the error group “gateway timeout <n>”.')
    expect(s('alert_resolved', { rule_name: 'Payment errors' })).toBe('Rule “Payment errors” resolved.')
    expect(s('alert_closed', { rule_name: 'A', reason: 'rule_disabled' })).toBe('Rule “A” is no longer linked: the rule was disabled.')
    expect(s('alert_closed', { rule_name: 'A', reason: 'rule_deleted' })).toBe('Rule “A” is no longer linked: the rule was deleted.')
    expect(s('alert_closed', { rule_name: 'A', reason: 'rule_changed' })).toBe('Rule “A” is no longer linked: the rule was changed and its states were reset.')
    expect(s('all_alerts_resolved')).toBe('All linked alerts are resolved.')
    expect(s('group_linked', { fingerprint: '00000000000000ab', template: 'db down', source: 'auto' })).toBe('The error group “db down” was linked automatically.')
    expect(s('group_linked', { fingerprint: 'abcdef0123456789', source: 'manual' }, alice)).toBe('alice@example.com linked the error group “group abcdef01”.')
    expect(s('group_unlinked', { fingerprint: 'abcdef0123456789', source: 'manual' }, alice)).toBe('alice@example.com unlinked the error group “group abcdef01”.')
    expect(s('status_changed', { from: 'open', to: 'acknowledged' }, alice)).toBe('alice@example.com acknowledged the incident.')
    expect(s('status_changed', { from: 'acknowledged', to: 'resolved' }, alice)).toBe('alice@example.com resolved the incident.')
    expect(s('status_changed', { from: 'resolved', to: 'open' }, alice)).toBe('alice@example.com changed the status from resolved to open.')
    expect(s('severity_changed', { from: 'high', to: 'critical' }, alice)).toBe('alice@example.com changed the severity from high to critical.')
    expect(s('assigned', { user_id: 'u-bob' }, alice)).toBe('alice@example.com assigned the incident to bob@example.com.')
    expect(s('assigned', { user_id: 'u-gone' }, alice)).toBe('alice@example.com assigned the incident to a project member.')
    expect(s('assigned', {}, alice)).toBe('alice@example.com removed the assignee.')
    expect(s('comment', {}, { ...alice, body: 'hello' })).toBe('alice@example.com commented:')
    expect(s('comment', {}, { ...alice, body: null })).toBe('alice@example.com wrote a comment that was deleted.')
    expect(s('comment_deleted', { comment_id: 4 }, alice)).toBe('alice@example.com deleted a comment.')
    expect(s('reopened', { rule_id: 'r1' })).toBe('Reopened because rule “Payment errors” fired again.')
    expect(s('reopened', { rule_id: 'r-unknown' })).toBe('Reopened because a linked rule fired again.')
    expect(s('reopened', {}, alice)).toBe('alice@example.com reopened the incident.')
  })

  it('counts flapping repeats from the closed fields', () => {
    expect(s('alert_firing', { rule_name: 'Flappy' }, { count: 5, last_at: '2026-11-10T14:05:30Z' })).toBe('Rule “Flappy” fired 5 times, most recently at 14:05 UTC.')
    expect(s('alert_resolved', { rule_name: 'Flappy' }, { count: 3, last_at: '2026-11-10T09:07:00Z' })).toBe('Rule “Flappy” resolved 3 times, most recently at 09:07 UTC.')
  })

  it('an unknown type is a neutral line, never the payload', () => {
    const out = s('deploy', { version: '1.2.3', secret: 'lsk_LEAK' })
    expect(out).toBe('Something changed on this incident.')
    expect(out).not.toContain('lsk_LEAK')
  })

  it('names the actor, the system or someone', () => {
    expect(actorName({ actor: null })).toBe('LogSage')
    expect(actorName({ actor: 'u1', actor_email: '' })).toBe('Someone')
  })
})

describe('Open in Logs', () => {
  it('covers the incident window and, with one group, filters to it', () => {
    expect(logsLink('shop', '2026-11-10T12:00:00Z', '2026-11-10T12:30:00Z', ['00000000000000ab'])).toBe(
      '/projects/shop/logs?from=2026-11-10T11%3A45%3A00Z&to=2026-11-10T12%3A31%3A00Z&fingerprint=00000000000000ab',
    )
    expect(logsLink('shop', '2026-11-10T12:00:00Z', '2026-11-10T12:30:00Z', ['a', 'b'])).not.toContain('fingerprint')
    // At most the explorer's 7-day window.
    const long = new URL(logsLink('shop', '2026-10-01T00:00:00Z', '2026-11-10T00:00:00Z', []), 'http://x').searchParams
    expect(Date.parse(long.get('to') ?? '') - Date.parse(long.get('from') ?? '')).toBeLessThanOrEqual(7 * 86_400_000)
  })
})

describe('error mapping', () => {
  const e = (status: number, error?: string, headers: Record<string, string> = {}) => errorFromResponse(json(status, error === undefined ? undefined : { error }, headers))
  it('tells a stale version from the event cap and other conflicts', async () => {
    const stale = await e(409, 'version: the incident changed; reload it and try again')
    expect(isStale(stale) && writeError(stale) === STALE).toBe(true)
    const cap = await e(409, 'events: this incident has reached 1000 timeline events')
    expect(isEventCap(cap) && writeError(cap) === EVENT_CAP).toBe(true)
    expect(writeError(await e(409, 'status: acknowledge from acknowledged is not allowed'))).toBe(TRANSITION)
    expect(writeError(await e(409, 'incidents: at most 1000 open incidents per project'))).toBe('Incidents: at most 1000 open incidents per project.')
  })
  it('maps validation to fields and reads to problems', async () => {
    const v = await e(400, 'title: 1 to 200 characters')
    expect(fieldOf(v)).toBe('title')
    expect(writeError(v)).toBe('1 to 200 characters.')
    expect(readProblem(await e(404), 'incident')).toEqual({ text: 'This incident does not exist in this project.', retry: false, notFound: true })
    expect(readProblem(await e(429, 'slow', { 'Retry-After': '9' }))).toEqual({ text: 'Too many requests. Try again in 9 seconds.', retry: true })
    expect(readProblem(await e(503))).toMatchObject({ retry: true })
    expect(writeError(await e(429, 'slow', { 'Retry-After': '4' }))).toBe('Too many requests. Try again in 4 seconds.')
  })
  it('merges pages without repeats', () => {
    expect(mergeIncidents([{ incidents: [incident(3), incident(2)] }, { incidents: [incident(2), incident(1)] }]).map((i) => i.number)).toEqual([3, 2, 1])
  })
})
