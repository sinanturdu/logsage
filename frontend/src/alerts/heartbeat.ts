// The evaluator's heartbeat in words (K2.2): when a rule was last checked,
// and whether it is overdue, which usually means the evaluator (cmd/alerts)
// is not running. Pure: the caller passes the time.

import { relativeTime } from '../groups/time'
import { parseGoDuration } from './model'

export type EvalError = '' | 'query_timeout' | 'query_rejected' | 'store_unavailable' | 'unknown'
export type Heartbeat = { enabled: boolean; interval: string; last_evaluated_at: string | null; last_eval_error: EvalError }
export type CheckStatus =
  | { kind: 'disabled' }
  | { kind: 'never'; text: string }
  | { kind: 'stale'; text: string }
  | { kind: 'checked'; text: string; failed: string }

export const NOT_CHECKED_YET = 'Not checked yet'
export const NOT_BEING_CHECKED = 'Not being checked. Is the evaluator running?'
/** A rule is overdue after three intervals, and never sooner than this. */
export const MIN_OVERDUE_MS = 120_000

const FAILED: Record<Exclude<EvalError, ''>, string> = {
  query_timeout: 'The last check failed: the query timed out.',
  query_rejected: 'The last check failed: the log store rejected the query.',
  store_unavailable: 'The last check failed: the log store was not reachable.',
  unknown: 'The last check failed.',
}

/** "Checked 20 s ago", "Checked 4 min ago". */
export function checkedAgo(ms: number, now: number): string {
  const s = Math.max(0, Math.floor((now - ms) / 1000))
  return s < 60 ? `Checked ${s} s ago` : `Checked ${relativeTime(ms, now)}`
}

export function checkStatus(r: Heartbeat, now: number): CheckStatus {
  if (!r.enabled) return { kind: 'disabled' }
  const last = r.last_evaluated_at ? Date.parse(r.last_evaluated_at) : NaN
  if (!Number.isFinite(last)) return { kind: 'never', text: NOT_CHECKED_YET }
  const interval = (parseGoDuration(r.interval) ?? 60) * 1000
  if (now - last > Math.max(3 * interval, MIN_OVERDUE_MS)) return { kind: 'stale', text: NOT_BEING_CHECKED }
  return { kind: 'checked', text: checkedAgo(last, now), failed: r.last_eval_error ? FAILED[r.last_eval_error] : '' }
}
