import { useId, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, useSearchParams } from 'react-router'
import { useGroupTitle } from '../groups/api'
import { groupHref, parseRange } from '../groups/model'
import { TemplateText } from '../groups/parts'
import { request } from '../api/client'
import { LogText } from '../components/LogText'
import { copyText } from './clipboard'
import { SAME_TRACE_LIMIT, groupAttributes, logJson, sameTraceWindow, zoneLayout, type Pair } from './detail'
import type { LogEvent } from './api'
import { LEVEL_SHAPE, isoUtc } from './model'
import { rowTime } from './time'

type Props = {
  slug: string
  event: LogEvent
  onFilterTrace: (traceId: string) => void
  onShow: (e: LogEvent) => void
}

const plural = (n: number, word: string) => `${n} ${n === 1 ? word : `${word}s`}`

export const COPY_FALLBACK = 'Copy is not available here. Select the text and copy it with Ctrl+C.'
export const TRACE_EMPTY = 'No other logs share this trace in this window.'
export const TRACE_ERROR = 'The other logs of this trace could not be loaded.'

/**
 * The logs that share a trace id, 10 minutes around the log. Fetched only
 * while a detail is open; the request takes the query's AbortSignal, so it
 * is cancelled when the row collapses (the query loses its last observer).
 */
export function useSameTrace(slug: string, traceId: string, ts: number) {
  const w = sameTraceWindow(ts)
  return useQuery({
    queryKey: ['trace', slug, traceId, w.from, w.to],
    enabled: traceId !== '',
    queryFn: ({ signal }) => {
      const p = new URLSearchParams({ from: isoUtc(w.from), to: isoUtc(w.to), trace_id: traceId, limit: String(SAME_TRACE_LIMIT) })
      return request<{ events: LogEvent[] }>('GET', `/api/projects/${encodeURIComponent(slug)}/logs?${p.toString()}`, { signal })
    },
    staleTime: 60_000,
  })
}

function Pairs({ pairs }: { pairs: Pair[] }) {
  return (
    <dl className="pairs">
      {pairs.map(([k, v]) => (
        <div key={k}>
          <dt>
            <LogText text={k} mono={false} />
          </dt>
          <dd>
            <LogText text={v} />
          </dd>
        </div>
      ))}
    </dl>
  )
}

function SameTrace({ slug, event, onShow }: { slug: string; event: LogEvent; onShow: (e: LogEvent) => void }) {
  const trace = useSameTrace(slug, event.trace_id, Date.parse(event.timestamp))
  const [all, setAll] = useState(false)
  const id = useId()
  let body
  if (trace.isPending) {
    body = (
      <div className="trace-skeleton" aria-hidden="true">
        <span />
        <span />
        <span />
      </div>
    )
  } else if (trace.isError) {
    body = (
      <p className="trace-state" role="alert">
        {TRACE_ERROR}{' '}
        <button type="button" className="button button-quiet link" onClick={() => void trace.refetch()}>
          Retry
        </button>
      </p>
    )
  } else {
    const events = trace.data.events
    const others = events.filter((e) => e.event_id !== event.event_id)
    if (others.length === 0) {
      body = <p className="trace-state">{TRACE_EMPTY}</p>
    } else {
      const shown = all ? events : events.slice(0, 5)
      body = (
        <>
          <ol className="trace-list" aria-labelledby={`${id}-h`}>
            {shown.map((e) => (
              <li key={e.event_id}>
                <button
                  type="button"
                  className={`trace-item level-${e.level}`}
                  aria-current={e.event_id === event.event_id ? 'true' : undefined}
                  title={e.message}
                  onClick={() => onShow(e)}
                >
                  <span className="t">{rowTime(Date.parse(e.timestamp), false)}</span>{' '}
                  <span className="shape" aria-hidden="true">
                    {LEVEL_SHAPE[e.level] ?? '?'}
                  </span>{' '}
                  <span className="lv">{e.level}</span> <LogText text={e.service} mono={false} />:{' '}
                  <LogText text={e.message} mono={false} />
                </button>
              </li>
            ))}
          </ol>
          {!all && events.length > 5 && (
            <button type="button" className="button button-quiet link" onClick={() => setAll(true)}>
              Show all {events.length}
            </button>
          )}
        </>
      )
    }
  }
  return (
    <section className="zone same" aria-labelledby={`${id}-h`}>
      <h3 id={`${id}-h`}>Same trace{trace.data ? ` (${plural(trace.data.events.length, 'log')})` : ''}</h3>
      {body}
    </section>
  )
}

export const NOT_GROUPED = 'Not grouped (ingested before v0.3)'

/**
 * The log's error group (ADR 0023): its template and a link, loaded lazily
 * and cached per fingerprint. A warn, error or fatal log without one was
 * ingested before grouping; debug and info logs are never grouped.
 */
function GroupLine({ slug, event }: { slug: string; event: LogEvent }) {
  const [params] = useSearchParams()
  const fp = event.fingerprint && event.fingerprint !== '0000000000000000' ? event.fingerprint : ''
  const title = useGroupTitle(slug, fp)
  if (!fp) {
    return ['warn', 'error', 'fatal'].includes(event.level) ? <p className="group-line muted">{NOT_GROUPED}</p> : null
  }
  return (
    <p className="group-line">
      Group:{' '}
      {title.data ? <TemplateText text={title.data} /> : <span className="mono">{title.isError ? fp : `${fp.slice(0, 8)}…`}</span>} -{' '}
      <Link to={groupHref(slug, fp, parseRange(params))}>View group</Link>
    </p>
  )
}

/** The expanded detail of one log (B2 polish round, ADR 0018). */
export function RowDetail({ slug, event, onFilterTrace, onShow }: Props) {
  const [note, setNote] = useState('')
  const g = groupAttributes(event.attributes)
  const hasTrace = event.trace_id !== ''
  const copy = async (text: string, what: string) => {
    setNote((await copyText(text)) ? `Copied ${what}.` : COPY_FALLBACK)
  }
  return (
    <div className="detail">
      <p className="full">
        <LogText text={event.message} />
      </p>
      <GroupLine slug={slug} event={event} />
      <div className="idline">
        <dl className="ids">
          <div>
            <dt>trace_id</dt>
            <dd>{hasTrace ? <LogText text={event.trace_id} /> : 'none'}</dd>
            {hasTrace && (
              <button type="button" className="button button-quiet link" onClick={() => void copy(event.trace_id, 'the trace id')} aria-label="Copy trace id">
                Copy
              </button>
            )}
          </div>
          {g.spanId !== null && (
            <div>
              <dt>span_id</dt>
              <dd>
                <LogText text={g.spanId} />
              </dd>
              <button type="button" className="button button-quiet link" onClick={() => void copy(g.spanId ?? '', 'the span id')} aria-label="Copy span id">
                Copy
              </button>
            </div>
          )}
        </dl>
        <div className="actions">
          <p className="copied" role="status" aria-live="polite">
            {note}
          </p>
          {hasTrace && (
            <button type="button" className="button" onClick={() => onFilterTrace(event.trace_id)}>
              Filter by this trace
            </button>
          )}
          <button type="button" className="button" onClick={() => void copy(logJson(event), 'the log as JSON')}>
            Copy as JSON
          </button>
        </div>
      </div>
      <div className={`zones layout-${zoneLayout(g, hasTrace)}${hasTrace ? '' : ' no-trace'}`}>
        {g.request.length > 0 && (
          <section className="zone">
            <h3>Request</h3>
            <Pairs pairs={g.request} />
          </section>
        )}
        {g.context.length > 0 && (
          <section className="zone">
            <h3>Context</h3>
            <Pairs pairs={g.context} />
          </section>
        )}
        {g.request.length === 0 && g.context.length === 0 && <p className="zone none">No attributes</p>}
        {hasTrace && <SameTrace slug={slug} event={event} onShow={onShow} />}
      </div>
    </div>
  )
}
