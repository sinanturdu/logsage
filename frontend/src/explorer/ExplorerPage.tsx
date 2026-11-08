import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useParams, useSearchParams } from 'react-router'
import { ApiError, errorMessage } from '../api/client'
import { useProjects } from '../api/projects'
import { LogText } from '../components/LogText'
import { MAX_ROWS, STORE_DOWN, mergePages, useFacets, useHistogram, useLogs } from './api'
import { activeFilterCount } from './detail'
import { FilterChips } from './FilterChips'
import { FilterPanel, SearchBox } from './FilterPanel'
import { LogList, SkeletonRows, type Expanded } from './LogList'
import {
  EMPTY_FILTERS,
  contextWindow,
  hasFilters,
  parseState,
  presetLabel,
  resolveRange,
  serializeState,
  validTraceId,
  type ExplorerState,
  type Preset,
} from './model'
import { LiveBar } from '../live/LiveBar'
import { useLiveList } from '../live/useLiveList'
import { shortcutFor } from '../shortcuts'
import { useExplorerBridge } from '../palette/bridge'
import { RangeControls } from './RangeControls'
import { Timeline } from './Timeline'
import { SHARE_HIDDEN, SHARE_HIDDEN_VIEW, bucketTotal, onlyErrorLevels, onlyErrorsInView, rangeHeading } from './time'

export { STORE_DOWN }
export const NO_RESULTS = 'No logs match these filters. Widen the time range or clear a filter.'

const fmt = (n: number) => n.toLocaleString('en-US')

/** The timeline scope in words: the logs in view and, with a selection, in it. */
export function scopeText(inView: number, inSelection?: number): string {
  const view = `${fmt(inView)} ${inView === 1 ? 'log' : 'logs'} in view`
  return inSelection === undefined ? `${view}.` : `${view}, ${fmt(inSelection)} in the selection.`
}

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

const HISTOGRAM_EVERY_MS = 10_000

export function ExplorerPage() {
  const { slug = '' } = useParams()
  const projects = useProjects()
  const [params, setParams] = useSearchParams()
  const state = useMemo(() => parseState(params), [params])
  // "now" is fixed until the range changes or the user refreshes, so a
  // relative range resolves to stable query keys.
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000) * 1000)
  const refresh = useCallback(() => setNow(Math.floor(Date.now() / 1000) * 1000), [])
  // Like refresh, but always a new window, so the list is fetched again.
  const restart = useCallback(() => setNow((t) => Math.max(t + 1000, Math.floor(Date.now() / 1000) * 1000)), [])
  const search = useRef<HTMLInputElement>(null)
  const [selected, setSelected] = useState<number | null>(null)
  const [expanded, setExpanded] = useState<Expanded>(null)
  const [notice, setNotice] = useState('')
  // The narrow-screen Filters disclosure; component state, not the URL.
  const [filtersOpen, setFiltersOpen] = useState(false)

  const update = useCallback(
    (next: Partial<ExplorerState>, opts: { replace?: boolean } = {}) => {
      setParams(serializeState({ ...parseState(params), ...next }), { replace: opts.replace ?? false })
    },
    [params, setParams],
  )

  const live = state.live
  // While live the timeline follows now: it is refetched every 10 seconds.
  const [histNow, setHistNow] = useState(now)
  const win = resolveRange(state.range, now)
  const ctx = contextWindow(state.range, live ? Math.max(histNow, now) : now, state.zoom)
  const filters = { levels: state.levels, services: state.services, q: state.q, traceId: state.traceId, fingerprint: state.fingerprint }
  const histogram = useHistogram(slug, filters, ctx)
  const facets = useFacets(slug, filters, win)
  const logs = useLogs(slug, filters, win)
  const baseEvents = useMemo(() => mergePages(logs.data?.pages), [logs.data])

  // The reader is busy: new live logs wait in a buffer instead of moving the list.
  const [scrolledAway, setScrolledAway] = useState(false)
  useEffect(() => {
    if (!live) return
    const onScroll = () => {
      const top = document.querySelector('.results .rows')?.getBoundingClientRect().top ?? 0
      setScrolledAway(top < -40)
    }
    window.addEventListener('scroll', onScroll, { passive: true })
    return () => window.removeEventListener('scroll', onScroll)
  }, [live])
  const liveList = useLiveList({
    slug,
    filters,
    live,
    busy: expanded !== null || (selected !== null && selected !== 0) || scrolledAway,
    base: baseEvents,
    listKey: serializeState({ ...state, live: false }).toString() + now,
    // The server restarted the stream at now: reload the list up to now.
    onReset: restart,
  })
  const events = liveList.events
  const capped = events.length >= MAX_ROWS
  const tail = liveList.tail

  useEffect(() => {
    if (!live) return
    const id = setInterval(() => {
      // Respect a 429 on the histogram: wait out its Retry-After.
      const err = histogram.error
      if (err instanceof ApiError && err.kind === 'rate_limited' && Date.now() < histogram.errorUpdatedAt + (err.retryAfter ?? 1) * 1000) return
      setHistNow(Math.floor(Date.now() / 1000) * 1000)
    }, HISTOGRAM_EVERY_MS)
    return () => clearInterval(id)
  }, [live, histogram.error, histogram.errorUpdatedAt])

  const toggleLive = useCallback(() => {
    if (live) {
      update({ live: false })
      return
    }
    refresh()
    const preset = state.range.kind === 'relative' ? state.range.preset : '15m'
    update({ live: true, range: { kind: 'relative', preset }, zoom: false })
  }, [live, refresh, state.range, update])
  const resume = useCallback(() => {
    setExpanded(null)
    setSelected(null)
    liveList.showBuffered()
    setScrolledAway(false)
    document.querySelector('.results')?.scrollIntoView?.({ block: 'start' })
  }, [liveList])

  // A new result set starts without a selection.
  const [selKey, setSelKey] = useState('')
  const key = params.toString() + now
  if (key !== selKey) {
    setSelKey(key)
    setSelected(null)
    setExpanded(null)
  }

  const filterTrace = useCallback(
    (traceId: string) => {
      if (!validTraceId(traceId)) {
        setNotice('This trace id cannot be used as a filter: it is not 32 hexadecimal characters.')
        return
      }
      setNotice('')
      update({ traceId })
    },
    [update],
  )
  const toggle = useCallback(
    (i: number) => setExpanded((cur) => (cur?.index === i ? null : events[i] ? { index: i, shown: events[i] } : null)),
    [events],
  )

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // Keys come from the shared definition (shortcuts.ts), which the help lists.
      const id = shortcutFor(e, ['search', 'next', 'previous', 'escape', 'live', 'retry', 'trace'])
      if (id === 'search') {
        e.preventDefault()
        search.current?.focus()
      } else if (id === 'next') setSelected((s) => (events.length === 0 ? null : s === null ? 0 : Math.min(s + 1, events.length - 1)))
      else if (id === 'previous') setSelected((s) => (events.length === 0 ? null : s === null ? 0 : Math.max(s - 1, 0)))
      else if (id === 'escape') {
        // Escape closes the detail first (focus returns to its row), then clears the selection.
        if (expanded) {
          const row = document.querySelector<HTMLElement>(`[data-index="${expanded.index}"]`)
          setExpanded(null)
          row?.focus()
        } else setSelected(null)
      } else if (id === 'live') {
        toggleLive()
      } else if (id === 'retry' && tail.state.kind === 'reconnecting') {
        tail.retryNow()
      } else if (id === 'trace' && selected !== null) {
        const ev = events[selected]
        if (!ev) return
        if (!ev.trace_id) setNotice('This log has no trace_id, so there is no trace to filter by.')
        else filterTrace(ev.trace_id)
      }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [events, expanded, selected, filterTrace, toggleLive, tail])

  // The command palette reads this state and runs explorer commands through it.
  const rangeControls = useRef<{ openCustom: () => void }>(null)
  useExplorerBridge({
    ctx: {
      live,
      zoom: state.zoom,
      canZoom: state.range.kind === 'absolute' && !state.zoom,
      services: (facets.data?.services ?? []).filter((s) => !s.other).map((s) => s.name),
      selectedServices: state.services,
      levels: state.levels,
      hasFilters: hasFilters(state),
      preset: state.range.kind === 'relative' ? state.range.preset : null,
    },
    run: (a) => {
      switch (a.type) {
        case 'range':
          refresh()
          update({ range: { kind: 'relative', preset: a.preset }, zoom: false })
          return
        case 'custom-range':
          rangeControls.current?.openCustom()
          return
        case 'levels':
          update({ levels: a.levels })
          return
        case 'clear-filters':
          update(EMPTY_FILTERS)
          return
        case 'service':
          update({ services: [...state.services, a.name] })
          return
        case 'focus':
          if (a.field === 'search') search.current?.focus()
          else {
            setFiltersOpen(true) // on narrow screens the field sits behind the Filters button
            setTimeout(() => document.querySelector<HTMLInputElement>('[data-field="trace"]')?.focus(), 0)
          }
          return
        case 'live':
          if (a.on !== live) toggleLive()
          return
        case 'refresh':
          refresh()
          return
        case 'zoom':
          update({ zoom: a.on })
          return
      }
    },
  })

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
      : state.zoom
        ? 'zoomed to the selection'
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
          ref={rangeControls}
          range={state.range}
          window={win}
          onPreset={(p: Preset) => {
            refresh()
            update({ range: { kind: 'relative', preset: p } })
          }}
          onCustom={(from, to) => update({ range: { kind: 'absolute', from, to }, zoom: false, live: false })}
          onRefresh={refresh}
        />
        <h1 className="range-heading">
          {live ? rangeHeading(ctx.from, ctx.to, ctx.to) : rangeHeading(win.from, win.to, now)} <small>{contextText}</small>
        </h1>
      </div>

      {histogram.data ? (
        <Timeline
          buckets={histogram.data.buckets}
          bucketSeconds={histogram.data.bucketSeconds}
          window={ctx}
          showShare={!onlyErrorLevels(state.levels) && !onlyErrorsInView(facets.data?.levels)}
          shareNote={onlyErrorLevels(state.levels) ? SHARE_HIDDEN : SHARE_HIDDEN_VIEW}
          selection={state.range.kind === 'absolute' && !state.zoom ? win : null}
          // Brushing inside a zoomed view zooms further; Back undoes each step.
          onSelect={(from, to) => update({ range: { kind: 'absolute', from, to }, zoom: state.zoom, live: false })}
          scope={scopeText(histogram.data.buckets.reduce((n, b) => n + bucketTotal(b), 0), state.range.kind === 'absolute' && !state.zoom ? total : undefined)}
          tools={
            state.zoom ? (
              <button type="button" className="button button-quiet" onClick={() => update({ zoom: false })}>
                Zoom out
              </button>
            ) : (
              state.range.kind === 'absolute' && (
                <button type="button" className="button button-quiet" onClick={() => update({ zoom: true })}>
                  Zoom to selection
                </button>
              )
            )
          }
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
          <button
            type="button"
            className="button filters-toggle"
            aria-expanded={filtersOpen}
            aria-controls="filter-panel"
            onClick={() => setFiltersOpen((o) => !o)}
          >
            Filters ({activeFilterCount(state)})
          </button>
          <div id="filter-panel" className="filter-panel" data-open={filtersOpen}>
            <FilterPanel
              filters={filters}
              facets={facets.data}
              onChange={(next) => update(next)}
              onClear={() => update(EMPTY_FILTERS)}
              canClear={hasFilters(state)}
            />
          </div>
        </aside>

        <section className="results" aria-label="Logs" aria-busy={logs.isFetching}>
          <FilterChips slug={slug} state={state} onRemove={(next) => update(next)} onClear={() => update(EMPTY_FILTERS)} />
          <LiveBar
            live={live}
            state={tail.state}
            now={tail.now}
            paused={liveList.paused}
            buffered={liveList.buffered}
            onToggle={toggleLive}
            onRetry={tail.retryNow}
            onResume={resume}
          />
          <p className="visually-hidden" role="status" aria-live="polite">
            {liveList.announcement}
          </p>
          <p className="notice-line" role="status" aria-live="polite">
            {notice}
          </p>
          <p className="status" role="status" aria-live="polite">
            {problem
              ? ''
              : loadingFirst
                ? 'Loading logs…'
                : // The facets total covers the loaded window, not rows kept from a live tail.
                  total !== undefined && !live && liveList.fresh.size === 0
                  ? `Showing ${events.length.toLocaleString('en-US')} of ${total.toLocaleString('en-US')} logs, newest first.`
                  : `Showing ${events.length.toLocaleString('en-US')} logs, ${live ? 'latest arrivals first' : 'newest first'}.`}
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
              <LogList
                slug={slug}
                events={events}
                selected={selected}
                expanded={expanded}
                onSelect={setSelected}
                onToggle={toggle}
                onShow={(shown) => setExpanded((cur) => (cur ? { ...cur, shown } : cur))}
                onFilterTrace={filterTrace}
                withDate={win.to - win.from > 86_400_000}
                fresh={liveList.fresh}
                order={live ? 'latest arrivals first' : 'newest first'}
              />
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
