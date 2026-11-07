// Pure helpers of the row detail (ADR 0018).
import { LEVELS, MAX_WINDOW_MS, type ExplorerState } from './model'
import { clockUtc } from './time'

export type Pair = readonly [key: string, value: string]
export type Grouped = { spanId: string | null; request: Pair[]; context: Pair[] }

const REQUEST_PREFIXES = ['http.', 'url.', 'request.', 'response.', 'gateway.']
const REQUEST_KEYS = new Set(['method', 'status', 'timeout_ms', 'attempt', 'retry', 'duration_ms'])

/** Whether an attribute key belongs to the Request zone. */
export function isRequestKey(key: string): boolean {
  return REQUEST_KEYS.has(key) || REQUEST_PREFIXES.some((p) => key.startsWith(p))
}

const byKey = (a: Pair, b: Pair) => (a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0)

/**
 * The deterministic grouping of arbitrary attributes: span_id goes to the
 * top line; keys with an HTTP or gateway prefix, or one of a few request
 * words, go to Request; everything else to Context. Each group is sorted by
 * key (code-unit order, so the same input always gives the same layout).
 */
export function groupAttributes(attrs: Record<string, string> | null | undefined): Grouped {
  const request: Pair[] = []
  const context: Pair[] = []
  let spanId: string | null = null
  for (const [key, value] of Object.entries(attrs ?? {})) {
    const v = String(value)
    if (key === 'span_id') spanId = v
    else if (isRequestKey(key)) request.push([key, v])
    else context.push([key, v])
  }
  return { spanId, request: request.sort(byKey), context: context.sort(byKey) }
}

export const SAME_TRACE_HALF_MS = 10 * 60_000
export const SAME_TRACE_LIMIT = 50

/**
 * The Same trace window around a log: 10 minutes before and after, the end
 * exclusive so the log itself is inside; never wider than the server's
 * maximum window.
 */
export function sameTraceWindow(ts: number, half = SAME_TRACE_HALF_MS, max = MAX_WINDOW_MS): { from: number; to: number } {
  const h = Math.min(half, Math.floor((max - 1) / 2))
  return { from: ts - h, to: ts + h + 1 }
}

/** The full log object, pretty-printed, for Copy as JSON. */
export function logJson(e: object): string {
  return JSON.stringify(e, null, 2)
}

/** A chip's label is the full filter; the component may shorten what it shows. */
export type Chip = { id: string; label: string; remove: Partial<ExplorerState> }

/** One removable chip per active filter, in a stable order. */
export function filterChips(s: ExplorerState): Chip[] {
  const chips: Chip[] = []
  if (s.range.kind === 'absolute') {
    chips.push({
      id: 'range',
      label: `range ${clockUtc(s.range.from)} to ${clockUtc(s.range.to)} UTC`,
      remove: { range: { kind: 'relative', preset: '15m' }, zoom: false },
    })
  }
  if (s.levels.length > 0 && s.levels.length < LEVELS.length) {
    chips.push({ id: 'level', label: `level ${s.levels.join(', ')}`, remove: { levels: [] } })
  }
  for (const svc of s.services) {
    chips.push({ id: `service:${svc}`, label: `service ${svc}`, remove: { services: s.services.filter((x) => x !== svc) } })
  }
  if (s.q) chips.push({ id: 'q', label: `search ${s.q}`, remove: { q: '' } })
  if (s.traceId) chips.push({ id: 'trace', label: `trace ${s.traceId}`, remove: { traceId: '' } })
  return chips
}

/** Active filters for the "Filters (N)" button: the range does not count. */
export function activeFilterCount(s: ExplorerState): number {
  return (s.levels.length > 0 ? 1 : 0) + s.services.length + (s.q ? 1 : 0) + (s.traceId ? 1 : 0)
}

