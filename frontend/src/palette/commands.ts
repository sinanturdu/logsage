// The command palette's commands as a pure function of the app's state
// (ADR 0020). A command carries an action as data; the palette runs it.

import type { Level, Preset } from '../explorer/model'

export type ExplorerContext = {
  live: boolean
  zoom: boolean
  /** An absolute selection exists that is not zoomed yet. */
  canZoom: boolean
  /** Services listed in the facets (not the "other" row). */
  services: readonly string[]
  selectedServices: readonly string[]
  levels: readonly Level[]
  hasFilters: boolean
}

export type AppContext = {
  slug: string | null
  section: 'logs' | 'keys' | 'other'
  projects: readonly string[]
  explorer: ExplorerContext | null
}

export type Action =
  | { type: 'navigate'; to: string }
  | { type: 'range'; preset: Preset }
  | { type: 'custom-range' }
  | { type: 'levels'; levels: Level[] }
  | { type: 'clear-filters' }
  | { type: 'service'; name: string }
  | { type: 'focus'; field: 'search' | 'trace' }
  | { type: 'live'; on: boolean }
  | { type: 'refresh' }
  | { type: 'zoom'; on: boolean }
  | { type: 'copy-link' }
  | { type: 'account'; what: 'password' | 'all-devices' | 'sign-out' }
  | { type: 'help' }

export type Group = 'Navigation' | 'Time range' | 'Filters' | 'View' | 'Account' | 'Help'
export type Command = { id: string; label: string; keywords: readonly string[]; group: Group; action: Action }

const RANGES: { preset: Preset; label: string; keywords: string[] }[] = [
  { preset: '15m', label: 'Last 15 minutes', keywords: ['15m', 'quarter hour'] },
  { preset: '1h', label: 'Last 1 hour', keywords: ['1h', 'hour'] },
  { preset: '6h', label: 'Last 6 hours', keywords: ['6h'] },
  { preset: '24h', label: 'Last 24 hours', keywords: ['24h', 'day', 'today'] },
  { preset: '7d', label: 'Last 7 days', keywords: ['7d', 'week'] },
]

const ERRORS: Level[] = ['error', 'fatal']
const path = (slug: string, section: 'logs' | 'keys') => `/projects/${encodeURIComponent(slug)}/${section}`

/** The commands that apply now, in a stable order. */
export function commandsFor(ctx: AppContext): Command[] {
  const out: Command[] = []
  const add = (group: Group, id: string, label: string, action: Action, keywords: string[] = []) => out.push({ id, label, keywords, group, action })

  if (ctx.slug) {
    if (ctx.section !== 'logs') add('Navigation', 'go-logs', 'Go to Logs', { type: 'navigate', to: path(ctx.slug, 'logs') }, ['explorer', 'search', 'home'])
    if (ctx.section !== 'keys') add('Navigation', 'go-keys', 'Go to API keys', { type: 'navigate', to: path(ctx.slug, 'keys') }, ['tokens', 'ingest', 'credentials'])
  }
  const section = ctx.section === 'keys' ? 'keys' : 'logs'
  for (const p of ctx.projects) {
    if (p === ctx.slug) continue
    add('Navigation', `project:${p}`, `Switch project: ${p}`, { type: 'navigate', to: path(p, section) }, ['project', 'change'])
  }

  const ex = ctx.explorer
  if (ex) {
    for (const r of RANGES) add('Time range', `range:${r.preset}`, r.label, { type: 'range', preset: r.preset }, ['time', 'range', ...r.keywords])
    add('Time range', 'range:custom', 'Custom range...', { type: 'custom-range' }, ['time', 'from', 'to', 'utc'])

    const errorsOnly = ex.levels.length === 2 && ERRORS.every((l) => ex.levels.includes(l))
    if (!errorsOnly) add('Filters', 'levels:errors', 'Show errors and fatal only', { type: 'levels', levels: ERRORS }, ['level', 'error', 'fatal', 'problems'])
    if (ex.levels.length > 0) add('Filters', 'levels:all', 'Show all levels', { type: 'levels', levels: [] }, ['level', 'debug', 'info', 'warn'])
    if (ex.hasFilters) add('Filters', 'clear', 'Clear filters', { type: 'clear-filters' }, ['reset', 'remove'])
    for (const s of ex.services) {
      if (ex.selectedServices.includes(s)) continue
      add('Filters', `service:${s}`, `Filter by service: ${s}`, { type: 'service', name: s }, ['service'])
    }
    add('Filters', 'focus:search', 'Focus search', { type: 'focus', field: 'search' }, ['find', 'query', 'message'])
    add('Filters', 'focus:trace', 'Focus trace id', { type: 'focus', field: 'trace' }, ['trace_id', 'trace'])

    add('View', 'live', ex.live ? 'Turn live tail off' : 'Turn live tail on', { type: 'live', on: !ex.live }, ['live', 'tail', 'stream', 'follow'])
    add('View', 'refresh', 'Refresh', { type: 'refresh' }, ['reload', 'now'])
    if (ex.canZoom) add('View', 'zoom:in', 'Zoom to selection', { type: 'zoom', on: true }, ['timeline', 'chart'])
    if (ex.zoom) add('View', 'zoom:out', 'Zoom out', { type: 'zoom', on: false }, ['timeline', 'chart'])
  }
  add('View', 'copy-link', 'Copy link to this view', { type: 'copy-link' }, ['share', 'url', 'clipboard'])

  add('Account', 'password', 'Change password...', { type: 'account', what: 'password' }, ['security'])
  add('Account', 'all-devices', 'Sign out of all devices...', { type: 'account', what: 'all-devices' }, ['logout', 'sessions'])
  add('Account', 'sign-out', 'Sign out', { type: 'account', what: 'sign-out' }, ['logout', 'log out'])
  add('Help', 'help', 'Show keyboard shortcuts', { type: 'help' }, ['keys', 'help', 'hotkeys', '?'])
  return out
}

/** With an empty query: recent commands that still apply, then navigation, at most 8. */
export function defaultCommands(all: readonly Command[], recent: readonly string[]): Command[] {
  const byId = new Map(all.map((c) => [c.id, c]))
  const out: Command[] = []
  const push = (c: Command | undefined) => {
    if (c && !out.includes(c) && out.length < 8) out.push(c)
  }
  for (const id of recent) push(byId.get(id))
  for (const c of all) if (c.group === 'Navigation') push(c)
  return out
}
