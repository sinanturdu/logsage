import { screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import appCss from '../styles/app.css?raw'
import { json, renderApp } from '../test/util'
import { mockAlerts, rule } from '../alerts/testing'

afterEach(() => vi.unstubAllGlobals())

const css = appCss.replace(/\r\n/g, '\n')
const block = (selector: string) => {
  const at = css.indexOf(`\n${selector} {`)
  expect(at, selector).toBeGreaterThan(-1)
  return css.slice(css.indexOf('{', at) + 1, css.indexOf('}', at))
}

describe('header nav links', () => {
  it('share one color for every state; only the firing count is red', () => {
    expect(block('.project-nav a, .project-nav a:visited')).toContain('color: var(--pencil)')
    expect(block('.project-nav a:hover')).toContain('color: var(--ink)')
    expect(block(".project-nav a[aria-current='page'], .project-nav a[aria-current='page']:visited")).toContain('border-bottom-color: var(--pen)')
    expect(block('.project-nav a.nav-firing, .project-nav a.nav-firing:visited, .project-nav a.nav-firing:hover')).toContain('color: var(--level-error)')
    // No other rule colors a nav link (nothing like a warn tint on one of them).
    const navRules = css.split('\n').filter((l) => l.includes('.project-nav a') && l.includes('color:'))
    expect(navRules.join('\n')).not.toMatch(/level-warn|orange|#[0-9a-f]{3,6}/i)
  })

  it('renders the four links alike, with the class only on a firing Alerts link', async () => {
    mockAlerts({ 'GET /api/projects/shop/alerts/rules': () => json(200, { rules: [rule('r1', { state: 'firing', firing: 1 })] }) })
    renderApp('/projects/shop/groups')
    const nav = await screen.findByRole('navigation', { name: 'Project sections' })
    await within(nav).findByRole('link', { name: 'Alerts, 1 firing' })
    const links = within(nav).getAllByRole('link')
    expect(links.map((a) => a.textContent)).toEqual(['Logs', 'Error groups', 'Alerts, 1 firing', 'API keys'])
    expect(links.map((a) => a.getAttribute('style'))).toEqual([null, null, null, null])
    expect(links.map((a) => a.className.replace('active', '').trim())).toEqual(['', '', 'nav-firing', ''])
    expect(links.map((a) => a.getAttribute('aria-current'))).toEqual([null, 'page', null, null])
  })
})

describe('header nav at narrow widths', () => {
  it('keeps every label on one line and lets the nav scroll inside itself', () => {
    expect(block('.project-nav a')).toContain('white-space: nowrap')
    const nav = block('.project-nav')
    for (const d of ['overflow-x: auto', 'min-width: 0', 'flex: 0 1 auto']) expect(nav).toContain(d)
    // No rule hides a nav link.
    expect(css.split('\n').filter((l) => l.includes('.project-nav') && /display: none|visibility: hidden/.test(l))).toEqual([])
  })
})
