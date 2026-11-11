import { useEffect } from 'react'
import { useQueryClient, type QueryKey } from '@tanstack/react-query'
import { ApiError } from './api/client'

// One poll for the header counts (Alerts and Incidents, ADR 0027): a single
// timer refetches every listed query every 30 s while the tab is visible,
// refetches at once when it is shown again, waits for the longest
// Retry-After after a 429, and leaves out a query whose server said the
// feature is off.

export const NAV_POLL_MS = 30_000

/** Whether polling should stop for this error (the alerts "turned off" 503). */
const isOff = (err: unknown) => err instanceof ApiError && err.status === 503 && /^alerts are /.test(err.serverMessage ?? '')

export function useSharedPoll(keys: readonly QueryKey[], enabled: boolean) {
  const qc = useQueryClient()
  const id = JSON.stringify(keys)
  useEffect(() => {
    if (!enabled) return
    const list = JSON.parse(id) as QueryKey[]
    let timer: ReturnType<typeof setTimeout> | undefined
    let stopped = false
    const schedule = (ms: number) => {
      clearTimeout(timer)
      timer = setTimeout(() => void tick(), ms)
    }
    const tick = async () => {
      if (stopped) return
      if (document.visibilityState === 'hidden') return // resumed by visibilitychange
      const live = list.filter((k) => !isOff(qc.getQueryState(k)?.error))
      await Promise.all(live.map((k) => qc.refetchQueries({ queryKey: k, exact: true })))
      if (stopped) return
      let wait = NAV_POLL_MS
      for (const k of live) {
        const err = qc.getQueryState(k)?.error
        if (err instanceof ApiError && err.kind === 'rate_limited') wait = Math.max(wait, (err.retryAfter ?? 1) * 1000)
      }
      if (live.some((k) => !isOff(qc.getQueryState(k)?.error))) schedule(wait)
    }
    const onVisible = () => {
      if (document.visibilityState === 'visible') {
        clearTimeout(timer)
        void tick()
      }
    }
    schedule(NAV_POLL_MS)
    document.addEventListener('visibilitychange', onVisible)
    return () => {
      stopped = true
      clearTimeout(timer)
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, [qc, id, enabled])
}
