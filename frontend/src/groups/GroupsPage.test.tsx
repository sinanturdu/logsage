import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { json, renderApp } from '../test/util'
import { LIST_CHANGED } from './api'
import { NO_GROUPS, NOT_GROUPED_NOTE, summaryText, visibleGroups } from './GroupsPage'
import { group, mockGroups, NOW, page } from './testing'

const A = 'aaaaaaaaaaaaaaaa'
const B = 'bbbbbbbbbbbbbbbb'
const C = 'cccccccccccccccc'

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(NOW)
})
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

const location = () => screen.getByTestId('location').textContent ?? ''
const rows = () => screen.getAllByRole('row').slice(1)

describe('groups list', () => {
  it('shows a table with status, flags, template, services, levels, count, trend and times', async () => {
    const { requests } = mockGroups({
      'GET /api/projects/shop/groups': () =>
        page([
          group(A, { is_new: true, count: 1340, sparkline: [0, 340, 2], levels: ['error', 'fatal'], services: ['payments', 'api'], first_seen: '2026-11-07T20:11:00.000Z' }),
          group(B, { status: 'resolved', regressed: true, resolved_at: '2026-11-07T19:00:00.000Z', title: 'charge failed: upstream payments returned 504' }),
        ]),
    })
    renderApp('/projects/shop/groups')
    const table = await screen.findByRole('table')
    expect(within(table).getAllByRole('columnheader').map((h) => h.textContent)).toEqual([
      'Status',
      'Group',
      'Services',
      'Levels',
      'Events',
      'Trend',
      'Last seen',
      'First seen',
    ])
    const [first, second] = rows()
    expect(first?.textContent).toContain('Open')
    expect(within(first as HTMLElement).getByText('New').className).toContain('badge')
    expect(within(second as HTMLElement).getByText('Regressed').className).toContain('badge')
    expect(second?.textContent).toContain('Resolved')
    // The template: text with muted placeholder spans, as a link to the detail.
    const link = within(first as HTMLElement).getByRole('link')
    expect(link.getAttribute('href')).toBe(`/projects/shop/groups/${A}`)
    expect([...link.querySelectorAll('.ph')].map((s) => s.textContent)).toEqual(['<n>'])
    expect(within(first as HTMLElement).getByText('payments, api')).toBeTruthy()
    expect(within(first as HTMLElement).getByText('1,340')).toBeTruthy()
    expect(within(first as HTMLElement).getByRole('img').getAttribute('aria-label')).toBe('Trend, peak 340 events at 20:01 UTC')
    const firstSeen = within(first as HTMLElement).getAllByText(/ago|just now/)[1]
    expect(firstSeen?.textContent).toBe('4 min ago')
    expect(firstSeen?.getAttribute('title')).toBe('2026-11-07 20:11:00 UTC')
    expect(screen.getByText('Showing 2 groups, 1 new in this range.')).toBeTruthy()
    const req = requests.find((r) => r.path === '/api/projects/shop/groups')
    expect(req?.params.get('level')).toBe('error,fatal')
    expect(req?.params.get('status')).toBe('open')
    expect(req?.params.get('sort')).toBe('count')
    expect(req?.params.get('from')).toBe('2026-11-07T20:00:00Z')
  })

  it('status, sort and level filters go to the URL and the request', async () => {
    const { requests } = mockGroups({ 'GET /api/projects/shop/groups': () => page([group(A)]) })
    renderApp('/projects/shop/groups')
    await screen.findByRole('table')
    const user = userEvent.setup()
    await user.selectOptions(screen.getByLabelText('Status'), 'resolved')
    await user.selectOptions(screen.getByLabelText('Sort'), 'last_seen')
    await user.click(screen.getByRole('checkbox', { name: /warn/ }))
    expect(location()).toBe('/projects/shop/groups?level=warn%2Cerror%2Cfatal&status=resolved&sort=last_seen')
    await waitFor(() => {
      const last = requests.filter((r) => r.path === '/api/projects/shop/groups').at(-1)
      expect(last?.params.get('status')).toBe('resolved')
      expect(last?.params.get('sort')).toBe('last_seen')
      expect(last?.params.get('level')).toBe('warn,error,fatal')
    })
    await user.selectOptions(screen.getByLabelText('Status'), 'all')
    await waitFor(() => expect(requests.filter((r) => r.path === '/api/projects/shop/groups').at(-1)?.params.has('status')).toBe(false))
  })

  it('Load more appends the next page without duplicates', async () => {
    mockGroups({
      'GET /api/projects/shop/groups': (r) => (r.params.get('cursor') === 'c1' ? page([group(B), group(C)]) : page([group(A), group(B)], 'c1')),
    })
    renderApp('/projects/shop/groups')
    await screen.findByRole('table')
    await userEvent.setup().click(screen.getByRole('button', { name: 'Load more' }))
    await waitFor(() => expect(rows()).toHaveLength(3))
    expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull()
    expect(screen.getByText('Showing 3 groups, 0 new in this range.')).toBeTruthy()
  })

  it('a changed cursor says the list changed and reloads from the start', async () => {
    let calls = 0
    mockGroups({
      'GET /api/projects/shop/groups': (r) => {
        calls++
        if (r.params.get('cursor')) return json(400, { error: 'cursor: not valid for this query; repeat the same query with next_cursor' })
        return page([group(A)], 'c1')
      },
    })
    renderApp('/projects/shop/groups')
    await screen.findByRole('table')
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Load more' }))
    expect(await screen.findByText(LIST_CHANGED)).toBeTruthy()
    const before = calls
    await user.click(screen.getByRole('button', { name: 'Reload' }))
    await waitFor(() => expect(calls).toBeGreaterThan(before))
    await waitFor(() => expect(screen.queryByText(LIST_CHANGED)).toBeNull())
    expect(rows()).toHaveLength(1)
  })

  it('j and k move the selection, Enter opens the group, / focuses the search', async () => {
    mockGroups({ 'GET /api/projects/shop/groups': () => page([group(A), group(B)]) })
    renderApp('/projects/shop/groups?range=1h')
    await screen.findByRole('table')
    const user = userEvent.setup()
    await user.keyboard('/')
    expect(document.activeElement).toBe(screen.getByRole('searchbox', { name: 'Search templates' }))
    await user.click(screen.getByRole('heading', { level: 1 }))
    await user.keyboard('j')
    expect(rows()[0]?.className).toBe('selected')
    expect(document.activeElement).toBe(within(rows()[0] as HTMLElement).getByRole('link'))
    await user.keyboard('j')
    expect(rows()[1]?.className).toBe('selected')
    await user.keyboard('k')
    expect(rows()[0]?.className).toBe('selected')
    await user.keyboard('{Enter}')
    await waitFor(() => expect(location()).toBe(`/projects/shop/groups/${A}?range=1h`))
  })

  it('new only lists the new groups first seen in the range and stops paging', async () => {
    const { requests } = mockGroups({
      'GET /api/projects/shop/groups': () => page([group(A, { is_new: true }), group(B, { is_new: false }), group(C, { is_new: true })], 'c1'),
    })
    renderApp('/projects/shop/groups?new=1')
    await screen.findByRole('table')
    expect(rows()).toHaveLength(1)
    expect(screen.getByText('Showing 1 new group in this range.')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull()
    expect(requests.find((r) => r.path === '/api/projects/shop/groups')?.params.get('sort')).toBe('first_seen')
    expect((screen.getByLabelText('Sort') as HTMLSelectElement).disabled).toBe(true)
  })

  it('an empty range says how to widen it, with the note about older logs', async () => {
    mockGroups({ 'GET /api/projects/shop/groups': () => page([]) })
    renderApp('/projects/shop/groups')
    expect(await screen.findByText(NO_GROUPS)).toBeTruthy()
    expect(screen.getAllByText(NOT_GROUPED_NOTE)).toHaveLength(1)
    expect(screen.queryByRole('table')).toBeNull()
  })

  it.each([
    [json(429, { error: 'too many' }, { 'Retry-After': '9' }), 'Too many requests. Try again in 9 seconds.', 'Try again'],
    [json(502, {}), 'LogSage cannot reach its log store right now. Try again in a moment.', 'Try again'],
    [json(404, { error: 'not found' }), 'This project was not found, or you are not a member of it. Choose a project in the header.', null],
  ])('errors say what happened (%#)', async (res, text, button) => {
    mockGroups({ 'GET /api/projects/shop/groups': () => res.clone() })
    renderApp('/projects/shop/groups')
    expect(await screen.findByText(text)).toBeTruthy()
    if (button) expect(screen.getByRole('button', { name: button })).toBeTruthy()
    else expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull()
  })

  it('renders hostile templates and services as text', async () => {
    const xss = '<img src=x onerror="window.pwned=1"><script>window.pwned=1</script> <n>'
    mockGroups({ 'GET /api/projects/shop/groups': () => page([group(A, { title: xss, services: ['<b>svc</b>'] })]) })
    renderApp('/projects/shop/groups')
    await screen.findByRole('table')
    expect(document.querySelector('table img, table script, table b')).toBeNull()
    expect(within(screen.getByRole('table')).getByRole('link').textContent).toBe(xss)
    expect(screen.getByText('<b>svc</b>')).toBeTruthy()
    expect((window as unknown as { pwned?: number }).pwned).toBeUndefined()
  })
})

describe('groups palette commands', () => {
  it('change the status, sort and new only from the palette', async () => {
    mockGroups({ 'GET /api/projects/shop/groups': () => page([group(A)]) })
    renderApp('/projects/shop/groups')
    await screen.findByRole('table')
    const user = userEvent.setup()
    const run = async (text: string) => {
      await user.keyboard('{Control>}k{/Control}')
      const dialog = screen.getByRole('dialog', { name: /command/i })
      await user.type(within(dialog).getByRole('combobox'), text)
      await user.keyboard('{Enter}')
      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    }
    await run('Show resolved groups')
    await waitFor(() => expect(location()).toBe('/projects/shop/groups?status=resolved'))
    await run('Sort groups by last seen')
    await waitFor(() => expect(location()).toBe('/projects/shop/groups?status=resolved&sort=last_seen'))
    await run('Show new groups only')
    await waitFor(() => expect(location()).toBe('/projects/shop/groups?status=resolved&sort=last_seen&new=1'))
  })

  it('Go to Error groups is offered from the explorer', async () => {
    mockGroups({ 'GET /api/projects/shop/groups': () => page([group(A)]), 'GET /api/projects/shop/logs': () => json(200, { events: [], next_cursor: null }) })
    renderApp('/projects/shop/logs')
    await screen.findByText(/No logs match/)
    const user = userEvent.setup()
    await user.keyboard('{Control>}k{/Control}')
    await user.type(within(screen.getByRole('dialog', { name: /command/i })).getByRole('combobox'), 'Go to Error groups')
    await user.keyboard('{Enter}')
    await waitFor(() => expect(location()).toBe('/projects/shop/groups'))
  })
})

describe('list helpers', () => {
  it('summarizes only what is loaded', () => {
    expect(summaryText(0, 0, false)).toBe('Showing 0 groups, 0 new in this range.')
    expect(summaryText(1, 1, false)).toBe('Showing 1 group, 1 new in this range.')
    expect(summaryText(1200, 3, false)).toBe('Showing 1,200 groups, 3 new in this range.')
    expect(summaryText(2, 2, true)).toBe('Showing 2 new groups in this range.')
  })

  it('new only keeps the new prefix', () => {
    const gs = [group(A, { is_new: true }), group(B), group(C, { is_new: true })]
    expect(visibleGroups(gs, true)).toEqual({ groups: [gs[0]], ended: true })
    expect(visibleGroups([gs[0] as never], true).ended).toBe(false)
    expect(visibleGroups(gs, false).groups).toHaveLength(3)
  })
})
