import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useParams, useSearchParams } from 'react-router'
import { ApiError, errorMessage } from '../api/client'
import { useProjects } from '../api/projects'
import { LogText } from '../components/LogText'
import { MAX_ROWS, mergePages, useFacets, useHistogram, useLogs } from './api'
import { FilterPanel, SearchBox } from './FilterPanel'
import { LogList, SkeletonRows } from './LogList'
import {
  EMPTY_FILTERS,
  contextWindow,
  hasFilters,
  parseState,
  presetLabel,
  resolveRange,
  serializeState,
  type ExplorerState,
  type Preset,
} from './model'
import { RangeControls } from './RangeControls'
import { Timeline } from './Timeline'
import { rangeHeading } from './time'

export const STORE_DOWN = 'LogSage cannot reach its log store right now. Try again in a moment.'
export const NO_RESULTS = 'No logs match these filters. Widen the time range or clear a filter.'

/** Copy for a failed explorer request: what happened and how to fix it. */
export function explorerError(err: unknown): { text: string; retry: boolean } {
  if (err instanceof ApiError) {
    switch (err.kind) {
      case 'network':
      case 'server':
        return { text: STORE_DOWN, retry: true }
      case 'rate_limited':
        return { text: `Too many requests. Try again in ${err.retryAfter ?? 1} seconds.`, retry: true }
      case 'not_found':
        return { text: 'This project was not found, or you are not a member of it. Choose a project in the header.', retry: false }
      default:
        return { text: errorMessage(err), retry: false }
    }
  }
  return { text: errorMessage(err), retry: true }
}

const isTyping = (t: EventTarget | null) =>
  t instanceof HTMLElement && (t.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(t.tagName))

export function ExplorerPage() {
  const { slug = '' } = useParams()
  const projects = useProjects()
  const [params, setParams] = useSearchParams()
  const state = useMemo(() => parseState(params), [params])
  // "now" is fixed until the range changes or the user refreshes, so a
  // relative range resolves to stable query keys.
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000) * 1000)
  const refresh = useCallback(() => setNow(Math.floor(Date.now() / 1000) * 1000), [])
  const search = useRef<HTMLInputElement>(null)
  const [selected, setSelected] = useState<number | null>(null)

  const update = useCallback(
    (next: Partial<ExplorerState>, opts: { replace?: boolean } = {}) => {
      setParams(serializeState({ ...parseState(params), ...next }), { replace: opts.replace ?? false })
    },
    [params, setParams],
  )

  const win = resolveRange(state.range, now)
  const ctx = contextWindow(state.range, now)
  const filters = { levels: state.levels, services: state.services, q: state.q, traceId: state.traceId }
  const histogram = useHistogram(slug, filters, ctx)
  const facets = useFacets(slug, filters, win)
  const logs = useLogs(slug, filters, win)
  const events = useMemo(() => mergePages(logs.data?.pages), [logs.data])
  const capped = events.length >= MAX_ROWS

  // A new result set starts without a selection.
  const [selKey, setSelKey] = useState('')
  const key = params.toString() + now
  if (key !== selKey) {
    setSelKey(key)
    setSelected(null)
  }

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.ctrlKey || e.metaKey || e.altKey) return
      if (e.key === '/' && !isTyping(e.target)) {
        e.preventDefault()
        search.current?.focus()
        return
      }
      if (isTyping(e.target)) return
      if (e.key === 'j') setSelected((s) => (events.length === 0 ? null : s === null ? 0 : Math.min(s + 1, events.length - 1)))
      else if (e.key === 'k') setSelected((s) => (events.length === 0 ? null : s === null ? 0 : Math.max(s - 1, 0)))
      else if (e.key === 'Escape') setSelected(null)
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [events.length])

  const onSearch = useCallback((q: string, immediate: boolean) => update({ q }, { replace: !immediate }), [update])

  if (projects.isSuccess && !projects.data.some((p) => p.slug === slug)) {
    return (
      <div className="panel" role="alert">
        <h1>Project not found</h1>
        <p>
          There is no project <LogText text={slug} /> that you are a member of. Choose a project in the header.
        </p>
      </div>
    )
  }

  const contextText =
    state.range.kind === 'relative'
      ? presetLabel(state.range.preset)
      : ctx.preset
        ? `selected within the ${presetLabel(ctx.preset)}`
        : 'selected range'
  const error = logs.error ?? facets.error
  const problem = error && !(error instanceof ApiError && error.kind === 'unauthorized') ? explorerError(error) : null
  const total = facets.data?.total
  const loadingFirst = logs.isPending && !problem

  return (
    <div className="explorer">
      <div className="explorer-head">
        <RangeControls
          range={state.range}
          window={win}
          onPreset={(p: Preset) => {
            refresh()
            update({ range: { kind: 'relative', preset: p } })
          }}
          onCustom={(from, to) => update({ range: { kind: 'absolute', from, to } })}
          onRefresh={refresh}
        />
        <h1 className="range-heading">
          {rangeHeading(win.from, win.to, now)} <small>{contextText}</small>
        </h1>
      </div>

      {histogram.data ? (
        <Timeline
          buckets={histogram.data.buckets}
          bucketSeconds={histogram.data.bucketSeconds}
          window={ctx}
          selection={state.range.kind === 'absolute' ? win : null}
          onSelect={(from, to) => update({ range: { kind: 'absolute', from, to } })}
        />
      ) : histogram.isError ? (
        <p className="chart-error" role="alert">
          The timeline could not be loaded. {explorerError(histogram.error).text}
        </p>
      ) : (
        <div className="chart chart-loading" aria-hidden="true" />
      )}

      <div className="explorer-body">
        <aside aria-label="Filters">
          <SearchBox ref={search} value={state.q} onSearch={onSearch} />
          <FilterPanel
            filters={filters}
            facets={facets.data}
            onChange={(next) => update(next)}
            onClear={() => update(EMPTY_FILTERS)}
            canClear={hasFilters(state)}
          />
        </aside>

        <section className="results" aria-label="Logs" aria-busy={logs.isFetching}>
          <p className="status" role="status" aria-live="polite">
            {problem
              ? ''
              : loadingFirst
                ? 'Loading logs…'
                : total !== undefined
                  ? `Showing ${events.length.toLocaleString('en-US')} of ${total.toLocaleString('en-US')} logs, newest first.`
                  : `Showing ${events.length.toLocaleString('en-US')} logs, newest first.`}
          </p>
          {problem ? (
            <div className="state-panel" role="alert">
              <p>{problem.text}</p>
              {problem.retry && (
                <button
                  type="button"
                  className="button"
                  onClick={() => {
                    void logs.refetch()
                    void facets.refetch()
                    void histogram.refetch()
                  }}
                >
                  Try again
                </button>
              )}
            </div>
          ) : loadingFirst ? (
            <SkeletonRows />
          ) : events.length === 0 ? (
            <div className="state-panel">
              <p>{NO_RESULTS}</p>
              {hasFilters(state) && (
                <button type="button" className="button" onClick={() => update(EMPTY_FILTERS)}>
                  Clear filters
                </button>
              )}
            </div>
          ) : (
            <>
              <LogList events={events} selected={selected} onSelect={setSelected} withDate={win.to - win.from > 86_400_000} />
              {capped ? (
                <p className="cap-note">
                  {MAX_ROWS.toLocaleString('en-US')} logs loaded, the most this view shows. Narrow the time range or add
                  filters to see the rest.
                </p>
              ) : (
                logs.hasNextPage && (
                  <button type="button" className="button more" onClick={() => void logs.fetchNextPage()} disabled={logs.isFetchingNextPage}>
                    {logs.isFetchingNextPage ? 'Loading…' : 'Load more'}
                  </button>
                )
              )}
            </>
          )}
        </section>
      </div>
    </div>
  )
}
