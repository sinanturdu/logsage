import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { json, renderApp } from '../test/util'
import { NOT_GROUPED } from './RowDetail'
import { event, mockExplorer } from './testing'

const FP = '0123456789abcdef'
const TITLE = 'payment gateway timeout after <n> ms (psp-eu-<n>)'

afterEach(() => vi.unstubAllGlobals())

const where = () => screen.getByTestId('location').textContent ?? ''
const rows = () => Array.from(document.querySelectorAll<HTMLElement>('[role="treegrid"] [data-index]'))

function setup(events: object[], title: (() => Response) | null = () => json(200, { title: TITLE })) {
  const m = mockExplorer({ logs: () => json(200, { events, next_cursor: null, took_ms: 1 }) })
  let titleCalls = 0
  if (title) {
    m.routes[`/api/projects/shop/groups/${FP}`] = () => {
      titleCalls++
      return title()
    }
  }
  return { ...m, titleCalls: () => titleCalls }
}

describe('explorer fingerprint filter', () => {
  it('filters logs, histogram and facets, and shows a removable group chip', async () => {
    const { requests } = setup([event(0, { fingerprint: FP })])
    renderApp(`/projects/shop/logs?fingerprint=${FP.toUpperCase()}`)
    // The chip starts with the first 8 hex and is named by the template once loaded.
    const chip = await screen.findByRole('button', { name: `Remove filter group: ${TITLE}` })
    expect(chip.textContent).toContain('group: payment gateway timeout after <n> ms (ps…')
    for (const path of ['logs', 'histogram', 'facets']) {
      expect(requests.some((r) => r.path === `/api/projects/shop/${path}` && r.params.get('fingerprint') === FP)).toBe(true)
    }
    expect(screen.getByRole('button', { name: /^Filters \(1\)/ })).toBeTruthy()
    await userEvent.setup().click(chip)
    expect(where()).toBe('/projects/shop/logs')
  })

  it('falls back to the first 8 hex when the group cannot be loaded', async () => {
    setup([event(0)], () => json(500, {}))
    renderApp(`/projects/shop/logs?fingerprint=${FP}`)
    expect(await screen.findByRole('button', { name: 'Remove filter group: 01234567' })).toBeTruthy()
  })

  it('drops an invalid fingerprint', async () => {
    const { requests } = setup([event(0)])
    renderApp('/projects/shop/logs?fingerprint=nothex')
    await screen.findByRole('treegrid')
    expect(screen.queryByRole('button', { name: /Remove filter group/ })).toBeNull()
    expect(requests.some((r) => r.params.has('fingerprint'))).toBe(false)
  })
})

describe('row detail group line', () => {
  it('names the group lazily, once per fingerprint, with a link to it', async () => {
    const m = setup([event(0, { fingerprint: FP }), event(1, { fingerprint: FP })])
    renderApp('/projects/shop/logs?range=1h')
    await screen.findByRole('treegrid')
    expect(m.titleCalls()).toBe(0) // nothing until a detail opens
    const user = userEvent.setup()
    await user.click(rows()[0] as HTMLElement)
    const line = await screen.findByText('Group:', { exact: false, selector: 'p' })
    await waitFor(() => expect(line.textContent).toBe(`Group: ${TITLE} - View group`))
    expect(within(line).getByRole('link', { name: 'View group' }).getAttribute('href')).toBe(`/projects/shop/groups/${FP}?range=1h`)
    await user.click(rows()[1] as HTMLElement)
    await screen.findByText('View group')
    expect(m.titleCalls()).toBe(1)
  })

  it.each([
    ['warn', true],
    ['error', true],
    ['fatal', true],
    ['info', false],
    ['debug', false],
  ])('a %s log without a group says "not grouped": %s', async (level, shown) => {
    setup([event(0, { level, fingerprint: '' })], null)
    renderApp('/projects/shop/logs')
    await screen.findByRole('treegrid')
    await userEvent.setup().click(rows()[0] as HTMLElement)
    await screen.findByText(/^payment gateway timeout/, { selector: '.full span' })
    expect(screen.queryByText(NOT_GROUPED) !== null).toBe(shown)
    expect(screen.queryByText('View group')).toBeNull()
  })

  it('renders a hostile group title as text', async () => {
    const xss = '<img src=x onerror="window.pwned=1">'
    setup([event(0, { fingerprint: FP })], () => json(200, { title: xss }))
    renderApp('/projects/shop/logs')
    await screen.findByRole('treegrid')
    await userEvent.setup().click(rows()[0] as HTMLElement)
    await screen.findByText(xss)
    expect(document.querySelector('.detail img')).toBeNull()
    expect((window as unknown as { pwned?: number }).pwned).toBeUndefined()
  })
})
