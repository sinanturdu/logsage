import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { json, renderApp } from '../test/util'
import { COPY_FALLBACK, TRACE_EMPTY, TRACE_ERROR } from './RowDetail'
import { mockExplorer, type Req } from './testing'

const TRACE = '4bf92f3577b34da6a3ce929d0e0e4736'
const T0 = Date.UTC(2026, 10, 7, 2, 16, 58, 412)

type Ev = {
  event_id: string
  timestamp: string
  level: string
  service: string
  message: string
  trace_id: string
  attributes: Record<string, string>
}
function ev(i: number, over: Partial<Ev> = {}): Ev {
  return {
    event_id: `${2000 + i}-0`,
    timestamp: new Date(T0 - i * 1000).toISOString(),
    level: 'error',
    service: 'payments',
    message: `payment gateway timeout after 5000 ms (psp-eu-2) #${i}`,
    trace_id: TRACE,
    attributes: { span_id: '00f067aa0ba902b7', 'http.route': '/v1/charges', 'gateway.host': 'psp-eu-2.example.net', order_id: 'ord_7Q2K9M', region: 'eu-west-1' },
    ...over,
  }
}
const cascade = [
  ev(0),
  ev(1, { service: 'checkout', message: 'charge failed: upstream payments returned 504' }),
  ev(2, { service: 'api', message: 'POST /v1/checkout 502 in 5114 ms' }),
  ev(3, { level: 'warn', message: 'retrying charge for ord_7Q2K9M, attempt 3 of 3' }),
  ev(4, { service: 'checkout', level: 'info', message: 'cart converted' }),
  ev(5, { service: 'api', level: 'info', message: 'POST /v1/checkout accepted' }),
  ev(6, { level: 'debug', message: 'charge request queued' }),
]

/** The list answers with `list`; trace lookups (trace_id param) use `trace`. */
function setup(list: Ev[], trace: (r: Req) => Response | Promise<Response> = () => json(200, { events: cascade, next_cursor: null })) {
  return mockExplorer({ logs: (r) => (r.params.get('trace_id') && r.params.get('limit') === '50' ? trace(r) : json(200, { events: list, next_cursor: null })) })
}
const grid = () => screen.getByRole('treegrid', { name: 'Logs, newest first' })
const rows = () => Array.from(grid().querySelectorAll<HTMLElement>('[data-index]'))
const where = () => screen.getByTestId('location').textContent ?? ''
const detail = () => document.querySelector<HTMLElement>('.detail')

async function openFirst() {
  renderApp('/projects/shop/logs')
  await screen.findByRole('treegrid', { name: 'Logs, newest first' })
  const user = userEvent.setup()
  await user.click(rows()[0] as HTMLElement)
  return user
}

afterEach(() => {
  vi.unstubAllGlobals()
  Reflect.deleteProperty(document, 'execCommand') // back to jsdom's own
})

describe('row detail', () => {
  it('expands with a click, Enter or Space, keeps focus on the row, and collapses with Escape', async () => {
    setup([ev(0), ev(1, { trace_id: '' })])
    const user = await openFirst()
    const first = () => rows()[0] as HTMLElement
    expect(first().getAttribute('aria-expanded')).toBe('true')
    expect(document.activeElement).toBe(first())
    await user.keyboard('{Escape}')
    expect(first().getAttribute('aria-expanded')).toBe('false')
    expect(detail()).toBeNull()
    expect(document.activeElement).toBe(first())
    expect(first().getAttribute('aria-selected')).toBe('true') // Escape closed the detail, not the selection
    await user.keyboard('{Enter}')
    expect(first().getAttribute('aria-expanded')).toBe('true')
    await user.keyboard(' ')
    expect(first().getAttribute('aria-expanded')).toBe('false')
    await user.keyboard('j')
    await user.keyboard('{Enter}')
    expect(rows()[1]?.getAttribute('aria-expanded')).toBe('true')
  })

  it('shows the full message, ids and grouped attributes', async () => {
    setup([ev(0)])
    await openFirst()
    const d = detail() as HTMLElement
    expect(d.querySelector('.full')?.textContent).toBe('payment gateway timeout after 5000 ms (psp-eu-2) #0')
    expect(within(d).getByText(TRACE)).toBeTruthy()
    expect(within(d).getByText('00f067aa0ba902b7')).toBeTruthy()
    const zone = (name: string) => within(d).getByRole('heading', { name }).closest('section') as HTMLElement
    expect(Array.from(zone('Request').querySelectorAll('dt')).map((x) => x.textContent)).toEqual(['gateway.host', 'http.route'])
    expect(Array.from(zone('Context').querySelectorAll('dt')).map((x) => x.textContent)).toEqual(['order_id', 'region'])
    expect(within(d).queryByText('span_id', { selector: '.pairs dt' })).toBeNull() // promoted, not repeated
  })

  it('without a trace id: no Same trace zone, no filter action, Request and Context share the width', async () => {
    setup([ev(0, { trace_id: '', attributes: { method: 'GET', user: 'usr_1290' } })])
    await openFirst()
    const d = detail() as HTMLElement
    expect(within(d).queryByRole('heading', { name: /Same trace/ })).toBeNull()
    expect(within(d).queryByRole('button', { name: 'Filter by this trace' })).toBeNull()
    expect(d.querySelector('.zones')?.classList.contains('no-trace')).toBe(true)
    expect(within(d).getByText('none')).toBeTruthy()
  })

  it('says No attributes when there are none', async () => {
    setup([ev(0, { trace_id: '', attributes: {} })])
    await openFirst()
    expect(within(detail() as HTMLElement).getByText('No attributes')).toBeTruthy()
  })

  it('copies the log as JSON and announces it', async () => {
    const list = [ev(0)]
    setup(list)
    const user = await openFirst() // user-event installs its own clipboard stub; spy on it after setup
    const writeText = vi.spyOn(navigator.clipboard, 'writeText')
    await user.click(within(detail() as HTMLElement).getByRole('button', { name: 'Copy as JSON' }))
    expect(writeText).toHaveBeenCalledWith(JSON.stringify(list[0], null, 2))
    expect(within(detail() as HTMLElement).getByRole('status').textContent).toBe('Copied the log as JSON.')
  })

  it('falls back when the Clipboard API is missing', async () => {
    setup([ev(0)])
    const exec = vi.fn(() => true)
    Object.defineProperty(document, 'execCommand', { value: exec, configurable: true })
    const user = await openFirst()
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
    await user.click(within(detail() as HTMLElement).getByRole('button', { name: 'Copy trace id' }))
    expect(exec).toHaveBeenCalledWith('copy')
    expect(within(detail() as HTMLElement).getByRole('status').textContent).toBe('Copied the trace id.')
    exec.mockReturnValue(false)
    await user.click(within(detail() as HTMLElement).getByRole('button', { name: 'Copy as JSON' }))
    expect(within(detail() as HTMLElement).getByRole('status').textContent).toBe(COPY_FALLBACK)
  })

  it('filters by this trace', async () => {
    setup([ev(0)])
    const user = await openFirst()
    await user.click(within(detail() as HTMLElement).getByRole('button', { name: 'Filter by this trace' }))
    await waitFor(() => expect(where()).toBe(`/projects/shop/logs?trace_id=${TRACE}`))
  })

  it('refuses to filter by a trace id that is not 32 hex characters', async () => {
    setup([ev(0, { trace_id: 'tr-1a2b3c4d5e6f7a8b' })])
    const user = await openFirst()
    await user.click(within(detail() as HTMLElement).getByRole('button', { name: 'Filter by this trace' }))
    expect(screen.getByText('This trace id cannot be used as a filter: it is not 32 hexadecimal characters.')).toBeTruthy()
    expect(where()).toBe('/projects/shop/logs')
  })

  it('t filters by the selected row trace, or explains that there is none', async () => {
    setup([ev(0, { trace_id: '' }), ev(1)])
    renderApp('/projects/shop/logs')
    await screen.findByRole('treegrid', { name: 'Logs, newest first' })
    const user = userEvent.setup()
    await user.keyboard('j')
    await user.keyboard('t')
    expect(screen.getByText('This log has no trace_id, so there is no trace to filter by.')).toBeTruthy()
    expect(where()).toBe('/projects/shop/logs')
    await user.keyboard('j')
    await user.keyboard('t')
    await waitFor(() => expect(where()).toBe(`/projects/shop/logs?trace_id=${TRACE}`))
  })
})

describe('same trace', () => {
  it('loads lazily, lists five newest first with the current log marked, and shows all', async () => {
    const { requests } = setup([ev(0)])
    renderApp('/projects/shop/logs')
    await screen.findByRole('treegrid', { name: 'Logs, newest first' })
    const traceCalls = () => requests.filter((r) => r.params.get('limit') === '50')
    expect(traceCalls()).toHaveLength(0) // nothing until a row opens
    await userEvent.setup().click(rows()[0] as HTMLElement)
    const list = await within(detail() as HTMLElement).findByRole('list')
    const r = traceCalls()[0] as Req
    expect(r.params.get('trace_id')).toBe(TRACE)
    expect(Date.parse(r.params.get('to') ?? '') - Date.parse(r.params.get('from') ?? '')).toBe(20 * 60_000 + 1)
    const items = within(list).getAllByRole('button')
    expect(items).toHaveLength(5)
    expect(items[0]?.getAttribute('aria-current')).toBe('true')
    expect(items[0]?.textContent).not.toContain('this log')
    expect(items[1]?.textContent).toContain('checkout: charge failed: upstream payments returned 504')
    expect(items[2]?.textContent).toContain('api: POST /v1/checkout 502')
    expect(within(detail() as HTMLElement).getByRole('heading', { name: 'Same trace (7 logs)' })).toBeTruthy()
    await userEvent.setup().click(within(detail() as HTMLElement).getByRole('button', { name: 'Show all 7' }))
    expect(within(list).getAllByRole('button')).toHaveLength(7)
  })

  it('switches the detail to a clicked log while the list selection stays', async () => {
    setup([ev(0)])
    const user = await openFirst()
    const list = await within(detail() as HTMLElement).findByRole('list')
    await user.click(within(list).getAllByRole('button')[2] as HTMLElement)
    expect(detail()?.querySelector('.full')?.textContent).toBe('POST /v1/checkout 502 in 5114 ms')
    expect(rows()[0]?.getAttribute('aria-selected')).toBe('true')
    const current = (await within(detail() as HTMLElement).findByRole('list')).querySelector('[aria-current="true"]')
    expect(current?.textContent).toContain('POST /v1/checkout 502')
  })

  it('shows loading, empty and error states', async () => {
    let answer: (r: Req) => Response | Promise<Response> = () => new Promise<Response>(() => {})
    setup([ev(0)], (r) => answer(r))
    const user = await openFirst()
    expect(detail()?.querySelector('.trace-skeleton')).toBeTruthy()
    await user.keyboard('{Escape}')

    answer = () => json(200, { events: [ev(0)], next_cursor: null }) // only the log itself
    await user.click(rows()[0] as HTMLElement)
    expect(await within(detail() as HTMLElement).findByText(TRACE_EMPTY)).toBeTruthy()
    expect(within(detail() as HTMLElement).getByRole('heading', { name: 'Same trace (1 log)' })).toBeTruthy()
  })

  it('shows an error with retry', async () => {
    let fail = true
    setup([ev(0, { event_id: '9-0' })], () => (fail ? json(503, { error: 'unavailable' }) : json(200, { events: cascade, next_cursor: null })))
    const user = await openFirst()
    const err = await within(detail() as HTMLElement).findByText(new RegExp(TRACE_ERROR.replace(/[.()]/g, '\\$&')))
    fail = false
    await user.click(within(err).getByRole('button', { name: 'Retry' }))
    expect(await within(detail() as HTMLElement).findByRole('list')).toBeTruthy()
  })

  it('cancels the lookup when the row collapses, and ignores its late answer', async () => {
    let release: (r: Response) => void = () => {}
    const { requests } = setup([ev(0), ev(1, { event_id: '3000-0', trace_id: 'a'.repeat(32), message: 'other row' })], (r) =>
      r.params.get('trace_id') === TRACE ? new Promise<Response>((res) => (release = res)) : json(200, { events: [ev(9, { trace_id: 'a'.repeat(32), message: 'other trace member' })], next_cursor: null }),
    )
    const user = await openFirst()
    await user.keyboard('{Escape}')
    const first = requests.find((r) => r.params.get('trace_id') === TRACE)
    await waitFor(() => expect(first?.signal?.aborted).toBe(true))
    await user.click(rows()[1] as HTMLElement)
    expect(await within(detail() as HTMLElement).findByText(/other trace member/)).toBeTruthy()
    await act(async () => release(json(200, { events: cascade, next_cursor: null })))
    expect(within(detail() as HTMLElement).queryByText(/charge failed/)).toBeNull()
  })
})

describe('untrusted text in the detail', () => {
  it('renders HTML payloads in the message, attributes and trace list as text', async () => {
    const evil = '<img src=x onerror=alert(1)>'
    const script = '<script>alert(1)</script>'
    setup([ev(0, { message: evil, attributes: { [evil]: script, 'http.x': evil } })], () =>
      json(200, { events: [ev(0, { message: evil }), ev(1, { service: script, message: script })], next_cursor: null }),
    )
    await openFirst()
    await within(detail() as HTMLElement).findByRole('list')
    const d = detail() as HTMLElement
    expect(d.querySelector('img, script')).toBeNull()
    expect(grid().querySelector('img, script')).toBeNull()
    for (const el of grid().querySelectorAll('*')) {
      expect(el.getAttributeNames().some((a) => a.startsWith('on'))).toBe(false)
    }
    expect(d.querySelector('.full')?.textContent).toBe(evil)
    expect(within(d).getAllByText(script).length).toBeGreaterThan(0)
  })
})
