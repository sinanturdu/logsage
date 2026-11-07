import { useCallback, useEffect, useReducer, useRef, useState } from 'react'
import { ApiError } from '../api/client'
import type { LogEvent } from '../explorer/api'
import { BASE_RETRY_MS, OFF, next, type LiveEvent, type LiveState } from './machine'
import { streamTail, tailUrl, type TailFilters } from './sse'

type Machine = { s: LiveState; gen: number; base: number; url: string }

/** Wraps the pure machine; gen counts connection attempts so each one gets its own effect. */
function reducer(m: Machine, e: LiveEvent | { type: 'base'; ms: number } | { type: 'open'; url: string }): Machine {
  if (e.type === 'base') return { ...m, base: e.ms }
  // The URL travels with start, so a filter change starts exactly one stream.
  const url = e.type === 'open' ? e.url : m.url
  const s = next(m.s, e.type === 'open' ? { type: 'start' } : e, m.base)
  if (s === m.s && url === m.url) return m
  return { s, base: m.base, url, gen: s.kind === 'connecting' && m.s.kind !== 'connecting' ? m.gen + 1 : m.gen }
}

const frame = (fn: () => void): (() => void) => {
  if (typeof requestAnimationFrame === 'function') {
    const id = requestAnimationFrame(fn)
    return () => cancelAnimationFrame(id)
  }
  const id = setTimeout(fn, 16)
  return () => clearTimeout(id)
}

type Options = {
  slug: string
  filters: TailFilters
  enabled: boolean
  /** New logs, batched per animation frame, in arrival order (oldest first). */
  onLogs: (batch: LogEvent[]) => void
  /** The server reset the cursor: refetch the first page. */
  onReset: () => void
}

/**
 * Runs the live tail while enabled (ADR 0019). Every change of slug or
 * filters, turning it off and unmounting abort the stream at once. The
 * cursor (Last-Event-ID) survives reconnects of the same filters.
 */
export function useLiveTail({ slug, filters, enabled, onLogs, onReset }: Options) {
  const [m, dispatch] = useReducer(reducer, { s: OFF, gen: 0, base: BASE_RETRY_MS, url: '' })
  const url = tailUrl(slug, filters)
  const lastId = useRef('')
  const handlers = useRef({ onLogs, onReset })
  useEffect(() => {
    handlers.current = { onLogs, onReset }
  })

  // Start and stop; a new URL is a new stream with a fresh cursor.
  useEffect(() => {
    lastId.current = ''
    if (!enabled) return
    dispatch({ type: 'open', url })
    return () => dispatch({ type: 'stop' })
  }, [enabled, url])

  const active = m.s.kind === 'connecting' || m.s.kind === 'live' || m.s.kind === 'degraded'
  useEffect(() => {
    if (!active) return
    const ac = new AbortController()
    let queue: LogEvent[] = []
    let cancelFrame: (() => void) | null = null
    let closedByStatus = false
    const flush = () => {
      cancelFrame = null
      const batch = queue
      queue = []
      if (batch.length > 0) handlers.current.onLogs(batch)
    }
    streamTail({
      url: m.url,
      lastEventId: lastId.current,
      signal: ac.signal,
      onOpen: () => dispatch({ type: 'opened', now: Date.now() }),
      onMessage: (msg) => {
        if (msg.id) lastId.current = msg.id
        let data: unknown
        try {
          data = JSON.parse(msg.data)
        } catch {
          return // a malformed event is skipped, never shown
        }
        if (msg.event === 'log') {
          queue.push(data as LogEvent)
          cancelFrame ??= frame(flush)
        } else if (msg.event === 'status' && data && typeof data === 'object') {
          const d = data as { state?: string; reason?: string }
          if (d.state === 'live' || d.state === 'degraded' || d.state === 'closed') {
            if (d.state === 'closed') {
              closedByStatus = true
              flush()
            }
            dispatch({ type: 'status', state: d.state, reason: d.reason, now: Date.now() })
          }
        } else if (msg.event === 'reset') {
          queue = []
          handlers.current.onReset()
        }
      },
    }).then(
      (end) => {
        if (end.retry !== undefined) dispatch({ type: 'base', ms: end.retry })
        if (end.kind === 'eof') {
          flush()
          if (!closedByStatus) dispatch({ type: 'ended', now: Date.now() })
        }
      },
      (err: unknown) => {
        if (ac.signal.aborted) return
        const f = err instanceof ApiError ? err : new ApiError('network', 0)
        dispatch({ type: 'failed', error: { kind: f.kind, status: f.status, retryAfter: f.retryAfter, serverMessage: f.serverMessage }, now: Date.now() })
      },
    )
    return () => {
      ac.abort()
      cancelFrame?.()
    }
    // One stream per attempt (gen); a new URL arrives with a new attempt.
  }, [active, m.gen, m.url])

  // The countdown: tick every second, retry when it runs out.
  const [now, setNow] = useState(() => Date.now())
  const retryAt = m.s.kind === 'reconnecting' ? m.s.retryAt : null
  useEffect(() => {
    if (retryAt === null) return
    const first = setTimeout(() => setNow(Date.now()), 0)
    const tick = setInterval(() => setNow(Date.now()), 1000)
    const fire = setTimeout(() => dispatch({ type: 'retry', now: Date.now() }), Math.max(0, retryAt - Date.now()))
    return () => {
      clearTimeout(first)
      clearInterval(tick)
      clearTimeout(fire)
    }
  }, [retryAt])

  const retryNow = useCallback(() => dispatch({ type: 'retry', now: Date.now() }), [])
  return { state: m.s, now, retryNow }
}
