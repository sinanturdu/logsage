// Incidents UI model (ADR 0027): the list's URL state (parsed defensively,
// defaults left out), the status and severity marks, and the timeline
// sentences, built only from the closed event payload fields (ADR 0026).

import type { LinkedAlert, Member, Severity, Status, TimelineEvent } from './api'
import { SEVERITIES } from './api'
import { isoUtc } from '../explorer/model'

export const STATUS_FILTERS = ['active', 'open', 'acknowledged', 'resolved', 'all'] as const
export type StatusFilter = (typeof STATUS_FILTERS)[number]
export const STATUS_FILTER_LABEL: Record<StatusFilter, string> = {
  active: 'Open and acknowledged',
  open: 'Open',
  acknowledged: 'Acknowledged',
  resolved: 'Resolved',
  all: 'All',
}
export const ASSIGNEES = ['any', 'me', 'unassigned'] as const
export type AssigneeFilter = (typeof ASSIGNEES)[number]
export const RANGES = ['all', '24h', '7d', '30d'] as const
export type RangeFilter = (typeof RANGES)[number]
export const RANGE_LABEL: Record<RangeFilter, string> = { all: 'Any time', '24h': 'Opened in the last 24 hours', '7d': 'Last 7 days', '30d': 'Last 30 days' }

export type Filters = {
  status: StatusFilter
  severity: Severity[]
  assignee: AssigneeFilter
  service: string
  q: string
  range: RangeFilter
}
export const DEFAULT_FILTERS: Filters = { status: 'active', severity: [], assignee: 'any', service: '', q: '', range: 'all' }

const oneOf = <T extends string>(all: readonly T[], v: string | null, fallback: T): T => (v !== null && (all as readonly string[]).includes(v) ? (v as T) : fallback)
const MAX_TEXT = 200

export function parseFilters(p: URLSearchParams): Filters {
  const sev = (p.get('severity') ?? '').split(',').filter((s): s is Severity => (SEVERITIES as readonly string[]).includes(s))
  const service = p.get('service') ?? ''
  const q = p.get('q') ?? ''
  return {
    status: oneOf(STATUS_FILTERS, p.get('status'), 'active'),
    severity: SEVERITIES.filter((s) => sev.includes(s)),
    assignee: oneOf(ASSIGNEES, p.get('assignee'), 'any'),
    service: service.length <= 256 ? service : '',
    q: Array.from(q).length <= MAX_TEXT ? q : '',
    range: oneOf(RANGES, p.get('range'), 'all'),
  }
}

export function serializeFilters(f: Filters): URLSearchParams {
  const p = new URLSearchParams()
  if (f.status !== 'active') p.set('status', f.status)
  if (f.severity.length > 0) p.set('severity', SEVERITIES.filter((s) => f.severity.includes(s)).join(','))
  if (f.assignee !== 'any') p.set('assignee', f.assignee)
  if (f.service) p.set('service', f.service)
  if (f.q) p.set('q', f.q)
  if (f.range !== 'all') p.set('range', f.range)
  return p
}

export const incidentsPath = (slug: string, f?: Partial<Filters>) => {
  const q = serializeFilters({ ...DEFAULT_FILTERS, ...f }).toString()
  return `/projects/${encodeURIComponent(slug)}/incidents${q ? `?${q}` : ''}`
}
export const incidentPath = (slug: string, n: number) => `/projects/${encodeURIComponent(slug)}/incidents/${n}`

/** A route number: a positive integer, nothing else. */
export function parseNumber(v: string | undefined): number | null {
  if (!v || !/^[1-9][0-9]{0,8}$/.test(v)) return null
  return Number(v)
}

// --- marks (text plus shape; ADR 0027 lists why these shapes) ---

/** Differ from the alert states (○ ◐ ◉ ✓ ⊘), the levels (○ ● ▲ ■ ◆) and the group statuses (□ – ✓). */
export const STATUS_SHAPE: Record<Status, string> = { open: '▶', acknowledged: '◧', resolved: '◇' }
export const SEVERITY_BARS: Record<Severity, string> = { critical: '▰▰▰▰', high: '▰▰▰▱', medium: '▰▰▱▱', low: '▰▱▱▱' }

// --- timeline sentences ---

const str = (p: Record<string, unknown>, k: string) => (typeof p[k] === 'string' ? (p[k] as string) : '')
const num = (p: Record<string, unknown>, k: string) => (typeof p[k] === 'number' ? (p[k] as number) : 0)
const STATUS_WORD: Record<string, string> = { open: 'open', acknowledged: 'acknowledged', resolved: 'resolved' }
const CLOSE_REASON: Record<string, string> = {
  rule_disabled: 'the rule was disabled',
  rule_deleted: 'the rule was deleted',
  rule_changed: 'the rule was changed and its states were reset',
}

const pad = (n: number) => String(n).padStart(2, '0')
/** "14:05 UTC". */
export const clockUtc = (iso: string) => {
  const d = new Date(iso)
  return Number.isFinite(d.getTime()) ? `${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())} UTC` : ''
}

export type Ctx = { alerts: readonly LinkedAlert[]; members: readonly Member[]; templates?: ReadonlyMap<string, string> }

/** Who did it: the actor's email, "Someone" when unknown, "LogSage" for the system. */
export function actorName(e: Pick<TimelineEvent, 'actor' | 'actor_email'>): string {
  if (!e.actor) return 'LogSage'
  return e.actor_email || 'Someone'
}

const groupName = (p: Record<string, unknown>, ctx: Ctx) => {
  const fp = str(p, 'fingerprint')
  return str(p, 'template') || ctx.templates?.get(fp) || (fp ? `group ${fp.slice(0, 8)}` : 'an error group')
}

/** Repeated (flapping) events: "fired 5 times, most recently at 14:05 UTC". */
function times(verb: string, e: TimelineEvent): string {
  return e.count > 1 ? `${verb} ${e.count} times, most recently at ${clockUtc(e.last_at)}` : verb
}

/**
 * One plain sentence per event, from the type's closed payload fields only.
 * Comments return their own marker sentence; the body is shown apart, as
 * text. An unknown type gives a neutral line, never the payload.
 */
export function eventSentence(e: TimelineEvent, ctx: Ctx): string {
  const p = e.payload ?? {}
  const who = actorName(e)
  const rule = str(p, 'rule_name') ? `Rule “${str(p, 'rule_name')}”` : 'A rule'
  const forGroup = str(p, 'template') ? ` for the error group “${str(p, 'template')}”` : ''
  switch (e.type) {
    case 'opened': {
      const rec = num(p, 'recurrence_of') ? ` It is a recurrence of #${num(p, 'recurrence_of')}.` : ''
      return str(p, 'source') === 'auto' ? `${rule} opened this incident.${rec}` : `${who} opened this incident.${rec}`
    }
    case 'alert_firing':
      return `${rule} ${times('fired', e)}${forGroup}.`
    case 'alert_resolved':
      return `${rule} ${times('resolved', e)}${forGroup}.`
    case 'alert_closed':
      return `${rule} is no longer linked: ${CLOSE_REASON[str(p, 'reason')] ?? 'the rule changed'}.`
    case 'all_alerts_resolved':
      return 'All linked alerts are resolved.'
    case 'group_linked':
      return str(p, 'source') === 'auto' ? `The error group “${groupName(p, ctx)}” was linked automatically.` : `${who} linked the error group “${groupName(p, ctx)}”.`
    case 'group_unlinked':
      return `${who} unlinked the error group “${groupName(p, ctx)}”.`
    case 'status_changed': {
      const to = str(p, 'to')
      if (to === 'acknowledged') return `${who} acknowledged the incident.`
      if (to === 'resolved') return `${who} resolved the incident.`
      return `${who} changed the status from ${STATUS_WORD[str(p, 'from')] ?? 'unknown'} to ${STATUS_WORD[to] ?? 'unknown'}.`
    }
    case 'severity_changed':
      return `${who} changed the severity from ${str(p, 'from') || 'unknown'} to ${str(p, 'to') || 'unknown'}.`
    case 'assigned': {
      const uid = str(p, 'user_id')
      if (!uid) return `${who} removed the assignee.`
      const m = ctx.members.find((x) => x.user_id === uid)
      return `${who} assigned the incident to ${m ? m.email : 'a project member'}.`
    }
    case 'comment':
      return e.body === null ? `${who} wrote a comment that was deleted.` : `${who} commented:`
    case 'comment_deleted':
      return `${who} deleted a comment.`
    case 'reopened': {
      const rid = str(p, 'rule_id')
      if (!rid) return `${who} reopened the incident.`
      const a = ctx.alerts.find((x) => x.rule_id === rid)
      return a ? `Reopened because rule “${a.rule_name}” fired again.` : 'Reopened because a linked rule fired again.'
    }
    default:
      return 'Something changed on this incident.'
  }
}

/** The explorer over the incident's window (and its one group, when exactly one is linked). */
export function logsLink(slug: string, openedAt: string, endAt: string, fingerprints: readonly string[]): string {
  const lead = 15 * 60_000
  const maxWindow = 7 * 86_400_000
  const end = Date.parse(endAt)
  const from = Math.max(Date.parse(openedAt) - lead, end - maxWindow + 60_000)
  const p = new URLSearchParams({ from: isoUtc(from), to: isoUtc(end + 60_000) })
  if (fingerprints.length === 1 && fingerprints[0]) p.set('fingerprint', fingerprints[0])
  return `/projects/${encodeURIComponent(slug)}/logs?${p.toString()}`
}
