import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { json, renderApp } from '../test/util'
import { event, facets, mockExplorer, type Req } from './testing'
import { NO_RESULTS, STORE_DOWN } from './ExplorerPage'
import { SEARCH_TOO_LONG, TRACE_INVALID } from './FilterPanel'

const of = (requests: Req[], endpoint: string) => requests.filter((r) => r.path === `/api/projects/shop/${endpoint}`)
const where = () => screen.getByTestId('location').textContent ?? ''
const grid = () => screen.getByRole('treegrid', { name: 'Logs, newest first' })
const rows = () => Array.from(grid().querySelectorAll<HTMLElement>('[data-index]'))
const statusText = () => document.querySelector('.results .status')?.textContent

describe('log explorer', () => {
  it('loads the timeline, facets and logs for the URL state', async () => {
    const { requests } = mockExplorer()
    renderApp('/projects/shop/logs?range=1h&level=error,fatal&service=payments&q=timeout')
    expect(await screen.findByText(/^payment gateway timeout after 5000 ms \(psp-eu-2\) #0$/)).toBeTruthy()
    for (const ep of ['histogram', 'facets', 'logs']) {
      const r = of(requests, ep)[0]
      expect(r, ep).toBeTruthy()
      expect(r?.params.get('level')).toBe('error,fatal')
      expect(r?.params.get('service')).toBe('payments')
      expect(r?.params.get('q')).toBe('timeout')
      const span = Date.parse(r?.params.get('to') ?? '') - Date.parse(r?.params.get('from') ?? '')
      expect(span).toBe(3_600_000)
    }
    expect(of(requests, 'logs')[0]?.params.get('limit')).toBe('100')
    expect(statusText()).toBe('Showing 3 of 3 logs, newest first.')
    expect(screen.getByText(/Peak volume 43 logs per 10 s at 02:16:40 UTC/)).toBeTruthy()
    expect(screen.getByRole('heading', { level: 1 }).textContent).toMatch(/ UTC last hour$/)
    // Level marks: shape plus word.
    const row = rows()[0] as HTMLElement
    expect(within(row).getByText('■')).toBeTruthy()
    expect(row.textContent).toContain('error')
    expect(row.querySelector('.msg')?.getAttribute('title')).toBe('payment gateway timeout after 5000 ms (psp-eu-2) #0')
  })

  it('updates the URL and refetches when a filter changes, cancelling the old request', async () => {
    let releaseFirst: (r: Response) => void = () => {}
    let call = 0
    const { requests } = mockExplorer({
      logs: (r) => {
        call++
        if (call === 1) return new Promise<Response>((res) => (releaseFirst = res))
        return json(200, { events: r.params.get('level') === 'warn,error,fatal' ? [event(7, { level: 'warn', message: 'newer result' })] : [], next_cursor: null })
      },
    })
    renderApp('/projects/shop/logs')
    const user = userEvent.setup()
    await screen.findByText('debug')
    await user.click(screen.getByRole('checkbox', { name: /debug/ }))
    await user.click(screen.getByRole('checkbox', { name: /info/ }))
    await waitFor(() => expect(where()).toBe('/projects/shop/logs?level=warn%2Cerror%2Cfatal'))
    expect(await screen.findByText('newer result')).toBeTruthy()
    // The first request was cancelled; its late answer changes nothing.
    expect(of(requests, 'logs')[0]?.signal?.aborted).toBe(true)
    await act(async () => releaseFirst(json(200, { events: [event(0, { message: 'stale result' })], next_cursor: null })))
    expect(screen.queryByText('stale result')).toBeNull()
    expect(screen.getByText('newer result')).toBeTruthy()
  })

  it('appends pages without duplicate event ids', async () => {
    mockExplorer({
      facets: () => json(200, facets(5)),
      logs: (r) =>
        r.params.get('cursor') === 'c1'
          ? json(200, { events: [event(2), event(3), event(4)], next_cursor: null }) // event 2 again
          : json(200, { events: [event(0), event(1), event(2)], next_cursor: 'c1' }),
    })
    renderApp('/projects/shop/logs')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Load more' }))
    await waitFor(() => expect(rows()).toHaveLength(5))
    expect(statusText()).toBe('Showing 5 of 5 logs, newest first.')
    expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull()
  })

  it('stops at 1,000 rows and says how to see the rest', async () => {
    let page = 0
    mockExplorer({
      facets: () => json(200, facets(5000)),
      logs: () => {
        const start = page++ * 100
        return json(200, { events: Array.from({ length: 100 }, (_, i) => event(start + i)), next_cursor: `c${page}` })
      },
    })
    renderApp('/projects/shop/logs')
    const user = userEvent.setup()
    for (let i = 0; i < 9; i++) await user.click(await screen.findByRole('button', { name: 'Load more' }))
    expect(await screen.findByText(/1,000 logs loaded, the most this view shows\. Narrow the time range or add filters/)).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull()
  }, 15_000) // renders up to 1,000 rows ten times in jsdom; slow when the suite runs in parallel

  it('shows the empty state with Clear filters', async () => {
    mockExplorer({ logs: () => json(200, { events: [], next_cursor: null }), facets: () => json(200, { ...facets(0), total: 0 }) })
    renderApp('/projects/shop/logs?service=checkout&q=nothing')
    expect(await screen.findByText(NO_RESULTS)).toBeTruthy()
    const user = userEvent.setup()
    const clears = screen.getAllByRole('button', { name: 'Clear filters' })
    await user.click(clears[clears.length - 1] as HTMLElement)
    await waitFor(() => expect(where()).toBe('/projects/shop/logs'))
  })

  it.each([
    [503, {}, STORE_DOWN, true],
    [400, {}, 'To: range from..to is 200h0m0s, max 168h0m0s.', false],
    [429, { 'Retry-After': '9' }, 'Too many requests. Try again in 9 seconds.', true],
    [404, {}, 'This project was not found, or you are not a member of it. Choose a project in the header.', false],
  ])('explains a %i from the logs request', async (status, headers, text, retry) => {
    mockExplorer({ logs: () => json(status, { error: status === 400 ? 'to: range from..to is 200h0m0s, max 168h0m0s' : 'x' }, headers as Record<string, string>) })
    renderApp('/projects/shop/logs')
    const alert = await screen.findByText(text)
    expect(alert.closest('[role="alert"]')).toBeTruthy()
    expect(!!within(alert.closest('[role="alert"]') as HTMLElement).queryByRole('button', { name: 'Try again' })).toBe(retry)
  })

  it('explains an unreachable API and retries', async () => {
    let fail = true
    mockExplorer({ logs: () => (fail ? Promise.reject(new TypeError('Failed to fetch')) : json(200, { events: [event(0)], next_cursor: null })) })
    renderApp('/projects/shop/logs')
    expect(await screen.findByText(STORE_DOWN)).toBeTruthy()
    fail = false
    await userEvent.setup().click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByText(/#0$/)).toBeTruthy()
  })

  it('moves the selection with j and k, clears it with Escape, and focuses search with /', async () => {
    mockExplorer()
    renderApp('/projects/shop/logs')
    await screen.findByRole('treegrid', { name: 'Logs, newest first' })
    const user = userEvent.setup()
    const options = () => rows()
    await user.keyboard('j')
    expect(options()[0]?.getAttribute('aria-selected')).toBe('true')
    await user.keyboard('j')
    await user.keyboard('j')
    await user.keyboard('j') // stays on the last row
    expect(options()[2]?.getAttribute('aria-selected')).toBe('true')
    await user.keyboard('k')
    expect(options()[1]?.getAttribute('aria-selected')).toBe('true')
    expect(document.activeElement).toBe(options()[1])
    await user.keyboard('{Escape}')
    expect(options().every((o) => o.getAttribute('aria-selected') === 'false')).toBe(true)
    await user.keyboard('/')
    expect(document.activeElement).toBe(screen.getByLabelText('Search messages'))
    await user.keyboard('j') // typing in search does not move the selection
    expect(options().every((o) => o.getAttribute('aria-selected') === 'false')).toBe(true)
  })

  it('searches after a pause, at once on Enter, and refuses over-long text', async () => {
    const { requests } = mockExplorer()
    renderApp('/projects/shop/logs')
    const user = userEvent.setup()
    const box = await screen.findByLabelText('Search messages')
    await user.type(box, 'gateway{Enter}')
    await waitFor(() => expect(where()).toBe('/projects/shop/logs?q=gateway'))
    await waitFor(() => expect(of(requests, 'logs').some((r) => r.params.get('q') === 'gateway')).toBe(true))
    fireEvent.change(box, { target: { value: 'x'.repeat(257) } })
    expect(screen.getByText(SEARCH_TOO_LONG)).toBeTruthy()
    await new Promise((r) => setTimeout(r, 400))
    expect(where()).toBe('/projects/shop/logs?q=gateway')
  })

  it('validates the trace id', async () => {
    mockExplorer()
    renderApp('/projects/shop/logs')
    const user = userEvent.setup()
    const input = await screen.findByLabelText('Trace id')
    await user.type(input, '4bf92f35-77b3-4da6{Enter}')
    expect(screen.getByText(TRACE_INVALID)).toBeTruthy()
    expect(where()).toBe('/projects/shop/logs')
    await user.clear(input)
    await user.type(input, '4BF92F3577B34DA6A3CE929D0E0E4736{Enter}')
    await waitFor(() => expect(where()).toBe('/projects/shop/logs?trace_id=4bf92f3577b34da6a3ce929d0e0e4736'))
    expect(screen.queryByText(TRACE_INVALID)).toBeNull()
  })

  it('lists services from the facets with a non-selectable other row', async () => {
    mockExplorer()
    renderApp('/projects/shop/logs')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('checkbox', { name: /checkout/ }))
    await waitFor(() => expect(where()).toBe('/projects/shop/logs?service=checkout'))
    expect(screen.getByText('other services')).toBeTruthy()
    expect(screen.queryByRole('checkbox', { name: /other/ })).toBeNull()
  })

  it('sets the range with presets and a validated custom UTC range', async () => {
    mockExplorer()
    renderApp('/projects/shop/logs')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: '6 h' }))
    await waitFor(() => expect(where()).toBe('/projects/shop/logs?range=6h'))
    await user.click(screen.getByRole('button', { name: 'Custom' }))
    const from = screen.getByLabelText('From (UTC)')
    const to = screen.getByLabelText('To (UTC)')
    fireEvent.change(from, { target: { value: '2026-11-07T02:15:00' } })
    fireEvent.change(to, { target: { value: '2026-11-07T02:10:00' } })
    await user.click(screen.getByRole('button', { name: 'Apply' }))
    expect(screen.getByText(/The end must be after the start/)).toBeTruthy()
    fireEvent.change(to, { target: { value: '2026-11-07T02:17:30' } })
    await user.click(screen.getByRole('button', { name: 'Apply' }))
    await waitFor(() => expect(where()).toBe('/projects/shop/logs?from=2026-11-07T02%3A15%3A00Z&to=2026-11-07T02%3A17%3A30Z'))
    expect(screen.getByRole('heading', { level: 1 }).textContent).toContain('02:15:00 to 02:17:30 UTC')
  })

  it('renders an HTML payload in a log message as text', async () => {
    mockExplorer({ logs: () => json(200, { events: [event(0, { message: '<img src=x onerror=alert(1)>', service: '<b>svc</b>' })], next_cursor: null }) })
    renderApp('/projects/shop/logs')
    expect(await screen.findByText('<img src=x onerror=alert(1)>')).toBeTruthy()
    const list = grid()
    expect(list.querySelector('img, b')).toBeNull()
    for (const el of list.querySelectorAll('*')) {
      expect(el.getAttributeNames().some((a) => a.startsWith('on'))).toBe(false)
    }
  })
})
