// The list's tiny trend line: pure functions, unit-tested on their own.

import { dateUtc, minuteUtc } from '../explorer/time'

export const SPARK_W = 96
export const SPARK_H = 20

/** The highest bucket (the first one on a tie), or null when every bucket is 0. */
export function sparkPeak(values: readonly number[]): { index: number; value: number } | null {
  let best: { index: number; value: number } | null = null
  values.forEach((v, index) => {
    if (v > 0 && (best === null || v > best.value)) best = { index, value: v }
  })
  return best
}

/**
 * An SVG path through the buckets, scaled to the box with a 1px margin.
 * One bucket is a flat line across the box; no buckets is an empty path.
 */
export function sparkPath(values: readonly number[], w = SPARK_W, h = SPARK_H): string {
  if (values.length === 0) return ''
  const max = Math.max(1, ...values)
  const y = (v: number) => (h - 1 - (Math.max(0, v) / max) * (h - 2)).toFixed(1)
  if (values.length === 1) return `M0 ${y(values[0] ?? 0)} L${w} ${y(values[0] ?? 0)}`
  const step = w / (values.length - 1)
  return values.map((v, i) => `${i === 0 ? 'M' : 'L'}${(i * step).toFixed(1)} ${y(v)}`).join(' ')
}

/** "Trend, peak 340 events at 20:58 UTC" (the peak bucket's start). */
export function sparkLabel(values: readonly number[], from: number, bucketSeconds: number): string {
  const peak = sparkPeak(values)
  if (!peak) return 'Trend, no events in this range'
  const at = from + peak.index * bucketSeconds * 1000
  const when = values.length * bucketSeconds > 86_400 ? `${dateUtc(at)} ${minuteUtc(at)}` : minuteUtc(at)
  return `Trend, peak ${peak.value.toLocaleString('en-US')} ${peak.value === 1 ? 'event' : 'events'} at ${when} UTC`
}
