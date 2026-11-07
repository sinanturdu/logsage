import { describe, expect, it } from 'vitest'
import { axisTicks, brushRange, bucketLabel, pixelToTime, rangeHeading, rowTime, summarize, timeToFraction, type Bucket } from './time'

const T = (s: string) => Date.parse(s)
const W = { from: T('2026-11-07T02:02:00Z'), to: T('2026-11-07T02:17:00Z') }
const b = (t: string, info: number, error = 0, fatal = 0): Bucket => ({ t: T(t), debug: 0, info, warn: 0, error, fatal })

describe('formatting', () => {
  it('shows UTC times', () => {
    expect(rowTime(T('2026-11-07T02:16:58.412Z'), false)).toBe('02:16:58.412')
    expect(rowTime(T('2026-11-07T02:16:58.012Z'), true)).toBe('11-07 02:16:58.012')
    expect(rangeHeading(T('2026-11-07T02:11:30Z'), T('2026-11-07T02:17:00Z'), T('2026-11-07T02:17:05Z'))).toBe('02:11:30 to 02:17:00 UTC')
    expect(rangeHeading(T('2026-11-05T02:11:30Z'), T('2026-11-05T03:00:00Z'), T('2026-11-07T00:00:00Z'))).toBe('2026-11-05, 02:11:30 to 03:00:00 UTC')
    expect(rangeHeading(T('2026-11-05T23:00:00Z'), T('2026-11-06T01:00:00Z'), T('2026-11-07T00:00:00Z'))).toBe('2026-11-05 23:00:00 to 2026-11-06 01:00:00 UTC')
  })

  it('labels buckets', () => {
    expect([10, 30, 60, 300, 900, 3600, 21600, 86400].map(bucketLabel)).toEqual(['10 s', '30 s', '1 min', '5 min', '15 min', '1 h', '6 h', '1 day'])
  })

  it('spaces axis ticks evenly', () => {
    expect(axisTicks(0, 1000, 6)).toEqual([0, 200, 400, 600, 800, 1000])
  })
})

describe('brush math', () => {
  it('maps pixels to time and back, clamped', () => {
    expect(pixelToTime(0, 900, W)).toBe(W.from)
    expect(pixelToTime(900, 900, W)).toBe(W.to)
    expect(pixelToTime(450, 900, W)).toBe(W.from + 7.5 * 60_000)
    expect(pixelToTime(-20, 900, W)).toBe(W.from)
    expect(pixelToTime(5000, 900, W)).toBe(W.to)
    expect(timeToFraction(W.from + 3 * 60_000, W)).toBeCloseTo(0.2)
    expect(timeToFraction(W.to + 1, W)).toBe(1)
  })

  it('snaps to whole buckets, outward', () => {
    const r = brushRange(T('2026-11-07T02:11:41Z'), T('2026-11-07T02:14:07Z'), 30_000, W)
    expect(r).toEqual({ from: T('2026-11-07T02:11:30Z'), to: T('2026-11-07T02:14:30Z') })
    // Dragged right to left gives the same range.
    expect(brushRange(T('2026-11-07T02:14:07Z'), T('2026-11-07T02:11:41Z'), 30_000, W)).toEqual(r)
  })

  it('enforces the minimum span and ignores clicks', () => {
    expect(brushRange(T('2026-11-07T02:10:00Z'), T('2026-11-07T02:10:05Z'), 30_000, W)).toBeNull() // under half a bucket
    expect(brushRange(T('2026-11-07T02:10:00Z'), T('2026-11-07T02:10:20Z'), 30_000, W)).toEqual({
      from: T('2026-11-07T02:10:00Z'),
      to: T('2026-11-07T02:11:00Z'), // two buckets
    })
  })

  it('clamps to the window', () => {
    expect(brushRange(T('2026-11-07T02:16:50Z'), T('2026-11-07T02:20:00Z'), 30_000, W)).toEqual({
      from: T('2026-11-07T02:16:00Z'), // keeps the minimum span inside the window
      to: W.to,
    })
    expect(brushRange(T('2026-11-07T01:00:00Z'), T('2026-11-07T02:03:00Z'), 30_000, W)).toEqual({ from: W.from, to: T('2026-11-07T02:03:00Z') })
  })
})

describe('summary line', () => {
  it('names the peak volume and the peak error share, earliest on ties', () => {
    const buckets = [
      b('2026-11-07T02:15:00Z', 200, 2),
      b('2026-11-07T02:15:30Z', 300, 150, 10),
      b('2026-11-07T02:16:00Z', 500, 241),
      b('2026-11-07T02:16:30Z', 500, 100),
    ]
    expect(summarize(buckets, 30)).toBe(
      'Peak volume 741 logs per 30 s at 02:16:00 UTC. Peak error and fatal share 35% at 02:15:30 UTC.',
    )
    expect(summarize(buckets, 30)).toBe(summarize([...buckets], 30)) // deterministic
  })

  it('handles empty data and no errors', () => {
    expect(summarize([], 10)).toBe('No logs in this time range.')
    expect(summarize([b('2026-11-07T02:15:00Z', 0)], 10)).toBe('No logs in this time range.')
    expect(summarize([b('2026-11-07T02:15:00Z', 12)], 60)).toBe('Peak volume 12 logs per 1 min at 02:15:00 UTC. No errors or fatal logs.')
  })
})
