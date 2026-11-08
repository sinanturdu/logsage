import { clockUtc, dateUtc } from '../explorer/time'

/** "2026-11-07 02:16:58 UTC". */
export function fullUtc(ms: number): string {
  return `${dateUtc(ms)} ${clockUtc(ms)} UTC`
}

/** "just now", "4 min ago", "3 h ago", "2 days ago"; a time ahead of now (clock skew) is "just now". */
export function relativeTime(ms: number, now: number): string {
  const s = Math.floor((now - ms) / 1000)
  if (s < 60) return 'just now'
  const m = Math.floor(s / 60)
  if (m < 60) return `${m} min ago`
  const h = Math.floor(m / 60)
  if (h < 48) return `${h} h ago`
  const d = Math.floor(h / 24)
  return `${d} days ago`
}
