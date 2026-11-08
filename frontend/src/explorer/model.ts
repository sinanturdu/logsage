// The explorer's state model (ADR 0017). Everything a user chooses lives in
// the URL search params; these functions parse them defensively (invalid
// values are dropped, never thrown) and serialize them minimally.

export const LEVELS = ['debug', 'info', 'warn', 'error', 'fatal'] as const
export type Level = (typeof LEVELS)[number]

export const LEVEL_SHAPE: Record<Level, string> = { debug: '○', info: '●', warn: '▲', error: '■', fatal: '◆' }

export const PRESETS = ['15m', '1h', '6h', '24h', '7d'] as const
export type Preset = (typeof PRESETS)[number]
export const DEFAULT_PRESET: Preset = '15m'

const PRESET_MS: Record<Preset, number> = {
  '15m': 15 * 60_000,
  '1h': 3_600_000,
  '6h': 6 * 3_600_000,
  '24h': 24 * 3_600_000,
  '7d': 7 * 24 * 3_600_000,
}
const PRESET_LABEL: Record<Preset, string> = {
  '15m': 'last 15 min',
  '1h': 'last hour',
  '6h': 'last 6 hours',
  '24h': 'last 24 hours',
  '7d': 'last 7 days',
}

/** The server's largest window (QUERY_MAX_WINDOW, 168h). */
export const MAX_WINDOW_MS = 7 * 24 * 3_600_000
export const MAX_TEXT = 256
export const MAX_SERVICES = 20
const TRACE_ID = /^[0-9a-f]{32}$/
const FINGERPRINT = /^[0-9a-f]{16}$/

export type TimeRange = { kind: 'relative'; preset: Preset } | { kind: 'absolute'; from: number; to: number }

export type ExplorerState = {
  range: TimeRange
  levels: Level[] // empty: every level
  services: string[] // empty: every service
  q: string
  traceId: string
  /** One error group (ADR 0022, 0023): 16 hex, or empty. */
  fingerprint: string
  /** Zoomed in: the timeline shows only the absolute selection (ADR 0018). */
  zoom: boolean
  /** Live tail on (ADR 0019); only with a relative range. */
  live: boolean
}

export const EMPTY_FILTERS = { levels: [] as Level[], services: [] as string[], q: '', traceId: '', fingerprint: '' }

export function isPreset(v: string | null): v is Preset {
  return v !== null && (PRESETS as readonly string[]).includes(v)
}
export function presetMs(p: Preset): number {
  return PRESET_MS[p]
}
export function presetLabel(p: Preset): string {
  return PRESET_LABEL[p]
}

export function validTraceId(v: string): boolean {
  return TRACE_ID.test(v)
}

/**
 * A group fingerprint from a URL: 16 hexadecimal characters (upper case is
 * accepted and lowered). All zeros means "not grouped", so it is no group.
 */
export function parseFingerprint(v: string | null | undefined): string | null {
  const s = (v ?? '').toLowerCase()
  return FINGERPRINT.test(s) && s !== '0000000000000000' ? s : null
}

/** Parses an RFC3339 instant; null for anything else. */
function parseInstant(v: string | null): number | null {
  if (!v || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d{1,9})?)?(Z|[+-]\d{2}:\d{2})$/.test(v)) return null
  const t = Date.parse(v)
  return Number.isFinite(t) ? t : null
}

function list(v: string | null): string[] {
  if (!v) return []
  const out: string[] = []
  for (const part of v.split(',')) {
    if (part && !out.includes(part)) out.push(part)
  }
  return out
}

export function parseState(params: URLSearchParams): ExplorerState {
  let range: TimeRange = { kind: 'relative', preset: DEFAULT_PRESET }
  const from = parseInstant(params.get('from'))
  const to = parseInstant(params.get('to'))
  const preset = params.get('range')
  // While live the list follows now: from/to are ignored.
  const live = params.get('live') === '1'
  if (!live && from !== null && to !== null && from < to && to - from <= MAX_WINDOW_MS) {
    range = { kind: 'absolute', from, to }
  } else if (isPreset(preset)) {
    range = { kind: 'relative', preset }
  }
  const levels = list(params.get('level')).filter((l): l is Level => (LEVELS as readonly string[]).includes(l))
  const services = list(params.get('service'))
    .filter((s) => s.length <= MAX_TEXT)
    .slice(0, MAX_SERVICES)
  const qRaw = params.get('q') ?? ''
  const q = qRaw.length <= MAX_TEXT ? qRaw : ''
  const tRaw = (params.get('trace_id') ?? '').toLowerCase()
  return {
    range,
    levels: levels.length === LEVELS.length ? [] : sortLevels(levels),
    services,
    q,
    traceId: validTraceId(tRaw) ? tRaw : '',
    fingerprint: parseFingerprint(params.get('fingerprint')) ?? '',
    zoom: range.kind === 'absolute' && params.get('zoom') === '1',
    live,
  }
}

export function sortLevels(levels: readonly Level[]): Level[] {
  return LEVELS.filter((l) => levels.includes(l))
}

/** RFC3339 UTC with milliseconds only when needed. */
export function isoUtc(ms: number): string {
  const s = new Date(ms).toISOString()
  return s.endsWith('.000Z') ? s.slice(0, -5) + 'Z' : s
}

/** The minimal search params for a state: defaults are left out. */
export function serializeState(s: ExplorerState): URLSearchParams {
  const p = new URLSearchParams()
  if (s.range.kind === 'absolute') {
    p.set('from', isoUtc(s.range.from))
    p.set('to', isoUtc(s.range.to))
  } else if (s.range.preset !== DEFAULT_PRESET) {
    p.set('range', s.range.preset)
  }
  if (s.levels.length > 0 && s.levels.length < LEVELS.length) p.set('level', sortLevels(s.levels).join(','))
  if (s.services.length > 0) p.set('service', s.services.join(','))
  if (s.q) p.set('q', s.q)
  if (s.traceId) p.set('trace_id', s.traceId)
  if (s.fingerprint) p.set('fingerprint', s.fingerprint)
  if (s.zoom && s.range.kind === 'absolute') p.set('zoom', '1')
  if (s.live && s.range.kind === 'relative') p.set('live', '1')
  return p
}

/** The concrete window of a range; relative ranges end at now. */
export function resolveRange(r: TimeRange, now: number): { from: number; to: number } {
  if (r.kind === 'absolute') return { from: r.from, to: r.to }
  return { from: now - PRESET_MS[r.preset], to: now }
}

/**
 * The context window the timeline shows: a relative range is its own
 * context; an absolute selection is shown inside the smallest preset that
 * ends now and contains it ("selected within the last 15 min"), or on its
 * own when it is older than every preset.
 */
export function contextWindow(r: TimeRange, now: number, zoom = false): { from: number; to: number; preset: Preset | null } {
  if (r.kind === 'relative') return { ...resolveRange(r, now), preset: r.preset }
  if (zoom) return { from: r.from, to: r.to, preset: null }
  for (const p of PRESETS) {
    if (r.from >= now - PRESET_MS[p] && r.to <= now + 60_000) return { from: now - PRESET_MS[p], to: now, preset: p }
  }
  return { from: r.from, to: r.to, preset: null }
}

export function hasFilters(s: ExplorerState): boolean {
  return s.levels.length > 0 || s.services.length > 0 || s.q !== '' || s.traceId !== '' || s.fingerprint !== ''
}

/**
 * Level checkboxes: no level filter means every level is checked. Unchecking
 * one from that state keeps the other four; checking all again clears the
 * filter. Unchecking the last checked level is refused (no levels would
 * show nothing at all).
 */
export function toggleLevel(current: readonly Level[], level: Level): Level[] {
  const checked = current.length === 0 ? [...LEVELS] : [...current]
  const next = checked.includes(level) ? checked.filter((l) => l !== level) : [...checked, level]
  if (next.length === 0) return sortLevels(checked)
  return next.length === LEVELS.length ? [] : sortLevels(next)
}

/**
 * Service checkboxes select services: none checked means every service.
 * At most MAX_SERVICES can be selected (the server's limit).
 */
export function toggleService(current: readonly string[], service: string): string[] {
  if (current.includes(service)) return current.filter((s) => s !== service)
  if (current.length >= MAX_SERVICES) return [...current]
  return [...current, service]
}
