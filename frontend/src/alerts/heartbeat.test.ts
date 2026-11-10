import { describe, expect, it } from 'vitest'
import { MIN_OVERDUE_MS, NOT_BEING_CHECKED, NOT_CHECKED_YET, checkStatus, checkedAgo } from './heartbeat'

// A fixed clock: 2026-11-10 12:00:00 UTC.
const NOW = Date.UTC(2026, 10, 10, 12)
const at = (msBefore: number) => new Date(NOW - msBefore).toISOString()
const rule = (over: Partial<Parameters<typeof checkStatus>[0]> = {}) => ({ enabled: true, interval: '1m0s', last_evaluated_at: at(20_000), last_eval_error: '' as const, ...over })

describe('checkStatus', () => {
  it('says when a rule was last checked', () => {
    expect(checkStatus(rule(), NOW)).toEqual({ kind: 'checked', text: 'Checked 20 s ago', failed: '' })
    expect(checkStatus(rule({ last_evaluated_at: at(150_000) }), NOW)).toEqual({ kind: 'checked', text: 'Checked 2 min ago', failed: '' })
    expect(checkStatus(rule({ last_eval_error: 'store_unavailable' }), NOW)).toMatchObject({ failed: 'The last check failed: the log store was not reachable.' })
  })

  it('is overdue after three intervals, and never sooner than two minutes', () => {
    // 1 minute interval: overdue after 3 minutes.
    expect(checkStatus(rule({ last_evaluated_at: at(180_000) }), NOW).kind).toBe('checked')
    expect(checkStatus(rule({ last_evaluated_at: at(180_001) }), NOW)).toEqual({ kind: 'stale', text: NOT_BEING_CHECKED })
    // 30 s interval: three intervals is 90 s, but the floor is 2 minutes.
    expect(checkStatus(rule({ interval: '30s', last_evaluated_at: at(MIN_OVERDUE_MS) }), NOW).kind).toBe('checked')
    expect(checkStatus(rule({ interval: '30s', last_evaluated_at: at(MIN_OVERDUE_MS + 1) }), NOW).kind).toBe('stale')
    // 1 hour interval: 3 hours.
    expect(checkStatus(rule({ interval: '1h0m0s', last_evaluated_at: at(2 * 3_600_000) }), NOW).kind).toBe('checked')
    expect(checkStatus(rule({ interval: '1h0m0s', last_evaluated_at: at(3 * 3_600_000 + 1) }), NOW).kind).toBe('stale')
  })

  it('a rule never evaluated is "not checked yet"; a disabled rule is never overdue', () => {
    expect(checkStatus(rule({ last_evaluated_at: null }), NOW)).toEqual({ kind: 'never', text: NOT_CHECKED_YET })
    expect(checkStatus(rule({ enabled: false, last_evaluated_at: at(86_400_000) }), NOW)).toEqual({ kind: 'disabled' })
  })

  it('formats the age', () => {
    expect(checkedAgo(NOW - 999, NOW)).toBe('Checked 0 s ago')
    expect(checkedAgo(NOW - 59_000, NOW)).toBe('Checked 59 s ago')
    expect(checkedAgo(NOW + 5_000, NOW)).toBe('Checked 0 s ago') // clock skew
  })
})
