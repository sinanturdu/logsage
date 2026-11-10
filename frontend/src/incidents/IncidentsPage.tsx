import { useCallback, useEffect, useId, useMemo, useRef, useState, type FormEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router'
import { useProjects } from '../api/projects'
import { Dialog } from '../components/Dialog'
import { LogText } from '../components/LogText'
import { shortcutFor } from '../shortcuts'
import {
  LIMITS,
  SEVERITIES,
  createIncident,
  fieldOf,
  incidentsKey,
  mergeIncidents,
  readProblem,
  useIncidents,
  useSummary,
  writeError,
  type Severity,
} from './api'
import {
  ASSIGNEES,
  RANGES,
  RANGE_LABEL,
  STATUS_FILTERS,
  STATUS_FILTER_LABEL,
  incidentPath,
  parseFilters,
  serializeFilters,
  type Filters,
} from './model'
import { Ago, CountedField, SeverityMark, StatusMark } from './parts'

export const EMPTY_FILTERED = 'No incidents match these filters. Widen the filters or include resolved ones.'
export const EMPTY_NONE = 'No incidents yet. They open automatically when an alert rule that opens incidents fires, or you can create one yourself.'
export const VIEWER_INCIDENTS = 'You can see this project’s incidents. Only owners and admins can create or change them.'
const ASSIGNEE_LABEL = { any: 'Anyone', me: 'Assigned to me', unassigned: 'Unassigned' } as const
const INTERACTIVE = 'a, button, input, select, textarea'

/** Create an incident by hand: title, severity and summary with live counters. */
export function CreateIncidentDialog({ slug, onClose }: { slug: string; onClose: () => void }) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const id = useId()
  const [title, setTitle] = useState('')
  const [severity, setSeverity] = useState<Severity>('high')
  const [summary, setSummary] = useState('')
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const titleRef = useRef<HTMLInputElement & HTMLTextAreaElement>(null)

  async function submit(e: FormEvent) {
    e.preventDefault()
    const t = title.trim()
    const errs: Record<string, string> = {}
    if (t === '') errs.title = 'Enter a title, for example "Checkout errors after the deploy".'
    else if (Array.from(t).length > LIMITS.title) errs.title = `Use at most ${LIMITS.title} characters.`
    if (Array.from(summary.trim()).length > LIMITS.summary) errs.summary = `Use at most ${LIMITS.summary} characters.`
    setErrors(errs)
    if (Object.keys(errs).length > 0) return titleRef.current?.focus()
    setBusy(true)
    try {
      const res = await createIncident(slug, { title: t, severity, summary: summary.trim() })
      void qc.invalidateQueries({ queryKey: incidentsKey(slug) })
      onClose()
      void navigate(incidentPath(slug, res.incident.number))
    } catch (err) {
      const field = fieldOf(err)
      setErrors(field ? { [field]: writeError(err) } : { form: writeError(err) })
      setBusy(false)
    }
  }

  return (
    <Dialog title="Create an incident" onClose={onClose} initialFocus={titleRef}>
      <form onSubmit={(e) => void submit(e)} noValidate>
        <CountedField label="Title" value={title} onChange={setTitle} max={LIMITS.title} error={errors.title} inputRef={titleRef} />
        <div className="afield">
          <label htmlFor={`${id}-sev`}>Severity</label>
          <select id={`${id}-sev`} value={severity} onChange={(e) => setSeverity(e.target.value as Severity)}>
            {SEVERITIES.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
          {errors.severity && <p className="field-error">{errors.severity}</p>}
        </div>
        <CountedField label="Summary (optional)" value={summary} onChange={setSummary} max={LIMITS.summary} error={errors.summary} multiline hint="What is affected and what is known so far." />
        {errors.form && (
          <p className="form-error" role="alert">
            {errors.form}
          </p>
        )}
        <div className="dialog-actions">
          <button type="button" className="button button-quiet" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" className="button button-primary" disabled={busy}>
            {busy ? 'Creating…' : 'Create incident'}
          </button>
        </div>
      </form>
    </Dialog>
  )
}

/** /projects/:slug/incidents (ADR 0027). */
export function IncidentsPage() {
  const { slug = '' } = useParams()
  const [params, setParams] = useSearchParams()
  const f = parseFilters(params)
  const navigate = useNavigate()
  const projects = useProjects()
  const project = projects.data?.find((p) => p.slug === slug)
  const canManage = project?.role === 'owner' || project?.role === 'admin'
  const [now] = useState(() => Date.now())
  const list = useIncidents(slug, f, now)
  const summary = useSummary(slug, project !== undefined)
  const rows = useMemo(() => mergeIncidents(list.data?.pages), [list.data])
  const [creating, setCreating] = useState(false)
  const [q, setQ] = useState(f.q)
  const [service, setService] = useState(f.service)
  const search = useRef<HTMLInputElement>(null)
  const table = useRef<HTMLTableElement>(null)
  const id = useId()

  const update = useCallback((next: Partial<Filters>) => setParams(serializeFilters({ ...parseFilters(params), ...next })), [params, setParams])

  // ?create=1 (the command palette) opens the dialog once and leaves the URL.
  const [handled, setHandled] = useState(false)
  if (params.get('create') === '1' && projects.isSuccess && !handled) {
    setHandled(true)
    if (canManage) setCreating(true)
  }
  useEffect(() => {
    if (params.get('create') === '1' && projects.isSuccess) {
      const p = new URLSearchParams(params)
      p.delete('create')
      setParams(p, { replace: true })
    }
  }, [params, projects.isSuccess, setParams])

  // A new result set starts without a selection.
  const [selected, setSelected] = useState<number | null>(null)
  const [selKey, setSelKey] = useState('')
  const key = params.toString()
  if (key !== selKey) {
    setSelKey(key)
    setSelected(null)
  }
  const focusNext = useRef(false)
  useEffect(() => {
    if (selected === null || !focusNext.current) return
    focusNext.current = false
    table.current?.querySelector<HTMLElement>(`tr[data-index="${selected}"] a`)?.focus()
  }, [selected])
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (document.querySelector('[role="dialog"]')) return
      const sc = shortcutFor(e, ['incidents-search', 'incidents-next', 'incidents-previous', 'incidents-open'])
      if (sc === 'incidents-search') {
        e.preventDefault()
        search.current?.focus()
      } else if (sc === 'incidents-next' || sc === 'incidents-previous') {
        focusNext.current = true
        const d = sc === 'incidents-next' ? 1 : -1
        setSelected((s) => (rows.length === 0 ? null : s === null ? 0 : Math.max(0, Math.min(rows.length - 1, s + d))))
      } else if (sc === 'incidents-open' && selected !== null) {
        if (e.target instanceof Element && e.target.closest(INTERACTIVE)) return
        const r = rows[selected]
        if (r) void navigate(incidentPath(slug, r.number))
      }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [rows, selected, navigate, slug])

  if (projects.isSuccess && !project) {
    return (
      <div className="panel" role="alert">
        <h1>Project not found</h1>
        <p>
          There is no project <LogText text={slug} /> that you are a member of. Choose a project in the header.
        </p>
      </div>
    )
  }

  const open = rows.filter((r) => r.status === 'open').length
  const noneAtAll = summary.data !== undefined && summary.data.open + summary.data.acknowledged + summary.data.resolved === 0
  const problem = list.isError ? readProblem(list.error) : null

  return (
    <div className="incidents-page">
      <div className="keys-head">
        <h1>Incidents</h1>
        {canManage && (
          <button type="button" className="button button-primary" onClick={() => setCreating(true)}>
            Create incident
          </button>
        )}
      </div>
      {project && !canManage && <p className="notice">{VIEWER_INCIDENTS}</p>}
      <form
        className="groups-toolbar"
        aria-label="Filters"
        onSubmit={(e) => {
          e.preventDefault()
          update({ q: q.trim(), service: service.trim() })
        }}
      >
        <label htmlFor={`${id}-q`} className="visually-hidden">
          Search titles
        </label>
        <input
          ref={search}
          id={`${id}-q`}
          className="search"
          type="search"
          placeholder="Search titles (press /)"
          maxLength={LIMITS.title}
          value={q}
          onChange={(e) => setQ(e.target.value)}
          onBlur={() => q.trim() !== f.q && update({ q: q.trim() })}
        />
        <label>
          Status{' '}
          <select value={f.status} onChange={(e) => update({ status: e.target.value as Filters['status'] })}>
            {STATUS_FILTERS.map((s) => (
              <option key={s} value={s}>
                {STATUS_FILTER_LABEL[s]}
              </option>
            ))}
          </select>
        </label>
        <fieldset className="inline-set">
          <legend className="visually-hidden">Severity</legend>
          {SEVERITIES.map((s) => (
            <label key={s} className="check">
              <input
                type="checkbox"
                checked={f.severity.includes(s)}
                onChange={() => update({ severity: f.severity.includes(s) ? f.severity.filter((x) => x !== s) : [...f.severity, s] })}
              />{' '}
              {s}
            </label>
          ))}
        </fieldset>
        <label>
          Assignee{' '}
          <select value={f.assignee} onChange={(e) => update({ assignee: e.target.value as Filters['assignee'] })}>
            {ASSIGNEES.map((a) => (
              <option key={a} value={a}>
                {ASSIGNEE_LABEL[a]}
              </option>
            ))}
          </select>
        </label>
        <label>
          Service{' '}
          <input value={service} maxLength={256} size={12} onChange={(e) => setService(e.target.value)} onBlur={() => service.trim() !== f.service && update({ service: service.trim() })} />
        </label>
        <label>
          Opened{' '}
          <select value={f.range} onChange={(e) => update({ range: e.target.value as Filters['range'] })}>
            {RANGES.map((r) => (
              <option key={r} value={r}>
                {RANGE_LABEL[r]}
              </option>
            ))}
          </select>
        </label>
      </form>

      {list.isPending ? (
        <p role="status">Loading incidents…</p>
      ) : problem ? (
        <div className="state-panel" role="alert">
          <p>{problem.text}</p>
          {problem.retry && (
            <button type="button" className="button" onClick={() => void list.refetch()}>
              Try again
            </button>
          )}
        </div>
      ) : rows.length === 0 ? (
        <p className="state-panel">{noneAtAll ? EMPTY_NONE : EMPTY_FILTERED}</p>
      ) : (
        <>
          <p className="status" role="status">
            Showing {rows.length} {rows.length === 1 ? 'incident' : 'incidents'}, {open} open{list.hasNextPage ? ' (more to load)' : ''}.
          </p>
          <div className="table-wrap">
            <table className="alerts incidents" ref={table}>
              <caption className="visually-hidden">Incidents, most recently active first</caption>
              <thead>
                <tr>
                  <th scope="col">Number</th>
                  <th scope="col">Title</th>
                  <th scope="col">Severity</th>
                  <th scope="col">Status</th>
                  <th scope="col">Services</th>
                  <th scope="col">Assignee</th>
                  <th scope="col">Opened</th>
                  <th scope="col">Last activity</th>
                  <th scope="col" className="num">
                    Alerts
                  </th>
                  <th scope="col" className="num">
                    Groups
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r, i) => (
                  <tr key={r.id} data-index={i} className={i === selected ? 'selected' : undefined} onClick={() => setSelected(i)}>
                    <td className="mono">#{r.number}</td>
                    <td className="wrap">
                      <Link to={incidentPath(slug, r.number)}>
                        <LogText text={r.title} mono={false} />
                      </Link>
                      {r.all_alerts_resolved && r.status !== 'resolved' && <span className="badge badge-calm">All alerts resolved</span>}
                      {r.recurrence_of !== null && (
                        <span className="muted">
                          {' '}
                          (recurrence of <Link to={incidentPath(slug, r.recurrence_of)}>#{r.recurrence_of}</Link>)
                        </span>
                      )}
                    </td>
                    <td>
                      <SeverityMark severity={r.severity} />
                    </td>
                    <td>
                      <StatusMark status={r.status} />
                    </td>
                    <td className="wrap">{r.services.length > 0 ? <LogText text={r.services.join(', ')} mono={false} /> : <span className="muted">all</span>}</td>
                    <td className="wrap">{r.assignee ? <LogText text={r.assignee_email || 'a project member'} mono={false} /> : <span className="muted">nobody</span>}</td>
                    <td>
                      <Ago iso={r.opened_at} now={now} />
                    </td>
                    <td>
                      <Ago iso={r.last_activity_at} now={now} />
                    </td>
                    <td className="num">{r.alert_count}</td>
                    <td className="num">{r.group_count}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {list.hasNextPage && (
            <button type="button" className="button more" disabled={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>
              {list.isFetchingNextPage ? 'Loading…' : 'Load more'}
            </button>
          )}
        </>
      )}
      {creating && <CreateIncidentDialog slug={slug} onClose={() => setCreating(false)} />}
    </div>
  )
}

/** A clear 404 for a malformed incident number in the address. */
export function IncidentNotFound({ slug }: { slug: string }) {
  return (
    <div className="panel" role="alert">
      <h1>Incident not found</h1>
      <p>
        This address does not name an incident. <Link to={`/projects/${encodeURIComponent(slug)}/incidents`}>See all incidents</Link>.
      </p>
    </div>
  )
}
