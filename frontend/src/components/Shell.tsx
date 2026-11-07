import { useEffect, useState } from 'react'
import { Link, NavLink, Outlet, useMatch } from 'react-router'
import { shortcutFor } from '../shortcuts'
import { ShortcutsHelp } from './ShortcutsHelp'
import { AccountMenu } from './AccountMenu'
import { ProjectSwitcher } from './ProjectSwitcher'

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
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (shortcutFor(e, ['help']) === 'help') {
        e.preventDefault()
        setHelp(true)
      }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [])
  return (
    <>
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
        {/* The command palette arrives in I2c; the hint makes the shortcut learnable now. */}
        <kbd title="Command palette (not available yet)">Ctrl K</kbd>
        <AccountMenu />
      </header>
      <main id="main" className="shell-main" tabIndex={-1}>
        <Outlet />
      </main>
      {help && <ShortcutsHelp onClose={() => setHelp(false)} />}
    </>
  )
}
