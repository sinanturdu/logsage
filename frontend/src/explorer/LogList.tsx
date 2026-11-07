import { useEffect, useRef, type KeyboardEvent } from 'react'
import { LogText } from '../components/LogText'
import { LEVEL_SHAPE } from './model'
import type { LogEvent } from './api'
import { rowTime } from './time'
import { RowDetail } from './RowDetail'

export type Expanded = { index: number; shown: LogEvent } | null

type Props = {
  slug: string
  events: LogEvent[]
  selected: number | null
  expanded: Expanded
  onSelect: (i: number | null) => void
  onToggle: (i: number) => void
  onShow: (e: LogEvent) => void
  onFilterTrace: (traceId: string) => void
  withDate: boolean
}

/**
 * B2 single-line rows as a treegrid: rows carry aria-selected and
 * aria-expanded, and an expanded row is followed by its detail. Messages are
 * text only (LogText).
 */
export function LogList({ slug, events, selected, expanded, onSelect, onToggle, onShow, onFilterTrace, withDate }: Props) {
  const list = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (selected === null) return
    const row = list.current?.querySelector<HTMLElement>(`[data-index="${selected}"]`)
    row?.scrollIntoView?.({ block: 'nearest' })
    row?.focus({ preventScroll: true })
  }, [selected])

  const onKey = (e: KeyboardEvent<HTMLDivElement>, i: number) => {
    if (e.target !== e.currentTarget) return // keys inside the detail belong to its controls
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      onSelect(i)
      onToggle(i)
    }
  }

  return (
    <div ref={list} className="rows" role="treegrid" aria-label="Logs, newest first">
      {events.map((e, i) => {
        const ms = Date.parse(e.timestamp)
        const open = expanded?.index === i
        return (
          <div key={e.event_id} role="rowgroup" className="rowgroup">
            <div
              role="row"
              aria-selected={selected === i}
              aria-expanded={open}
              tabIndex={selected === i || (selected === null && i === 0) ? 0 : -1}
              data-index={i}
              className={`row level-${e.level}`}
              onClick={() => {
                onSelect(i)
                onToggle(i)
              }}
              onKeyDown={(ev) => onKey(ev, i)}
            >
              <span role="gridcell" className="t" title={e.timestamp}>
                {rowTime(ms, withDate)}
              </span>
              <span role="gridcell" className="lv">
                <span className="shape" aria-hidden="true">
                  {LEVEL_SHAPE[e.level] ?? '?'}
                </span>{' '}
                {e.level}
              </span>
              <span role="gridcell" className="svc">
                <LogText text={e.service} mono={false} />
              </span>
              <span role="gridcell" className="msg" title={e.message}>
                <LogText text={e.message} mono={false} />
              </span>
            </div>
            {open && expanded && (
              <div role="row" className="detail-row">
                <div role="gridcell">
                  <RowDetail slug={slug} event={expanded.shown} onShow={onShow} onFilterTrace={onFilterTrace} />
                </div>
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}

export function SkeletonRows({ count = 8 }: { count?: number }) {
  return (
    <div className="rows" aria-hidden="true">
      {Array.from({ length: count }, (_, i) => (
        <div key={i} className="row skeleton">
          <span />
          <span />
          <span />
          <span />
        </div>
      ))}
    </div>
  )
}
