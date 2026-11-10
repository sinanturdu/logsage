// The live tail state machine (ADR 0019). Pure: the hook feeds it events
// and the current time, and schedules the retry it asks for.

import type { ApiErrorKind } from '../api/client'
import { clockUtc } from '../explorer/time'

export const BASE_RETRY_MS = 5000
export const MAX_RETRY_MS = 30_000

export type LiveState =
  | { kind: 'off' }
  | { kind: 'connecting'; attempt: number }
  | { kind: 'live'; attempt: number; openedAt: number }
  | { kind: 'degraded'; attempt: number; openedAt: number }
  | { kind: 'reconnecting'; attempt: number; since: number; retryAt: number; reason: string }
  | { kind: 'stopped'; reason: string }

export type Failure = { kind: ApiErrorKind; status: number; retryAfter?: number | undefined; serverMessage?: string | undefined }

export type LiveEvent =
  | { type: 'start' }
  | { type: 'stop' }
  | { type: 'opened'; now: number }
  | { type: 'status'; state: 'live' | 'degraded' | 'closed'; reason?: string | undefined; now: number }
  | { type: 'ended'; now: number } // the stream ended without a closed status
  | { type: 'failed'; error: Failure; now: number }
  | { type: 'retry'; now: number } // the countdown ran out, or the user pressed r

export const OFF: LiveState = { kind: 'off' }

/** The wait before attempt n (0-based): the server's retry, doubled each time, capped. */
export function backoff(attempt: number, base = BASE_RETRY_MS): number {
  return Math.min(base * 2 ** Math.max(0, attempt), MAX_RETRY_MS)
}

const attemptOf = (s: LiveState) => ('attempt' in s ? s.attempt : 0)

function reconnect(s: LiveState, now: number, reason: string, base: number, minDelay = 0): LiveState {
  const attempt = attemptOf(s)
  return { kind: 'reconnecting', attempt: attempt + 1, since: now, retryAt: now + Math.max(backoff(attempt, base), minDelay), reason }
}

const STOP = {
  session: 'your session ended. Sign in again to continue.',
  revoked: 'you no longer have access to this project.',
  notFound: 'this project was not found, or you are not a member of it.',
  forbidden: 'you do not have permission to read this project.',
}

/**
 * The next state. `base` is the server's retry value (5 s by default). A
 * failure never reconnects sooner than the backoff for its attempt, and a
 * stream that was live resets the attempt count.
 */
export function next(s: LiveState, e: LiveEvent, base = BASE_RETRY_MS): LiveState {
  switch (e.type) {
    case 'start':
      return s.kind === 'off' || s.kind === 'stopped' ? { kind: 'connecting', attempt: 0 } : s
    case 'stop':
      return OFF
  }
  if (s.kind === 'off' || s.kind === 'stopped') return s // late events of a closed stream
  switch (e.type) {
    case 'opened':
      return s.kind === 'connecting' ? { kind: 'live', attempt: s.attempt, openedAt: e.now } : s
    case 'retry':
      return s.kind === 'reconnecting' ? { kind: 'connecting', attempt: s.attempt } : s
    case 'status': {
      if (s.kind !== 'live' && s.kind !== 'degraded') return s
      // Data flows again: the backoff starts over.
      if (e.state === 'live') return { kind: 'live', attempt: 0, openedAt: s.openedAt }
      if (e.state === 'degraded') return { kind: 'degraded', attempt: 0, openedAt: s.openedAt }
      switch (e.reason) {
        case 'max_duration':
          // Expected: reconnect silently with Last-Event-ID, unless the stream
          // lasted less than the backoff (then it is a loop and waits).
          return e.now - s.openedAt >= base ? { kind: 'connecting', attempt: 0 } : reconnect(s, e.now, 'the stream was closed by the server', base)
        case 'session_ended':
          return { kind: 'stopped', reason: STOP.session }
        case 'access_revoked':
          return { kind: 'stopped', reason: STOP.revoked }
        case 'shutdown':
          return reconnect(s, e.now, 'the server is restarting', base)
        default:
          return reconnect(s, e.now, 'the stream was closed by the server', base)
      }
    }
    case 'ended':
      return reconnect(s, e.now, 'the connection was lost', base)
    case 'failed': {
      const f = e.error
      switch (f.kind) {
        case 'unauthorized':
          return { kind: 'stopped', reason: STOP.session }
        case 'not_found':
          return { kind: 'stopped', reason: STOP.notFound }
        case 'forbidden':
        case 'csrf': // never for this GET; stop as for forbidden
          return { kind: 'stopped', reason: STOP.forbidden }
        case 'invalid':
        case 'conflict':
          return { kind: 'stopped', reason: invalidReason(f.serverMessage) }
        case 'rate_limited':
          return reconnect(s, e.now, 'too many requests', base, (f.retryAfter ?? 1) * 1000)
        case 'server':
          return reconnect(s, e.now, `the LogSage API failed (status ${f.status})`, base)
        case 'network':
          return reconnect(s, e.now, 'the connection to LogSage was lost', base)
      }
    }
  }
}

function invalidReason(msg: string | undefined): string {
  const m = msg ? msg.replace(/[.\s]+$/, '') : 'the filters are not valid'
  return `${m}. Change the filters to try again.`
}

/** Seconds left until the retry, rounded up, never negative. */
export const secondsLeft = (s: LiveState, now: number) => (s.kind === 'reconnecting' ? Math.max(0, Math.ceil((s.retryAt - now) / 1000)) : 0)

export const LIVE_TEXT = 'Receiving new logs'
export const DEGRADED_TEXT = 'Live tail degraded: the log store is slow to respond. Showing what arrived, retrying.'

/** The state in words, for the status line; color is never the only signal. */
export function describe(s: LiveState, now: number): string {
  switch (s.kind) {
    case 'off':
      return ''
    case 'connecting':
      return 'Connecting'
    case 'live':
      // The button already says Live; the status says what is happening.
      return LIVE_TEXT
    case 'degraded':
      return DEGRADED_TEXT
    case 'reconnecting':
      return `Live tail disconnected at ${clockUtc(s.since)}: ${s.reason}. Reconnecting in ${secondsLeft(s, now)} s, or press r to retry now.`
    case 'stopped':
      return `Live tail stopped: ${s.reason}`
  }
}
