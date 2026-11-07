import { describe, expect, it } from 'vitest'
import {
  LEVELS,
  LEVEL_SHAPE,
  contextWindow,
  parseState,
  resolveRange,
  serializeState,
  toggleLevel,
  toggleService,
  validTraceId,
  type ExplorerState,
} from './model'

const p = (s: string) => parseState(new URLSearchParams(s))
const NOW = Date.parse('2026-11-07T02:17:00Z')

describe('URL state', () => {
  it('defaults to the last 15 minutes with no filters', () => {
    expect(p('')).toEqual({ range: { kind: 'relative', preset: '15m' }, levels: [], services: [], q: '', traceId: '', zoom: false, live: false })
    expect(serializeState(p('')).toString()).toBe('')
  })

  it('round-trips every field', () => {
    const s: ExplorerState = {
      range: { kind: 'absolute', from: Date.parse('2026-11-07T02:11:30Z'), to: Date.parse('2026-11-07T02:17:00.250Z') },
      levels: ['warn', 'error'],
      services: ['payments', 'checkout'],
      q: 'gateway timeout',
      traceId: '4bf92f3577b34da6a3ce929d0e0e4736',
      zoom: true,
      live: false,
    }
    const qs = serializeState(s).toString()
    expect(qs).toBe(
      'from=2026-11-07T02%3A11%3A30Z&to=2026-11-07T02%3A17%3A00.250Z&level=warn%2Cerror&service=payments%2Ccheckout&q=gateway+timeout&trace_id=4bf92f3577b34da6a3ce929d0e0e4736&zoom=1',
    )
    expect(p(qs)).toEqual(s)
    expect(serializeState(p('range=6h')).toString()).toBe('range=6h')
  })

  it('serializes minimally', () => {
    expect(serializeState(p('range=15m&level=debug,info,warn,error,fatal')).toString()).toBe('')
    expect(serializeState(p('level=fatal,error,error')).get('level')).toBe('error,fatal') // ordered, deduplicated
  })

  it.each([
    ['range=2h', '15m relative'],
    ['range=', '15m relative'],
    ['from=yesterday&to=today', '15m relative'],
    ['from=2026-11-07T02:00:00Z', '15m relative'], // to missing
    ['from=2026-11-07T03:00:00Z&to=2026-11-07T02:00:00Z', '15m relative'], // reversed
    ['from=2026-10-01T00:00:00Z&to=2026-11-07T00:00:00Z', '15m relative'], // wider than 7 days
    ['from=2026-11-07 02:00:00&to=2026-11-07 03:00:00', '15m relative'], // not RFC3339
  ])('drops an invalid range %s', (qs) => {
    expect(p(qs).range).toEqual({ kind: 'relative', preset: '15m' })
  })

  it('drops invalid filter values without failing', () => {
    const s = p(`level=error,loud,,ERROR&service=${'x'.repeat(300)},payments&q=${'q'.repeat(300)}&trace_id=not-hex&unknown=1`)
    expect(s.levels).toEqual(['error'])
    expect(s.services).toEqual(['payments'])
    expect(s.q).toBe('')
    expect(s.traceId).toBe('')
    expect(p('trace_id=4BF92F3577B34DA6A3CE929D0E0E4736').traceId).toBe('4bf92f3577b34da6a3ce929d0e0e4736')
    const many = Array.from({ length: 30 }, (_, i) => `s${i}`).join(',')
    expect(p(`service=${many}`).services).toHaveLength(20)
  })

  it('keeps live only with a relative range and ignores from/to while live', () => {
    expect(p('live=1')).toMatchObject({ live: true, range: { kind: 'relative', preset: '15m' } })
    expect(p('range=1h&live=1&from=2026-11-07T02:00:00Z&to=2026-11-07T02:05:00Z')).toMatchObject({ live: true, range: { kind: 'relative', preset: '1h' } })
    expect(serializeState(p('range=1h&live=1&from=2026-11-07T02:00:00Z&to=2026-11-07T02:05:00Z')).toString()).toBe('range=1h&live=1')
    expect(p('live=yes').live).toBe(false)
  })

  it('keeps zoom only with an absolute range', () => {
    expect(p('range=1h&zoom=1').zoom).toBe(false)
    expect(serializeState(p('zoom=1')).toString()).toBe('')
    expect(p('from=2026-11-07T02:00:00Z&to=2026-11-07T02:05:00Z&zoom=1').zoom).toBe(true)
    expect(p('from=2026-11-07T02:00:00Z&to=2026-11-07T02:05:00Z&zoom=yes').zoom).toBe(false)
  })

  it('absolute wins over relative when both are given', () => {
    expect(p('range=1h&from=2026-11-07T02:00:00Z&to=2026-11-07T02:05:00Z').range.kind).toBe('absolute')
  })
})

describe('ranges', () => {
  it('resolves relative ranges at now', () => {
    expect(resolveRange({ kind: 'relative', preset: '1h' }, NOW)).toEqual({ from: NOW - 3_600_000, to: NOW })
  })

  it('puts an absolute selection inside the smallest preset that contains it', () => {
    const sel = { kind: 'absolute' as const, from: NOW - 5 * 60_000, to: NOW - 60_000 }
    expect(contextWindow(sel, NOW)).toEqual({ from: NOW - 15 * 60_000, to: NOW, preset: '15m' })
    const older = { kind: 'absolute' as const, from: NOW - 3 * 3_600_000, to: NOW - 2 * 3_600_000 }
    expect(contextWindow(older, NOW).preset).toBe('6h')
    const ancient = { kind: 'absolute' as const, from: NOW - 30 * 86_400_000, to: NOW - 29 * 86_400_000 }
    expect(contextWindow(ancient, NOW)).toEqual({ from: ancient.from, to: ancient.to, preset: null })
    expect(contextWindow(sel, NOW, true)).toEqual({ from: sel.from, to: sel.to, preset: null }) // zoomed in
  })
})

describe('facet toggles', () => {
  it('levels: all checked by default, never none', () => {
    expect(toggleLevel([], 'debug')).toEqual(['info', 'warn', 'error', 'fatal'])
    expect(toggleLevel(['info', 'warn', 'error', 'fatal'], 'debug')).toEqual([]) // all again: no filter
    expect(toggleLevel(['error'], 'warn')).toEqual(['warn', 'error'])
    expect(toggleLevel(['error'], 'error')).toEqual(['error']) // the last one stays
  })

  it('services: selection with the server limit', () => {
    expect(toggleService([], 'payments')).toEqual(['payments'])
    expect(toggleService(['payments', 'api'], 'payments')).toEqual(['api'])
    const twenty = Array.from({ length: 20 }, (_, i) => `s${i}`)
    expect(toggleService(twenty, 'one-more')).toEqual(twenty)
  })
})

describe('levels and trace ids', () => {
  it('maps every level to a distinct shape', () => {
    expect(LEVELS.map((l) => LEVEL_SHAPE[l])).toEqual(['○', '●', '▲', '■', '◆'])
  })

  it('accepts only 32 lowercase hex characters', () => {
    expect(validTraceId('4bf92f3577b34da6a3ce929d0e0e4736')).toBe(true)
    for (const bad of ['', '4bf92f35-77b3-4da6', '4bf92f3577b34da6a3ce929d0e0e473', '4bf92f3577b34da6a3ce929d0e0e47366', 'zbf92f3577b34da6a3ce929d0e0e4736']) {
      expect(validTraceId(bad)).toBe(false)
    }
  })
})
