import { describe, expect, it } from 'vitest'
import { sparkLabel, sparkPath, sparkPeak } from './sparkline'
import { relativeTime, fullUtc } from './time'

const from = Date.UTC(2026, 10, 7, 20, 0)

describe('sparkline', () => {
  it('draws the buckets left to right inside the box', () => {
    expect(sparkPath([0, 10, 5], 100, 20)).toBe('M0.0 19.0 L50.0 1.0 L100.0 10.0')
  })

  it('is empty without buckets and flat for one bucket', () => {
    expect(sparkPath([])).toBe('')
    expect(sparkPath([7], 96, 20)).toBe('M0 1.0 L96 1.0')
    expect(sparkPath([0, 0], 10, 20)).toBe('M0.0 19.0 L10.0 19.0')
  })

  it('finds the first highest bucket', () => {
    expect(sparkPeak([1, 340, 2, 340])).toEqual({ index: 1, value: 340 })
    expect(sparkPeak([0, 0])).toBeNull()
    expect(sparkPeak([])).toBeNull()
  })

  it('labels the peak with its bucket time', () => {
    const v = Array.from({ length: 24 }, (_, i) => (i === 23 ? 340 : 1))
    expect(sparkLabel(v, from, 150)).toBe('Trend, peak 340 events at 20:57 UTC')
    expect(sparkLabel([0, 1], from, 60)).toBe('Trend, peak 1 event at 20:01 UTC')
    expect(sparkLabel([0, 0], from, 60)).toBe('Trend, no events in this range')
    expect(sparkLabel([5, 1], from, 86_400)).toBe('Trend, peak 5 events at 2026-11-07 20:00 UTC')
  })
})

describe('group times', () => {
  const now = Date.UTC(2026, 10, 7, 21, 0)
  it.each([
    [now + 5000, 'just now'],
    [now - 30_000, 'just now'],
    [now - 4 * 60_000, '4 min ago'],
    [now - 3 * 3_600_000, '3 h ago'],
    [now - 5 * 86_400_000, '5 days ago'],
  ])('%s is %s', (t, want) => {
    expect(relativeTime(t, now)).toBe(want)
  })
  it('prints the full UTC time', () => {
    expect(fullUtc(Date.UTC(2026, 10, 7, 2, 16, 58))).toBe('2026-11-07 02:16:58 UTC')
  })
})
