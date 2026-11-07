import { useId, useImperativeHandle, useRef, useState, type FormEvent, type Ref } from 'react'
import { MAX_WINDOW_MS, PRESETS, type Preset, type TimeRange } from './model'

const PRESET_TEXT: Record<Preset, string> = { '15m': '15 min', '1h': '1 h', '6h': '6 h', '24h': '24 h', '7d': '7 days' }

// "2026-11-07T02:11:30" for a datetime-local input that we read as UTC.
const toInput = (ms: number) => new Date(ms).toISOString().slice(0, 19)
const fromInput = (v: string): number | null => {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2})?$/.test(v)) return null
  const t = Date.parse(v.length === 16 ? `${v}:00Z` : `${v}Z`)
  return Number.isFinite(t) ? t : null
}

type Props = {
  range: TimeRange
  window: { from: number; to: number }
  onPreset: (p: Preset) => void
  onCustom: (from: number, to: number) => void
  onRefresh: () => void
  /** Lets the command palette open the custom range form. */
  ref?: Ref<{ openCustom: () => void }>
}

/** Range presets and a custom UTC range: the keyboard alternative to brushing. */
export function RangeControls({ range, window: win, onPreset, onCustom, onRefresh, ref }: Props) {
  const id = useId()
  const [open, setOpen] = useState(false)
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [error, setError] = useState('')

  function openCustom(force = false) {
    setFrom(toInput(win.from))
    setTo(toInput(win.to))
    setError('')
    setOpen((o) => force || !o)
  }
  const fromInput$ = useRef<HTMLInputElement>(null)
  useImperativeHandle(ref, () => ({
    openCustom: () => {
      openCustom(true)
      setTimeout(() => fromInput$.current?.focus(), 0)
    },
  }))

  function apply(e: FormEvent) {
    e.preventDefault()
    const f = fromInput(from)
    const t = fromInput(to)
    if (f === null || t === null) return setError('Enter both times as date and time in UTC.')
    if (t <= f) return setError('The end must be after the start. Swap the times or pick a later end.')
    if (t - f > MAX_WINDOW_MS) return setError('Choose a range of at most 7 days.')
    setError('')
    setOpen(false)
    onCustom(f, t)
  }

  return (
    <div className="range-controls">
      <div role="group" aria-label="Time range" className="presets">
        {PRESETS.map((p) => (
          <button
            key={p}
            type="button"
            className="chip"
            aria-pressed={range.kind === 'relative' && range.preset === p}
            onClick={() => onPreset(p)}
          >
            {PRESET_TEXT[p]}
          </button>
        ))}
        <button type="button" className="chip" aria-expanded={open} aria-controls={`${id}-custom`} onClick={() => openCustom()}>
          Custom
        </button>
        <button type="button" className="chip" onClick={onRefresh} title="Load the newest logs for this range">
          Refresh
        </button>
      </div>
      {open && (
        <form id={`${id}-custom`} className="custom-range" onSubmit={apply} noValidate aria-label="Custom time range">
          <label>
            From (UTC)
            <input ref={fromInput$} type="datetime-local" step={1} value={from} onChange={(e) => setFrom(e.target.value)} />
          </label>
          <label>
            To (UTC)
            <input type="datetime-local" step={1} value={to} onChange={(e) => setTo(e.target.value)} />
          </label>
          <button type="submit" className="button">
            Apply
          </button>
          {error && (
            <p className="field-error" role="alert">
              {error}
            </p>
          )}
        </form>
      )}
    </div>
  )
}
