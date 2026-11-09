import { fullUtc, relativeTime } from '../groups/time'
import type { DeliveryStatus, Problem, RuleState } from './api'

/** A rule's state as shape plus word; disabled wins over the evaluated state. */
export type ShownState = RuleState | 'disabled'
export const STATE_SHAPE: Record<ShownState, string> = { ok: '○', pending: '◐', firing: '◉', resolved: '✓', disabled: '⊘' }

export function StateMark({ state }: { state: ShownState }) {
  return (
    <span className={`astate astate-${state}`}>
      <span className="shape" aria-hidden="true">
        {STATE_SHAPE[state]}
      </span>{' '}
      {state}
    </span>
  )
}

const DELIVERY_SHAPE: Record<DeliveryStatus, string> = { pending: '◌', sending: '◐', sent: '✓', failed: '✕', cancelled: '–' }

/** A delivery's status: failed is said in words ("failed"), with a shape and weight. */
export function DeliveryMark({ status }: { status: DeliveryStatus }) {
  return (
    <span className={`dstatus dstatus-${status}`}>
      <span className="shape" aria-hidden="true">
        {DELIVERY_SHAPE[status]}
      </span>{' '}
      {status}
    </span>
  )
}

/** A UTC time with relative text and the full time in the title. */
export function Ago({ iso, now, none = 'never' }: { iso: string | null; now: number; none?: string }) {
  if (!iso) return <>{none}</>
  const ms = Date.parse(iso)
  if (!Number.isFinite(ms)) return <>{none}</>
  return (
    <time dateTime={iso} title={fullUtc(ms)}>
      {relativeTime(ms, now)}
    </time>
  )
}

/** The full UTC time, for history tables. */
export function Utc({ iso, none = '–' }: { iso: string | null; none?: string }) {
  if (!iso) return <>{none}</>
  const ms = Date.parse(iso)
  if (!Number.isFinite(ms)) return <>{none}</>
  return <time dateTime={iso}>{fullUtc(ms)}</time>
}

export const KEY_COMMAND =
  '$b = New-Object byte[] 32; [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($b); [Convert]::ToBase64String($b)'

/** One clear state when the server answers 503 for alerts. */
export function AlertsOff({ onRetry, busy }: { onRetry: () => void; busy: boolean }) {
  return (
    <div className="panel alerts-off" role="alert">
      <h2>Alerts are turned off on this server.</h2>
      <p>An administrator of this LogSage server can turn them on:</p>
      <ol>
        <li>
          Make a key: 32 random bytes in base64. In PowerShell:
          <pre className="curl">{KEY_COMMAND}</pre>
        </li>
        <li>
          In <code>.env</code>, set <code>ALERTS_ENABLED=true</code> and <code>ALERTS_SECRET_KEY</code> to that key. Keep the key safe: losing or changing it
          makes stored channel URLs and secrets unreadable.
        </li>
        <li>
          Restart the api and start the evaluator (<code>--profile alerts</code>). For local development, <code>scripts\dev.ps1 up</code> does this for you.
        </li>
      </ol>
      <button type="button" className="button" disabled={busy} onClick={onRetry}>
        {busy ? 'Checking…' : 'Check again'}
      </button>
    </div>
  )
}

/** An error with its retry button, or the off state. */
export function ProblemPanel({ problem, onRetry, busy = false }: { problem: Problem; onRetry: () => void; busy?: boolean }) {
  if (problem.off) return <AlertsOff onRetry={onRetry} busy={busy} />
  return (
    <div className="state-panel" role="alert">
      <p>{problem.text}</p>
      {problem.retry && (
        <button type="button" className="button" disabled={busy} onClick={onRetry}>
          Try again
        </button>
      )}
    </div>
  )
}

/** A rule event's value in words for its type. */
export function formatValue(type: string, v: number): string {
  if (type === 'error_rate') return `${Number(v.toFixed(2))}%`
  return v.toLocaleString('en-US')
}
