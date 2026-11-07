// UTC formatting, histogram bucket helpers, brush math and the summary line.
// All pure and deterministic, so they are unit-tested on their own.

const pad = (n: number, w = 2) => String(n).padStart(w, '0')

/** "02:11:30" (UTC). */
export function clockUtc(ms: number): string {
  const d = new Date(ms)
  return `${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}:${pad(d.getUTCSeconds())}`
}

/** "02:11" (UTC). */
export function minuteUtc(ms: number): string {
  const d = new Date(ms)
  return `${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}`
}

/** "2026-11-07" (UTC). */
export function dateUtc(ms: number): string {
  const d = new Date(ms)
  return `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())}`
}

/** Row time: "02:16:58.412", with the date when the range spans days. */
export function rowTime(ms: number, withDate: boolean): string {
  const d = new Date(ms)
  const t = `${clockUtc(ms)}.${pad(d.getUTCMilliseconds(), 3)}`
  return withDate ? `${dateUtc(ms).slice(5)} ${t}` : t
}

/** The range heading: "02:11:30 to 02:17:00 UTC" (dates added when needed). */
export function rangeHeading(from: number, to: number, now: number): string {
  const sameDay = dateUtc(from) === dateUtc(to - 1)
  const today = dateUtc(now) === dateUtc(to - 1)
  if (sameDay && today) return `${clockUtc(from)} to ${clockUtc(to)} UTC`
  if (sameDay) return `${dateUtc(from)}, ${clockUtc(from)} to ${clockUtc(to)} UTC`
  return `${dateUtc(from)} ${clockUtc(from)} to ${dateUtc(to)} ${clockUtc(to)} UTC`
}

/** "30 s", "5 min", "1 h", "1 day". */
export function bucketLabel(seconds: number): string {
  if (seconds < 60) return `${seconds} s`
  if (seconds < 3600) return `${seconds / 60} min`
  if (seconds < 86400) return seconds === 3600 ? '1 h' : `${seconds / 3600} h`
  return seconds === 86400 ? '1 day' : `${seconds / 86400} days`
}

export type Bucket = { t: number; debug: number; info: number; warn: number; error: number; fatal: number }

export const bucketTotal = (b: Bucket) => b.debug + b.info + b.warn + b.error + b.fatal
export const bucketErrors = (b: Bucket) => b.error + b.fatal
/** Error plus fatal share of a bucket in percent (0 for an empty bucket). */
export const errorShare = (b: Bucket) => {
  const total = bucketTotal(b)
  return total === 0 ? 0 : (bucketErrors(b) / total) * 100
}

/** Evenly spaced axis ticks between from and to (inclusive). */
export function axisTicks(from: number, to: number, count = 6): number[] {
  const step = (to - from) / (count - 1)
  return Array.from({ length: count }, (_, i) => Math.round(from + i * step))
}

// ---- brush ----

export type BrushWindow = { from: number; to: number }

/** Time at a horizontal position (0..width) inside the window, clamped. */
export function pixelToTime(x: number, width: number, w: BrushWindow): number {
  const f = width <= 0 ? 0 : Math.min(1, Math.max(0, x / width))
  return w.from + f * (w.to - w.from)
}

/** Horizontal position (0..1) of a time inside the window, clamped. */
export function timeToFraction(t: number, w: BrushWindow): number {
  if (w.to <= w.from) return 0
  return Math.min(1, Math.max(0, (t - w.from) / (w.to - w.from)))
}

/**
 * The range a brush from a to b selects: ordered, widened to whole buckets
 * (start down, end up, aligned to the Unix epoch like the server's
 * buckets), at least minBuckets wide, and clamped to the window. Null when
 * the drag is too short to mean a selection.
 */
export function brushRange(a: number, b: number, bucketMs: number, w: BrushWindow, minBuckets = 2): BrushWindow | null {
  if (bucketMs <= 0) return null
  let from = Math.min(a, b)
  let to = Math.max(a, b)
  if (to - from < bucketMs / 2) return null // a click, not a drag
  from = Math.floor(from / bucketMs) * bucketMs
  to = Math.ceil(to / bucketMs) * bucketMs
  const min = minBuckets * bucketMs
  if (to - from < min) to = from + min
  if (to > w.to) {
    to = w.to
    from = Math.min(from, to - min)
  }
  if (from < w.from) from = w.from
  return to > from ? { from, to } : null
}

// ---- summary line ----

/**
 * A plain-language summary of the timeline: peak volume and when, peak
 * error share and when. Ties go to the earliest bucket, so the text is
 * deterministic for the same data.
 */
export function summarize(buckets: readonly Bucket[], bucketSeconds: number): string {
  let peak: Bucket | undefined
  let peakShare: Bucket | undefined
  let total = 0
  for (const b of buckets) {
    total += bucketTotal(b)
    if (!peak || bucketTotal(b) > bucketTotal(peak)) peak = b
    if (bucketTotal(b) > 0 && (!peakShare || errorShare(b) > errorShare(peakShare))) peakShare = b
  }
  if (!peak || total === 0) return 'No logs in this time range.'
  const per = bucketLabel(bucketSeconds)
  let s = `${total.toLocaleString('en-US')} logs. Peak volume ${bucketTotal(peak).toLocaleString('en-US')} logs per ${per} at ${clockUtc(peak.t)} UTC.`
  if (peakShare && bucketErrors(peakShare) > 0) {
    s += ` Peak error and fatal share ${Math.round(errorShare(peakShare))}% at ${clockUtc(peakShare.t)} UTC.`
  } else {
    s += ' No errors or fatal logs.'
  }
  return s
}
