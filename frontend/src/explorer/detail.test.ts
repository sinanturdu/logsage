import { describe, expect, it } from 'vitest'
import { activeFilterCount, filterChips, groupAttributes, isRequestKey, logJson, sameTraceWindow } from './detail'
import { MAX_WINDOW_MS, parseState } from './model'
import { MIN_SHARE_SAMPLE, shareOrNull, summarize, type Bucket } from './time'

describe('attribute grouping', () => {
  it('sorts known request keys into Request and the rest into Context, both by key', () => {
    const g = groupAttributes({
      region: 'eu-west-1',
      'http.route': '/v1/charges',
      order_id: 'ord_7Q2K9M',
      attempt: '3',
      'gateway.host': 'psp-eu-2.example.net',
      span_id: '00f067aa0ba902b7',
      'http.method': 'POST',
      timeout_ms: '5000',
      'k8s.pod': 'payments-6f9c',
      'url.path': '/charges',
      'response.status': '504',
      'request.id': 'r1',
      method: 'POST',
      status: '504',
      retry: 'true',
      duration_ms: '5114',
      'httpx.custom': 'not a request prefix',
      statuses: 'not an exact key',
    })
    expect(g.spanId).toBe('00f067aa0ba902b7')
    expect(g.request.map(([k]) => k)).toEqual([
      'attempt', 'duration_ms', 'gateway.host', 'http.method', 'http.route', 'method',
      'request.id', 'response.status', 'retry', 'status', 'timeout_ms', 'url.path',
    ])
    expect(g.context.map(([k]) => k)).toEqual(['httpx.custom', 'k8s.pod', 'order_id', 'region', 'statuses'])
  })

  it('handles empty, missing and unusual input', () => {
    expect(groupAttributes({})).toEqual({ spanId: null, request: [], context: [] })
    expect(groupAttributes(null)).toEqual({ spanId: null, request: [], context: [] })
    expect(groupAttributes({ only: 'context' }).request).toEqual([])
    expect(groupAttributes({ B: '1', a: '2', '<img src=x>': '3' }).context.map(([k]) => k)).toEqual(['<img src=x>', 'B', 'a'])
    expect(isRequestKey('HTTP.route')).toBe(false) // case-sensitive, like the keys senders use
  })
})

describe('same trace window', () => {
  it('spans 10 minutes before and after, including the log', () => {
    const ts = Date.parse('2026-11-07T02:16:58.412Z')
    const w = sameTraceWindow(ts)
    expect(w.from).toBe(ts - 600_000)
    expect(w.to).toBe(ts + 600_001)
    expect(w.from <= ts && ts < w.to).toBe(true)
  })

  it('never exceeds the server maximum', () => {
    const w = sameTraceWindow(0, 5 * 86_400_000) // a half wider than 7 days / 2
    expect(w.to - w.from).toBeLessThanOrEqual(MAX_WINDOW_MS)
    const tiny = sameTraceWindow(1000, 600_000, 60_000)
    expect(tiny.to - tiny.from).toBeLessThanOrEqual(60_000)
  })
})

describe('copy as JSON', () => {
  it('pretty-prints the whole object', () => {
    expect(logJson({ event_id: '1-0', attributes: { a: '<b>' } })).toBe('{\n  "event_id": "1-0",\n  "attributes": {\n    "a": "<b>"\n  }\n}')
  })
})

describe('filter chips', () => {
  const p = (qs: string) => parseState(new URLSearchParams(qs))
  it('lists one chip per active filter with a removal', () => {
    const s = p('from=2026-11-07T02:11:30Z&to=2026-11-07T02:17:00Z&zoom=1&level=error,fatal&service=api,payments&q=timeout&trace_id=4bf92f3577b34da6a3ce929d0e0e4736')
    const chips = filterChips(s)
    expect(chips.map((c) => c.label)).toEqual([
      'range 02:11:30 to 02:17:00 UTC',
      'level error, fatal',
      'service api',
      'service payments',
      'search timeout',
      'trace 4bf92f3577b34da6a3ce929d0e0e4736',
    ])
    expect(chips[0]?.remove).toEqual({ range: { kind: 'relative', preset: '15m' }, zoom: false })
    expect(chips[2]?.remove).toEqual({ services: ['payments'] })
    expect(activeFilterCount(s)).toBe(5) // level, two services, search, trace
    expect(filterChips(p(''))).toEqual([])
    expect(filterChips(p('range=6h'))).toEqual([]) // a preset is the view, not a filter
  })
})

describe('small samples', () => {
  const b = (t: number, info: number, error: number): Bucket => ({ t, debug: 0, info, warn: 0, error, fatal: 0 })
  it('gives no share below the minimum sample', () => {
    expect(MIN_SHARE_SAMPLE).toBe(5)
    expect(shareOrNull(b(0, 2, 2))).toBeNull()
    expect(shareOrNull(b(0, 3, 2))).toBe(40)
  })

  it('ignores small buckets for the peak share', () => {
    const t = Date.parse('2026-11-07T02:15:00Z')
    const s = summarize([b(t, 0, 2), b(t + 10_000, 90, 10)], 10) // 2 of 2 errors is not "100%"
    expect(s).toBe('102 logs. Peak volume 100 logs per 10 s at 02:15:10 UTC. Peak error and fatal share 10% at 02:15:10 UTC.')
    expect(summarize([b(t, 1, 3)], 10)).toBe('4 logs. Peak volume 4 logs per 10 s at 02:15:00 UTC. No bucket has enough logs for an error share.')
  })
})
