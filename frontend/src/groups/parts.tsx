import { LEVEL_SHAPE, type Level } from '../explorer/model'
import type { Group } from './api'
import type { GroupStatus } from './model'
import { SPARK_H, SPARK_W, sparkLabel, sparkPath } from './sparkline'
import { fullUtc, relativeTime } from './time'

// Small pieces shared by the list and the detail page. Every value that
// came from a log (templates, services, notes) is rendered as text only.

const PLACEHOLDER = /(<(?:n|id|uuid|ip|ts|date|time|email|str|token|\*)>)/

/**
 * A template with its placeholders (<n>, <id>, <uuid>, <ip> and so on) as
 * muted spans. Split into text nodes only, never parsed as HTML.
 */
export function TemplateText({ text, className = 'template' }: { text: string; className?: string }) {
  const parts = text.split(PLACEHOLDER)
  return (
    <span className={className}>
      {parts.map((p, i) =>
        p === '' ? null : i % 2 === 1 ? (
          <span key={i} className="ph">
            {p}
          </span>
        ) : (
          p
        ),
      )}
    </span>
  )
}

/** Status shapes: distinct from the level shapes, always next to the word. */
export const STATUS_SHAPE: Record<GroupStatus, string> = { open: '□', ignored: '–', resolved: '✓' }
const STATUS_WORD: Record<GroupStatus, string> = { open: 'Open', ignored: 'Ignored', resolved: 'Resolved' }

export function StatusMark({ status }: { status: GroupStatus }) {
  return (
    <span className={`gstatus gstatus-${status}`}>
      <span className="shape" aria-hidden="true">
        {STATUS_SHAPE[status]}
      </span>{' '}
      {STATUS_WORD[status]}
    </span>
  )
}

/** "New" and "Regressed" as text badges (the word is the meaning). */
export function Flags({ group }: { group: Pick<Group, 'is_new' | 'regressed'> }) {
  if (!group.is_new && !group.regressed) return null
  return (
    <span className="flags">
      {group.is_new && <span className="badge badge-new">New</span>}
      {group.regressed && <span className="badge badge-regressed">Regressed</span>}
    </span>
  )
}

const isLevel = (l: string): l is Level => l in LEVEL_SHAPE

/** Level shapes with their words, in level order. */
export function Levels({ levels }: { levels: readonly string[] }) {
  const known = (['warn', 'error', 'fatal'] as const).filter((l) => levels.includes(l))
  const other = levels.filter((l) => !isLevel(l))
  return (
    <span className="glevels">
      {known.map((l) => (
        <span key={l} className={`level-${l}`}>
          <span className="shape" aria-hidden="true">
            {LEVEL_SHAPE[l]}
          </span>
          <span className="lv">{l}</span>
        </span>
      ))}
      {other.length > 0 && <span>{other.join(', ')}</span>}
    </span>
  )
}

/** The tiny trend line; its label states the peak in words. */
export function Sparkline({ values, from, bucketSeconds }: { values: readonly number[]; from: number; bucketSeconds: number }) {
  const label = sparkLabel(values, from, bucketSeconds)
  return (
    <svg className="spark" viewBox={`0 0 ${SPARK_W} ${SPARK_H}`} width={SPARK_W} height={SPARK_H} role="img" aria-label={label}>
      <title>{label}</title>
      <path d={sparkPath(values)} />
    </svg>
  )
}

/** A time as relative text, with the full UTC time in the title. */
export function When({ iso, now }: { iso: string; now: number }) {
  const ms = Date.parse(iso)
  if (!Number.isFinite(ms)) return <span>unknown</span>
  return (
    <time dateTime={iso} title={fullUtc(ms)}>
      {relativeTime(ms, now)}
    </time>
  )
}
