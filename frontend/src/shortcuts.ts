// The one definition of every keyboard shortcut (ADR 0020). The handlers
// match keys through `matches`, and the help dialog lists this array, so
// the two cannot drift apart.

export type ShortcutId =
  | 'palette'
  | 'help'
  | 'search'
  | 'next'
  | 'previous'
  | 'toggle'
  | 'escape'
  | 'trace'
  | 'live'
  | 'retry'
  | 'groups-search'
  | 'groups-next'
  | 'groups-previous'
  | 'groups-open'
  | 'alerts-next'
  | 'alerts-previous'
  | 'alerts-open'
  | 'incidents-search'
  | 'incidents-next'
  | 'incidents-previous'
  | 'incidents-open'

type Keyish = Pick<KeyboardEvent, 'key' | 'ctrlKey' | 'metaKey' | 'altKey'>

export type Shortcut = {
  id: ShortcutId
  /** What the help shows, one entry per key combination. */
  keys: readonly string[]
  description: string
  where: 'Anywhere' | 'Log explorer' | 'Live tail' | 'Error groups' | 'Alerts' | 'Incidents'
  /** Also active while typing in a text field. */
  whileTyping: boolean
  matches: (e: Keyish) => boolean
}

const plain = (e: Keyish) => !e.ctrlKey && !e.metaKey && !e.altKey

export const SHORTCUTS: readonly Shortcut[] = [
  {
    id: 'palette',
    keys: ['Ctrl K', '⌘ K'],
    description: 'Open the command palette',
    where: 'Anywhere',
    whileTyping: true,
    matches: (e) => (e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === 'k',
  },
  { id: 'help', keys: ['?'], description: 'Show keyboard shortcuts', where: 'Anywhere', whileTyping: false, matches: (e) => plain(e) && e.key === '?' },
  { id: 'search', keys: ['/'], description: 'Focus the search box', where: 'Log explorer', whileTyping: false, matches: (e) => plain(e) && e.key === '/' },
  { id: 'next', keys: ['j'], description: 'Select the next log', where: 'Log explorer', whileTyping: false, matches: (e) => plain(e) && e.key === 'j' },
  { id: 'previous', keys: ['k'], description: 'Select the previous log', where: 'Log explorer', whileTyping: false, matches: (e) => plain(e) && e.key === 'k' },
  {
    id: 'toggle',
    keys: ['Enter', 'Space'],
    description: 'Expand or collapse the selected log',
    where: 'Log explorer',
    whileTyping: false,
    matches: (e) => plain(e) && (e.key === 'Enter' || e.key === ' '),
  },
  {
    id: 'escape',
    keys: ['Escape'],
    description: 'Close the open detail, then clear the selection; also closes menus and dialogs',
    where: 'Log explorer',
    whileTyping: false,
    matches: (e) => e.key === 'Escape',
  },
  { id: 'trace', keys: ['t'], description: 'Filter by the selected log’s trace', where: 'Log explorer', whileTyping: false, matches: (e) => plain(e) && e.key === 't' },
  { id: 'live', keys: ['l'], description: 'Turn the live tail on or off', where: 'Live tail', whileTyping: false, matches: (e) => plain(e) && e.key === 'l' },
  {
    id: 'retry',
    keys: ['r'],
    description: 'Reconnect now while the live tail waits to retry',
    where: 'Live tail',
    whileTyping: false,
    matches: (e) => plain(e) && e.key === 'r',
  },
  { id: 'groups-search', keys: ['/'], description: 'Focus the search box', where: 'Error groups', whileTyping: false, matches: (e) => plain(e) && e.key === '/' },
  { id: 'groups-next', keys: ['j'], description: 'Select the next group', where: 'Error groups', whileTyping: false, matches: (e) => plain(e) && e.key === 'j' },
  { id: 'groups-previous', keys: ['k'], description: 'Select the previous group', where: 'Error groups', whileTyping: false, matches: (e) => plain(e) && e.key === 'k' },
  { id: 'groups-open', keys: ['Enter'], description: 'Open the selected group, or expand a combined row', where: 'Error groups', whileTyping: false, matches: (e) => plain(e) && e.key === 'Enter' },
  { id: 'alerts-next', keys: ['j'], description: 'Select the next row', where: 'Alerts', whileTyping: false, matches: (e) => plain(e) && e.key === 'j' },
  { id: 'alerts-previous', keys: ['k'], description: 'Select the previous row', where: 'Alerts', whileTyping: false, matches: (e) => plain(e) && e.key === 'k' },
  { id: 'alerts-open', keys: ['Enter'], description: 'Open the selected rule', where: 'Alerts', whileTyping: false, matches: (e) => plain(e) && e.key === 'Enter' },
  { id: 'incidents-search', keys: ['/'], description: 'Focus the title search', where: 'Incidents', whileTyping: false, matches: (e) => plain(e) && e.key === '/' },
  { id: 'incidents-next', keys: ['j'], description: 'Select the next incident', where: 'Incidents', whileTyping: false, matches: (e) => plain(e) && e.key === 'j' },
  { id: 'incidents-previous', keys: ['k'], description: 'Select the previous incident', where: 'Incidents', whileTyping: false, matches: (e) => plain(e) && e.key === 'k' },
  { id: 'incidents-open', keys: ['Enter'], description: 'Open the selected incident', where: 'Incidents', whileTyping: false, matches: (e) => plain(e) && e.key === 'Enter' },
]

/** Whether the event comes from a place where typed characters are text. */
export const isTyping = (t: EventTarget | null) =>
  t instanceof HTMLElement && (t.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(t.tagName))

/**
 * The shortcut an event triggers, among those listed, or null. Shortcuts
 * that are not active while typing are skipped when the target is a field.
 */
export function shortcutFor(e: Keyish & { target: EventTarget | null }, among: readonly ShortcutId[]): ShortcutId | null {
  for (const s of SHORTCUTS) {
    if (!among.includes(s.id)) continue
    if (!s.whileTyping && isTyping(e.target)) continue
    if (s.matches(e)) return s.id
  }
  return null
}
