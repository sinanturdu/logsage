import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, NavLink, Outlet, useLocation, useMatch, useNavigate } from 'react-router'
import { useProjects } from '../api/projects'
import { rulesKey, useFiringCount } from '../alerts/api'
import { summaryKey, useSummary } from '../incidents/api'
import { useSharedPoll } from '../poll'
import { copyText } from '../explorer/clipboard'
import { BridgeContext, type ExplorerBridge } from '../palette/bridge'
import { CommandPalette } from '../palette/CommandPalette'
import { commandsFor, defaultCommands, type Command, type ExplorerContext, type GroupsContext } from '../palette/commands'
import { shortcutFor } from '../shortcuts'
import { AccountDialogs, AccountMenu, useSignOut, type AccountDialog } from './AccountMenu'
import { ProjectSwitcher } from './ProjectSwitcher'
import { ShortcutsHelp } from './ShortcutsHelp'

export const LINK_COPIED = 'Copied the link to this view.'
export const linkFallback = (url: string) => `Copy is not available here. Select the link and copy it with Ctrl+C: ${url}`

/** "Alerts", or "Alerts, 2 firing" (enabled rules with a firing key). */
function AlertsLink({ slug, base, member }: { slug: string; base: string; member: boolean }) {
  const firing = useFiringCount(slug, member)
  const n = firing.data ?? 0
  return (
    <NavLink to={`${base}/alerts`} className={n > 0 ? 'nav-firing' : ''}>
      {n > 0 ? `Alerts, ${n} firing` : 'Alerts'}
    </NavLink>
  )
}

/** "Incidents", or "Incidents, 2 open" from the summary endpoint. */
function IncidentsLink({ slug, base, member }: { slug: string; base: string; member: boolean }) {
  const summary = useSummary(slug, member)
  const n = summary.data?.open ?? 0
  return (
    <NavLink to={`${base}/incidents`} className={n > 0 ? 'nav-open' : ''}>
      {n > 0 ? `Incidents, ${n} open` : 'Incidents'}
    </NavLink>
  )
}

/**
 * Logs, error groups, alerts, incidents and API keys of the current project.
 * The two counts share one poll (every 30 s while the tab is visible).
 */
function ProjectNav() {
  const m = useMatch('/projects/:slug/*')
  const slug = m?.params.slug ?? ''
  const projects = useProjects()
  const member = slug !== '' && (projects.data?.some((p) => p.slug === slug) ?? false)
  useSharedPoll([rulesKey(slug), summaryKey(slug)], member)
  if (!slug) return null
  const base = `/projects/${encodeURIComponent(slug)}`
  return (
    <nav className="project-nav" aria-label="Project sections">
      <NavLink to={`${base}/logs`}>Logs</NavLink>
      <NavLink to={`${base}/groups`}>Error groups</NavLink>
      <AlertsLink slug={slug} base={base} member={member} />
      <IncidentsLink slug={slug} base={base} member={member} />
      <NavLink to={`${base}/keys`}>API keys</NavLink>
    </nav>
  )
}

export function Shell() {
  const [help, setHelp] = useState(false)
  const [palette, setPalette] = useState(false)
  // The explorer's state when the palette opened (read from the bridge in an event, not during render).
  const [explorerCtx, setExplorerCtx] = useState<ExplorerContext | null>(null)
  const [groupsCtx, setGroupsCtx] = useState<GroupsContext | null>(null)
  const [account, setAccount] = useState<AccountDialog>(null)
  const [notice, setNotice] = useState('')
  // Recent commands, in memory only (never in storage).
  const [recent, setRecent] = useState<string[]>([])
  const bridge = useRef<ExplorerBridge | null>(null)
  const register = useCallback((b: ExplorerBridge | null) => {
    bridge.current = b
  }, [])
  // The copy notice goes away after a while.
  useEffect(() => {
    if (!notice) return
    const id = setTimeout(() => setNotice(''), 6000)
    return () => clearTimeout(id)
  }, [notice])
  const openPalette = useCallback(() => {
    setExplorerCtx(bridge.current?.ctx ?? null)
    setGroupsCtx(bridge.current?.groups ?? null)
    setPalette(true) // one boolean: opening again never stacks a second palette
  }, [])
  const closePalette = useCallback(() => setPalette(false), [])
  const paletteOpen = useRef(false)
  useEffect(() => {
    paletteOpen.current = palette
  }, [palette])
  const navigate = useNavigate()
  const { pathname } = useLocation()
  const projects = useProjects()
  const { signOut } = useSignOut()
  const m = /^\/projects\/([^/]+)\/(logs|keys|groups|alerts)/.exec(pathname)
  // Any route change closes the palette.
  const [path, setPath] = useState(pathname)
  if (path !== pathname) {
    setPath(pathname)
    if (palette) setPalette(false)
  }

  // Capture phase on the document, so the keys work wherever focus is (also on
  // <body> after a click outside the panel, or in a field) and before any handler
  // on the way can stop them. Ctrl K / Cmd K toggles the palette.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const id = shortcutFor(e, ['palette', 'help'])
      if (!id) return
      e.preventDefault() // the browser's own Ctrl K (search) must not run
      if (id === 'help') setHelp(true)
      else if (paletteOpen.current) {
        e.stopPropagation()
        setPalette(false)
      } else openPalette()
    }
    document.addEventListener('keydown', onKey, true)
    return () => document.removeEventListener('keydown', onKey, true)
  }, [openPalette])
  // While the palette is open, Escape closes it from anywhere in the page.
  useEffect(() => {
    if (!palette) return
    const onEscape = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      e.preventDefault()
      e.stopPropagation() // nothing underneath (the explorer's Escape) reacts as well
      setPalette(false)
    }
    document.addEventListener('keydown', onEscape, true)
    return () => document.removeEventListener('keydown', onEscape, true)
  }, [palette])

  const run = (c: Command) => {
    setRecent((r) => [c.id, ...r.filter((x) => x !== c.id)].slice(0, 5))
    setPalette(false)
    // After the palette has closed and returned focus, so an action that moves focus wins.
    setTimeout(() => {
      const a = c.action
      switch (a.type) {
        case 'navigate':
          void navigate(a.to)
          return
        case 'help':
          setHelp(true)
          return
        case 'account':
          if (a.what === 'sign-out') void signOut()
          else setAccount(a.what === 'password' ? 'password' : 'all')
          return
        case 'copy-link': {
          const url = window.location.href
          void copyText(url).then((ok) => setNotice(ok ? LINK_COPIED : linkFallback(url)))
          return
        }
        default:
          bridge.current?.run(a)
      }
    }, 0)
  }

  const commands = palette
    ? commandsFor({
        slug: m ? decodeURIComponent(m[1] ?? '') : null,
        section: m?.[2] === 'logs' || m?.[2] === 'keys' || m?.[2] === 'groups' || m?.[2] === 'alerts' ? m[2] : 'other',
        projects: projects.data?.map((p) => p.slug) ?? [],
        canManageAlerts: projects.data?.some((p) => m !== null && p.slug === decodeURIComponent(m[1] ?? '') && (p.role === 'owner' || p.role === 'admin')) ?? false,
        explorer: m?.[2] === 'logs' ? explorerCtx : null,
        // Only on the list itself, not on a group's page.
        groups: m?.[2] === 'groups' && /^\/projects\/[^/]+\/groups\/?$/.test(pathname) ? groupsCtx : null,
      })
    : []

  return (
    <BridgeContext.Provider value={register}>
      <a className="skip-link" href="#main">
        Skip to content
      </a>
      <header className="shell-header">
        <Link to="/" className="mark">
          LogSage
        </Link>
        <ProjectSwitcher />
        <ProjectNav />
        <span className="spacer" />
        <button type="button" className="button button-quiet palette-button" aria-keyshortcuts="Control+K Meta+K" onClick={openPalette}>
          Commands
        </button>
        <kbd title="Command palette">Ctrl K</kbd>
        <AccountMenu onDialog={setAccount} />
      </header>
      <main id="main" className="shell-main" tabIndex={-1}>
        <Outlet />
      </main>
      <p className="shell-notice" role="status" aria-live="polite">
        {notice}
      </p>
      {palette && <CommandPalette commands={commands} defaults={defaultCommands(commands, recent, m?.[2] === 'logs' ? explorerCtx : null)} onRun={run} onClose={closePalette} />}
      {help && <ShortcutsHelp onClose={() => setHelp(false)} />}
      <AccountDialogs dialog={account} onClose={() => setAccount(null)} />
    </BridgeContext.Provider>
  )
}
