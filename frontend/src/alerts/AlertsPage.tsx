import { useCallback, useEffect, useId, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router'
import { useProjects } from '../api/projects'
import { Dialog } from '../components/Dialog'
import { LogText } from '../components/LogText'
import { shortcutFor } from '../shortcuts'
import {
  alertsKey,
  alertsProblem,
  deleteRule,
  isAlertsOff,
  mergePages,
  setRuleEnabled,
  testChannel,
  useChannels,
  useDeliveries,
  useEvents,
  useLastDeliveries,
  useRules,
  writeError,
  failureSentence,
  TEST_LIMIT,
  type Channel,
  type Rule,
} from './api'
import { ApiError } from '../api/client'
import { ChannelFormDialog, DeleteChannelDialog, RotateDialog, kindLabel } from './ChannelDialogs'
import { TABS, TAB_LABEL, TYPE_LABEL, conditionSummary, parseAlertsState, parseGoDuration, rulePath, serializeAlertsState, type AlertsState, type Tab } from './model'
import { Ago, Checked, DeliveryMark, ProblemPanel, RuleStateCell, StateMark, Utc, formatValue, type ShownState } from './parts'
import { CREATE_A_CHANNEL, RuleFormDialog } from './RuleForm'

export const EMPTY_RULES = 'No alert rules yet. Create a channel first, then a rule that notifies it.'
export const FIRST_CHANNEL = 'Channel created. Create a rule that notifies it.'
export const EMPTY_CHANNELS = 'No channels yet. A channel is where notifications go: a webhook or a Slack incoming webhook.'
export const EMPTY_ACTIVITY = 'No alert activity yet. State changes of your rules appear here.'
export const VIEWER_ALERTS = 'You can see this project’s alerts. Only owners and admins can change rules and channels.'
export const DELETE_FIRING = 'This rule is firing: deleting it sends a final resolved notice to its channels, with the reason "rule deleted".'
export const DISABLE_FIRING = 'Disabling a firing rule sends a final resolved notice with the reason "rule disabled".'

export const shownState = (r: Pick<Rule, 'enabled' | 'state'>): ShownState => (r.enabled ? r.state : 'disabled')
const INTERACTIVE = 'a, button, input, select, textarea'

/** The tabs (WAI-ARIA: arrow keys, Home, End), kept in the URL. */
function Tabs({ tab, onSelect, id }: { tab: Tab; onSelect: (t: Tab) => void; id: string }) {
  const refs = useRef<(HTMLButtonElement | null)[]>([])
  const onKey = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    const i = TABS.indexOf(tab)
    const keys: Record<string, number> = { ArrowRight: i + 1, ArrowLeft: i - 1, Home: 0, End: TABS.length - 1 }
    const next = keys[e.key]
    if (next === undefined) return
    e.preventDefault()
    const t = TABS[(next + TABS.length) % TABS.length] as Tab
    onSelect(t)
    refs.current[TABS.indexOf(t)]?.focus()
  }
  return (
    <div role="tablist" aria-label="Alerts sections" className="atabs" onKeyDown={onKey}>
      {TABS.map((t, i) => (
        <button
          key={t}
          ref={(el) => {
            refs.current[i] = el
          }}
          type="button"
          role="tab"
          id={`${id}-tab-${t}`}
          aria-selected={t === tab}
          aria-controls={`${id}-panel`}
          tabIndex={t === tab ? 0 : -1}
          onClick={() => onSelect(t)}
        >
          {TAB_LABEL[t]}
        </button>
      ))}
    </div>
  )
}

/** j/k move through the table's rows; Enter opens the row's link. */
function useRowKeys(count: number, open: (i: number) => void, table: React.RefObject<HTMLTableElement | null>) {
  const [selected, setSelected] = useState<number | null>(null)
  // Only keyboard selection moves focus; a click selects the row in place.
  const focusNext = useRef(false)
  useEffect(() => {
    if (selected === null || !focusNext.current) return
    focusNext.current = false
    table.current?.querySelector<HTMLElement>(`tr[data-index="${selected}"] :is(a, button)`)?.focus()
  }, [selected, table])
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (document.querySelector('[role="dialog"]')) return
      const id = shortcutFor(e, ['alerts-next', 'alerts-previous', 'alerts-open'])
      if (id === 'alerts-next' || id === 'alerts-previous') focusNext.current = true
      if (id === 'alerts-next') setSelected((s) => (count === 0 ? null : s === null ? 0 : Math.min(s + 1, count - 1)))
      else if (id === 'alerts-previous') setSelected((s) => (count === 0 ? null : s === null ? 0 : Math.max(s - 1, 0)))
      else if (id === 'alerts-open' && selected !== null) {
        if (e.target instanceof Element && e.target.closest(INTERACTIVE)) return
        open(selected)
      }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [count, selected, open])
  return [selected, setSelected] as const
}

function EnabledSwitch({ slug, rule, canManage, onDisableFiring }: { slug: string; rule: Rule; canManage: boolean; onDisableFiring: (r: Rule) => void }) {
  const qc = useQueryClient()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  if (!canManage) return <span>{rule.enabled ? 'On' : 'Off'}</span>
  const flip = async () => {
    if (rule.enabled && rule.state === 'firing') return onDisableFiring(rule)
    setBusy(true)
    setError('')
    try {
      await setRuleEnabled(slug, rule.id, !rule.enabled)
      await qc.invalidateQueries({ queryKey: alertsKey(slug) })
    } catch (err) {
      setError(writeError(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <>
      <button
        type="button"
        role="switch"
        aria-checked={rule.enabled}
        aria-label={`Enabled: ${rule.name}`}
        className={`switch${rule.enabled ? ' switch-on' : ''}`}
        disabled={busy}
        onClick={() => void flip()}
      >
        <span className="switch-track" aria-hidden="true" />
        {rule.enabled ? 'On' : 'Off'}
      </button>
      {error && (
        <span className="field-error" role="alert">
          {' '}
          {error}
        </span>
      )}
    </>
  )
}

/** Confirm a delete, or a disable of a firing rule (both send a final resolved notice). */
export function RuleConfirmDialog({ slug, rule, action, onClose, onDone }: { slug: string; rule: Rule; action: 'delete' | 'disable'; onClose: () => void; onDone?: (() => void) | undefined }) {
  const qc = useQueryClient()
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const cancel = useRef<HTMLButtonElement>(null)
  const firing = rule.enabled && rule.state === 'firing'
  async function go() {
    setBusy(true)
    setError('')
    try {
      if (action === 'delete') await deleteRule(slug, rule.id)
      else await setRuleEnabled(slug, rule.id, false)
      await qc.invalidateQueries({ queryKey: alertsKey(slug) })
      onClose()
      onDone?.()
    } catch (err) {
      setError(writeError(err))
      setBusy(false)
    }
  }
  return (
    <Dialog title={action === 'delete' ? 'Delete this alert rule?' : 'Disable this firing rule?'} onClose={onClose} initialFocus={cancel}>
      <p>
        <LogText text={rule.name} mono={false} />
      </p>
      {action === 'delete' ? (
        <p>
          {firing ? DELETE_FIRING : 'Its pending notifications are cancelled.'} Its past events and deliveries stay in Activity. This cannot be undone.
        </p>
      ) : (
        <p>{DISABLE_FIRING} You can enable it again later.</p>
      )}
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <div className="dialog-actions">
        <button ref={cancel} type="button" className="button button-quiet" onClick={onClose}>
          Cancel
        </button>
        <button type="button" className="button button-danger" disabled={busy} onClick={() => void go()}>
          {busy ? 'Working…' : action === 'delete' ? 'Delete rule' : 'Disable rule'}
        </button>
      </div>
    </Dialog>
  )
}

function ChannelNames({ ids, channels }: { ids: readonly string[]; channels: readonly Channel[] }) {
  const names = ids.map((id) => channels.find((c) => c.id === id)?.name ?? 'deleted channel')
  return (
    <>
      {ids.length}: <LogText text={names.join(', ')} mono={false} />
    </>
  )
}

function RulesTab({ slug, canManage, rules, channels, now, onCreate, onCreateChannel }: { slug: string; canManage: boolean; rules: Rule[]; channels: Channel[]; now: number; onCreate: () => void; onCreateChannel: () => void }) {
  const navigate = useNavigate()
  const table = useRef<HTMLTableElement>(null)
  const [editing, setEditing] = useState<Rule | null>(null)
  const [confirm, setConfirm] = useState<{ rule: Rule; action: 'delete' | 'disable' } | null>(null)
  const open = useCallback(
    (i: number) => {
      const r = rules[i]
      if (r) void navigate(rulePath(slug, r.id))
    },
    [rules, navigate, slug],
  )
  const [selected, setSelected] = useRowKeys(rules.length, open, table)
  const firing = rules.filter((r) => r.enabled && r.state === 'firing').length

  return (
    <>
      <div className="tab-head">
        <p className="status" role="status">
          {rules.length === 0 ? '' : `${rules.length} ${rules.length === 1 ? 'rule' : 'rules'}, ${firing} firing.`}
        </p>
        {canManage && (
          <button type="button" className="button button-primary" onClick={onCreate} disabled={rules.length >= 50} title={rules.length >= 50 ? 'At most 50 rules per project.' : undefined}>
            Create rule
          </button>
        )}
      </div>
      {rules.length === 0 ? (
        <div className="state-panel">
          <p>{EMPTY_RULES}</p>
          {canManage && channels.length === 0 && (
            <button type="button" className="button" onClick={onCreateChannel}>
              {CREATE_A_CHANNEL}
            </button>
          )}
        </div>
      ) : (
        <div className="table-wrap">
          <table className="alerts" ref={table}>
            <caption className="visually-hidden">Alert rules of this project</caption>
            <thead>
              <tr>
                <th scope="col">Name</th>
                <th scope="col">Type</th>
                <th scope="col">Condition</th>
                <th scope="col">State</th>
                <th scope="col">Checked</th>
                <th scope="col">Last change</th>
                <th scope="col">Enabled</th>
                <th scope="col">Channels</th>
                {canManage && (
                  <th scope="col">
                    <span className="visually-hidden">Actions</span>
                  </th>
                )}
              </tr>
            </thead>
            <tbody>
              {rules.map((r, i) => (
                <tr key={r.id} data-index={i} className={i === selected ? 'selected' : undefined} onClick={() => setSelected(i)}>
                  <td className="wrap">
                    <Link to={rulePath(slug, r.id)}>
                      <LogText text={r.name} mono={false} />
                    </Link>
                  </td>
                  <td>{TYPE_LABEL[r.type]}</td>
                  <td className="wrap">
                    <LogText text={conditionSummary(r.type, r.params, parseGoDuration(r.window) ?? 0)} mono={false} />
                  </td>
                  <td>
                    <RuleStateCell rule={r} state={shownState(r)} now={now} />
                    {r.enabled && r.firing + r.pending > 1 && <span className="muted"> ({r.firing} firing, {r.pending} pending)</span>}
                  </td>
                  <td className="wrap">
                    <Checked rule={r} now={now} />
                  </td>
                  <td>
                    <Ago iso={r.last_change_at} now={now} none="no changes yet" />
                  </td>
                  <td>
                    <EnabledSwitch slug={slug} rule={r} canManage={canManage} onDisableFiring={(rule) => setConfirm({ rule, action: 'disable' })} />
                  </td>
                  <td className="wrap">
                    <ChannelNames ids={r.channels} channels={channels} />
                  </td>
                  {canManage && (
                    <td className="actions">
                      <button type="button" className="button button-quiet" aria-label={`Edit rule ${r.name}`} onClick={() => setEditing(r)}>
                        Edit
                      </button>
                      <button type="button" className="button button-quiet" aria-label={`Delete rule ${r.name}`} onClick={() => setConfirm({ rule: r, action: 'delete' })}>
                        Delete
                      </button>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {editing && <RuleFormDialog slug={slug} channels={channels} rule={editing} onClose={() => setEditing(null)} />}
      {confirm && <RuleConfirmDialog slug={slug} rule={confirm.rule} action={confirm.action} onClose={() => setConfirm(null)} />}
    </>
  )
}

type TestResult = { id: string; text: string }

function ChannelsTab({ slug, canManage, rules, channels, now, onCreate }: { slug: string; canManage: boolean; rules: Rule[]; channels: Channel[]; now: number; onCreate: () => void }) {
  const last = useLastDeliveries(slug, channels)
  const [editing, setEditing] = useState<Channel | null>(null)
  const [rotating, setRotating] = useState<Channel | null>(null)
  const [deleting, setDeleting] = useState<Channel | null>(null)
  const [testing, setTesting] = useState('')
  const [result, setResult] = useState<TestResult | null>(null)
  const table = useRef<HTMLTableElement>(null)
  const noop = useCallback(() => {}, [])
  const [selected, setSelected] = useRowKeys(channels.length, noop, table)

  // After a 429 the channel's Send test waits for the Retry-After: one
  // click is one request, never a burst of refused ones. Never retried.
  const [limited, setLimited] = useState<ReadonlySet<string>>(new Set())
  const timers = useRef(new Map<string, ReturnType<typeof setTimeout>>())
  useEffect(() => {
    const all = timers.current
    return () => all.forEach((t) => clearTimeout(t))
  }, [])
  const sendTest = async (c: Channel) => {
    setTesting(c.id)
    setResult(null)
    try {
      await testChannel(slug, c.id)
      setResult({ id: c.id, text: 'Test queued. It is marked as a test ("test": true, event alert.test) and appears in the delivery history.' })
    } catch (err) {
      const rateLimited = err instanceof ApiError && err.kind === 'rate_limited'
      const wait = rateLimited ? (err.retryAfter ?? 600) : 0
      setResult({ id: c.id, text: rateLimited ? TEST_LIMIT(wait) : writeError(err) })
      if (rateLimited) {
        setLimited((s) => new Set(s).add(c.id))
        clearTimeout(timers.current.get(c.id))
        timers.current.set(
          c.id,
          setTimeout(() => {
            timers.current.delete(c.id)
            setLimited((s) => {
              const next = new Set(s)
              next.delete(c.id)
              return next
            })
          }, wait * 1000),
        )
      }
    } finally {
      setTesting('')
    }
  }

  return (
    <>
      <div className="tab-head">
        <p className="status">{channels.length === 0 ? '' : `${channels.length} of 10 channels.`}</p>
        {canManage && (
          <button type="button" className="button button-primary" onClick={onCreate} disabled={channels.length >= 10} title={channels.length >= 10 ? 'At most 10 channels per project.' : undefined}>
            Create channel
          </button>
        )}
      </div>
      {channels.length === 0 ? (
        <p className="state-panel">{EMPTY_CHANNELS}</p>
      ) : (
        <div className="table-wrap">
          <table className="alerts" ref={table}>
            <caption className="visually-hidden">Notification channels of this project</caption>
            <thead>
              <tr>
                <th scope="col">Type</th>
                <th scope="col">Name</th>
                <th scope="col">Host</th>
                <th scope="col">Last delivery</th>
                <th scope="col">Sample log</th>
                {canManage && (
                  <th scope="col">
                    <span className="visually-hidden">Actions</span>
                  </th>
                )}
              </tr>
            </thead>
            <tbody>
              {channels.map((c, i) => {
                const d = last[i]?.data
                return (
                  <tr key={c.id} data-index={i} className={i === selected ? 'selected' : undefined} onClick={() => setSelected(i)}>
                    <td>{kindLabel(c.kind)}</td>
                    <td className="wrap">
                      <LogText text={c.name} mono={false} />
                    </td>
                    <td>
                      <LogText text={c.url_host} />
                    </td>
                    <td className="wrap">
                      {d ? (
                        <>
                          <DeliveryMark status={d.status} />
                          {d.response_status !== null && <> (HTTP {d.response_status})</>}, <Ago iso={d.created_at} now={now} />
                          {d.status !== 'sent' && failureSentence(d) && <span className="muted"> {failureSentence(d)}</span>}
                          {d.test && ' (test)'}
                        </>
                      ) : last[i]?.isPending ? (
                        '…'
                      ) : (
                        'none yet'
                      )}
                    </td>
                    <td>{c.include_sample ? 'Included' : 'Not included'}</td>
                    {canManage && (
                      <td className="actions">
                        <button
                          type="button"
                          className="button button-quiet"
                          disabled={testing === c.id || limited.has(c.id)}
                          aria-label={`Send a test to ${c.name}`}
                          onClick={() => void sendTest(c)}
                        >
                          {testing === c.id ? 'Sending…' : 'Send test'}
                        </button>
                        {c.kind === 'webhook' && (
                          <button type="button" className="button button-quiet" aria-label={`Rotate the secret of ${c.name}`} onClick={() => setRotating(c)}>
                            Rotate secret
                          </button>
                        )}
                        <button type="button" className="button button-quiet" aria-label={`Edit channel ${c.name}`} onClick={() => setEditing(c)}>
                          Edit
                        </button>
                        <button type="button" className="button button-quiet" aria-label={`Delete channel ${c.name}`} onClick={() => setDeleting(c)}>
                          Delete
                        </button>
                        {result?.id === c.id && (
                          <p className="test-result" role="status">
                            {result.text}
                          </p>
                        )}
                      </td>
                    )}
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
      {editing && <ChannelFormDialog slug={slug} channel={editing} onClose={() => setEditing(null)} />}
      {rotating && <RotateDialog slug={slug} channel={rotating} onClose={() => setRotating(null)} />}
      {deleting && <DeleteChannelDialog slug={slug} channel={deleting} rules={rules} onClose={() => setDeleting(null)} />}
    </>
  )
}

function ActivityTab({ slug, rules, rule, onRule }: { slug: string; rules: Rule[]; rule: string; onRule: (id: string) => void }) {
  const id = useId()
  const events = useEvents(slug, rule)
  const failed = useDeliveries(slug, { status: 'failed', ...(rule && { rule }) })
  const evs = mergePages(events.data?.pages, 'events')
  const fails = mergePages(failed.data?.pages, 'notifications')
  const table = useRef<HTMLTableElement>(null)
  const noop = useCallback(() => {}, [])
  const [selected, setSelected] = useRowKeys(evs.length, noop, table)
  return (
    <>
      <div className="tab-head">
        <label htmlFor={`${id}-rule`}>Rule</label>
        <select id={`${id}-rule`} value={rule} onChange={(e) => onRule(e.target.value)}>
          <option value="">All rules</option>
          {rules.map((r) => (
            <option key={r.id} value={r.id}>
              {r.name}
            </option>
          ))}
        </select>
      </div>
      {fails.length > 0 && (
        <section className="failed-deliveries" aria-labelledby={`${id}-failed`}>
          <h2 id={`${id}-failed`}>Failed deliveries ({fails.length}{failed.hasNextPage ? '+' : ''})</h2>
          <ul>
            {fails.slice(0, 10).map((d) => (
              <li key={d.id}>
                <DeliveryMark status="failed" /> <LogText text={d.rule_name || 'test'} mono={false} /> to <LogText text={d.channel_name} mono={false} />, {d.event}, after{' '}
                {d.attempts} {d.attempts === 1 ? 'attempt' : 'attempts'}
                .{failureSentence(d) && ` ${failureSentence(d)}`} (<Utc iso={d.created_at} />)
              </li>
            ))}
          </ul>
        </section>
      )}
      {events.isPending ? (
        <p role="status">Loading activity…</p>
      ) : events.isError ? (
        <ProblemPanel problem={alertsProblem(events.error)} onRetry={() => void events.refetch()} busy={events.isFetching} />
      ) : evs.length === 0 ? (
        <p className="state-panel">{EMPTY_ACTIVITY}</p>
      ) : (
        <div className="table-wrap">
          <table className="alerts" ref={table}>
            <caption className="visually-hidden">State changes of the alert rules, newest first</caption>
            <thead>
              <tr>
                <th scope="col">Time (UTC)</th>
                <th scope="col">Rule</th>
                <th scope="col">Change</th>
                <th scope="col">Value</th>
                <th scope="col">Group</th>
              </tr>
            </thead>
            <tbody>
              {evs.map((e, i) => (
                <tr key={e.id} data-index={i} className={i === selected ? 'selected' : undefined} onClick={() => setSelected(i)}>
                  <td>
                    <Utc iso={e.at} />
                  </td>
                  <td className="wrap">
                    {e.rule_id ? (
                      <Link to={rulePath(slug, e.rule_id)}>
                        <LogText text={e.rule_name} mono={false} />
                      </Link>
                    ) : (
                      <>
                        <LogText text={e.rule_name} mono={false} /> <span className="muted">(deleted)</span>
                      </>
                    )}
                  </td>
                  <td>
                    {e.from} → <StateMark state={e.to as ShownState} />
                    {e.reason && <span className="muted"> ({e.reason.replace(/_/g, ' ')})</span>}
                  </td>
                  <td>
                    {formatValue(e.rule_type, e.value)} <span className="muted">(threshold {formatValue(e.rule_type, e.threshold)})</span>
                  </td>
                  <td className="wrap">{e.template ? <LogText text={e.template} /> : '–'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {events.hasNextPage && (
        <button type="button" className="button more" disabled={events.isFetchingNextPage} onClick={() => void events.fetchNextPage()}>
          {events.isFetchingNextPage ? 'Loading…' : 'Load more'}
        </button>
      )}
    </>
  )
}

/** /projects/:slug/alerts (ADR 0025): Rules, Channels and Activity as tabs in the URL. */
export function AlertsPage() {
  const { slug = '' } = useParams()
  const [params, setParams] = useSearchParams()
  const state = parseAlertsState(params)
  const projects = useProjects()
  const rules = useRules(slug)
  const channels = useChannels(slug)
  const id = useId()
  // Relative times count from the last refresh of the shared rules query.
  const now = rules.dataUpdatedAt
  const [creating, setCreating] = useState<'' | 'rule' | 'channel'>('')
  const project = projects.data?.find((p) => p.slug === slug)
  const canManage = project?.role === 'owner' || project?.role === 'admin'

  const update = useCallback((next: Partial<AlertsState>, replace = false) => setParams(serializeAlertsState({ ...parseAlertsState(params), ...next }), { replace }), [params, setParams])

  // ?create=rule|channel (from the command palette) opens the dialog once,
  // then leaves the URL (replace), so Back and reload do not open it again.
  const [handled, setHandled] = useState('')
  if (state.create && projects.isSuccess && handled !== state.create) {
    setHandled(state.create)
    if (canManage) setCreating(state.create)
  } else if (!state.create && handled) setHandled('')
  useEffect(() => {
    if (state.create && projects.isSuccess) update({ create: '', tab: state.create === 'channel' ? 'channels' : state.tab }, true)
  }, [state.create, state.tab, projects.isSuccess, update])

  const sorted = useMemo(() => rules.data ?? [], [rules.data])
  // No channel yet: the rule dialog and the empty Rules tab send the user to
  // the Channels tab with the channel dialog open (create=channel, removed once handled).
  const [firstChannel, setFirstChannel] = useState(false)
  const createChannelFirst = () => {
    setCreating('')
    update({ tab: 'channels', create: 'channel' })
  }
  const refresh = () => {
    void rules.refetch()
    void channels.refetch()
  }

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

  const err = rules.error ?? channels.error
  const off = isAlertsOff(rules.error) || isAlertsOff(channels.error)
  return (
    <div className="alerts-page">
      <div className="keys-head">
        <h1>Alerts</h1>
        {!off && !err && (
          <button type="button" className="button button-quiet" onClick={refresh}>
            Refresh
          </button>
        )}
      </div>
      {off || err ? (
        <ProblemPanel problem={alertsProblem(err)} onRetry={refresh} busy={rules.isFetching || channels.isFetching} />
      ) : (
        <>
          {project && !canManage && <p className="notice">{VIEWER_ALERTS}</p>}
          {firstChannel && creating === '' && (
            <div className="notice first-channel" role="status">
              <span>{FIRST_CHANNEL}</span>{' '}
              <button
                type="button"
                className="button button-primary"
                onClick={() => {
                  setFirstChannel(false)
                  setCreating('rule')
                }}
              >
                Create rule
              </button>
            </div>
          )}
          <Tabs tab={state.tab} onSelect={(t) => update({ tab: t, rule: '' })} id={id} />
          <div role="tabpanel" id={`${id}-panel`} aria-labelledby={`${id}-tab-${state.tab}`} className="atab-panel">
            {rules.isPending || channels.isPending ? (
              <p role="status">Loading alerts…</p>
            ) : state.tab === 'rules' ? (
              <RulesTab slug={slug} canManage={canManage} rules={sorted} channels={channels.data ?? []} now={now} onCreate={() => setCreating('rule')} onCreateChannel={createChannelFirst} />
            ) : state.tab === 'channels' ? (
              <ChannelsTab slug={slug} canManage={canManage} rules={sorted} channels={channels.data ?? []} now={now} onCreate={() => setCreating('channel')} />
            ) : (
              <ActivityTab slug={slug} rules={sorted} rule={state.rule} onRule={(r) => update({ rule: r })} />
            )}
          </div>
        </>
      )}
      {creating === 'rule' && channels.data && (
        <RuleFormDialog slug={slug} channels={channels.data} onClose={() => setCreating('')} {...(channels.data.length === 0 && { onCreateChannel: createChannelFirst })} />
      )}
      {creating === 'channel' && <ChannelFormDialog slug={slug} onClose={() => setCreating('')} onCreated={() => setFirstChannel((channels.data ?? []).length === 0)} />}
    </div>
  )
}
