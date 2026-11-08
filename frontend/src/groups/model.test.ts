import { describe, expect, it } from 'vitest'
import {
  apiSort,
  groupHref,
  groupsParams,
  logsHref,
  parseGroupsState,
  serializeGroupsState,
  toggleGroupLevel,
  type GroupsState,
} from './model'

const parse = (q: string) => parseGroupsState(new URLSearchParams(q))
const DEFAULT: GroupsState = {
  range: { kind: 'relative', preset: '15m' },
  levels: ['error', 'fatal'],
  services: [],
  q: '',
  status: 'open',
  sort: 'count',
  newOnly: false,
}

describe('groups URL state', () => {
  it('defaults: last 15 min, error and fatal, open, by count', () => {
    expect(parse('')).toEqual(DEFAULT)
    expect(serializeGroupsState(DEFAULT).toString()).toBe('')
  })

  it('round-trips every choice', () => {
    const s: GroupsState = {
      range: { kind: 'absolute', from: Date.UTC(2026, 10, 7, 2, 0), to: Date.UTC(2026, 10, 7, 3, 0) },
      levels: ['warn', 'error', 'fatal'],
      services: ['payments', 'checkout'],
      q: 'timeout <n>',
      status: 'all',
      sort: 'last_seen',
      newOnly: true,
    }
    const p = serializeGroupsState(s)
    expect(p.toString()).toBe(
      'from=2026-11-07T02%3A00%3A00Z&to=2026-11-07T03%3A00%3A00Z&level=warn%2Cerror%2Cfatal&service=payments%2Ccheckout&q=timeout+%3Cn%3E&status=all&sort=last_seen&new=1',
    )
    expect(parseGroupsState(p)).toEqual(s)
  })

  it.each([
    ['level=info,debug', { levels: ['error', 'fatal'] }],
    ['level=fatal,warn,bogus', { levels: ['warn', 'fatal'] }],
    ['status=deleted', { status: 'open' }],
    ['status=resolved', { status: 'resolved' }],
    ['sort=random', { sort: 'count' }],
    ['sort=first_seen', { sort: 'first_seen' }],
    ['new=yes', { newOnly: false }],
    ['range=99y', { range: { kind: 'relative', preset: '15m' } }],
    ['range=24h', { range: { kind: 'relative', preset: '24h' } }],
    ['from=2026-11-07T03:00:00Z&to=2026-11-07T02:00:00Z', { range: { kind: 'relative', preset: '15m' } }],
    ['from=2026-01-01T00:00:00Z&to=2026-02-01T00:00:00Z', { range: { kind: 'relative', preset: '15m' } }],
    [`q=${'x'.repeat(257)}`, { q: '' }],
    ['service=a,,a,b', { services: ['a', 'b'] }],
  ])('drops or keeps %s', (q, want) => {
    expect(parse(q)).toEqual({ ...DEFAULT, ...want })
  })

  it('keeps at least one level when unchecking', () => {
    expect(toggleGroupLevel(['error'], 'error')).toEqual(['error'])
    expect(toggleGroupLevel(['error', 'fatal'], 'warn')).toEqual(['warn', 'error', 'fatal'])
    expect(toggleGroupLevel(['warn', 'error', 'fatal'], 'error')).toEqual(['warn', 'fatal'])
  })
})

describe('API parameters', () => {
  const w = { from: Date.UTC(2026, 10, 7, 2, 0), to: Date.UTC(2026, 10, 7, 2, 15) }

  it('sends the window, levels, status and sort', () => {
    expect(groupsParams(DEFAULT, w).toString()).toBe('from=2026-11-07T02%3A00%3A00Z&to=2026-11-07T02%3A15%3A00Z&level=error%2Cfatal&status=open&sort=count')
  })

  it('leaves status out for all, and asks for the newest first when new only', () => {
    const p = groupsParams({ ...DEFAULT, status: 'all', newOnly: true, sort: 'count', services: ['a'], q: 'x' }, w)
    expect(p.has('status')).toBe(false)
    expect(p.get('sort')).toBe('first_seen')
    expect(p.get('service')).toBe('a')
    expect(p.get('q')).toBe('x')
    expect(apiSort({ sort: 'last_seen', newOnly: false })).toBe('last_seen')
  })

  it('links keep the range', () => {
    const range = { kind: 'relative', preset: '1h' } as const
    expect(groupHref('my shop', '0123456789abcdef', range)).toBe('/projects/my%20shop/groups/0123456789abcdef?range=1h')
    expect(groupHref('shop', '0123456789abcdef', { kind: 'relative', preset: '15m' })).toBe('/projects/shop/groups/0123456789abcdef')
    expect(logsHref('shop', '0123456789abcdef', range)).toBe('/projects/shop/logs?range=1h&fingerprint=0123456789abcdef')
  })
})
