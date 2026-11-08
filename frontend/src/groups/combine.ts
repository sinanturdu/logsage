// "Combine same message" (ADR 0023 addendum): groups whose template is the
// same in several services are shown as one parent row. Computed in the
// browser over the groups loaded so far; pure, so it is unit-tested alone.

import type { Group } from './api'
import { GROUP_LEVELS, type GroupStatus, type Sort } from './model'

export type Combined = {
  title: string
  count: number
  services: string[]
  levels: string[]
  first_seen: string
  last_seen: string
  /** The children's status when they all share it, else "mixed". */
  status: GroupStatus | 'mixed'
  is_new: boolean
  regressed: boolean
  sparkline: number[]
}

export type Row = { kind: 'group'; group: Group } | { kind: 'parent'; key: string; groups: Group[]; combined: Combined }

/** Bucket-by-bucket sum of sparklines that share one bucket size. */
export function sumSparklines(lines: readonly (readonly number[])[]): number[] {
  const out: number[] = []
  for (const line of lines) line.forEach((v, i) => (out[i] = (out[i] ?? 0) + v))
  return out
}

const time = (iso: string) => {
  const t = Date.parse(iso)
  return Number.isFinite(t) ? t : 0
}

function combine(groups: readonly Group[]): Combined {
  const first = groups[0] as Group
  const services: string[] = []
  const levels = new Set<string>()
  for (const g of groups) {
    for (const s of g.services) if (!services.includes(s)) services.push(s)
    for (const l of g.levels) levels.add(l)
  }
  const known = GROUP_LEVELS.filter((l) => levels.has(l))
  const other = [...levels].filter((l) => !(GROUP_LEVELS as readonly string[]).includes(l))
  const statuses = new Set(groups.map((g) => g.status))
  return {
    title: first.title,
    count: groups.reduce((n, g) => n + g.count, 0),
    services,
    levels: [...known, ...other],
    first_seen: groups.reduce((a, g) => (time(g.first_seen) < time(a) ? g.first_seen : a), first.first_seen),
    last_seen: groups.reduce((a, g) => (time(g.last_seen) > time(a) ? g.last_seen : a), first.last_seen),
    status: statuses.size === 1 ? first.status : 'mixed',
    is_new: groups.some((g) => g.is_new),
    regressed: groups.some((g) => g.regressed),
    sparkline: sumSparklines(groups.map((g) => g.sparkline)),
  }
}

const sortValue = (r: Row, sort: Sort): number => {
  const v = r.kind === 'group' ? r.group : r.combined
  return sort === 'count' ? v.count : time(sort === 'last_seen' ? v.last_seen : v.first_seen)
}

/**
 * One row per distinct template, in the list's sort order computed from the
 * combined values (largest or newest first; ties keep the API's order). A
 * template seen in one group only stays a plain group row. Children keep the
 * API's order.
 */
export function combineGroups(groups: readonly Group[], sort: Sort): Row[] {
  const byTitle = new Map<string, Group[]>()
  for (const g of groups) {
    const list = byTitle.get(g.title)
    if (list) list.push(g)
    else byTitle.set(g.title, [g])
  }
  const rows: Row[] = [...byTitle.entries()].map(([key, members]) =>
    members.length === 1 ? { kind: 'group', group: members[0] as Group } : { kind: 'parent', key, groups: members, combined: combine(members) },
  )
  const order = new Map(rows.map((r, i) => [r, i]))
  return rows.sort((a, b) => sortValue(b, sort) - sortValue(a, sort) || (order.get(a) ?? 0) - (order.get(b) ?? 0))
}
