import { Link, Outlet } from 'react-router'
import { AccountMenu } from './AccountMenu'
import { ProjectSwitcher } from './ProjectSwitcher'

export function Shell() {
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
        <span className="spacer" />
        {/* The command palette arrives in I2c; the hint makes the shortcut learnable now. */}
        <kbd title="Command palette (not available yet)">Ctrl K</kbd>
        <AccountMenu />
      </header>
      <main id="main" className="shell-main" tabIndex={-1}>
        <Outlet />
      </main>
    </>
  )
}
