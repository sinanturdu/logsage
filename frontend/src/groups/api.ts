import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { ApiError, errorMessage, request } from '../api/client'
import { STORE_DOWN, type LogEvent } from '../explorer/api'
import { isoUtc } from '../explorer/model'
import { groupsParams, type GroupStatus, type GroupsState } from './model'

// The error group endpoints of cmd/api (ADR 0022, contract in
// docs/architecture.md). Every request takes React Query's AbortSignal.

export type Group = {
  fingerprint: string
  title: string
  sample: string
  error_type: string
  levels: string[]
  services: string[]
  /** Events in the requested range. */
  count: number
  /** All time, UTC. */
  first_seen: string
  last_seen: string
  /** Events per bucket over the range, oldest first. */
  sparkline: number[]
  is_new: boolean
  status: GroupStatus
  regressed: boolean
  resolved_at: string | null
}
export type GroupsResponse = { groups: Group[]; next_cursor: string | null; sparkline_bucket_seconds: number; took_ms: number }
export type GroupDetail = Group & {
  counts: { service: string; level: string; count: number }[]
  samples: LogEvent[]
  note: string
  updated_by: string
}
export type StatusResponse = {
  group: { fingerprint: string; status: GroupStatus; resolved_at: string | null; note: string; updated_by: string; updated_at: string }
}

/** The server's note limit (postgres.MaxGroupNote), in characters. */
export const MAX_NOTE = 500
export const GROUPS_PAGE = 50

const base = (slug: string) => `/api/projects/${encodeURIComponent(slug)}`

export function useGroups(slug: string, s: GroupsState, w: { from: number; to: number }) {
  const params = groupsParams(s, w)
  return useInfiniteQuery({
    queryKey: ['groups', slug, params.toString()],
    initialPageParam: '',
    queryFn: ({ signal, pageParam }) => {
      const p = new URLSearchParams(params)
      p.set('limit', String(GROUPS_PAGE))
      if (pageParam) p.set('cursor', pageParam)
      return request<GroupsResponse>('GET', `${base(slug)}/groups?${p.toString()}`, { signal })
    },
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  })
}

export function useGroupDetail(slug: string, fingerprint: string, w: { from: number; to: number } | null) {
  return useQuery({
    queryKey: ['group', slug, fingerprint, w?.from, w?.to],
    enabled: w !== null,
    queryFn: ({ signal }) => {
      const win = w ?? { from: 0, to: 0 }
      const p = new URLSearchParams({ from: isoUtc(win.from), to: isoUtc(win.to) })
      return request<GroupDetail>('GET', `${base(slug)}/groups/${fingerprint}?${p.toString()}`, { signal })
    },
    staleTime: 30_000,
  })
}

export function setGroupStatus(slug: string, fingerprint: string, status: GroupStatus, note: string) {
  return request<StatusResponse>('POST', `${base(slug)}/groups/${fingerprint}/status`, { body: { status, note } })
}

/** Pages in order without duplicate fingerprints (offsets can shift between pages). */
export function mergeGroups(pages: readonly { groups: Group[] }[] | undefined): Group[] {
  const seen = new Set<string>()
  const out: Group[] = []
  for (const page of pages ?? []) {
    for (const g of page.groups) {
      if (seen.has(g.fingerprint)) continue
      seen.add(g.fingerprint)
      out.push(g)
    }
  }
  return out
}

export const LIST_CHANGED = 'The list changed. Reload it.'

/** A 400 because the cursor belongs to another query (the list moved under it). */
export function isCursorChanged(err: unknown): boolean {
  return err instanceof ApiError && err.kind === 'invalid' && (err.serverMessage ?? '').startsWith('cursor:')
}

export type Problem = { text: string; retry: boolean; reload?: boolean; notFound?: boolean }

/** Copy for a failed groups request: what happened and how to fix it. */
export function groupsError(err: unknown, what: 'list' | 'group' = 'list'): Problem {
  if (isCursorChanged(err)) return { text: LIST_CHANGED, retry: false, reload: true }
  if (err instanceof ApiError) {
    switch (err.kind) {
      case 'network':
      case 'server':
        return { text: STORE_DOWN, retry: true }
      case 'rate_limited':
        return { text: `Too many requests. Try again in ${err.retryAfter ?? 1} seconds.`, retry: true }
      case 'not_found':
        return what === 'group'
          ? { text: 'This group does not exist in this project.', retry: false, notFound: true }
          : { text: 'This project was not found, or you are not a member of it. Choose a project in the header.', retry: false }
      default:
        return { text: errorMessage(err), retry: false }
    }
  }
  return { text: errorMessage(err), retry: true }
}
