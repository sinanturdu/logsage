import { useEffect, useRef } from 'react'
import { LogText } from '../components/LogText'
import { LEVEL_SHAPE } from './model'
import type { LogEvent } from './api'
import { rowTime } from './time'

type Props = {
  events: LogEvent[]
  selected: number | null
  onSelect: (i: number | null) => void
  withDate: boolean
}

/** B2 single-line rows; messages are text only (LogText). */
export function LogList({ events, selected, onSelect, withDate }: Props) {
  const list = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (selected === null) return
    const row = list.current?.querySelector<HTMLElement>(`[data-index="${selected}"]`)
    row?.scrollIntoView?.({ block: 'nearest' })
    row?.focus({ preventScroll: true })
  }, [selected])

  return (
    <div ref={list} className="rows" role="listbox" aria-label="Logs, newest first">
      {events.map((e, i) => {
        const ms = Date.parse(e.timestamp)
        return (
          <div
            key={e.event_id}
            role="option"
            aria-selected={selected === i}
            tabIndex={selected === i || (selected === null && i === 0) ? 0 : -1}
            data-index={i}
            className={`row level-${e.level}`}
            onClick={() => onSelect(i)}
          >
            <span className="t" title={e.timestamp}>
              {rowTime(ms, withDate)}
            </span>
            <span className="lv">
              <span className="shape" aria-hidden="true">
                {LEVEL_SHAPE[e.level] ?? '?'}
              </span>{' '}
              {e.level}
            </span>
            <span className="svc">
              <LogText text={e.service} mono={false} />
            </span>
            <span className="msg" title={e.message}>
              <LogText text={e.message} mono={false} />
            </span>
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
