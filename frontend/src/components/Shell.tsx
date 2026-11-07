import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, NavLink, Outlet, useLocation, useMatch, useNavigate } from 'react-router'
import { useProjects } from '../api/projects'
import { copyText } from '../explorer/clipboard'
import { BridgeContext, type ExplorerBridge } from '../palette/bridge'
import { CommandPalette } from '../palette/CommandPalette'
import { commandsFor, type Command } from '../palette/commands'
import { shortcutFor } from '../shortcuts'
import { AccountDialogs, AccountMenu, useSignOut, type AccountDialog } from './AccountMenu'
import { ProjectSwitcher } from './ProjectSwitcher'
import { ShortcutsHelp } from './ShortcutsHelp'

export const LINK_COPIED = 'Copied the link to this view.'
export const linkFallback = (url: string) => `Copy is not available here. Select the link and copy it with Ctrl+C: ${url}`

/** Logs and API keys of the current project. */
function ProjectNav() {
  const m = useMatch('/projects/:slug/*')
  const slug = m?.params.slug
  if (!slug) return null
  const base = `/projects/${encodeURIComponent(slug)}`
  return (
    <nav className="project-nav" aria-label="Project sections">
      <NavLink to={`${base}/logs`}>Logs</NavLink>
      <NavLink to={`${base}/keys`}>API keys</NavLink>
    </nav>
  )
}

export function Shell() {
  const [help, setHelp] = useState(false)
  const [palette, setPalette] = useState(false)
  // The explorer's state when the palette opened (read from the bridge in an event, not during render).
  const [explorerCtx, setExplorerCtx] = useState<ExplorerBridge['ctx'] | null>(null)
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
  const m = /^\/projects\/([^/]+)\/(logs|keys)/.exec(pathname)
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
        section: m?.[2] === 'logs' ? 'logs' : m?.[2] === 'keys' ? 'keys' : 'other',
        projects: projects.data?.map((p) => p.slug) ?? [],
        explorer: m?.[2] === 'logs' ? explorerCtx : null,
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
      {palette && <CommandPalette commands={commands} recent={recent} onRun={run} onClose={closePalette} />}
      {help && <ShortcutsHelp onClose={() => setHelp(false)} />}
      <AccountDialogs dialog={account} onClose={() => setAccount(null)} />
    </BridgeContext.Provider>
  )
}
