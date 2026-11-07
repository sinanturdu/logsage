import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { MAX_ROWS, type LogEvent } from '../explorer/api'
import type { TailFilters } from './sse'
import { useLiveTail } from './useLiveTail'

export const ANNOUNCE_EVERY_MS = 5000

/** Newest first, without duplicate event ids, capped (the oldest drop off). */
export function mergeNewest(top: readonly LogEvent[], rest: readonly LogEvent[], max = MAX_ROWS): LogEvent[] {
  const seen = new Set<string>()
  const out: LogEvent[] = []
  for (const e of [...top, ...rest]) {
    if (seen.has(e.event_id)) continue
    seen.add(e.event_id)
    out.push(e)
    if (out.length >= max) break
  }
  return out
}

const ts = (e: LogEvent) => {
  const t = Date.parse(e.timestamp)
  return Number.isFinite(t) ? t : 0
}

/** Sorts in place by event time, newest first; equal times keep their order. */
export function sortNewest(list: LogEvent[]): LogEvent[] {
  return list.sort((a, b) => ts(b) - ts(a))
}

export const countText = (n: number) => `${n.toLocaleString('en-US')} new ${n === 1 ? 'log' : 'logs'}`

type Options = {
  slug: string
  filters: TailFilters
  live: boolean
  /** The reader is busy (a detail open, a selection below the top, scrolled away). */
  busy: boolean
  /** The list from /logs, newest first. */
  base: readonly LogEvent[]
  /** Changes when the list's query changes; live rows of an older list are dropped. */
  listKey: string
  onReset: () => void
}

/**
 * The live part of the list (ADR 0019): arrivals go on top, or into a
 * buffer while the reader is busy; screen readers get one polite summary
 * at most every 5 seconds.
 */
export function useLiveList({ slug, filters, live, busy, base, listKey, onReset }: Options) {
  const [rows, setRows] = useState<LogEvent[]>([])
  const [buffer, setBuffer] = useState<LogEvent[]>([])
  const [announcement, setAnnouncement] = useState('')
  const [key, setKey] = useState(listKey)
  if (key !== listKey) {
    setKey(listKey)
    setRows([])
    setBuffer([])
  }

  const paused = live && (busy || buffer.length > 0)
  // Live: latest arrivals first (each batch sorted). Off: strict newest first again.
  const events = useMemo(() => {
    const merged = mergeNewest(rows, base)
    return live ? merged : sortNewest([...merged])
  }, [rows, base, live])

  const known = useRef<Set<string>>(new Set())
  const pausedRef = useRef(paused)
  useEffect(() => {
    known.current = new Set([...events, ...buffer].map((e) => e.event_id))
    pausedRef.current = paused
  })
  const arrived = useRef(0)

  const onLogs = useCallback((batch: LogEvent[]) => {
    const fresh: LogEvent[] = []
    for (const e of batch) {
      if (known.current.has(e.event_id)) continue // a redelivery or already listed
      known.current.add(e.event_id)
      fresh.push(e)
    }
    // Arrival order is visibility order, not event time: each batch goes on
    // top sorted by timestamp, newest first.
    sortNewest(fresh)
    if (fresh.length === 0) return
    arrived.current += fresh.length
    if (pausedRef.current) setBuffer((b) => mergeNewest(fresh, b))
    else setRows((r) => mergeNewest(fresh, r))
  }, [])

  const reset = useCallback(() => {
    setRows([])
    setBuffer([])
    onReset()
  }, [onReset])

  const tail = useLiveTail({ slug, filters, enabled: live, onLogs, onReset: reset })

  useEffect(() => {
    if (!live) return
    const id = setInterval(() => {
      if (arrived.current === 0) return
      setAnnouncement(countText(arrived.current))
      arrived.current = 0
    }, ANNOUNCE_EVERY_MS)
    return () => {
      clearInterval(id)
      arrived.current = 0
    }
  }, [live])

  /** Inserts the buffered logs on top (the caller clears what made it busy). */
  const showBuffered = useCallback(() => {
    setRows((r) => mergeNewest(buffer, r))
    setBuffer([])
  }, [buffer])

  const fresh = useMemo(() => new Set(rows.map((e) => e.event_id)), [rows])
  return { events, buffered: buffer.length, paused, showBuffered, announcement, fresh, tail }
}
