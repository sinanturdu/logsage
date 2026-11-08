// The error groups' URL model (ADR 0023). Like the explorer's (ADR 0017),
// everything a user chooses lives in the URL; parsing drops invalid values
// instead of throwing, and serializing leaves defaults out.

import { DEFAULT_PRESET, MAX_SERVICES, MAX_TEXT, isoUtc, parseFingerprint, parseState, type TimeRange } from '../explorer/model'

export { parseFingerprint }

/** Only these levels are grouped (ADR 0022). */
export const GROUP_LEVELS = ['warn', 'error', 'fatal'] as const
export type GroupLevel = (typeof GROUP_LEVELS)[number]
export const DEFAULT_GROUP_LEVELS: readonly GroupLevel[] = ['error', 'fatal']

export const STATUS_FILTERS = ['open', 'ignored', 'resolved', 'all'] as const
export type StatusFilter = (typeof STATUS_FILTERS)[number]
export type GroupStatus = Exclude<StatusFilter, 'all'>

export const SORTS = ['count', 'last_seen', 'first_seen'] as const
export type Sort = (typeof SORTS)[number]
export const SORT_LABEL: Record<Sort, string> = { count: 'Most events', last_seen: 'Last seen', first_seen: 'First seen' }
export const STATUS_LABEL: Record<StatusFilter, string> = { open: 'Open', ignored: 'Ignored', resolved: 'Resolved', all: 'All' }

export type GroupsState = {
  range: TimeRange
  /** Never empty: no level would list nothing. */
  levels: GroupLevel[]
  services: string[]
  /** Searches the template. */
  q: string
  status: StatusFilter
  sort: Sort
  /** Only groups first seen in the range ("Show new groups only"). */
  newOnly: boolean
}

function list(v: string | null): string[] {
  if (!v) return []
  const out: string[] = []
  for (const part of v.split(',')) if (part && !out.includes(part)) out.push(part)
  return out
}

/** The range part of the URL, parsed exactly as the explorer does. */
export function parseRange(params: URLSearchParams): TimeRange {
  const only = new URLSearchParams()
  for (const k of ['range', 'from', 'to']) {
    const v = params.get(k)
    if (v !== null) only.set(k, v)
  }
  return parseState(only).range
}

export function serializeRange(range: TimeRange, p = new URLSearchParams()): URLSearchParams {
  if (range.kind === 'absolute') {
    p.set('from', isoUtc(range.from))
    p.set('to', isoUtc(range.to))
  } else if (range.preset !== DEFAULT_PRESET) {
    p.set('range', range.preset)
  }
  return p
}

const oneOf = <T extends string>(all: readonly T[], v: string | null, fallback: T): T => (v !== null && (all as readonly string[]).includes(v) ? (v as T) : fallback)

export function sortGroupLevels(levels: readonly string[]): GroupLevel[] {
  return GROUP_LEVELS.filter((l) => levels.includes(l))
}

export function parseGroupsState(params: URLSearchParams): GroupsState {
  const levels = sortGroupLevels(list(params.get('level')))
  const qRaw = params.get('q') ?? ''
  return {
    range: parseRange(params),
    levels: levels.length > 0 ? levels : [...DEFAULT_GROUP_LEVELS],
    services: list(params.get('service'))
      .filter((s) => s.length <= MAX_TEXT)
      .slice(0, MAX_SERVICES),
    q: qRaw.length <= MAX_TEXT ? qRaw : '',
    status: oneOf(STATUS_FILTERS, params.get('status'), 'open'),
    sort: oneOf(SORTS, params.get('sort'), 'count'),
    newOnly: params.get('new') === '1',
  }
}

const sameLevels = (a: readonly string[], b: readonly string[]) => a.length === b.length && a.every((l) => b.includes(l))

export function serializeGroupsState(s: GroupsState): URLSearchParams {
  const p = serializeRange(s.range)
  const levels = sortGroupLevels(s.levels)
  if (levels.length > 0 && !sameLevels(levels, DEFAULT_GROUP_LEVELS)) p.set('level', levels.join(','))
  if (s.services.length > 0) p.set('service', s.services.join(','))
  if (s.q) p.set('q', s.q)
  if (s.status !== 'open') p.set('status', s.status)
  if (s.sort !== 'count') p.set('sort', s.sort)
  if (s.newOnly) p.set('new', '1')
  return p
}

/**
 * Level checkboxes for groups: unchecking the last checked level is refused
 * (no level would list nothing).
 */
export function toggleGroupLevel(current: readonly GroupLevel[], level: GroupLevel): GroupLevel[] {
  const next = current.includes(level) ? current.filter((l) => l !== level) : [...current, level]
  return next.length === 0 ? [...current] : sortGroupLevels(next)
}

/**
 * The sort sent to the API. "New only" asks for the newest first: new
 * groups (first seen in the range) then come first, so the list can stop at
 * the first group that is not new. The API has no is_new filter.
 */
export const apiSort = (s: Pick<GroupsState, 'sort' | 'newOnly'>): Sort => (s.newOnly ? 'first_seen' : s.sort)

/** Query parameters for GET groups. */
export function groupsParams(s: GroupsState, w: { from: number; to: number }): URLSearchParams {
  const p = new URLSearchParams({ from: isoUtc(w.from), to: isoUtc(w.to) })
  p.set('level', sortGroupLevels(s.levels).join(','))
  if (s.services.length > 0) p.set('service', s.services.join(','))
  if (s.q) p.set('q', s.q)
  if (s.status !== 'all') p.set('status', s.status)
  p.set('sort', apiSort(s))
  return p
}

const base = (slug: string) => `/projects/${encodeURIComponent(slug)}`

/** The detail page of a group, keeping the range. */
export function groupHref(slug: string, fingerprint: string, range: TimeRange): string {
  const q = serializeRange(range).toString()
  return `${base(slug)}/groups/${fingerprint}${q ? `?${q}` : ''}`
}

/** The explorer filtered to one group, over the same range. */
export function logsHref(slug: string, fingerprint: string, range: TimeRange): string {
  const p = serializeRange(range)
  p.set('fingerprint', fingerprint)
  return `${base(slug)}/logs?${p.toString()}`
}
