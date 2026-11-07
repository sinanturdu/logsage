import { act, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { json, renderApp } from '../test/util'
import { event, mockExplorer, type Req } from '../explorer/testing'
import { DEGRADED_TEXT } from './machine'
import { controlledStream } from './testing'
import { countText, mergeNewest } from './useLiveList'
import type { LogEvent } from '../explorer/api'

const where = () => screen.getByTestId('location').textContent ?? ''
const grid = () => screen.getByRole('treegrid', { name: 'Logs, newest first' })
const rows = () => Array.from(grid().querySelectorAll<HTMLElement>('[data-index]'))
const firstMessage = () => rows()[0]?.querySelector('.msg')?.textContent

/** The explorer with a tail endpoint whose streams the test drives. */
function setup(tail?: (r: Req) => Response | Promise<Response>) {
  const streams: ReturnType<typeof controlledStream>[] = []
  const m = mockExplorer({
    tail:
      tail ??
      ((r) => {
        const s = controlledStream(r.signal)
        streams.push(s)
        return s.response
      }),
  })
  const tails = () => m.requests.filter((r) => r.path.endsWith('/tail'))
  return { ...m, streams, tails }
}
const arrive = (i: number, extra: Partial<Record<string, string>> = {}) => event(-i, { message: `arrived #${i}`, ...extra })

async function openLive(path = '/projects/shop/logs?live=1') {
  renderApp(path)
  await screen.findByRole('treegrid', { name: 'Logs, newest first' })
  await screen.findByText('Live', { selector: '.live-state' })
  return userEvent.setup()
}

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('live toggle', () => {
  it('turns on and off with l and the button, keeps rows, ignores l while typing', async () => {
    const { tails, streams } = setup()
    renderApp('/projects/shop/logs?service=api&q=timeout&from=2026-11-07T02:00:00Z&to=2026-11-07T02:05:00Z')
    await screen.findByRole('treegrid', { name: 'Logs, newest first' })
    const user = userEvent.setup()
    await user.click(screen.getByRole('searchbox'))
    await user.keyboard('l')
    expect(where()).not.toContain('live=1')
    // Undo the typed l, so the debounced search keeps q=timeout.
    await user.keyboard('{Backspace}')
    await new Promise((r) => setTimeout(r, 400))
    expect(where()).toBe('/projects/shop/logs?service=api&q=timeout&from=2026-11-07T02:00:00Z&to=2026-11-07T02:05:00Z')
    ;(document.activeElement as HTMLElement).blur()
    await user.keyboard('l')
    await waitFor(() => expect(where()).toBe('/projects/shop/logs?service=api&q=timeout&live=1'))
    await waitFor(() => expect(tails()).toHaveLength(1))
    const t = tails()[0] as Req
    expect([...t.params.keys()].sort()).toEqual(['q', 'service'])
    expect(screen.getByRole('button', { name: /Live/ }).getAttribute('aria-pressed')).toBe('true')
    await screen.findByText('Live', { selector: '.live-state' })

    act(() => streams[0]?.log(arrive(1), 'c1'))
    await waitFor(() => expect(firstMessage()).toBe('arrived #1'))
    await user.click(screen.getByRole('button', { name: /Live/ }))
    await waitFor(() => expect(where()).toBe('/projects/shop/logs?service=api&q=timeout'))
    expect(t.signal?.aborted).toBe(true)
    expect(firstMessage()).toBe('arrived #1') // turning Live off keeps the rows
  })
})

describe('arrivals', () => {
  it('inserts new logs on top without duplicates and marks them for the highlight', async () => {
    const { streams } = setup()
    await openLive()
    act(() => {
      streams[0]?.log(arrive(1), 'c1')
      streams[0]?.log(arrive(2), 'c2')
      streams[0]?.log(arrive(1), 'c1') // a redelivery
      streams[0]?.log(event(0), 'c0') // already in the list from /logs
    })
    await waitFor(() => expect(firstMessage()).toBe('arrived #2'))
    expect(rows()).toHaveLength(5)
    expect(rows()[1]?.classList.contains('arrived')).toBe(true)
    expect(rows()[2]?.classList.contains('arrived')).toBe(false)
    expect(screen.getByText('Showing 5 logs, newest first.')).toBeTruthy()
  })

  it('caps the list at 1,000 rows, dropping the oldest', () => {
    const many = Array.from({ length: 1200 }, (_, i) => ({ event_id: `n${i}` }) as LogEvent)
    const out = mergeNewest(many.slice(0, 600), many.slice(500))
    expect(out).toHaveLength(1000)
    expect(out[0]?.event_id).toBe('n0')
    expect(out.at(-1)?.event_id).toBe('n999')
    expect(countText(1)).toBe('1 new log')
    expect(countText(1200)).toBe('1,200 new logs')
  })

  it('renders HTML payloads in live messages as text', async () => {
    const { streams } = setup()
    await openLive()
    act(() => streams[0]?.log(arrive(1, { message: '<img src=x onerror=alert(1)><script>alert(2)</script>', service: '<b>x</b>' }), 'c1'))
    await waitFor(() => expect(firstMessage()).toBe('<img src=x onerror=alert(1)><script>alert(2)</script>'))
    expect(grid().querySelector('img, script, b')).toBeNull()
  })
})

describe('auto-pause', () => {
  it('buffers while a row is expanded and inserts on "N new logs"', async () => {
    const { streams } = setup()
    const user = await openLive()
    await user.click(rows()[1] as HTMLElement)
    expect(await screen.findByText('Paused while you read; new logs wait here.')).toBeTruthy()
    act(() => {
      streams[0]?.log(arrive(1), 'c1')
      streams[0]?.log(arrive(2), 'c2')
    })
    const more = await screen.findByRole('button', { name: '2 new logs' })
    expect(firstMessage()).not.toContain('arrived')
    await user.click(more)
    await waitFor(() => expect(firstMessage()).toBe('arrived #2'))
    expect(document.querySelector('.detail')).toBeNull()
    expect(screen.queryByText('Paused while you read; new logs wait here.')).toBeNull()
  })

  it('pauses while the selection is below the first row; Resume clears it', async () => {
    const { streams } = setup()
    const user = await openLive()
    await user.keyboard('jj')
    await user.click(await screen.findByRole('button', { name: 'Resume' }))
    expect(rows().some((r) => r.getAttribute('aria-selected') === 'true')).toBe(false)
    act(() => streams[0]?.log(arrive(1), 'c1'))
    await waitFor(() => expect(firstMessage()).toBe('arrived #1'))
  })

  it('pauses when the list is scrolled away from the top', async () => {
    const { streams } = setup()
    await openLive()
    const list = document.querySelector('.results .rows') as HTMLElement
    list.getBoundingClientRect = () => ({ top: -400 }) as DOMRect
    act(() => {
      window.dispatchEvent(new Event('scroll'))
    })
    act(() => streams[0]?.log(arrive(1), 'c1'))
    expect(await screen.findByRole('button', { name: '1 new log' })).toBeTruthy()
  })
})

describe('announcements', () => {
  it('summarizes arrivals politely at most every 5 seconds', async () => {
    const { streams } = setup()
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'], shouldAdvanceTime: true })
    await openLive()
    const region = () => document.querySelectorAll('[aria-live="polite"]')
    const said = () => Array.from(region()).map((r) => r.textContent)
    act(() => {
      for (let i = 1; i <= 12; i++) streams[0]?.log(arrive(i), `c${i}`)
    })
    await waitFor(() => expect(firstMessage()).toBe('arrived #12'))
    expect(said()).not.toContain('12 new logs')
    await act(() => vi.advanceTimersByTimeAsync(5000))
    expect(said()).toContain('12 new logs')
    act(() => streams[0]?.log(arrive(13), 'c13'))
    await waitFor(() => expect(firstMessage()).toBe('arrived #13'))
    expect(said()).toContain('12 new logs') // not one announcement per row
    await act(() => vi.advanceTimersByTimeAsync(5000))
    expect(said()).toContain('1 new log')
  })

  it('refreshes the histogram every 10 seconds while live', async () => {
    const { requests } = setup()
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'], shouldAdvanceTime: true })
    await openLive()
    const n = () => requests.filter((r) => r.path.endsWith('/histogram')).length
    const before = n()
    vi.setSystemTime(Date.now() + 10_000)
    await act(() => vi.advanceTimersByTimeAsync(10_000))
    await waitFor(() => expect(n()).toBe(before + 1))
  })
})

describe('connection states', () => {
  it('shows degraded in words and recovers', async () => {
    const { streams } = setup()
    await openLive()
    act(() => streams[0]?.status({ state: 'degraded' }))
    expect(await screen.findByText(DEGRADED_TEXT, { selector: '.live-state' })).toBeTruthy()
    act(() => streams[0]?.status({ state: 'live', heartbeat: true }))
    expect(await screen.findByText('Live', { selector: '.live-state' })).toBeTruthy()
  })

  it('counts down after a drop, and r or Retry now reconnects with Last-Event-ID', async () => {
    const { streams, tails } = setup()
    const user = await openLive()
    act(() => streams[0]?.log(arrive(1), 'c7'))
    await waitFor(() => expect(firstMessage()).toBe('arrived #1'))
    act(() => streams[0]?.close())
    expect(await screen.findByText(/^Live tail disconnected at \d\d:\d\d:\d\d: the connection was lost\. Reconnecting in [45] s, or press r to retry now\.$/)).toBeTruthy()
    await user.keyboard('r')
    await waitFor(() => expect(tails()).toHaveLength(2))
    expect(tails()[1]?.headers['Last-Event-ID']).toBe('c7')
    await screen.findByText('Live', { selector: '.live-state' })
    act(() => streams[1]?.close())
    await user.click(await screen.findByRole('button', { name: 'Retry now' }))
    await waitFor(() => expect(tails()).toHaveLength(3))
  })

  it.each([
    [404, {}, 'Live tail stopped: this project was not found, or you are not a member of it.'],
    [400, { error: 'q must be at most 256 characters' }, 'Live tail stopped: q must be at most 256 characters. Change the filters to try again.'],
    [429, {}, /^Live tail disconnected at .*: too many requests\. Reconnecting in (9|10) s/],
    [503, {}, /^Live tail disconnected at .*: the LogSage API failed \(status 503\)\./],
  ] as const)('explains a %i', async (status, body, text) => {
    setup(() => json(status, body, status === 429 ? { 'Retry-After': '10' } : {}))
    renderApp('/projects/shop/logs?live=1')
    expect(await screen.findByText(text, { selector: '.live-state' })).toBeTruthy()
  })

  it('a 401 ends the session and returns to sign-in', async () => {
    setup(() => json(401, {}))
    renderApp('/projects/shop/logs?live=1')
    await waitFor(() => expect(where()).toMatch(/^\/login\?next=/))
  })

  it('a reset refetches the first page and continues', async () => {
    const { streams, requests } = setup()
    await openLive()
    act(() => streams[0]?.log(arrive(1), 'c1'))
    await waitFor(() => expect(firstMessage()).toBe('arrived #1'))
    const logs = () => requests.filter((r) => r.path.endsWith('/logs')).length
    const before = logs()
    act(() => streams[0]?.send('event: reset\ndata: {"reason":"cursor_too_old"}\n\n'))
    await waitFor(() => expect(logs()).toBe(before + 1))
    await waitFor(() => expect(firstMessage()).toBe(event(0).message))
    act(() => streams[0]?.log(arrive(2), 'c2'))
    await waitFor(() => expect(firstMessage()).toBe('arrived #2'))
  })
})

describe('cancellation', () => {
  it('closes the stream when the filters change and when leaving the page', async () => {
    const { tails } = setup()
    const { unmount } = renderApp('/projects/shop/logs?live=1')
    await screen.findByText('Live', { selector: '.live-state' })
    const user = userEvent.setup()
    await user.click(screen.getByRole('checkbox', { name: /^error/ }))
    await waitFor(() => expect(tails()).toHaveLength(2))
    expect(tails()[0]?.signal?.aborted).toBe(true)
    expect(tails()[1]?.params.get('level')).toBeTruthy()
    unmount()
    expect(tails()[1]?.signal?.aborted).toBe(true)
    expect(tails()).toHaveLength(2)
  })
})
