import { useEffect, useId, useRef, useState, type FormEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Link, useParams } from 'react-router'
import { useProjects } from '../api/projects'
import { useMe } from '../auth/session'
import { Dialog } from '../components/Dialog'
import { LogText } from '../components/LogText'
import { StateMark, type ShownState } from '../alerts/parts'
import { rulePath } from '../alerts/model'
import { useGroupDetail } from '../groups/api'
import { TemplateText } from '../groups/parts'
import { fullUtc, relativeTime } from '../groups/time'
import { copyText } from '../explorer/clipboard'
import { useExplorerBridge } from '../palette/bridge'
import {
  LIMITS,
  SEVERITIES,
  STALE,
  addComment,
  assign,
  changeStatus,
  deleteComment,
  detailKey,
  fetchEvents,
  fieldOf,
  incidentsKey,
  isStale,
  linkGroup,
  patchIncident,
  readProblem,
  summaryKey,
  unlinkGroup,
  useDetail,
  useMembers,
  writeError,
  type Action,
  type Detail,
  type Incident,
  type LinkedAlert,
  type LinkedGroup,
  type Severity,
  type TimelineEvent,
} from './api'
import { incidentPath, incidentsPath, eventSentence, logsLink, parseNumber, actorName, type Ctx } from './model'
import { IncidentNotFound } from './IncidentsPage'
import { Ago, CountedField, SeverityMark, StatusMark, useAnnouncer } from './parts'

export const SUGGESTION_NOTE = 'Suggestions are the biggest error groups on the affected services during this incident. They are not confirmed causes.'
export const ALL_RESOLVED_PROMPT = 'All linked alerts are resolved. Resolve this incident?'
export const VIEWER_DETAIL = 'You can read this incident. Only owners and admins can change it or comment.'
export const DROPPED = 'Older events were not kept because this incident reached the event limit.'
const ACTION_LABEL: Record<Action, string> = { acknowledge: 'Acknowledge', resolve: 'Resolve', reopen: 'Reopen' }
const ACTION_DONE: Record<Action, string> = { acknowledge: 'Incident acknowledged.', resolve: 'Incident resolved.', reopen: 'Incident reopened.' }
const ACTION_STATUS: Record<Action, Incident['status']> = { acknowledge: 'acknowledged', resolve: 'resolved', reopen: 'open' }
const CLOSE_TEXT: Record<string, string> = {
  resolved: 'resolved',
  rule_disabled: 'rule disabled',
  rule_deleted: 'rule deleted',
  rule_changed: 'rule changed',
}

/** The incident's window for group counts and Open in Logs: opened − 15 min to resolved (or last activity + 1 min), at most 7 days. */
function windowOf(i: Incident): { from: number; to: number } {
  const to = Date.parse(i.resolved_at ?? i.last_activity_at) + 60_000
  return { from: Math.max(Date.parse(i.opened_at) - 15 * 60_000, to - 7 * 86_400_000 + 60_000), to }
}

/** The 409 banner: says what happened, keeps the user's input, offers a reload. */
function StaleNotice({ onReload }: { onReload: () => void }) {
  return (
    <div className="notice stale" role="alert">
      <span>{STALE}</span>{' '}
      <button type="button" className="button" onClick={onReload}>
        Reload
      </button>
    </div>
  )
}

/** Acknowledge, resolve or reopen, with an optional note. */
function StatusDialog({
  action,
  onConfirm,
  onClose,
  onReload,
}: {
  action: Action
  onConfirm: (note: string) => Promise<unknown>
  onClose: () => void
  onReload: () => void
}) {
  const [note, setNote] = useState('')
  const [error, setError] = useState('')
  const [stale, setStale] = useState(false)
  const [busy, setBusy] = useState(false)
  const noteRef = useRef<HTMLInputElement & HTMLTextAreaElement>(null)
  async function submit(e: FormEvent) {
    e.preventDefault()
    if (Array.from(note.trim()).length > LIMITS.comment) return setError(`Use at most ${LIMITS.comment} characters.`)
    setBusy(true)
    setError('')
    setStale(false)
    try {
      await onConfirm(note.trim())
      onClose()
    } catch (err) {
      if (isStale(err)) setStale(true)
      else setError(writeError(err))
      setBusy(false)
    }
  }
  return (
    <Dialog title={`${ACTION_LABEL[action]} this incident?`} onClose={onClose} initialFocus={noteRef}>
      <form onSubmit={(e) => void submit(e)} noValidate>
        <CountedField label="Note (optional, added as a comment)" value={note} onChange={setNote} max={LIMITS.comment} multiline inputRef={noteRef} error={error || undefined} />
        {stale && (
          <StaleNotice
            onReload={() => {
              setStale(false)
              onReload()
            }}
          />
        )}
        <div className="dialog-actions">
          <button type="button" className="button button-quiet" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" className="button button-primary" disabled={busy}>
            {busy ? 'Saving…' : ACTION_LABEL[action]}
          </button>
        </div>
      </form>
    </Dialog>
  )
}

/** Edit title, severity and summary; a 409 keeps the input and offers a reload. */
function EditDialog({ incident, onSave, onClose, onReload }: { incident: Incident; onSave: (b: { title: string; severity: Severity; summary: string }) => Promise<unknown>; onClose: () => void; onReload: () => void }) {
  const id = useId()
  const [title, setTitle] = useState(incident.title)
  const [severity, setSeverity] = useState<Severity>(incident.severity)
  const [summary, setSummary] = useState(incident.summary)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [stale, setStale] = useState(false)
  const [busy, setBusy] = useState(false)
  const titleRef = useRef<HTMLInputElement & HTMLTextAreaElement>(null)
  async function submit(e: FormEvent) {
    e.preventDefault()
    const t = title.trim()
    const errs: Record<string, string> = {}
    if (t === '') errs.title = 'Enter a title.'
    else if (Array.from(t).length > LIMITS.title) errs.title = `Use at most ${LIMITS.title} characters.`
    if (Array.from(summary.trim()).length > LIMITS.summary) errs.summary = `Use at most ${LIMITS.summary} characters.`
    setErrors(errs)
    if (Object.keys(errs).length) return
    setBusy(true)
    setStale(false)
    try {
      await onSave({ title: t, severity, summary: summary.trim() })
      onClose()
    } catch (err) {
      if (isStale(err)) setStale(true)
      else {
        const f = fieldOf(err)
        setErrors(f ? { [f]: writeError(err) } : { form: writeError(err) })
      }
      setBusy(false)
    }
  }
  return (
    <Dialog title="Edit incident" onClose={onClose} initialFocus={titleRef}>
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
        </div>
        <CountedField label="Summary" value={summary} onChange={setSummary} max={LIMITS.summary} error={errors.summary} multiline />
        {stale && (
          <StaleNotice
            onReload={() => {
              setStale(false)
              onReload()
            }}
          />
        )}
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
            {busy ? 'Saving…' : 'Save'}
          </button>
        </div>
      </form>
    </Dialog>
  )
}

/** A linked group: the stored template, else the group's own (fetched lazily, per fingerprint), else the first 8 hex. */
function LinkedGroupRow({ slug, g, win, canManage, onUnlink }: { slug: string; g: LinkedGroup; win: { from: number; to: number }; canManage: boolean; onUnlink: (fp: string) => void }) {
  const detail = useGroupDetail(slug, g.fingerprint, win)
  const template = g.template || detail.data?.title || ''
  return (
    <tr>
      <td className="wrap">
        <Link to={`/projects/${encodeURIComponent(slug)}/groups/${g.fingerprint}`} aria-label={`Error group ${template || g.fingerprint.slice(0, 8)}`}>
          {template ? <TemplateText text={template} /> : <span className="mono">{g.fingerprint.slice(0, 8)}</span>}
        </Link>
      </td>
      <td className="num">{detail.data ? detail.data.count.toLocaleString('en-US') : detail.isError ? '–' : '…'}</td>
      <td>{g.source === 'auto' ? 'automatic' : 'by hand'}</td>
      <td>
        <time dateTime={g.linked_at} title={fullUtc(Date.parse(g.linked_at))}>
          {fullUtc(Date.parse(g.linked_at))}
        </time>
      </td>
      {canManage && (
        <td>
          <button type="button" className="button button-quiet" aria-label={`Unlink group ${template || g.fingerprint.slice(0, 8)}`} onClick={() => onUnlink(g.fingerprint)}>
            Unlink
          </button>
        </td>
      )}
    </tr>
  )
}

function alertTemplate(a: LinkedAlert, groups: readonly LinkedGroup[]): string {
  const m = /^fp:([0-9a-f]{16})/.exec(a.state_key)
  if (!m) return ''
  return groups.find((g) => g.fingerprint === m[1])?.template ?? ''
}

/** One timeline entry: the sentence, the actor and time, the comment body as text. */
function TimelineItem({ e, ctx, now, canDelete, onDelete }: { e: TimelineEvent; ctx: Ctx; now: number; canDelete: boolean; onDelete: (e: TimelineEvent) => void }) {
  const at = Date.parse(e.at)
  return (
    <li className={`tl tl-${e.type === 'comment' ? 'comment' : 'event'}`}>
      <p className="tl-line">
        <LogText text={eventSentence(e, ctx)} mono={false} />{' '}
        <span className="muted">
          <time dateTime={e.at} title={fullUtc(at)}>
            {relativeTime(at, now)}
          </time>
          , {fullUtc(at)}
        </span>
      </p>
      {e.type === 'comment' && e.body !== null && (
        <div className="comment-body">
          <LogText text={e.body} mono={false} />
          {canDelete && (
            <button type="button" className="button button-quiet" aria-label={`Delete the comment by ${actorName(e)}`} onClick={() => onDelete(e)}>
              Delete
            </button>
          )}
        </div>
      )}
    </li>
  )
}

/** /projects/:slug/incidents/:number; a malformed number is a 404 page. */
export function IncidentDetailRoute() {
  const { slug = '', number } = useParams()
  const n = parseNumber(number)
  if (n === null) return <IncidentNotFound slug={slug} />
  return <IncidentDetailPage key={n} slug={slug} n={n} />
}

function IncidentDetailPage({ slug, n }: { slug: string; n: number }) {
  const qc = useQueryClient()
  const projects = useProjects()
  const project = projects.data?.find((p) => p.slug === slug)
  const canManage = project?.role === 'owner' || project?.role === 'admin'
  const detail = useDetail(slug, n)
  const members = useMembers(slug)
  const me = useMe()
  const [now] = useState(() => Date.now())
  const [dialog, setDialog] = useState<Action | 'edit' | null>(null)
  const [deleting, setDeleting] = useState<TimelineEvent | null>(null)
  const [older, setOlder] = useState<TimelineEvent[]>([])
  const [olderBefore, setOlderBefore] = useState<number | null | undefined>(undefined)
  const [loadingOlder, setLoadingOlder] = useState(false)
  const [comment, setComment] = useState('')
  const [commentError, setCommentError] = useState('')
  const [actionError, setActionError] = useState('')
  const [notice, setNotice] = useState('')
  const [announcer, announce] = useAnnouncer()
  const id = useId()

  const reload = () => {
    setOlder([])
    setOlderBefore(undefined)
    void qc.invalidateQueries({ queryKey: detailKey(slug, n) })
  }
  const refreshLists = () => {
    void qc.invalidateQueries({ queryKey: incidentsKey(slug), exact: false, refetchType: 'none' })
    void qc.invalidateQueries({ queryKey: summaryKey(slug) })
  }
  const setIncident = (i: Incident) => qc.setQueryData<Detail>(detailKey(slug, n), (d) => (d ? { ...d, incident: i } : d))

  /** Optimistic status change: shown at once, rolled back on failure. */
  async function runAction(action: Action, note: string) {
    const before = qc.getQueryData<Detail>(detailKey(slug, n))
    if (!before) return
    qc.setQueryData<Detail>(detailKey(slug, n), { ...before, incident: { ...before.incident, status: ACTION_STATUS[action] } })
    try {
      const res = await changeStatus(slug, n, action, before.incident.version, note)
      setIncident(res.incident)
      announce(ACTION_DONE[action])
      setActionError('')
      reload()
      refreshLists()
    } catch (err) {
      qc.setQueryData(detailKey(slug, n), before)
      throw err
    }
  }

  // The command palette runs the detail's actions here (only those the role may do).
  const d = detail.data
  useExplorerBridge({
    incident: d ? { canManage, status: d.incident.status } : undefined,
    run: (a) => {
      if (a.type === 'incident-action') setDialog(a.action)
      else if (a.type === 'incident-assign-me') void doAssign(meId)
      else if (a.type === 'incident-copy-link') void copyText(window.location.href).then((ok) => setNotice(ok ? 'Copied the link to this incident.' : `Copy is not available here. Copy the address: ${window.location.href}`))
    },
  })

  // "Assign to me": the signed-in user, when they are a member of the list.
  const meId = members.data?.some((m) => m.user_id === me.data?.user.id) ? (me.data?.user.id ?? '') : ''

  async function doAssign(userId: string | null) {
    try {
      const res = await assign(slug, n, userId)
      setIncident(res.incident)
      announce(userId ? 'Incident assigned.' : 'Assignee removed.')
      setActionError('')
      reload()
      refreshLists()
    } catch (err) {
      setActionError(writeError(err))
    }
  }

  async function groupAction(fp: string, link: boolean) {
    try {
      await (link ? linkGroup(slug, n, fp) : unlinkGroup(slug, n, fp))
      announce(link ? 'Group linked.' : 'Group unlinked.')
      setActionError('')
      reload()
      refreshLists()
    } catch (err) {
      setActionError(writeError(err))
    }
  }

  async function submitComment(e: FormEvent) {
    e.preventDefault()
    const body = comment.trim()
    if (body === '') return setCommentError('Write a comment first.')
    if (Array.from(body).length > LIMITS.comment) return setCommentError(`Use at most ${LIMITS.comment} characters.`)
    try {
      await addComment(slug, n, body)
      setComment('')
      setCommentError('')
      announce('Comment added.')
      reload()
    } catch (err) {
      setCommentError(writeError(err))
    }
  }

  async function loadOlder() {
    const before = olderBefore ?? d?.next_before
    if (!before) return
    setLoadingOlder(true)
    try {
      const page = await fetchEvents(slug, n, before)
      setOlder((o) => [...o, ...page.events.filter((x) => !o.some((y) => y.id === x.id))])
      setOlderBefore(page.next_before)
    } catch (err) {
      setActionError(writeError(err))
    } finally {
      setLoadingOlder(false)
    }
  }

  // The copy notice goes away after a while.
  useEffect(() => {
    if (!notice) return
    const t = setTimeout(() => setNotice(''), 6000)
    return () => clearTimeout(t)
  }, [notice])

  const back = (
    <p className="back">
      <Link to={incidentsPath(slug)}>All incidents</Link>
    </p>
  )
  if (detail.isPending) {
    return (
      <div className="incident-page">
        {back}
        <div className="skeleton" role="status" aria-label="Loading the incident">
          <div className="sk-line sk-wide" />
          <div className="sk-line" />
          <div className="sk-line" />
        </div>
      </div>
    )
  }
  if (detail.isError) {
    const p = readProblem(detail.error, 'incident')
    return (
      <div className="incident-page">
        {back}
        <div className="state-panel" role="alert">
          <p>{p.text}</p>
          {p.retry && (
            <button type="button" className="button" onClick={() => void detail.refetch()}>
              Try again
            </button>
          )}
        </div>
      </div>
    )
  }

  const data = detail.data
  const inc = data.incident
  const win = windowOf(inc)
  const ctx: Ctx = { alerts: data.alerts, members: members.data ?? [], templates: new Map(data.groups.map((g) => [g.fingerprint, g.template])) }
  const events = [...data.events, ...older.filter((o) => !data.events.some((e) => e.id === o.id))]
  const hasOlder = olderBefore === undefined ? data.next_before !== null : olderBefore !== null
  const assigneeLabel = inc.assignee ? inc.assignee_email || members.data?.find((m) => m.user_id === inc.assignee)?.email || 'a project member' : 'nobody'

  return (
    <div className="incident-page">
      {back}
      {announcer}
      <h1 className="incident-title">
        <span className="mono">#{inc.number}</span> <LogText text={inc.title} mono={false} />
      </h1>
      <p className="incident-marks">
        <SeverityMark severity={inc.severity} /> <StatusMark status={inc.status} />
        {inc.recurrence_of !== null && (
          <span>
            {' '}
            Recurrence of <Link to={incidentPath(slug, inc.recurrence_of)}>#{inc.recurrence_of}</Link>.
          </span>
        )}
      </p>
      <dl className="facts">
        <div>
          <dt>Assignee</dt>
          <dd>
            <LogText text={assigneeLabel} mono={false} />
          </dd>
        </div>
        <div>
          <dt>Opened</dt>
          <dd>
            <Ago iso={inc.opened_at} now={now} /> ({inc.source === 'auto' ? 'by an alert rule' : 'by hand'})
          </dd>
        </div>
        <div>
          <dt>Last activity</dt>
          <dd>
            <Ago iso={inc.last_activity_at} now={now} />
          </dd>
        </div>
        <div>
          <dt>Services</dt>
          <dd>{inc.services.length ? <LogText text={inc.services.join(', ')} mono={false} /> : 'all'}</dd>
        </div>
      </dl>
      {inc.summary && (
        <p className="rule-note">
          <LogText text={inc.summary} mono={false} />
        </p>
      )}
      <p>
        <Link to={logsLink(slug, inc.opened_at, inc.resolved_at ?? inc.last_activity_at, data.groups.map((g) => g.fingerprint))}>Open in Logs</Link>
      </p>
      {!canManage && project && <p className="notice">{VIEWER_DETAIL}</p>}
      {canManage && (
        <div className="status-actions">
          {inc.status === 'open' && (
            <button type="button" className="button" onClick={() => setDialog('acknowledge')}>
              Acknowledge
            </button>
          )}
          {inc.status !== 'resolved' && (
            <button type="button" className="button" onClick={() => setDialog('resolve')}>
              Resolve
            </button>
          )}
          {inc.status === 'resolved' && (
            <button type="button" className="button" onClick={() => setDialog('reopen')}>
              Reopen
            </button>
          )}
          <button type="button" className="button button-quiet" onClick={() => setDialog('edit')}>
            Edit
          </button>
          <label htmlFor={`${id}-assign`} className="visually-hidden">
            Assignee
          </label>
          <select
            id={`${id}-assign`}
            value={inc.assignee ?? ''}
            onChange={(e) => void doAssign(e.target.value || null)}
            aria-describedby={`${id}-assign-hint`}
          >
            <option value="">Unassigned</option>
            {(members.data ?? []).map((m) => (
              <option key={m.user_id} value={m.user_id}>
                {m.email}
              </option>
            ))}
          </select>
          <span id={`${id}-assign-hint`} className="visually-hidden">
            Choose a project member to assign
          </span>
          {meId && inc.assignee !== meId && (
            <button type="button" className="button button-quiet" onClick={() => void doAssign(meId)}>
              Assign to me
            </button>
          )}
        </div>
      )}
      {actionError && (
        <p className="form-error" role="alert">
          {actionError}
        </p>
      )}
      <p className="shell-notice" role="status">
        {notice}
      </p>
      {inc.all_alerts_resolved && inc.status !== 'resolved' && (
        <div className="notice calm">
          <span>{ALL_RESOLVED_PROMPT}</span>{' '}
          {canManage && (
            <button type="button" className="button" onClick={() => setDialog('resolve')}>
              Resolve
            </button>
          )}
        </div>
      )}

      <div className="incident-sections">
        <section aria-labelledby={`${id}-alerts`}>
          <h2 id={`${id}-alerts`}>Linked alerts</h2>
          {data.alerts.length === 0 ? (
            <p>No alerts are linked.</p>
          ) : (
            <div className="table-wrap">
              <table className="alerts">
                <caption className="visually-hidden">Alert states linked to this incident</caption>
                <thead>
                  <tr>
                    <th scope="col">Rule</th>
                    <th scope="col">State</th>
                    <th scope="col">Group</th>
                    <th scope="col">Linked since</th>
                    <th scope="col">Closed</th>
                  </tr>
                </thead>
                <tbody>
                  {data.alerts.map((a) => {
                    const tpl = alertTemplate(a, data.groups)
                    return (
                      <tr key={`${a.rule_id ?? a.rule_name}/${a.state_key}`}>
                        <td className="wrap">
                          {a.rule_id ? (
                            <Link to={rulePath(slug, a.rule_id)}>
                              <LogText text={a.rule_name} mono={false} />
                            </Link>
                          ) : (
                            <>
                              <LogText text={a.rule_name} mono={false} /> <span className="muted">(deleted)</span>
                            </>
                          )}
                        </td>
                        <td>{a.state ? <StateMark state={a.state as ShownState} /> : <span className="muted">no state</span>}</td>
                        <td className="wrap">{tpl ? <TemplateText text={tpl} /> : a.state_key.startsWith('fp:') ? <span className="mono">{a.state_key.slice(3, 11)}</span> : '–'}</td>
                        <td>
                          <Ago iso={a.linked_at} now={now} />
                        </td>
                        <td>{a.closed_at ? CLOSE_TEXT[a.close_reason] ?? 'closed' : <span className="muted">open</span>}</td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          )}
        </section>

        <section aria-labelledby={`${id}-groups`}>
          <h2 id={`${id}-groups`}>Linked error groups</h2>
          {data.groups.length === 0 ? (
            <p>No error groups are linked.</p>
          ) : (
            <div className="table-wrap">
              <table className="alerts">
                <caption className="visually-hidden">Error groups linked to this incident</caption>
                <thead>
                  <tr>
                    <th scope="col">Group</th>
                    <th scope="col" className="num">
                      Events in the incident window
                    </th>
                    <th scope="col">Linked</th>
                    <th scope="col">Since (UTC)</th>
                    {canManage && (
                      <th scope="col">
                        <span className="visually-hidden">Actions</span>
                      </th>
                    )}
                  </tr>
                </thead>
                <tbody>
                  {data.groups.map((g) => (
                    <LinkedGroupRow key={g.fingerprint} slug={slug} g={g} win={win} canManage={canManage} onUnlink={(fp) => void groupAction(fp, false)} />
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </section>

        <section aria-labelledby={`${id}-suggested`}>
          <h2 id={`${id}-suggested`}>Suggested error groups</h2>
          <p className="muted">{SUGGESTION_NOTE}</p>
          {data.suggested_groups === null ? (
            <p>Suggestions are not available right now{data.suggested_groups_error ? `: ${data.suggested_groups_error}` : ''}.</p>
          ) : data.suggested_groups.length === 0 ? (
            <p>No other error groups were active on these services during this incident.</p>
          ) : (
            <ul className="suggestions">
              {data.suggested_groups.map((s) => (
                <li key={s.fingerprint}>
                  <Link to={`/projects/${encodeURIComponent(slug)}/groups/${s.fingerprint}`}>
                    <TemplateText text={s.title} />
                  </Link>{' '}
                  <span className="muted">
                    {s.count.toLocaleString('en-US')} events
                    {s.services.length > 0 && (
                      <>
                        {' '}
                        in <LogText text={s.services.join(', ')} mono={false} />
                      </>
                    )}
                  </span>
                  {s.is_new && <span className="badge badge-new">New</span>}
                  {s.regressed && <span className="badge badge-regressed">Regressed</span>}
                  {canManage && (
                    <button type="button" className="button button-quiet" aria-label={`Link group ${s.title}`} onClick={() => void groupAction(s.fingerprint, true)}>
                      Link
                    </button>
                  )}
                </li>
              ))}
            </ul>
          )}
        </section>

        <section aria-labelledby={`${id}-timeline`}>
          <h2 id={`${id}-timeline`}>Timeline</h2>
          {canManage ? (
            <form className="comment-form" onSubmit={(e) => void submitComment(e)} noValidate>
              <CountedField label="Add a comment" value={comment} onChange={setComment} max={LIMITS.comment} multiline error={commentError || undefined} />
              <button type="submit" className="button">
                Add comment
              </button>
            </form>
          ) : (
            <p className="muted">Only owners and admins can comment.</p>
          )}
          <ol className="timeline incident-timeline">
            {events.map((e) => (
              <TimelineItem key={e.id} e={e} ctx={ctx} now={now} canDelete={canManage} onDelete={setDeleting} />
            ))}
          </ol>
          {hasOlder && (
            <button type="button" className="button more" disabled={loadingOlder} onClick={() => void loadOlder()}>
              {loadingOlder ? 'Loading…' : 'Load more'}
            </button>
          )}
          {!hasOlder && inc.events_dropped > 0 && <p className="notice">{DROPPED}</p>}
        </section>
      </div>

      {(dialog === 'acknowledge' || dialog === 'resolve' || dialog === 'reopen') && (
        <StatusDialog action={dialog} onConfirm={(note) => runAction(dialog, note)} onClose={() => setDialog(null)} onReload={reload} />
      )}
      {dialog === 'edit' && (
        <EditDialog
          incident={inc}
          onClose={() => setDialog(null)}
          onReload={reload}
          onSave={async (b) => {
            const cur = qc.getQueryData<Detail>(detailKey(slug, n))?.incident ?? inc
            const res = await patchIncident(slug, n, { ...b, version: cur.version })
            setIncident(res.incident)
            announce('Incident saved.')
            reload()
            refreshLists()
          }}
        />
      )}
      {deleting && (
        <DeleteCommentDialog
          event={deleting}
          onClose={() => setDeleting(null)}
          onConfirm={async () => {
            await deleteComment(slug, n, deleting.id)
            announce('Comment deleted.')
            reload()
          }}
        />
      )}
    </div>
  )
}

function DeleteCommentDialog({ event, onConfirm, onClose }: { event: TimelineEvent; onConfirm: () => Promise<void>; onClose: () => void }) {
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const cancel = useRef<HTMLButtonElement>(null)
  return (
    <Dialog title="Delete this comment?" onClose={onClose} initialFocus={cancel}>
      <p>
        The comment by <LogText text={actorName(event)} mono={false} /> is removed for everyone. The timeline keeps a note that a comment was deleted.
      </p>
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <div className="dialog-actions">
        <button ref={cancel} type="button" className="button button-quiet" onClick={onClose}>
          Cancel
        </button>
        <button
          type="button"
          className="button button-danger"
          disabled={busy}
          onClick={async () => {
            setBusy(true)
            try {
              await onConfirm()
              onClose()
            } catch (err) {
              setError(writeError(err))
              setBusy(false)
            }
          }}
        >
          {busy ? 'Deleting…' : 'Delete comment'}
        </button>
      </div>
    </Dialog>
  )
}
