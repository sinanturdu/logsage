import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams } from 'react-router'
import { useProjects } from '../api/projects'
import { LogText } from '../components/LogText'
import { TemplateText } from '../groups/parts'
import { alertsKey, alertsProblem, failureSentence, mergePages, setRuleEnabled, useChannels, useDeliveries, useEvents, useRule, useStates, writeError } from './api'
import { RuleConfirmDialog, shownState } from './AlertsPage'
import { TYPE_LABEL, alertsPath, conditionSummary, humanDuration, isGroupType, parseGoDuration } from './model'
import { Ago, Checked, DeliveryMark, ProblemPanel, RuleStateCell, StateMark, Utc, formatValue, type ShownState } from './parts'
import { RuleFormDialog } from './RuleForm'

const groupHref = (slug: string, fp: string) => `/projects/${encodeURIComponent(slug)}/groups/${encodeURIComponent(fp)}`
const dur = (v: string) => {
  const s = parseGoDuration(v)
  return s === null ? v : humanDuration(s)
}

/** /projects/:slug/alerts/rules/:id */
export function RuleDetailPage() {
  const { slug = '', id = '' } = useParams()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const projects = useProjects()
  const rule = useRule(slug, id)
  const channels = useChannels(slug)
  const states = useStates(slug)
  const events = useEvents(slug, id)
  const deliveries = useDeliveries(slug, { rule: id })
  const [now] = useState(() => Date.now())
  const [editing, setEditing] = useState(false)
  const [confirm, setConfirm] = useState<'delete' | 'disable' | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const project = projects.data?.find((p) => p.slug === slug)
  const canManage = project?.role === 'owner' || project?.role === 'admin'
  const back = (
    <p className="back">
      <Link to={alertsPath(slug)}>All alert rules</Link>
    </p>
  )

  if (rule.isPending) return <>{back}<p role="status">Loading the rule…</p></>
  if (rule.isError) {
    return (
      <>
        {back}
        <ProblemPanel problem={alertsProblem(rule.error, 'rule')} onRetry={() => void rule.refetch()} busy={rule.isFetching} />
      </>
    )
  }
  const r = rule.data
  const window = parseGoDuration(r.window) ?? 0
  const mine = (states.data ?? []).filter((s) => s.rule_id === r.id)
  const evs = mergePages(events.data?.pages, 'events')
  const ds = mergePages(deliveries.data?.pages, 'notifications')
  const chs = channels.data ?? []
  const threshold = evs[0]?.threshold
  const group = isGroupType(r.type)

  const toggle = async () => {
    if (r.enabled && r.state === 'firing') return setConfirm('disable')
    setBusy(true)
    setError('')
    try {
      await setRuleEnabled(slug, r.id, !r.enabled)
      await qc.invalidateQueries({ queryKey: alertsKey(slug) })
    } catch (err) {
      setError(writeError(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="rule-detail">
      {back}
      <h1>
        <LogText text={r.name} mono={false} />
      </h1>
      <p>
        <RuleStateCell rule={r} state={shownState(r) as ShownState} now={now} /> <span className="muted">since</span> <Ago iso={r.last_change_at} now={now} none="created, no changes yet" />
      </p>
      <p className="checked-line">
        <Checked rule={r} now={now} />
      </p>
      <dl className="facts">
        <div>
          <dt>Type</dt>
          <dd>{TYPE_LABEL[r.type]}</dd>
        </div>
        <div>
          <dt>Condition</dt>
          <dd>
            <LogText text={conditionSummary(r.type, r.params, window)} mono={false} />
          </dd>
        </div>
        <div>
          <dt>For</dt>
          <dd>{dur(r.for)}</dd>
        </div>
        <div>
          <dt>Clear for</dt>
          <dd>{dur(r.clear_for)}</dd>
        </div>
        <div>
          <dt>Checked every</dt>
          <dd>{dur(r.interval)}</dd>
        </div>
        <div>
          <dt>Repeat</dt>
          <dd>{parseGoDuration(r.repeat) ? `every ${dur(r.repeat)}` : 'never'}</dd>
        </div>
        <div>
          <dt>Notifies</dt>
          <dd>
            <LogText text={r.channels.map((c) => chs.find((x) => x.id === c)?.name ?? 'deleted channel').join(', ')} mono={false} />
          </dd>
        </div>
      </dl>
      {r.note && (
        <p className="rule-note">
          <LogText text={r.note} mono={false} />
        </p>
      )}
      {canManage && (
        <div className="status-actions">
          <button type="button" className="button" disabled={busy} onClick={() => void toggle()}>
            {r.enabled ? 'Disable' : 'Enable'}
          </button>
          <button type="button" className="button" onClick={() => setEditing(true)}>
            Edit
          </button>
          <button type="button" className="button button-danger" onClick={() => setConfirm('delete')}>
            Delete
          </button>
        </div>
      )}
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}

      <h2>Current state</h2>
      {mine.length === 0 ? (
        <p>
          {r.enabled ? 'Nothing is pending or firing.' : 'The rule is disabled.'}
          {threshold !== undefined && evs[0] && (
            <>
              {' '}
              Last value {formatValue(r.type, evs[0].value)}, threshold {formatValue(r.type, threshold)}.
            </>
          )}
        </p>
      ) : (
        <div className="table-wrap">
          <table className="alerts">
            <caption className="visually-hidden">Pending and firing {group ? 'groups' : 'states'} of this rule</caption>
            <thead>
              <tr>
                <th scope="col">{group ? 'Group' : 'Key'}</th>
                <th scope="col">State</th>
                <th scope="col">Since (UTC)</th>
                <th scope="col">Value</th>
              </tr>
            </thead>
            <tbody>
              {mine.map((s) => (
                <tr key={s.dedup_key}>
                  <td className="wrap">
                    {s.fingerprint ? (
                      <Link to={groupHref(slug, s.fingerprint)} aria-label={`Error group ${s.template || s.fingerprint}`}>
                        <TemplateText text={s.template || s.fingerprint} />
                      </Link>
                    ) : (
                      'The whole rule'
                    )}
                  </td>
                  <td>
                    <StateMark state={s.state} />
                  </td>
                  <td>
                    <Utc iso={s.since} />
                  </td>
                  <td>
                    {formatValue(r.type, s.value)}
                    {threshold !== undefined && <span className="muted"> (threshold {formatValue(r.type, threshold)})</span>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <h2>Timeline</h2>
      {events.isError ? (
        <ProblemPanel problem={alertsProblem(events.error)} onRetry={() => void events.refetch()} />
      ) : events.isPending ? (
        <p role="status">Loading the timeline…</p>
      ) : evs.length === 0 ? (
        <p>No state changes yet.</p>
      ) : (
        <ol className="timeline">
          {evs.map((e) => (
            <li key={e.id}>
              <Utc iso={e.at} />: {e.from} → <StateMark state={e.to as ShownState} />, value {formatValue(r.type, e.value)} (threshold {formatValue(r.type, e.threshold)})
              {e.template && (
                <>
                  , <TemplateText text={e.template} />
                </>
              )}
              {e.reason && <span className="muted"> ({e.reason.replace(/_/g, ' ')})</span>}
            </li>
          ))}
        </ol>
      )}
      {events.hasNextPage && (
        <button type="button" className="button more" disabled={events.isFetchingNextPage} onClick={() => void events.fetchNextPage()}>
          {events.isFetchingNextPage ? 'Loading…' : 'Load more events'}
        </button>
      )}

      <h2>Deliveries</h2>
      {deliveries.isError ? (
        <ProblemPanel problem={alertsProblem(deliveries.error)} onRetry={() => void deliveries.refetch()} />
      ) : deliveries.isPending ? (
        <p role="status">Loading deliveries…</p>
      ) : ds.length === 0 ? (
        <p>No notifications sent for this rule yet.</p>
      ) : (
        <div className="table-wrap">
          <table className="alerts">
            <caption className="visually-hidden">Notifications of this rule, newest first</caption>
            <thead>
              <tr>
                <th scope="col">Created (UTC)</th>
                <th scope="col">Channel</th>
                <th scope="col">Event</th>
                <th scope="col">Status</th>
                <th scope="col">Attempts</th>
                <th scope="col">Last HTTP status</th>
                <th scope="col">Next attempt (UTC)</th>
              </tr>
            </thead>
            <tbody>
              {ds.map((d) => (
                <tr key={d.id} className={d.status === 'failed' ? 'failed' : undefined}>
                  <td>
                    <Utc iso={d.created_at} />
                  </td>
                  <td className="wrap">
                    <LogText text={d.channel_name} mono={false} />
                  </td>
                  <td>{d.event}</td>
                  <td className="wrap">
                    <DeliveryMark status={d.status} />
                    {failureSentence(d) && <>: {failureSentence(d)}</>}
                  </td>
                  <td>{d.attempts}</td>
                  <td>{d.response_status ?? '–'}</td>
                  <td>{d.status === 'pending' ? <Utc iso={d.next_attempt_at} /> : '–'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {deliveries.hasNextPage && (
        <button type="button" className="button more" disabled={deliveries.isFetchingNextPage} onClick={() => void deliveries.fetchNextPage()}>
          {deliveries.isFetchingNextPage ? 'Loading…' : 'Load more deliveries'}
        </button>
      )}

      {editing && channels.data && <RuleFormDialog slug={slug} channels={channels.data} rule={r} onClose={() => setEditing(false)} />}
      {confirm && (
        <RuleConfirmDialog
          slug={slug}
          rule={r}
          action={confirm}
          onClose={() => setConfirm(null)}
          onDone={confirm === 'delete' ? () => void navigate(alertsPath(slug), { replace: true }) : undefined}
        />
      )}
    </div>
  )
}
