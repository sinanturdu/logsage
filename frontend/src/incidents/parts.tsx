import { useId, useState, type ReactNode } from 'react'
import { fullUtc, relativeTime } from '../groups/time'
import type { Severity, Status } from './api'
import { SEVERITY_BARS, STATUS_SHAPE } from './model'

/** Status as shape plus word (shapes differ from alert states, levels and group statuses). */
export function StatusMark({ status }: { status: Status }) {
  return (
    <span className={`istatus istatus-${status}`}>
      <span className="shape" aria-hidden="true">
        {STATUS_SHAPE[status]}
      </span>{' '}
      {status}
    </span>
  )
}

/** Severity as four bars plus the word. */
export function SeverityMark({ severity }: { severity: Severity }) {
  return (
    <span className={`isev isev-${severity}`}>
      <span className="bars" aria-hidden="true">
        {SEVERITY_BARS[severity]}
      </span>{' '}
      {severity}
    </span>
  )
}

/** Relative text with the full UTC time in the title. */
export function Ago({ iso, now }: { iso: string | null; now: number }) {
  if (!iso) return <>–</>
  const ms = Date.parse(iso)
  if (!Number.isFinite(ms)) return <>–</>
  return (
    <time dateTime={iso} title={fullUtc(ms)}>
      {relativeTime(ms, now)}
    </time>
  )
}

/** A labelled text field with a live character counter and its error. */
export function CountedField({
  label,
  value,
  onChange,
  max,
  error,
  multiline = false,
  hint,
  inputRef,
}: {
  label: string
  value: string
  onChange: (v: string) => void
  max: number
  error?: string | undefined
  multiline?: boolean
  hint?: ReactNode
  inputRef?: React.Ref<HTMLInputElement & HTMLTextAreaElement>
}) {
  const id = useId()
  const used = Array.from(value).length
  const over = used > max
  const described = [`${id}-count`, hint ? `${id}-hint` : '', error ? `${id}-error` : ''].filter(Boolean).join(' ')
  const common = {
    id,
    value,
    'aria-invalid': error || over ? true : undefined,
    'aria-describedby': described,
    onChange: (e: { target: { value: string } }) => onChange(e.target.value),
  }
  return (
    <div className="afield">
      <label htmlFor={id}>{label}</label>
      {multiline ? <textarea ref={inputRef} rows={4} {...common} /> : <input ref={inputRef} autoComplete="off" {...common} />}
      <p className={`field-hint counter${over ? ' counter-over' : ''}`} id={`${id}-count`}>
        {used} of {max} characters{over ? ` (${used - max} too many)` : ''}
      </p>
      {hint && (
        <p className="field-hint" id={`${id}-hint`}>
          {hint}
        </p>
      )}
      {error && (
        <p className="field-error" id={`${id}-error`}>
          {error}
        </p>
      )}
    </div>
  )
}

/** A polite live region: announces status changes once. */
export function useAnnouncer(): [ReactNode, (msg: string) => void] {
  const [msg, setMsg] = useState('')
  return [
    <p key="announcer" className="visually-hidden" role="status" aria-live="polite">
      {msg}
    </p>,
    setMsg,
  ]
}
