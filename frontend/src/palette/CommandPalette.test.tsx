import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { json, project, renderApp } from '../test/util'
import { facets, mockExplorer } from '../explorer/testing'
import { LINK_COPIED } from '../components/Shell'

const EVIL = '<img src=x onerror=alert(1)>'
const where = () => screen.getByTestId('location').textContent ?? ''
const palette = () => screen.getByRole('dialog', { name: 'Command palette' })
const combo = () => within(palette()).getByRole('combobox')
const options = () => within(palette()).queryAllByRole('option')
const activeLabel = () => document.getElementById(combo().getAttribute('aria-activedescendant') ?? '')?.querySelector('.label')?.textContent

function setup(extra: Parameters<typeof mockExplorer>[0] = {}) {
  return mockExplorer({
    projects: () => json(200, { projects: [project('shop', 'Shop'), project('demo', 'Demo')] }),
    tail: () => json(503, {}),
    ...extra,
  })
}
async function open(path = '/projects/shop/logs') {
  renderApp(path)
  await screen.findByRole('treegrid')
  const user = userEvent.setup()
  await user.keyboard('{Control>}k{/Control}')
  await screen.findByRole('dialog', { name: 'Command palette' })
  return user
}

afterEach(() => {
  vi.unstubAllGlobals()
  Reflect.deleteProperty(document, 'execCommand')
})

describe('opening', () => {
  it('opens with Ctrl K and Cmd K, also from the search box, and prevents the browser shortcut', async () => {
    setup()
    renderApp('/projects/shop/logs')
    const search = await screen.findByRole('searchbox')
    search.focus()
    const ev = new KeyboardEvent('keydown', { key: 'k', ctrlKey: true, bubbles: true, cancelable: true })
    search.dispatchEvent(ev)
    expect(ev.defaultPrevented).toBe(true)
    expect(await screen.findByRole('dialog', { name: 'Command palette' })).toBeTruthy()
    await userEvent.setup().keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(document.activeElement).toBe(search) // focus returns to where it was
    fireEvent.keyDown(document.body, { key: 'k', metaKey: true })
    expect(await screen.findByRole('dialog', { name: 'Command palette' })).toBeTruthy()
  })

  it('opens from the visible Commands button and returns focus to it', async () => {
    setup()
    renderApp('/projects/shop/logs')
    const user = userEvent.setup()
    const button = await screen.findByRole('button', { name: 'Commands' })
    expect(button.getAttribute('aria-keyshortcuts')).toBe('Control+K Meta+K')
    await user.click(button)
    expect(document.activeElement).toBe(combo())
    await user.keyboard('{Escape}')
    expect(document.activeElement).toBe(button)
  })
})

describe('combobox and listbox', () => {
  it('has the ARIA roles and points at the active option', async () => {
    setup()
    await open()
    const c = combo()
    const list = within(palette()).getByRole('listbox')
    expect(c.getAttribute('aria-expanded')).toBe('true')
    expect(c.getAttribute('aria-controls')).toBe(list.id)
    expect(c.getAttribute('aria-autocomplete')).toBe('list')
    const first = options()[0] as HTMLElement
    expect(c.getAttribute('aria-activedescendant')).toBe(first.id)
    expect(first.getAttribute('aria-selected')).toBe('true')
    expect(options().filter((o) => o.getAttribute('aria-selected') === 'true')).toHaveLength(1)
  })

  it('moves with the arrow keys (wrapping), Home and End; hovering does not move it', async () => {
    setup()
    const user = await open()
    await user.type(combo(), 'last')
    const labels = options().map((o) => o.querySelector('.label')?.textContent)
    expect(activeLabel()).toBe(labels[0])
    await user.keyboard('{ArrowUp}')
    expect(activeLabel()).toBe(labels.at(-1)) // wraps to the end
    await user.keyboard('{ArrowDown}')
    expect(activeLabel()).toBe(labels[0]) // and back
    await user.keyboard('{ArrowDown}{ArrowDown}')
    expect(activeLabel()).toBe(labels[2])
    fireEvent.mouseMove(options()[0] as HTMLElement)
    fireEvent.mouseEnter(options()[0] as HTMLElement)
    expect(activeLabel()).toBe(labels[2])
    await user.keyboard('{End}')
    expect(activeLabel()).toBe(labels.at(-1))
    await user.keyboard('{Home}')
    expect(activeLabel()).toBe(labels[0])
  })

  it('announces the result count politely and highlights matches as text', async () => {
    setup()
    const user = await open()
    const status = within(palette()).getByRole('status')
    expect(status.getAttribute('aria-live')).toBe('polite')
    await user.type(combo(), 'zzzz')
    expect(status.textContent).toBe('No commands match.')
    expect(combo().getAttribute('aria-expanded')).toBe('false')
    expect(combo().hasAttribute('aria-activedescendant')).toBe(false)
    await user.clear(combo())
    await user.type(combo(), 'refresh')
    expect(status.textContent).toBe(`${options().length === 1 ? '1 command.' : `${options().length} commands.`}`)
    expect(activeLabel()).toBe('Refresh')
    expect(Array.from(options()[0]?.querySelectorAll('.hit') ?? []).map((h) => h.textContent).join('')).toBe('Refresh')
  })

  it('traps focus inside the palette', async () => {
    setup()
    const user = await open()
    for (let i = 0; i < 4; i++) {
      await user.tab()
      expect(palette().contains(document.activeElement)).toBe(true)
    }
  })
})

describe('running commands', () => {
  it.each([
    ['Last 1 hour', '/projects/shop/logs?range=1h'],
    ['Show errors and fatal only', '/projects/shop/logs?level=error%2Cfatal'],
    ['Filter by service: checkout', '/projects/shop/logs?service=checkout'],
    ['Turn live tail on', '/projects/shop/logs?live=1'],
    ['Go to API keys', '/projects/shop/keys'],
    ['Switch project: demo', '/projects/demo/logs'],
  ])('%s', async (label, url) => {
    setup()
    const user = await open()
    await user.type(combo(), label)
    expect(activeLabel()).toBe(label)
    await user.keyboard('{Enter}')
    expect(screen.queryByRole('dialog', { name: 'Command palette' })).toBeNull()
    await waitFor(() => expect(where()).toBe(url))
  })

  it('runs a clicked command and focuses search', async () => {
    setup()
    const user = await open()
    await user.type(combo(), 'focus search')
    await user.click(options()[0] as HTMLElement)
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('searchbox')))
  })

  it('offers Zoom only when it applies, and Clear filters with filters', async () => {
    setup()
    const user = await open('/projects/shop/logs?from=2026-11-07T02:00:00Z&to=2026-11-07T02:15:00Z&service=api')
    await user.type(combo(), 'zoom')
    expect(options().map((o) => o.querySelector('.label')?.textContent)).toEqual(['Zoom to selection'])
    await user.keyboard('{Enter}')
    await waitFor(() => expect(where()).toContain('zoom=1'))
    await user.keyboard('{Control>}k{/Control}')
    await user.type(combo(), 'clear')
    await user.keyboard('{Enter}')
    await waitFor(() => expect(where()).not.toContain('service='))
  })

  it('opens the sign-out-of-all-devices confirmation and never signs out directly', async () => {
    const { requests } = setup()
    const user = await open()
    await user.type(combo(), 'sign out of all')
    await user.keyboard('{Enter}')
    expect(await screen.findByRole('dialog', { name: 'Sign out of all devices?' })).toBeTruthy()
    expect(requests.some((r) => r.path === '/api/auth/logout-all')).toBe(false)
  })

  it('opens the keyboard shortcuts help', async () => {
    setup()
    const user = await open()
    await user.type(combo(), 'shortcuts')
    await user.keyboard('{Enter}')
    expect(await screen.findByRole('dialog', { name: 'Keyboard shortcuts' })).toBeTruthy()
  })

  it('copies the link with a polite message, and shows the link when copying fails', async () => {
    setup()
    const user = await open('/projects/shop/logs?range=1h')
    const write = vi.spyOn(navigator.clipboard, 'writeText')
    await user.type(combo(), 'copy link')
    await user.keyboard('{Enter}')
    expect(await screen.findByText(LINK_COPIED)).toBeTruthy()
    expect(write).toHaveBeenCalledWith(window.location.href)
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
    Object.defineProperty(document, 'execCommand', { value: () => false, configurable: true })
    await user.keyboard('{Control>}k{/Control}')
    await user.type(combo(), 'copy link')
    await user.keyboard('{Enter}')
    expect(await screen.findByText(/^Copy is not available here\. Select the link and copy it with Ctrl\+C: http/)).toBeTruthy()
  })

  it('shows recent commands first on an empty query, kept in memory only', async () => {
    setup()
    const user = await open()
    await user.type(combo(), 'refresh')
    await user.keyboard('{Enter}')
    await user.keyboard('{Control>}k{/Control}')
    expect(options()[0]?.querySelector('.label')?.textContent).toBe('Refresh')
    expect(JSON.stringify({ ...localStorage, ...sessionStorage })).not.toContain('refresh')
  })
})

describe('untrusted text', () => {
  it('renders HTML payloads in project and service names as text', async () => {
    setup({
      projects: () => json(200, { projects: [project('shop', 'Shop'), project(EVIL, EVIL)] }),
      facets: () => json(200, { ...facets(3), services: [{ name: '<script>alert(2)</script>', count: 3 }] }),
    })
    const user = await open()
    await user.type(combo(), 'img')
    expect(options()[0]?.textContent).toContain(`Switch project: ${EVIL}`)
    await user.clear(combo())
    await user.type(combo(), 'script')
    expect(options()[0]?.textContent).toContain('Filter by service: <script>alert(2)</script>')
    expect(palette().querySelector('img, script')).toBeNull()
  })
})
