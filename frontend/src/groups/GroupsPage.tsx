import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router'
import { ApiError } from '../api/client'
import { useProjects } from '../api/projects'
import { LogText } from '../components/LogText'
import { useFacets } from '../explorer/api'
import { SearchBox } from '../explorer/FilterPanel'
import { LEVEL_SHAPE, presetLabel, resolveRange, toggleService, type Preset } from '../explorer/model'
import { RangeControls } from '../explorer/RangeControls'
import { rangeHeading } from '../explorer/time'
import { useExplorerBridge } from '../palette/bridge'
import { shortcutFor } from '../shortcuts'
import { groupsError, mergeGroups, useGroups, type Group } from './api'
import { combineGroups, type Combined } from './combine'
import {
  GROUP_LEVELS,
  SORTS,
  SORT_LABEL,
  STATUS_FILTERS,
  STATUS_LABEL,
  apiSort,
  groupHref,
  parseGroupsState,
  serializeGroupsState,
  toggleGroupLevel,
  type GroupsState,
  type Sort,
  type StatusFilter,
} from './model'
import { Flags, Levels, Sparkline, StatusMark, TemplateText, When } from './parts'

export const NO_GROUPS = 'No error groups in this range. Widen the range or include warn.'
export const NOT_GROUPED_NOTE = 'Logs ingested before v0.3 are not grouped.'

const n = (x: number) => x.toLocaleString('en-US')
const groupsWord = (x: number) => (x === 1 ? 'group' : 'groups')

/**
 * The summary line, from what is loaded only (the API gives no total). It
 * names a single status, because the Status column is hidden then.
 */
export function summaryText(shown: number, newCount: number, newOnly: boolean, status: StatusFilter = 'all'): string {
  const what = `${status === 'all' ? '' : `${status} `}${groupsWord(shown)}`
  if (newOnly) return `Showing ${n(shown)} new ${what} in this range.`
  return `Showing ${n(shown)} ${what}, ${n(newCount)} new in this range.`
}

/**
 * "New only": the API lists the newest first, so new groups are a prefix.
 * The list ends at the first group that is not new, and paging stops there.
 */
export function visibleGroups(all: readonly Group[], newOnly: boolean): { groups: Group[]; ended: boolean } {
  if (!newOnly) return { groups: [...all], ended: false }
  const i = all.findIndex((g) => !g.is_new)
  return i === -1 ? { groups: [...all], ended: false } : { groups: all.slice(0, i), ended: true }
}

const INTERACTIVE = 'a, button, input, select, textarea, summary, [role="button"]'

/** A row as shown: a group, a combined parent, or a parent's child (one service). */
export type ShownRow =
  | { kind: 'group' | 'child'; key: string; group: Group }
  | { kind: 'parent'; key: string; groups: Group[]; combined: Combined; open: boolean }

/** The visible rows: groups as they are, or combined parents with their open children. */
export function shownRows(groups: readonly Group[], combine: boolean, sort: Sort, open: ReadonlySet<string>): ShownRow[] {
  if (!combine) return groups.map((g) => ({ kind: 'group', key: g.fingerprint, group: g }))
  const out: ShownRow[] = []
  for (const r of combineGroups(groups, sort)) {
    if (r.kind === 'group') {
      out.push({ kind: 'group', key: r.group.fingerprint, group: r.group })
      continue
    }
    const isOpen = open.has(r.key)
    out.push({ kind: 'parent', key: `parent:${r.key}`, groups: r.groups, combined: r.combined, open: isOpen })
    if (isOpen) for (const g of r.groups) out.push({ kind: 'child', key: g.fingerprint, group: g })
  }
  return out
}

export const combinedNote = (loaded: number) => `Combined from the ${n(loaded)} ${groupsWord(loaded)} loaded so far.`

export function GroupsPage() {
  const { slug = '' } = useParams()
  const projects = useProjects()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [params, setParams] = useSearchParams()
  const state = useMemo(() => parseGroupsState(params), [params])
  // "now" is fixed until the range changes or the user refreshes (as in the explorer).
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000) * 1000)
  const refresh = useCallback(() => setNow(Math.floor(Date.now() / 1000) * 1000), [])
  const search = useRef<HTMLInputElement>(null)
  const table = useRef<HTMLTableSectionElement>(null)
  const [selected, setSelected] = useState<number | null>(null)
  // Combined parents that are expanded, by template.
  const [open, setOpen] = useState<ReadonlySet<string>>(() => new Set())
  const toggleOpen = useCallback((title: string) => {
    setOpen((cur) => {
      const next = new Set(cur)
      if (next.has(title)) next.delete(title)
      else next.add(title)
      return next
    })
  }, [])

  const update = useCallback(
    (next: Partial<GroupsState>, opts: { replace?: boolean } = {}) => {
      setParams(serializeGroupsState({ ...parseGroupsState(params), ...next }), { replace: opts.replace ?? false })
    },
    [params, setParams],
  )

  const win = resolveRange(state.range, now)
  const list = useGroups(slug, state, win)
  const facets = useFacets(slug, { levels: state.levels, services: [], q: '', traceId: '' }, win)
  const all = useMemo(() => mergeGroups(list.data?.pages), [list.data])
  const { groups, ended } = visibleGroups(all, state.newOnly)
  const bucketSeconds = list.data?.pages[0]?.sparkline_bucket_seconds ?? 0
  const rows = useMemo(() => shownRows(groups, state.combine, apiSort(state), open), [groups, state, open])

  // A new result set starts without a selection.
  const [selKey, setSelKey] = useState('')
  const key = params.toString() + now
  if (key !== selKey) {
    setSelKey(key)
    setSelected(null)
    setOpen(new Set())
  }
  useEffect(() => {
    if (selected === null) return
    table.current?.querySelector<HTMLElement>(`tr[data-index="${selected}"] :is(a, button)`)?.focus()
  }, [selected])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const id = shortcutFor(e, ['groups-search', 'groups-next', 'groups-previous', 'groups-open'])
      if (id === 'groups-search') {
        e.preventDefault()
        search.current?.focus()
      } else if (id === 'groups-next') setSelected((s) => (rows.length === 0 ? null : s === null ? 0 : Math.min(s + 1, rows.length - 1)))
      else if (id === 'groups-previous') setSelected((s) => (rows.length === 0 ? null : s === null ? 0 : Math.max(s - 1, 0)))
      else if (id === 'groups-open' && selected !== null) {
        // A focused link or button handles Enter itself.
        if (e.target instanceof Element && e.target.closest(INTERACTIVE)) return
        const r = rows[selected]
        if (r?.kind === 'parent') toggleOpen(r.combined.title)
        else if (r) void navigate(groupHref(slug, r.group.fingerprint, state.range))
      }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [rows, selected, navigate, slug, state.range, toggleOpen])

  // The command palette reads the list's state and runs its commands here.
  useExplorerBridge({
    groups: { status: state.status, sort: state.sort, newOnly: state.newOnly, combine: state.combine },
    run: (a) => {
      if (a.type === 'group-status') update({ status: a.status })
      else if (a.type === 'group-combine') update({ combine: a.on })
      else if (a.type === 'group-sort') update({ sort: a.sort })
      else if (a.type === 'group-new') update({ newOnly: a.on })
    },
  })

  const onSearch = useCallback((q: string, immediate: boolean) => update({ q }, { replace: !immediate }), [update])
  const reload = () => {
    refresh()
    void qc.resetQueries({ queryKey: ['groups', slug] })
  }

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

  const firstError = list.isError && !list.isFetchNextPageError ? list.error : null
  const problem = firstError && !(firstError instanceof ApiError && firstError.kind === 'unauthorized') ? groupsError(firstError) : null
  const nextProblem = list.isFetchNextPageError ? groupsError(list.error) : null
  const services = (facets.data?.services ?? []).filter((s) => !s.other).map((s) => s.name)
  for (const s of state.services) if (!services.includes(s)) services.push(s)
  const newCount = groups.filter((g) => g.is_new).length
  // With one status selected every row would say the same: the column is left out.
  const showStatus = state.status === 'all'

  return (
    <div className="groups-page">
      <div className="explorer-head page-head">
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
          Error groups <small>{rangeHeading(win.from, win.to, now)}{state.range.kind === 'relative' ? `, ${presetLabel(state.range.preset)}` : ''}</small>
        </h1>
      </div>

      <div className="groups-toolbar" role="group" aria-label="Group filters">
        <SearchBox ref={search} value={state.q} onSearch={onSearch} label="Search templates" placeholder="Search templates, press /" />
        <fieldset className="inline-set">
          <legend>Levels</legend>
          {GROUP_LEVELS.map((l) => (
            <label key={l} className={`level-${l}`}>
              <input type="checkbox" checked={state.levels.includes(l)} onChange={() => update({ levels: toggleGroupLevel(state.levels, l) })} />
              <span className="shape" aria-hidden="true">
                {LEVEL_SHAPE[l]}
              </span>
              {l}
            </label>
          ))}
        </fieldset>
        <label className="inline-field">
          Status
          <select value={state.status} onChange={(e) => update({ status: e.target.value as StatusFilter })}>
            {STATUS_FILTERS.map((s) => (
              <option key={s} value={s}>
                {STATUS_LABEL[s]}
              </option>
            ))}
          </select>
        </label>
        <label className="inline-field">
          Sort
          <select value={state.newOnly ? 'first_seen' : state.sort} disabled={state.newOnly} onChange={(e) => update({ sort: e.target.value as Sort })}>
            {SORTS.map((s) => (
              <option key={s} value={s}>
                {SORT_LABEL[s]}
              </option>
            ))}
          </select>
        </label>
        <label className="inline-check">
          <input type="checkbox" checked={state.newOnly} onChange={(e) => update({ newOnly: e.target.checked })} />
          New in this range only
        </label>
        <label className="inline-check">
          <input type="checkbox" checked={state.combine} onChange={(e) => update({ combine: e.target.checked })} />
          Combine same message
        </label>
        <details className="service-filter">
          <summary>Services{state.services.length > 0 ? ` (${state.services.length} selected)` : ''}</summary>
          {services.length === 0 ? (
            <p className="muted">No services in this range.</p>
          ) : (
            <fieldset>
              <legend className="visually-hidden">Services</legend>
              {services.map((s) => (
                <label key={s}>
                  <input type="checkbox" checked={state.services.includes(s)} onChange={() => update({ services: toggleService(state.services, s) })} />
                  <LogText text={s} mono={false} />
                </label>
              ))}
            </fieldset>
          )}
        </details>
      </div>

      <section className="groups-results" aria-label="Error groups" aria-busy={list.isFetching}>
        <p className="status" role="status" aria-live="polite">
          {problem ? '' : list.isPending ? 'Loading error groups…' : summaryText(groups.length, newCount, state.newOnly, state.status)}
        </p>
        {state.combine && !problem && !list.isPending && groups.length > 0 && <p className="muted combined-note">{combinedNote(groups.length)}</p>}
        {problem ? (
          <div className="state-panel" role="alert">
            <p>{problem.text}</p>
            {(problem.retry || problem.reload) && (
              <button type="button" className="button" onClick={() => (problem.reload ? reload() : void list.refetch())}>
                {problem.reload ? 'Reload' : 'Try again'}
              </button>
            )}
          </div>
        ) : list.isPending ? (
          <GroupsSkeleton />
        ) : groups.length === 0 ? (
          <div className="state-panel">
            <p>{NO_GROUPS}</p>
            <p className="muted">{NOT_GROUPED_NOTE}</p>
          </div>
        ) : (
          <>
            <div className="table-wrap">
              <table className="groups">
                <caption className="visually-hidden">
                  {state.status === 'all' ? 'Error groups' : `${STATUS_LABEL[state.status]} error groups`}, {state.newOnly ? 'newest first' : SORT_LABEL[state.sort].toLowerCase()}
                </caption>
                <thead>
                  <tr>
                    {showStatus && <th scope="col">Status</th>}
                    <th scope="col">Group</th>
                    <th scope="col">Services</th>
                    <th scope="col">Levels</th>
                    <th scope="col" className="num">
                      Events
                    </th>
                    <th scope="col">Trend</th>
                    <th scope="col">Last seen</th>
                    <th scope="col">First seen</th>
                  </tr>
                </thead>
                <tbody ref={table}>
                  {rows.map((r, i) => (
                    <GroupRow
                      key={r.key}
                      row={r}
                      index={i}
                      selected={i === selected}
                      onSelect={() => setSelected(i)}
                      onToggle={toggleOpen}
                      showStatus={showStatus}
                      href={(fp) => groupHref(slug, fp, state.range)}
                      from={win.from}
                      bucketSeconds={bucketSeconds}
                      now={now}
                    />
                  ))}
                </tbody>
              </table>
            </div>
            {nextProblem ? (
              <div className="state-panel" role="alert">
                <p>{nextProblem.text}</p>
                <button type="button" className="button" onClick={() => (nextProblem.reload ? reload() : void list.fetchNextPage())}>
                  {nextProblem.reload ? 'Reload' : 'Try again'}
                </button>
              </div>
            ) : (
              list.hasNextPage &&
              !ended && (
                <button type="button" className="button more" onClick={() => void list.fetchNextPage()} disabled={list.isFetchingNextPage}>
                  {list.isFetchingNextPage ? 'Loading…' : 'Load more'}
                </button>
              )
            )}
          </>
        )}
      </section>
    </div>
  )
}

type RowProps = {
  row: ShownRow
  index: number
  selected: boolean
  onSelect: () => void
  onToggle: (title: string) => void
  showStatus: boolean
  href: (fingerprint: string) => string
  from: number
  bucketSeconds: number
  now: number
}

/** One table row: a group (or a parent's child), or a combined parent with a disclosure button. */
function GroupRow({ row, index, selected, onSelect, onToggle, showStatus, href, from, bucketSeconds, now }: RowProps) {
  const cls = [selected ? 'selected' : '', row.kind === 'child' ? 'child' : '', row.kind === 'parent' ? 'parent' : ''].filter(Boolean).join(' ') || undefined
  const v: Group | Combined = row.kind === 'parent' ? row.combined : row.group
  return (
    <tr data-index={index} className={cls} onClick={onSelect}>
      {showStatus && (
        <td>
          {v.status === 'mixed' ? (
            <span className="gstatus gstatus-mixed">
              <span className="shape" aria-hidden="true">
                ◫
              </span>{' '}
              Mixed
            </span>
          ) : (
            <StatusMark status={v.status} />
          )}
        </td>
      )}
      <td className="gtitle">
        <Flags group={v} />
        {row.kind === 'parent' ? (
          <button type="button" className="disclosure" aria-expanded={row.open} onClick={() => onToggle(row.combined.title)} onFocus={onSelect} title={row.combined.title}>
            <span className="tw" aria-hidden="true">
              {row.open ? '▾' : '▸'}
            </span>
            <TemplateText text={row.combined.title} />
            <span className="visually-hidden">, {row.groups.length} groups</span>
          </button>
        ) : (
          <Link to={href(row.group.fingerprint)} title={row.group.title} onFocus={onSelect}>
            <TemplateText text={row.group.title} />
          </Link>
        )}
      </td>
      <td>
        {row.kind === 'parent' ? (
          <span title={row.combined.services.join(', ')}>
            {row.combined.services.length} services<span className="visually-hidden">: {row.combined.services.join(', ')}</span>
          </span>
        ) : (
          <LogText text={row.group.services.join(', ')} mono={false} />
        )}
      </td>
      <td>
        <Levels levels={v.levels} />
      </td>
      <td className="num">{n(v.count)}</td>
      <td>
        <Sparkline values={v.sparkline} from={from} bucketSeconds={bucketSeconds} />
      </td>
      <td>
        <When iso={v.last_seen} now={now} />
      </td>
      <td>
        <When iso={v.first_seen} now={now} />
      </td>
    </tr>
  )
}

function GroupsSkeleton({ rows = 6 }: { rows?: number }) {
  return (
    <div className="groups-skeleton" aria-hidden="true">
      {Array.from({ length: rows }, (_, i) => (
        <span key={i} />
      ))}
    </div>
  )
}
