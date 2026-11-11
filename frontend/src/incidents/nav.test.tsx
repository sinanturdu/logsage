import { act, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { NAV_POLL_MS } from '../poll'
import { json, renderApp } from '../test/util'
import { B, incident, mockIncidents } from './testing'

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true })
})

describe('header counts share one poll', () => {
  it('"Incidents, 2 open" and the Alerts count refresh together, pause while hidden and wait for a 429', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    let limited = false
    const { requests, state } = mockIncidents({
      incidents: [incident(1), incident(2), incident(3, { status: 'acknowledged' })],
      routes: { [`GET ${B}/summary`]: () => (limited ? json(429, { error: 'slow' }, { 'Retry-After': '75' }) : json(200, { open: state.incidents.filter((i) => i.status === 'open').length, acknowledged: 0, resolved: 0 })) },
    })
    renderApp('/projects/shop/logs')
    expect(await screen.findByRole('link', { name: 'Incidents, 2 open' })).toBeTruthy()
    const polls = (p: string) => requests.filter((r) => r.path === p).length
    const both = () => [polls(`${B}/summary`), polls('/api/projects/shop/alerts/rules')]
    expect(both()).toEqual([1, 1])
    const first = state.incidents[0]
    if (first) first.status = 'resolved'
    await act(() => vi.advanceTimersByTimeAsync(NAV_POLL_MS))
    expect(await screen.findByRole('link', { name: 'Incidents, 1 open' })).toBeTruthy()
    expect(both()).toEqual([2, 2]) // one tick, both counts

    Object.defineProperty(document, 'visibilityState', { value: 'hidden', configurable: true })
    document.dispatchEvent(new Event('visibilitychange', { bubbles: true }))
    await act(() => vi.advanceTimersByTimeAsync(NAV_POLL_MS * 3))
    expect(both()).toEqual([2, 2])
    Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true })
    document.dispatchEvent(new Event('visibilitychange', { bubbles: true }))
    await act(() => vi.advanceTimersByTimeAsync(0))
    expect(both()).toEqual([3, 3]) // at once when shown again

    limited = true
    await act(() => vi.advanceTimersByTimeAsync(NAV_POLL_MS))
    expect(both()).toEqual([4, 4])
    await act(() => vi.advanceTimersByTimeAsync(NAV_POLL_MS * 2))
    expect(both()).toEqual([4, 4]) // the 75 s Retry-After holds the one timer
    await act(() => vi.advanceTimersByTimeAsync(15_000))
    expect(both()).toEqual([5, 5])
  })

  it('says "Incidents" with nothing open', async () => {
    mockIncidents({ incidents: [incident(1, { status: 'resolved' })] })
    renderApp('/projects/shop/logs')
    expect(await screen.findByRole('link', { name: 'Incidents' })).toBeTruthy()
  })
})
