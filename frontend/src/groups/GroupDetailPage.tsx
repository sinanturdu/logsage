import { useCallback, useMemo, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Link, useParams, useSearchParams } from 'react-router'
import { ApiError, errorMessage } from '../api/client'
import { useProjects } from '../api/projects'
import { LogText } from '../components/LogText'
import { useHistogram } from '../explorer/api'
import { LEVEL_SHAPE, contextWindow, resolveRange, type Level, type Preset, type TimeRange } from '../explorer/model'
import { RangeControls } from '../explorer/RangeControls'
import { Timeline } from '../explorer/Timeline'
import { bucketTotal, rangeHeading, rowTime } from '../explorer/time'
import { groupsError, setGroupStatus, useGroupDetail, type GroupDetail } from './api'
import { logsHref, parseFingerprint, parseRange, serializeRange, type GroupStatus } from './model'
import { Flags, Levels, StatusMark, TemplateText, When } from './parts'
import { ACTIONS, StatusDialog, actionsFor } from './StatusDialog'
import { fullUtc } from './time'

export const VIEWER_STATUS = 'You can view this group. Owners and admins of the project can change its status.'
export const NEW_TEXT = 'New: first seen in this time range.'
export const regressedText = (resolvedAt: string | null) =>
  `Regressed: resolved on ${resolvedAt && Number.isFinite(Date.parse(resolvedAt)) ? fullUtc(Date.parse(resolvedAt)) : 'an earlier date'} and seen again after that.`

/** A page for an address that cannot be a group, or a group that does not exist. */
export function GroupNotFound({ slug, invalid }: { slug: string; invalid: boolean }) {
  return (
    <div className="panel" role="alert">
      <h1>Group not found</h1>
      <p>
        {invalid
          ? 'This address is not an error group: a group is named by 16 hexadecimal characters.'
          : 'This group does not exist in this project.'}{' '}
        <Link to={`/projects/${encodeURIComponent(slug)}/groups`}>Go to Error groups</Link>.
      </p>
    </div>
  )
}

/** /projects/:slug/groups/:fingerprint; anything but 16 hex is a 404 page. */
export function GroupDetailRoute() {
  const { slug = '', fingerprint } = useParams()
  const fp = parseFingerprint(fingerprint)
  if (!fp) return <GroupNotFound slug={slug} invalid />
  return <GroupDetailPage slug={slug} fingerprint={fp} />
}

const n = (x: number) => x.toLocaleString('en-US')
const isLevel = (l: string): l is Level => l in LEVEL_SHAPE

function GroupDetailPage({ slug, fingerprint }: { slug: string; fingerprint: string }) {
  const projects = useProjects()
  const qc = useQueryClient()
  const [params, setParams] = useSearchParams()
  const range = useMemo(() => parseRange(params), [params])
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000) * 1000)
  const refresh = useCallback(() => setNow(Math.floor(Date.now() / 1000) * 1000), [])
  const setRange = (r: TimeRange) => setParams(serializeRange(r))
  const win = resolveRange(range, now)
  const ctx = contextWindow(range, now)
  const detail = useGroupDetail(slug, fingerprint, win)
  const histogram = useHistogram(slug, { levels: [], services: [], q: '', traceId: '', fingerprint }, ctx)
  const [dialog, setDialog] = useState<GroupStatus | null>(null)
  const [announce, setAnnounce] = useState('')
  const [statusError, setStatusError] = useState('')

  const project = projects.data?.find((p) => p.slug === slug)
  const canManage = project?.role === 'owner' || project?.role === 'admin'
  const key = ['group', slug, fingerprint, win.from, win.to]

  // Optimistic: the page shows the new status at once; an error restores the old one.
  const change = async (status: GroupStatus, note: string) => {
    setDialog(null)
    setStatusError('')
    setAnnounce('')
    const before = qc.getQueryData<GroupDetail>(key)
    if (before) {
      qc.setQueryData<GroupDetail>(key, {
        ...before,
        status,
        note,
        regressed: false,
        resolved_at: status === 'resolved' ? (before.status === 'resolved' ? before.resolved_at : new Date().toISOString()) : null,
      })
    }
    try {
      const r = await setGroupStatus(slug, fingerprint, status, note)
      qc.setQueryData<GroupDetail>(key, (cur) =>
        cur ? { ...cur, status: r.group.status, resolved_at: r.group.resolved_at, note: r.group.note, updated_by: r.group.updated_by } : cur,
      )
      setAnnounce(ACTIONS[status].done)
      void qc.invalidateQueries({ queryKey: ['groups', slug] })
    } catch (err) {
      if (before) qc.setQueryData<GroupDetail>(key, before)
      setStatusError(`The status was not changed. ${errorMessage(err)}`)
    }
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

  const back = (
    <p className="back">
      <Link to={`/projects/${encodeURIComponent(slug)}/groups${serializeRange(range).toString() ? `?${serializeRange(range).toString()}` : ''}`}>All error groups</Link>
    </p>
  )
  const head = (
    <div className="explorer-head page-head">
      <RangeControls
        range={range}
        window={win}
        onPreset={(p: Preset) => {
          refresh()
          setRange({ kind: 'relative', preset: p })
        }}
        onCustom={(from, to) => setRange({ kind: 'absolute', from, to })}
        onRefresh={refresh}
      />
      <p className="range-line">{rangeHeading(win.from, win.to, now)}</p>
    </div>
  )

  if (detail.isPending) {
    return (
      <div className="group-detail">
        {back}
        {head}
        <p className="status" role="status">
          Loading the group…
        </p>
        <div className="groups-skeleton" aria-hidden="true">
          <span />
          <span />
          <span />
        </div>
      </div>
    )
  }
  if (detail.isError) {
    if (detail.error instanceof ApiError && detail.error.kind === 'unauthorized') return null
    const p = groupsError(detail.error, 'group')
    if (p.notFound) return <GroupNotFound slug={slug} invalid={false} />
    return (
      <div className="group-detail">
        {back}
        {head}
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

  const g = detail.data
  const counts = [...g.counts].sort((a, b) => b.count - a.count)
  return (
    <div className="group-detail">
      {back}
      {head}
      <h1 className="group-title">
        <TemplateText text={g.title} />
      </h1>
      <p className="group-marks">
        <StatusMark status={g.status} /> <Flags group={g} /> <Levels levels={g.levels} />{' '}
        <LogText className="muted" text={g.services.join(', ')} mono={false} />
      </p>
      {(g.is_new || g.regressed) && (
        <ul className="flag-text">
          {g.is_new && <li>{NEW_TEXT}</li>}
          {g.regressed && <li>{regressedText(g.resolved_at)}</li>}
        </ul>
      )}
      <dl className="facts">
        <div>
          <dt>Events in this range</dt>
          <dd>{n(g.count)}</dd>
        </div>
        <div>
          <dt>First seen</dt>
          <dd>
            <When iso={g.first_seen} now={now} />
          </dd>
        </div>
        <div>
          <dt>Last seen</dt>
          <dd>
            <When iso={g.last_seen} now={now} />
          </dd>
        </div>
        {g.error_type && (
          <div>
            <dt>Error type</dt>
            <dd>
              <LogText text={g.error_type} />
            </dd>
          </div>
        )}
      </dl>

      <section className="group-status" aria-labelledby="status-h">
        <h2 id="status-h">Status</h2>
        <p>
          <StatusMark status={g.status} />
          {g.status === 'resolved' && g.resolved_at && Number.isFinite(Date.parse(g.resolved_at)) && <> since {fullUtc(Date.parse(g.resolved_at))}</>}
        </p>
        {g.note && (
          <p>
            Note: <LogText text={g.note} mono={false} />
          </p>
        )}
        {g.updated_by && (
          <p className="muted">
            Last changed by <LogText text={g.updated_by} mono={false} />.
          </p>
        )}
        {canManage ? (
          <div className="status-actions">
            {actionsFor(g.status).map((s) => (
              <button key={s} type="button" className="button" onClick={() => setDialog(s)}>
                {ACTIONS[s].verb}
              </button>
            ))}
          </div>
        ) : (
          project && <p className="notice">{VIEWER_STATUS}</p>
        )}
        {statusError && (
          <p className="form-error" role="alert">
            {statusError}
          </p>
        )}
        <p className="visually-hidden" role="status" aria-live="polite">
          {announce}
        </p>
      </section>

      <section aria-labelledby="volume-h">
        <h2 id="volume-h">Volume</h2>
        {histogram.data ? (
          <Timeline
            buckets={histogram.data.buckets}
            bucketSeconds={histogram.data.bucketSeconds}
            window={ctx}
            volumeOnly
            selection={range.kind === 'absolute' ? win : null}
            onSelect={(from, to) => setRange({ kind: 'absolute', from, to })}
            scope={`${n(histogram.data.buckets.reduce((x, b) => x + bucketTotal(b), 0))} events of this group in view.`}
          />
        ) : histogram.isError ? (
          <p className="chart-error" role="alert">
            The volume could not be loaded. {groupsError(histogram.error).text}
          </p>
        ) : (
          <div className="chart chart-loading" aria-hidden="true" />
        )}
      </section>

      <section aria-labelledby="breakdown-h">
        <h2 id="breakdown-h">Where it happens</h2>
        {counts.length === 0 ? (
          <p className="muted">No events of this group in this range.</p>
        ) : (
          <div className="table-wrap">
            <table className="groups breakdown">
              <thead>
                <tr>
                  <th scope="col">Service</th>
                  <th scope="col">Level</th>
                  <th scope="col" className="num">
                    Events
                  </th>
                </tr>
              </thead>
              <tbody>
                {counts.map((c) => (
                  <tr key={`${c.service}\u0000${c.level}`}>
                    <td>
                      <LogText text={c.service} mono={false} />
                    </td>
                    <td>{isLevel(c.level) ? <Levels levels={[c.level]} /> : <LogText text={c.level} mono={false} />}</td>
                    <td className="num">{n(c.count)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section aria-labelledby="samples-h">
        <h2 id="samples-h">Latest logs</h2>
        {g.samples.length === 0 ? (
          <p className="muted">No logs of this group in this range.</p>
        ) : (
          <ul className="samples">
            {g.samples.map((e) => (
              <li key={e.event_id} className={`level-${e.level}`}>
                <span className="t mono">{rowTime(Date.parse(e.timestamp), true)}</span>{' '}
                <span className="shape" aria-hidden="true">
                  {isLevel(e.level) ? LEVEL_SHAPE[e.level] : ''}
                </span>{' '}
                <span className="lv">{e.level}</span> <LogText className="svc" text={e.service} mono={false} /> <LogText text={e.message} />
              </li>
            ))}
          </ul>
        )}
        <p>
          <Link to={logsHref(slug, fingerprint, range)}>Open in Logs</Link>
        </p>
      </section>

      {dialog && <StatusDialog target={dialog} note={g.note} onConfirm={(note) => void change(dialog, note)} onClose={() => setDialog(null)} />}
    </div>
  )
}

