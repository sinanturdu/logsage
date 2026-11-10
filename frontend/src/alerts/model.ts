// Alerts UI model (ADR 0025): the URL state, durations, the rule form's
// values and limits (mirroring alert.Validate in the backend, ADR 0024),
// and the plain-language sentences that describe a rule.

export const RULE_TYPES = ['error_rate', 'log_volume', 'new_group', 'regression', 'new_groups_per_hour'] as const
export type RuleType = (typeof RULE_TYPES)[number]
export const TYPE_LABEL: Record<RuleType, string> = {
  error_rate: 'Error rate',
  log_volume: 'Log volume',
  new_group: 'New error group',
  regression: 'Regression',
  new_groups_per_hour: 'New groups per hour',
}
export const TYPE_HINT: Record<RuleType, string> = {
  error_rate: 'The share of error and fatal logs among all logs.',
  log_volume: 'The number of logs, above or below a count. Below 1 means silence.',
  new_group: 'An error group seen for the first time. Fires once per group.',
  regression: 'A resolved error group that is seen again. Fires once per resolution.',
  new_groups_per_hour: 'More new error groups in the last hour than expected.',
}
export const isGroupType = (t: RuleType) => t === 'new_group' || t === 'regression'

/** The server's limits (alert.Validate, ADR 0024). Durations in seconds. */
export const LIMITS = {
  name: 100,
  note: 500,
  rules: 50,
  channels: 10,
  channelsPerRule: 10,
  channelName: 100,
  services: 20,
  interval: [30, 3600],
  window: [60, 86400],
  for: [0, 86400],
  clearFor: [0, 3600],
  repeat: [300, 7 * 86400],
  threshold: [0.1, 100],
  minLogs: [10, 1_000_000],
  count: [0, 1_000_000_000],
  minEvents: [1, 1_000_000],
  maxGroups: [1, 10_000],
} as const

export const LEVELS = ['debug', 'info', 'warn', 'error', 'fatal'] as const

// --- durations ---

const UNIT_SECONDS: Record<string, number> = { ns: 1e-9, us: 1e-6, µs: 1e-6, ms: 1e-3, s: 1, m: 60, h: 3600 }

/** Seconds of a Go duration string ("5m0s", "1h30m", "30s"); null when not one. */
export function parseGoDuration(v: string): number | null {
  const s = v.trim()
  if (s === '0' || s === '') return s === '0' ? 0 : null
  const re = /(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/gy
  let total = 0
  let m: RegExpExecArray | null
  let end = 0
  while ((m = re.exec(s)) !== null) {
    total += Number(m[1]) * (UNIT_SECONDS[m[2] as string] ?? 0)
    end = re.lastIndex
  }
  return end === s.length && end > 0 ? Math.round(total) : null
}

/** The API's form of a duration: whole seconds. */
export const goDuration = (seconds: number) => `${Math.round(seconds)}s`

const plural = (n: number, one: string) => `${n} ${one}${n === 1 ? '' : 's'}`

/** "30 seconds", "5 minutes", "1 hour 30 minutes", "2 days". */
export function humanDuration(seconds: number): string {
  if (seconds <= 0) return '0 seconds'
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = seconds % 60
  const parts: string[] = []
  if (d) parts.push(plural(d, 'day'))
  if (h) parts.push(plural(h, 'hour'))
  if (m) parts.push(plural(m, 'minute'))
  if (s) parts.push(plural(s, 'second'))
  return parts.slice(0, 2).join(' ')
}

export const DURATION_UNITS = [
  { id: 's', label: 'seconds', seconds: 1 },
  { id: 'm', label: 'minutes', seconds: 60 },
  { id: 'h', label: 'hours', seconds: 3600 },
  { id: 'd', label: 'days', seconds: 86400 },
] as const
export type DurationUnit = (typeof DURATION_UNITS)[number]['id']
/** A duration as the form edits it: the typed number and a unit. */
export type DurationValue = { amount: string; unit: DurationUnit }

/** The largest unit that divides the seconds evenly. */
export function toDurationValue(seconds: number): DurationValue {
  for (const u of [...DURATION_UNITS].reverse()) {
    if (seconds > 0 && seconds % u.seconds === 0) return { amount: String(seconds / u.seconds), unit: u.id }
  }
  return { amount: String(seconds), unit: 's' }
}

/** Seconds, or null when the amount is not a whole, non-negative number. */
export function durationSeconds(v: DurationValue): number | null {
  if (!/^\d+$/.test(v.amount.trim())) return null
  const unit = DURATION_UNITS.find((u) => u.id === v.unit)
  return unit ? Number(v.amount.trim()) * unit.seconds : null
}

// --- the rule form ---

export type RuleParams = {
  threshold_percent?: number
  min_logs?: number
  op?: 'above' | 'below'
  count?: number
  min_events?: number
  max_groups?: number
  services?: string[]
  levels?: string[]
  hysteresis_percent?: number
}

/** What the API takes for a rule (POST and PUT). */
export type RuleBody = {
  name: string
  note: string
  type: RuleType
  params: RuleParams
  interval: string
  window: string
  for: string
  clear_for: string
  repeat: string
  enabled: boolean
  channels: string[]
  open_incident: boolean
  incident_severity: IncidentSeverity
}

/** Incident severities (ADR 0026); a rule's default is high. */
export const INCIDENT_SEVERITIES = ['critical', 'high', 'medium', 'low'] as const
export type IncidentSeverity = (typeof INCIDENT_SEVERITIES)[number]

export type RuleForm = {
  name: string
  note: string
  type: RuleType
  threshold: string
  minLogs: string
  op: 'above' | 'below'
  count: string
  minEvents: string
  maxGroups: string
  services: string[]
  levels: string[]
  interval: DurationValue
  window: DurationValue
  for: DurationValue
  clearFor: DurationValue
  /** Repeat notifications while firing; off is "never". */
  repeatOn: boolean
  repeat: DurationValue
  enabled: boolean
  channels: string[]
  /** Kept from the API on edit (the form has no field for it; the default is 10%). */
  hysteresis?: number | undefined
  /** Opens or joins an incident when it fires (ADR 0026). */
  openIncident: boolean
  incidentSeverity: IncidentSeverity
}

export const emptyRuleForm = (type: RuleType = 'error_rate'): RuleForm => ({
  name: '',
  note: '',
  type,
  threshold: '5',
  minLogs: '50',
  op: 'above',
  count: '1000',
  minEvents: '1',
  maxGroups: '50',
  services: [],
  levels: [],
  interval: { amount: '1', unit: 'm' },
  window: { amount: '5', unit: 'm' },
  for: { amount: '0', unit: 'm' },
  clearFor: { amount: '1', unit: 'm' },
  repeatOn: false,
  openIncident: false,
  incidentSeverity: 'high',
  repeat: { amount: '1', unit: 'h' },
  enabled: true,
  channels: [],
})

/** A form filled from a rule read from the API. */
export function formFromRule(r: {
  name: string
  note: string
  type: RuleType
  params: RuleParams
  interval: string
  window: string
  for: string
  clear_for: string
  repeat: string
  enabled: boolean
  channels: string[]
  open_incident?: boolean
  incident_severity?: string
}): RuleForm {
  const base = emptyRuleForm(r.type)
  const d = (v: string, fallback: DurationValue) => {
    const s = parseGoDuration(v)
    return s === null ? fallback : s === 0 ? { amount: '0', unit: fallback.unit } : toDurationValue(s)
  }
  const repeat = parseGoDuration(r.repeat) ?? 0
  const p = r.params
  return {
    ...base,
    name: r.name,
    note: r.note,
    threshold: p.threshold_percent !== undefined ? String(p.threshold_percent) : base.threshold,
    minLogs: p.min_logs !== undefined ? String(p.min_logs) : base.minLogs,
    op: p.op ?? base.op,
    count: p.count !== undefined ? String(p.count) : r.type === 'log_volume' ? '0' : base.count,
    minEvents: p.min_events !== undefined ? String(p.min_events) : base.minEvents,
    maxGroups: p.max_groups !== undefined ? String(p.max_groups) : base.maxGroups,
    services: p.services ?? [],
    levels: p.levels ?? [],
    interval: d(r.interval, base.interval),
    window: d(r.window, base.window),
    for: d(r.for, base.for),
    clearFor: d(r.clear_for, base.clearFor),
    repeatOn: repeat > 0,
    repeat: repeat > 0 ? toDurationValue(repeat) : base.repeat,
    enabled: r.enabled,
    channels: [...r.channels],
    hysteresis: p.hysteresis_percent,
    openIncident: r.open_incident ?? false,
    incidentSeverity: (INCIDENT_SEVERITIES as readonly string[]).includes(r.incident_severity ?? '') ? (r.incident_severity as IncidentSeverity) : 'high',
  }
}

export type FieldName =
  | 'name'
  | 'note'
  | 'type'
  | 'threshold'
  | 'minLogs'
  | 'op'
  | 'count'
  | 'minEvents'
  | 'maxGroups'
  | 'services'
  | 'levels'
  | 'interval'
  | 'window'
  | 'for'
  | 'clearFor'
  | 'repeat'
  | 'channels'
export type FieldErrors = Partial<Record<FieldName, string>>

/** Which fields a type shows (the common ones are always there). */
export function typeFields(t: RuleType): FieldName[] {
  switch (t) {
    case 'error_rate':
      return ['threshold', 'minLogs', 'window', 'services']
    case 'log_volume':
      return ['op', 'count', 'window', 'services', 'levels']
    case 'new_group':
      return ['minEvents', 'window', 'services']
    case 'regression':
      return ['window', 'services']
    case 'new_groups_per_hour':
      return ['maxGroups']
  }
}

const num = (v: string): number | null => {
  const t = v.trim()
  if (!/^\d+(\.\d+)?$/.test(t)) return null
  return Number(t)
}
const whole = (v: string): number | null => (/^\d+$/.test(v.trim()) ? Number(v.trim()) : null)
const chars = (s: string) => Array.from(s).length
const fmt = (n: number) => n.toLocaleString('en-US')

/** The form's errors, with the same limits as the server. Empty when valid. */
export function validateRule(f: RuleForm): FieldErrors {
  const e: FieldErrors = {}
  const name = f.name.trim()
  if (name === '') e.name = 'Enter a name.'
  else if (chars(name) > LIMITS.name) e.name = `Use at most ${LIMITS.name} characters.`
  if (chars(f.note.trim()) > LIMITS.note) e.note = `Use at most ${LIMITS.note} characters.`
  const range = (field: FieldName, v: number | null, [lo, hi]: readonly [number, number], what: string) => {
    if (v === null || v < lo || v > hi) e[field] = `${what} between ${fmt(lo)} and ${fmt(hi)}.`
  }
  const dur = (field: FieldName, v: DurationValue, [lo, hi]: readonly [number, number]) => {
    const s = durationSeconds(v)
    if (s === null || s < lo || s > hi) e[field] = `Enter a duration between ${humanDuration(lo)} and ${humanDuration(hi)}.`
  }
  const fields = typeFields(f.type)
  if (fields.includes('threshold')) range('threshold', num(f.threshold), LIMITS.threshold, 'Enter a percentage')
  if (fields.includes('minLogs')) range('minLogs', whole(f.minLogs), LIMITS.minLogs, 'Enter a whole number')
  if (fields.includes('count')) {
    const c = whole(f.count)
    if (f.op === 'below' && c === 0) e.count = 'Enter at least 1 for "below" (below 1 means no logs at all).'
    else range('count', c, LIMITS.count, 'Enter a whole number')
  }
  if (fields.includes('minEvents')) range('minEvents', whole(f.minEvents), LIMITS.minEvents, 'Enter a whole number')
  if (fields.includes('maxGroups')) range('maxGroups', whole(f.maxGroups), LIMITS.maxGroups, 'Enter a whole number')
  if (fields.includes('services') && f.services.length > LIMITS.services) e.services = `Choose at most ${LIMITS.services} services.`
  dur('interval', f.interval, LIMITS.interval)
  if (fields.includes('window')) dur('window', f.window, LIMITS.window)
  dur('for', f.for, LIMITS.for)
  const cf = durationSeconds(f.clearFor)
  if (cf === null || cf > LIMITS.clearFor[1]) e.clearFor = `Enter a duration between 0 seconds and ${humanDuration(LIMITS.clearFor[1])}.`
  if (f.repeatOn) dur('repeat', f.repeat, LIMITS.repeat)
  if (f.channels.length === 0) e.channels = 'Choose at least one channel.'
  else if (f.channels.length > LIMITS.channelsPerRule) e.channels = `Choose at most ${LIMITS.channelsPerRule} channels.`
  return e
}

/** The API body for a valid form (only the chosen type's parameters). */
export function ruleBody(f: RuleForm): RuleBody {
  const params: RuleParams = {}
  const fields = typeFields(f.type)
  if (fields.includes('threshold')) params.threshold_percent = Number(f.threshold.trim())
  if (fields.includes('minLogs')) params.min_logs = Number(f.minLogs.trim())
  if (fields.includes('op')) params.op = f.op
  if (fields.includes('count')) params.count = Number(f.count.trim())
  if (fields.includes('minEvents')) params.min_events = Number(f.minEvents.trim())
  if (fields.includes('maxGroups')) params.max_groups = Number(f.maxGroups.trim())
  if (fields.includes('services') && f.services.length > 0) params.services = [...f.services]
  if (fields.includes('levels') && f.levels.length > 0) params.levels = [...f.levels]
  if (f.hysteresis !== undefined) params.hysteresis_percent = f.hysteresis
  const s = (v: DurationValue) => goDuration(durationSeconds(v) ?? 0)
  return {
    name: f.name.trim(),
    note: f.note.trim(),
    type: f.type,
    params,
    interval: s(f.interval),
    window: f.type === 'new_groups_per_hour' ? '1h' : s(f.window),
    for: s(f.for),
    clear_for: s(f.clearFor),
    repeat: f.repeatOn ? s(f.repeat) : '0s',
    enabled: f.enabled,
    channels: [...f.channels],
    open_incident: f.openIncident,
    incident_severity: f.incidentSeverity,
  }
}

/** The form field a server message is about ("window: must be ..."). */
export function fieldForServerMessage(msg: string): FieldName | null {
  const key = msg.split(':', 1)[0]?.trim() ?? ''
  const map: Record<string, FieldName> = {
    name: 'name',
    note: 'note',
    type: 'type',
    threshold_percent: 'threshold',
    min_logs: 'minLogs',
    op: 'op',
    count: 'count',
    min_events: 'minEvents',
    max_groups: 'maxGroups',
    services: 'services',
    levels: 'levels',
    interval: 'interval',
    window: 'window',
    for: 'for',
    clear_for: 'clearFor',
    repeat: 'repeat',
    channels: 'channels',
  }
  return map[key] ?? null
}

// --- sentences ---

const list = (items: readonly string[]) =>
  items.length <= 1 ? (items[0] ?? '') : `${items.slice(0, -1).join(', ')} and ${items[items.length - 1] ?? ''}`
const servicesPart = (services: readonly string[] | undefined) =>
  services && services.length > 0 ? ` in ${services.length === 1 ? 'service' : 'services'} ${list(services)}` : ''
const pct = (n: number) => `${Number.isInteger(n) ? n : Number(n.toFixed(2))}%`

/** The condition in a few words, for the rules table. */
export function conditionSummary(type: RuleType, p: RuleParams, windowSeconds: number): string {
  const over = `over ${humanDuration(windowSeconds)}`
  switch (type) {
    case 'error_rate':
      return `Errors ≥ ${pct(p.threshold_percent ?? 0)} of logs ${over}${servicesPart(p.services)}, at least ${fmt(p.min_logs ?? 50)} logs`
    case 'log_volume': {
      const lv = p.levels && p.levels.length > 0 ? `${list(p.levels)} logs` : 'Logs'
      if (p.op === 'below' && p.count === 1) return `No ${lv === 'Logs' ? 'logs' : lv} ${over}${servicesPart(p.services)}`
      return `${lv} ${p.op === 'below' ? '<' : '>'} ${fmt(p.count ?? 0)} ${over}${servicesPart(p.services)}`
    }
    case 'new_group':
      return `New error group with ≥ ${fmt(p.min_events ?? 1)} ${p.min_events === 1 || p.min_events === undefined ? 'event' : 'events'} ${over}${servicesPart(p.services)}`
    case 'regression':
      return `Resolved group seen again ${over}${servicesPart(p.services)}`
    case 'new_groups_per_hour':
      return `More than ${fmt(p.max_groups ?? 0)} new groups in an hour`
  }
}

/** The live preview: when it fires, how it resolves, who it notifies. */
export function previewSentences(f: RuleForm, channelNames: readonly string[]): { fires: string; resolves: string } {
  const window = durationSeconds(f.window) ?? 0
  const forS = durationSeconds(f.for) ?? 0
  const interval = durationSeconds(f.interval) ?? 0
  const clearRaw = durationSeconds(f.clearFor) ?? 0
  // The server uses the interval (at most 1 hour) when clear_for is 0.
  const clear = clearRaw > 0 ? clearRaw : Math.min(interval, LIMITS.clearFor[1])
  const over = `over ${window > 0 ? humanDuration(window) : '…'}`
  const svc = servicesPart(f.services)
  const holds = forS > 0 ? `, for at least ${humanDuration(forS)}` : ''
  const n = (v: string) => (v.trim() === '' ? '…' : fmt(Number(v.trim())))
  const levels = f.levels.length > 0 ? `${list(f.levels)} logs` : 'logs'
  let fires: string
  let resolves: string
  const clearText = clear > 0 ? `, for at least ${humanDuration(clear)}` : ''
  switch (f.type) {
    case 'error_rate': {
      const t = Number(f.threshold.trim())
      fires = `Fires when error and fatal logs are at least ${f.threshold.trim() || '…'}% of all logs${svc} ${over}, with at least ${n(f.minLogs)} logs${holds}.`
      const below = Number.isFinite(t) && f.threshold.trim() !== '' ? pct(Number((t * 0.9).toFixed(2))) : '…'
      resolves = `Resolves when the share falls below ${below} (10% under the threshold) or there are fewer than ${n(f.minLogs)} logs${clearText}.`
      break
    }
    case 'log_volume': {
      const c = Number(f.count.trim())
      if (f.op === 'below' && c === 1) {
        fires = `Fires when there are no ${levels}${svc} ${over}${holds}.`
        resolves = `Resolves when ${levels} arrive again${clearText}.`
      } else if (f.op === 'below') {
        fires = `Fires when there are fewer than ${n(f.count)} ${levels}${svc} ${over}${holds}.`
        resolves = `Resolves when there are at least ${Number.isFinite(c) ? fmt(c + Math.floor(c * 0.1)) : '…'} ${levels} (10% over the count)${clearText}.`
      } else {
        fires = `Fires when there are more than ${n(f.count)} ${levels}${svc} ${over}${holds}.`
        resolves = `Resolves when there are at most ${Number.isFinite(c) ? fmt(c - Math.floor(c * 0.1)) : '…'} ${levels} (10% under the count)${clearText}.`
      }
      break
    }
    case 'new_group':
      fires = `Fires once for each error group seen for the first time${svc} ${over}, with at least ${n(f.minEvents)} ${f.minEvents.trim() === '1' ? 'event' : 'events'}${holds}.`
      resolves = `Each group resolves when it no longer matches${clearText}; it never fires again.`
      break
    case 'regression':
      fires = `Fires once each time a resolved error group is seen again${svc} ${over}${holds}.`
      resolves = `Each group resolves when it has no events ${over}${clearText}.`
      break
    case 'new_groups_per_hour': {
      const m = Number(f.maxGroups.trim())
      fires = `Fires when more than ${n(f.maxGroups)} new error groups appear in one hour${holds}.`
      resolves = `Resolves when there are at most ${Number.isFinite(m) ? fmt(m - Math.floor(m * 0.1)) : '…'} new groups in an hour (10% under the limit)${clearText}.`
      break
    }
  }
  const notifies = channelNames.length > 0 ? ` Notifies: ${list(channelNames)}.` : ' Choose a channel to notify.'
  const repeat = durationSeconds(f.repeat)
  const again = f.repeatOn && repeat ? ` While firing, notifies again every ${humanDuration(repeat)}.` : ''
  const opens = f.openIncident ? ` Opens a ${f.incidentSeverity} severity incident when it fires.` : ''
  return { fires: fires + notifies + again + opens, resolves }
}

// --- URL state ---

export const TABS = ['rules', 'channels', 'activity'] as const
export type Tab = (typeof TABS)[number]
export const TAB_LABEL: Record<Tab, string> = { rules: 'Rules', channels: 'Channels', activity: 'Activity' }

export type AlertsState = {
  tab: Tab
  /** Activity: one rule's events only. */
  rule: string
  /** Opens a create dialog once (from the command palette). */
  create: '' | 'rule' | 'channel'
}

const RULE_ID = /^[A-Za-z0-9-]{1,64}$/

export function parseAlertsState(p: URLSearchParams): AlertsState {
  const tab = p.get('tab')
  const rule = p.get('rule') ?? ''
  const create = p.get('create')
  return {
    tab: (TABS as readonly string[]).includes(tab ?? '') ? (tab as Tab) : 'rules',
    rule: RULE_ID.test(rule) ? rule : '',
    create: create === 'rule' || create === 'channel' ? create : '',
  }
}

export function serializeAlertsState(s: AlertsState): URLSearchParams {
  const p = new URLSearchParams()
  if (s.tab !== 'rules') p.set('tab', s.tab)
  if (s.tab === 'activity' && s.rule) p.set('rule', s.rule)
  if (s.create) p.set('create', s.create)
  return p
}

export const alertsPath = (slug: string, s: Partial<AlertsState> = {}) => {
  const q = serializeAlertsState({ tab: 'rules', rule: '', create: '', ...s }).toString()
  return `/projects/${encodeURIComponent(slug)}/alerts${q ? `?${q}` : ''}`
}
export const rulePath = (slug: string, id: string) => `/projects/${encodeURIComponent(slug)}/alerts/rules/${encodeURIComponent(id)}`
