import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { LogEvent } from '../explorer/api'
import { controlledStream } from './testing'
import { useLiveTail } from './useLiveTail'

type Call = { url: string; headers: Record<string, string>; signal: AbortSignal }
const F = { levels: [], services: [], q: '', traceId: '' }

function setup(answers: ((c: Call) => Response)[]) {
  const calls: Call[] = []
  const streams: ReturnType<typeof controlledStream>[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init: RequestInit) => {
      const c = { url, headers: init.headers as Record<string, string>, signal: init.signal as AbortSignal }
      calls.push(c)
      const a = answers[calls.length - 1]
      if (a) return a(c)
      const s = controlledStream(c.signal)
      streams.push(s)
      return s.response
    }),
  )
  return { calls, streams }
}
// Stream reads take several turns; flush them without moving the fake clock.
const settle = () =>
  act(async () => {
    for (let i = 0; i < 20; i++) await vi.advanceTimersByTimeAsync(0)
  })
const ev = (i: number) => ({ event_id: `e${i}` }) as LogEvent

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date', 'requestAnimationFrame', 'cancelAnimationFrame'] })
  vi.setSystemTime(Date.UTC(2026, 10, 7, 16, 40, 0))
})
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

function render(props: Partial<Parameters<typeof useLiveTail>[0]> = {}) {
  const onLogs = vi.fn()
  const onReset = vi.fn()
  const h = renderHook((p: Parameters<typeof useLiveTail>[0]) => useLiveTail(p), {
    initialProps: { slug: 'shop', filters: F, enabled: true, onLogs, onReset, ...props },
  })
  return { ...h, onLogs, onReset }
}

describe('useLiveTail', () => {
  it('connects, batches logs per frame, and keeps the cursor for the reconnect', async () => {
    const { calls, streams } = setup([])
    const h = render()
    await settle()
    expect(h.result.current.state.kind).toBe('live')
    streams[0]?.send('retry: 5000\n\n')
    streams[0]?.log(ev(1), 'c1')
    streams[0]?.log(ev(2), 'c2')
    // Both arrive in one animation frame (waitFor advances the fake clock).
    await act(() => vi.waitFor(() => expect(h.onLogs).toHaveBeenCalled(), { timeout: 2000, interval: 20 }))
    expect(h.onLogs).toHaveBeenCalledTimes(1)
    expect(h.onLogs).toHaveBeenCalledWith([ev(1), ev(2)])

    streams[0]?.close() // dropped without a closed status
    await settle()
    expect(h.result.current.state).toMatchObject({ kind: 'reconnecting', reason: 'the connection was lost' })
    await act(() => vi.advanceTimersByTimeAsync(4999))
    expect(calls).toHaveLength(1) // never sooner than the backoff
    await act(() => vi.advanceTimersByTimeAsync(1))
    expect(calls).toHaveLength(2)
    expect(calls[1]?.headers['Last-Event-ID']).toBe('c2')
  })

  it('doubles the wait on repeated 503s and retries at once on demand', async () => {
    const down = () => new Response('{}', { status: 503 })
    const { calls } = setup([down, down, down])
    const h = render()
    await settle()
    expect(h.result.current.state).toMatchObject({ kind: 'reconnecting', retryAt: Date.now() + 5000 })
    await act(() => vi.advanceTimersByTimeAsync(5000))
    expect(h.result.current.state).toMatchObject({ kind: 'reconnecting', retryAt: Date.now() + 10_000 })
    act(() => h.result.current.retryNow())
    await settle()
    expect(calls).toHaveLength(3)
    expect(h.result.current.state).toMatchObject({ kind: 'reconnecting', retryAt: Date.now() + 20_000 })
  })

  it('waits for Retry-After on 429', async () => {
    setup([() => new Response('{}', { status: 429, headers: { 'Retry-After': '17' } })])
    const h = render()
    await settle()
    expect(h.result.current.state).toMatchObject({ kind: 'reconnecting', retryAt: Date.now() + 17_000, reason: 'too many requests' })
  })

  it.each([
    [404, 'this project was not found, or you are not a member of it.'],
    [400, 'level is not valid. Change the filters to try again.'],
  ])('stops on %i without retrying', async (status, reason) => {
    const { calls } = setup([() => new Response(JSON.stringify({ error: 'level is not valid' }), { status })])
    const h = render()
    await settle()
    expect(h.result.current.state).toEqual({ kind: 'stopped', reason })
    await act(() => vi.advanceTimersByTimeAsync(60_000))
    expect(calls).toHaveLength(1)
  })

  it('reconnects silently after max_duration with Last-Event-ID', async () => {
    const { calls, streams } = setup([])
    const h = render()
    await settle()
    streams[0]?.log(ev(1), 'c9')
    await act(() => vi.advanceTimersByTimeAsync(15 * 60_000))
    streams[0]?.status({ state: 'closed', reason: 'max_duration' })
    streams[0]?.close()
    await settle()
    expect(calls).toHaveLength(2)
    expect(calls[1]?.headers['Last-Event-ID']).toBe('c9')
    expect(h.result.current.state.kind).toBe('live')
  })

  it('shows degraded and recovers', async () => {
    const { streams } = setup([])
    const h = render()
    await settle()
    streams[0]?.status({ state: 'degraded', reason: 'storage' })
    await settle()
    expect(h.result.current.state.kind).toBe('degraded')
    streams[0]?.status({ state: 'live', heartbeat: true })
    await settle()
    expect(h.result.current.state.kind).toBe('live')
  })

  it('passes a reset to the caller and drops queued logs', async () => {
    const { streams } = setup([])
    const h = render()
    await settle()
    streams[0]?.log(ev(1), 'c1')
    streams[0]?.send('event: reset\ndata: {"reason":"cursor_too_old"}\n\n')
    await settle()
    await act(() => vi.advanceTimersByTimeAsync(20))
    expect(h.onReset).toHaveBeenCalledTimes(1)
    expect(h.onLogs).not.toHaveBeenCalled()
  })

  it('aborts on filter change, when turned off and on unmount; a new filter starts without a cursor', async () => {
    const { calls, streams } = setup([])
    const h = render()
    await settle()
    streams[0]?.log(ev(1), 'c1')
    await settle()
    h.rerender({ slug: 'shop', filters: { ...F, q: 'timeout' }, enabled: true, onLogs: h.onLogs, onReset: h.onReset })
    await settle()
    expect(calls[0]?.signal.aborted).toBe(true)
    expect(calls[1]?.url).toBe('/api/projects/shop/tail?q=timeout')
    expect(calls[1]?.headers['Last-Event-ID']).toBeUndefined()
    h.rerender({ slug: 'shop', filters: { ...F, q: 'timeout' }, enabled: false, onLogs: h.onLogs, onReset: h.onReset })
    await settle()
    expect(calls[1]?.signal.aborted).toBe(true)
    expect(h.result.current.state.kind).toBe('off')
    h.rerender({ slug: 'shop', filters: { ...F, q: 'timeout' }, enabled: true, onLogs: h.onLogs, onReset: h.onReset })
    await settle()
    h.unmount()
    expect(calls[2]?.signal.aborted).toBe(true)
  })
})
