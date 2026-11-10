import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { ApiError, errorMessage, request } from '../api/client'
import { isoUtc } from '../explorer/model'
import type { Filters } from './model'

// The incident endpoints of cmd/api (ADR 0026, contract in docs/architecture.md).

export type Status = 'open' | 'acknowledged' | 'resolved'
export type Severity = 'critical' | 'high' | 'medium' | 'low'
export type Incident = {
  id: string
  number: number
  title: string
  severity: Severity
  status: Status
  source: 'auto' | 'manual'
  assignee: string | null
  assignee_email?: string
  summary: string
  services: string[]
  opened_at: string
  acknowledged_at: string | null
  acknowledged_by: string | null
  resolved_at: string | null
  resolved_by: string | null
  last_activity_at: string
  recurrence_of: number | null
  all_alerts_resolved: boolean
  events_dropped: number
  version: number
  alert_count: number
  group_count: number
}
export type LinkedAlert = {
  rule_id: string | null
  rule_name: string
  state_key: string
  services: string[]
  linked_at: string
  closed_at: string | null
  close_reason: '' | 'resolved' | 'rule_disabled' | 'rule_deleted' | 'rule_changed'
  state: string
}
export type LinkedGroup = { fingerprint: string; template: string; source: 'auto' | 'manual'; linked_at: string; linked_by: string | null }
export type Suggestion = {
  fingerprint: string
  title: string
  services: string[]
  levels: string[]
  count: number
  is_new: boolean
  regressed: boolean
  first_seen: string
  last_seen: string
}
export type TimelineEvent = {
  id: number
  type: string
  actor: string | null
  actor_email?: string
  at: string
  last_at: string
  count: number
  payload: Record<string, unknown>
  body: string | null
}
export type Detail = {
  incident: Incident
  alerts: LinkedAlert[]
  groups: LinkedGroup[]
  suggested_groups: Suggestion[] | null
  suggested_groups_error?: string
  events: TimelineEvent[]
  next_before: number | null
}
export type Summary = { open: number; acknowledged: number; resolved: number }
export type Member = { user_id: string; email: string; role: 'owner' | 'admin' | 'viewer' }

/** The server's limits (ADR 0026). */
export const LIMITS = { title: 200, summary: 2000, comment: 2000, page: 50 } as const
export const SEVERITIES: readonly Severity[] = ['critical', 'high', 'medium', 'low']

const base = (slug: string) => `/api/projects/${encodeURIComponent(slug)}/incidents`
export const incidentsKey = (slug: string) => ['incidents', slug] as const
export const summaryKey = (slug: string) => ['incidents', slug, 'summary'] as const
export const detailKey = (slug: string, n: number) => ['incidents', slug, 'detail', n] as const

const retry = (count: number, err: unknown) => count < 2 && err instanceof ApiError && (err.kind === 'network' || err.kind === 'server')

/** The list's query string from the filters (the API's names). */
export function listParams(f: Filters, now: number): URLSearchParams {
  const p = new URLSearchParams()
  if (f.status !== 'all') p.set('status', f.status === 'active' ? 'open,acknowledged' : f.status)
  if (f.severity.length > 0) p.set('severity', f.severity.join(','))
  if (f.assignee === 'me') p.set('assignee', 'me')
  if (f.assignee === 'unassigned') p.set('assignee', 'none')
  if (f.service) p.set('service', f.service)
  if (f.q) p.set('q', f.q)
  if (f.range !== 'all') p.set('from', isoUtc(now - RANGE_MS[f.range]))
  p.set('limit', String(LIMITS.page))
  return p
}
export const RANGE_MS = { '24h': 86_400_000, '7d': 7 * 86_400_000, '30d': 30 * 86_400_000 } as const

export function useIncidents(slug: string, f: Filters, now: number) {
  const params = listParams(f, now)
  return useInfiniteQuery({
    queryKey: [...incidentsKey(slug), 'list', params.toString()],
    initialPageParam: '',
    queryFn: ({ signal, pageParam }) => {
      const p = new URLSearchParams(params)
      if (pageParam) p.set('cursor', pageParam)
      return request<{ incidents: Incident[]; next_cursor: string | null }>('GET', `${base(slug)}?${p.toString()}`, { signal })
    },
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    retry,
  })
}

/** Pages in order without duplicate incidents (activity moves rows between pages). */
export function mergeIncidents(pages: readonly { incidents: Incident[] }[] | undefined): Incident[] {
  const seen = new Set<string>()
  const out: Incident[] = []
  for (const page of pages ?? []) {
    for (const i of page.incidents) {
      if (seen.has(i.id)) continue
      seen.add(i.id)
      out.push(i)
    }
  }
  return out
}

/** The nav's open count; refreshed by the header's shared poll (ADR 0027). */
export function useSummary(slug: string, enabled: boolean) {
  return useQuery({
    queryKey: summaryKey(slug),
    enabled,
    queryFn: ({ signal }) => request<Summary>('GET', `${base(slug)}/summary`, { signal }),
    retry,
    refetchOnWindowFocus: false,
    staleTime: 10_000,
  })
}

export function useDetail(slug: string, n: number) {
  return useQuery({
    queryKey: detailKey(slug, n),
    queryFn: ({ signal }) => request<Detail>('GET', `${base(slug)}/${n}`, { signal }),
    retry,
  })
}

export function useMembers(slug: string) {
  return useQuery({
    queryKey: ['members', slug],
    queryFn: ({ signal }) => request<{ members: Member[] }>('GET', `/api/projects/${encodeURIComponent(slug)}/members`, { signal }),
    select: (d) => d.members,
    retry,
    staleTime: 60_000,
  })
}

export const fetchEvents = (slug: string, n: number, before: number) =>
  request<{ events: TimelineEvent[]; next_before: number | null }>('GET', `${base(slug)}/${n}/events?before=${before}&limit=${LIMITS.page}`)

type IncidentResp = { incident: Incident }
export const createIncident = (slug: string, body: { title: string; severity: Severity; summary: string }) =>
  request<IncidentResp>('POST', base(slug), { body })
export const patchIncident = (slug: string, n: number, body: { title?: string; severity?: Severity; summary?: string; version: number }) =>
  request<IncidentResp>('PATCH', `${base(slug)}/${n}`, { body })
export type Action = 'acknowledge' | 'resolve' | 'reopen'
export const changeStatus = (slug: string, n: number, action: Action, version: number, note: string) =>
  request<IncidentResp>('POST', `${base(slug)}/${n}/${action}`, { body: { version, ...(note && { note }) } })
export const assign = (slug: string, n: number, userId: string | null) =>
  userId ? request<IncidentResp>('POST', `${base(slug)}/${n}/assign`, { body: { user_id: userId } }) : request<IncidentResp>('POST', `${base(slug)}/${n}/unassign`, { body: {} })
export const addComment = (slug: string, n: number, body: string) => request<{ event: TimelineEvent }>('POST', `${base(slug)}/${n}/comments`, { body: { body } })
export const deleteComment = (slug: string, n: number, id: number) => request<undefined>('DELETE', `${base(slug)}/${n}/comments/${id}`)
export const linkGroup = (slug: string, n: number, fp: string) => request<IncidentResp>('POST', `${base(slug)}/${n}/groups/${fp}`, { body: {} })
export const unlinkGroup = (slug: string, n: number, fp: string) => request<IncidentResp>('DELETE', `${base(slug)}/${n}/groups/${fp}`)

export const STALE = 'This incident changed while you were editing. Reload it.'
export const EVENT_CAP = 'This incident has reached its event limit. New comments cannot be added.'

/** 409 because the version is stale (the server says "version: ..."). */
export const isStale = (err: unknown) => err instanceof ApiError && err.status === 409 && (err.serverMessage ?? '').startsWith('version:')
/** 409 at the event cap ("events: ..."). */
export const isEventCap = (err: unknown) => err instanceof ApiError && err.status === 409 && (err.serverMessage ?? '').startsWith('events:')

export const TRANSITION = 'This status change is no longer possible: someone changed the status first. Reload the incident.'

const sentence = (s: string, keepField = false) => {
  const t = keepField ? s : s.replace(/^[a-z_]+:\s*/, '')
  return t.charAt(0).toUpperCase() + t.slice(1) + (/[.!?]$/.test(t) ? '' : '.')
}

/** The form field a 400 is about ("title: 1 to 200 characters"). */
export function fieldOf(err: unknown): string | null {
  if (!(err instanceof ApiError) || err.kind !== 'invalid' || !err.serverMessage) return null
  const k = err.serverMessage.split(':', 1)[0] ?? ''
  return ['title', 'severity', 'summary', 'body', 'note', 'assignee', 'user_id'].includes(k) ? k : null
}

/** Plain copy for a failed incident write. */
export function writeError(err: unknown): string {
  if (isStale(err)) return STALE
  if (isEventCap(err)) return EVENT_CAP
  if (err instanceof ApiError) {
    if (err.kind === 'invalid' && err.serverMessage) return sentence(err.serverMessage)
    if (err.status === 409 && (err.serverMessage ?? '').startsWith('status:')) return TRANSITION
    if (err.status === 409 && err.serverMessage) return sentence(err.serverMessage, true)
    if (err.kind === 'rate_limited') return `Too many requests. Try again in ${err.retryAfter ?? 1} seconds.`
  }
  return errorMessage(err)
}

export type Problem = { text: string; retry: boolean; notFound?: boolean }

/** Copy for a failed read. */
export function readProblem(err: unknown, what: 'list' | 'incident' = 'list'): Problem {
  if (err instanceof ApiError) {
    switch (err.kind) {
      case 'not_found':
        return what === 'incident'
          ? { text: 'This incident does not exist in this project.', retry: false, notFound: true }
          : { text: 'This project was not found, or you are not a member of it. Choose a project in the header.', retry: false }
      case 'rate_limited':
        return { text: `Too many requests. Try again in ${err.retryAfter ?? 1} seconds.`, retry: true }
      case 'network':
      case 'server':
        return { text: errorMessage(err), retry: true }
      default:
        return { text: errorMessage(err), retry: false }
    }
  }
  return { text: errorMessage(err), retry: true }
}
