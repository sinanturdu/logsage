import { describe, expect, it } from 'vitest'
import { DEGRADED_TEXT, OFF, backoff, describe as words, next, secondsLeft, type Failure, type LiveEvent, type LiveState } from './machine'

const T = Date.UTC(2026, 10, 7, 16, 40, 5)
const run = (events: LiveEvent[], from: LiveState = OFF) => events.reduce((s, e) => next(s, e), from)
const fail = (kind: Failure['kind'], extra: Partial<Failure> = {}, now = T): LiveEvent => ({ type: 'failed', error: { kind, status: 0, ...extra }, now })
const live = run([{ type: 'start' }, { type: 'opened', now: T }])

describe('backoff', () => {
  it('starts at the server retry, doubles and caps at 30 s', () => {
    expect([0, 1, 2, 3, 4, 9].map((n) => backoff(n))).toEqual([5000, 10000, 20000, 30000, 30000, 30000])
    expect(backoff(0, 2000)).toBe(2000)
  })
})

describe('next', () => {
  it('goes off, connecting, live, degraded, live, off', () => {
    let s = next(OFF, { type: 'start' })
    expect(s.kind).toBe('connecting')
    s = next(s, { type: 'opened', now: T })
    expect(s.kind).toBe('live')
    s = next(s, { type: 'status', state: 'degraded', now: T })
    expect(s.kind).toBe('degraded')
    s = next(s, { type: 'status', state: 'live', now: T })
    expect(s.kind).toBe('live')
    expect(next(s, { type: 'stop' })).toEqual(OFF)
  })

  it('backs off 5, 10, 20, 30, 30 s across repeated failures and never sooner', () => {
    let s: LiveState = run([{ type: 'start' }])
    const waits: number[] = []
    for (let i = 0; i < 5; i++) {
      s = next(s, fail('network', {}, T))
      if (s.kind !== 'reconnecting') throw new Error(s.kind)
      waits.push(s.retryAt - T)
      s = next(s, { type: 'retry', now: s.retryAt })
      expect(s.kind).toBe('connecting')
    }
    expect(waits).toEqual([5000, 10000, 20000, 30000, 30000])
  })

  it('resets the backoff once data flows again', () => {
    let s = run([fail('server', { status: 503 }), { type: 'retry', now: T }, fail('server', { status: 503 }), { type: 'retry', now: T }], live)
    expect(s).toMatchObject({ kind: 'connecting', attempt: 2 })
    s = run([{ type: 'opened', now: T }, { type: 'status', state: 'live', now: T }, { type: 'ended', now: T }], s)
    expect(s).toMatchObject({ kind: 'reconnecting', retryAt: T + 5000 })
  })

  it.each([
    ['unauthorized', 'your session ended. Sign in again to continue.'],
    ['not_found', 'this project was not found, or you are not a member of it.'],
    ['forbidden', 'you do not have permission to read this project.'],
  ] as const)('stops on %s', (kind, reason) => {
    expect(next(live, fail(kind))).toEqual({ kind: 'stopped', reason })
  })

  it('stops on 400 with the server message', () => {
    expect(next(live, fail('invalid', { status: 400, serverMessage: 'q must be at most 256 characters' }))).toEqual({
      kind: 'stopped',
      reason: 'q must be at most 256 characters. Change the filters to try again.',
    })
  })

  it('waits for Retry-After on 429 when it is longer than the backoff', () => {
    expect(next(live, fail('rate_limited', { status: 429, retryAfter: 12 }))).toMatchObject({ kind: 'reconnecting', retryAt: T + 12_000, reason: 'too many requests' })
    expect(next(live, fail('rate_limited', { status: 429, retryAfter: 1 }))).toMatchObject({ retryAt: T + 5000 })
  })

  it('backs off on 5xx, network errors and a stream that just ends', () => {
    expect(next(live, fail('server', { status: 503 }))).toMatchObject({ kind: 'reconnecting', reason: 'the LogSage API failed (status 503)' })
    expect(next(live, fail('network'))).toMatchObject({ kind: 'reconnecting', reason: 'the connection to LogSage was lost' })
    expect(next(live, { type: 'ended', now: T })).toMatchObject({ kind: 'reconnecting', reason: 'the connection was lost' })
  })

  it('reconnects silently after max_duration, but not in a loop', () => {
    const later = T + 15 * 60_000
    expect(next(live, { type: 'status', state: 'closed', reason: 'max_duration', now: later })).toEqual({ kind: 'connecting', attempt: 0 })
    expect(next(live, { type: 'status', state: 'closed', reason: 'max_duration', now: T + 100 })).toMatchObject({ kind: 'reconnecting', retryAt: T + 100 + 5000 })
  })

  it('handles the other close reasons', () => {
    const closed = (reason: string) => next(live, { type: 'status', state: 'closed', reason, now: T })
    expect(closed('session_ended')).toMatchObject({ kind: 'stopped' })
    expect(closed('access_revoked')).toEqual({ kind: 'stopped', reason: 'you no longer have access to this project.' })
    expect(closed('shutdown')).toMatchObject({ kind: 'reconnecting', reason: 'the server is restarting' })
  })

  it('ignores late events once off or stopped, and r only acts while reconnecting', () => {
    expect(next(OFF, { type: 'opened', now: T })).toEqual(OFF)
    const stopped = next(live, fail('not_found'))
    expect(next(stopped, fail('network'))).toEqual(stopped)
    expect(next(live, { type: 'retry', now: T })).toBe(live)
    expect(next(stopped, { type: 'start' }).kind).toBe('connecting')
  })
})

describe('words', () => {
  it('describes each state in text', () => {
    const r = next(live, fail('network'))
    expect(words(OFF, T)).toBe('')
    expect(words({ kind: 'connecting', attempt: 0 }, T)).toBe('Connecting')
    expect(words(live, T)).toBe('Live')
    expect(words({ kind: 'degraded', attempt: 0, openedAt: T }, T)).toBe(DEGRADED_TEXT)
    expect(words(r, T + 1200)).toBe('Live tail disconnected at 16:40:05: the connection to LogSage was lost. Reconnecting in 4 s, or press r to retry now.')
    expect(secondsLeft(r, T + 9000)).toBe(0)
  })
})
