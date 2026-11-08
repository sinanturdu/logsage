import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { request } from '../api/client'
import { isoUtc, type ExplorerState, type Level } from './model'
import type { Bucket } from './time'

// Session endpoints of cmd/api (ADR 0014). React Query passes an
// AbortSignal to every request: when the filters change, the old query is no
// longer observed and its request is cancelled, and a response that still
// arrives is stored under its own key, so it can never replace newer data.

export type LogEvent = {
  event_id: string
  timestamp: string
  level: Level
  service: string
  message: string
  trace_id: string
  attributes: Record<string, string>
  /** The error group: 16 hex, empty when the log is not grouped (ADR 0022). */
  fingerprint?: string
}
type HistogramResponse = {
  bucket_seconds: number
  buckets: ({ t: string } & Omit<Bucket, 't'>)[]
  took_ms: number
}
export type Facets = {
  total: number
  levels: Record<Level, number>
  services: { name: string; count: number; other?: boolean }[]
  took_ms: number
}
type LogsPage = { events: LogEvent[]; next_cursor: string | null; took_ms: number }

export const STORE_DOWN = 'LogSage cannot reach its log store right now. Try again in a moment.'

export const PAGE_SIZE = 100
export const MAX_ROWS = 1000

/** The filters every log read takes; fingerprint narrows to one error group (ADR 0022). */
export type Filters = Pick<ExplorerState, 'levels' | 'services' | 'q' | 'traceId'> & { fingerprint?: string }
export type Window = { from: number; to: number }

export function filterParams(f: Filters, w: Window): URLSearchParams {
  const p = new URLSearchParams({ from: isoUtc(w.from), to: isoUtc(w.to) })
  if (f.levels.length > 0) p.set('level', f.levels.join(','))
  if (f.services.length > 0) p.set('service', f.services.join(','))
  if (f.q) p.set('q', f.q)
  if (f.traceId) p.set('trace_id', f.traceId)
  if (f.fingerprint) p.set('fingerprint', f.fingerprint)
  return p
}

const base = (slug: string) => `/api/projects/${encodeURIComponent(slug)}`
const filterKey = (f: Filters) => [f.levels.join(','), f.services.join(','), f.q, f.traceId, f.fingerprint ?? '']

export function useHistogram(slug: string, f: Filters, w: Window) {
  return useQuery({
    queryKey: ['histogram', slug, w.from, w.to, ...filterKey(f)],
    queryFn: async ({ signal }) => {
      const r = await request<HistogramResponse>('GET', `${base(slug)}/histogram?${filterParams(f, w).toString()}`, { signal })
      return { bucketSeconds: r.bucket_seconds, buckets: r.buckets.map((b) => ({ ...b, t: Date.parse(b.t) })) }
    },
    placeholderData: (prev) => prev, // keep the old chart while the new one loads
  })
}

export function useFacets(slug: string, f: Filters, w: Window) {
  return useQuery({
    queryKey: ['facets', slug, w.from, w.to, ...filterKey(f)],
    queryFn: ({ signal }) => request<Facets>('GET', `${base(slug)}/facets?${filterParams(f, w).toString()}`, { signal }),
    placeholderData: (prev) => prev,
  })
}

export function useLogs(slug: string, f: Filters, w: Window) {
  return useInfiniteQuery({
    queryKey: ['logs', slug, w.from, w.to, ...filterKey(f)],
    initialPageParam: '',
    queryFn: ({ signal, pageParam }) => {
      const p = filterParams(f, w)
      p.set('limit', String(PAGE_SIZE))
      if (pageParam) p.set('cursor', pageParam)
      return request<LogsPage>('GET', `${base(slug)}/logs?${p.toString()}`, { signal })
    },
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  })
}

/** Pages flattened in order, without duplicate event ids, capped at max. */
export function mergePages(pages: readonly { events: LogEvent[] }[] | undefined, max = MAX_ROWS): LogEvent[] {
  const seen = new Set<string>()
  const out: LogEvent[] = []
  for (const page of pages ?? []) {
    for (const e of page.events) {
      if (seen.has(e.event_id)) continue
      seen.add(e.event_id)
      out.push(e)
      if (out.length >= max) return out
    }
  }
  return out
}
