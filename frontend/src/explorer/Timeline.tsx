import { useEffect, useRef, useState, type PointerEvent } from 'react'
import { minuteUtc, axisTicks, brushRange, bucketErrors, bucketLabel, bucketTotal, errorShare, pixelToTime, summarize, timeToFraction, type Bucket } from './time'

type Props = {
  buckets: Bucket[]
  bucketSeconds: number
  window: { from: number; to: number }
  selection: { from: number; to: number } | null
  onSelect: (from: number, to: number) => void
}

const H = 60

function line(values: number[], max: number): string {
  if (values.length === 0) return ''
  const w = 1000 / values.length
  return values.map((v, i) => `${((i + 0.5) * w).toFixed(1)},${(H - 4 - (v / max) * (H - 10)).toFixed(1)}`).join(' ')
}

/**
 * Two thin strips over one time axis (B2): logs per bucket and the error
 * plus fatal share. Dragging across either strip selects a range (snapped to
 * buckets, Escape cancels); the range presets and the custom range form are
 * the keyboard alternative, and the summary line states the peaks in words.
 */
export function Timeline({ buckets, bucketSeconds, window: win, selection, onSelect }: Props) {
  const area = useRef<HTMLDivElement>(null)
  const [drag, setDrag] = useState<{ start: number; end: number } | null>(null)
  const volumes = buckets.map(bucketTotal)
  const shares = buckets.map(errorShare)
  const peak = Math.max(0, ...volumes)
  const peakShare = Math.max(0, ...shares)
  const hasErrors = buckets.some((b) => bucketErrors(b) > 0)
  const summary = summarize(buckets, bucketSeconds)
  const per = bucketLabel(bucketSeconds)

  useEffect(() => {
    if (!drag) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setDrag(null)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [drag])

  const timeAt = (e: PointerEvent) => {
    const r = area.current?.getBoundingClientRect()
    return pixelToTime(e.clientX - (r?.left ?? 0), r?.width ?? 0, win)
  }
  const onDown = (e: PointerEvent) => {
    if (e.button !== 0) return
    area.current?.setPointerCapture?.(e.pointerId)
    const t = timeAt(e)
    setDrag({ start: t, end: t })
  }
  const onMove = (e: PointerEvent) => {
    if (drag) setDrag({ ...drag, end: timeAt(e) })
  }
  const onUp = (e: PointerEvent) => {
    if (!drag) return
    const r = brushRange(drag.start, timeAt(e), bucketSeconds * 1000, win)
    setDrag(null)
    if (r) onSelect(r.from, r.to)
  }

  const band = drag
    ? { from: Math.min(drag.start, drag.end), to: Math.max(drag.start, drag.end) }
    : selection
  const bandX = band ? timeToFraction(band.from, win) * 1000 : 0
  const bandW = band ? Math.max(2, (timeToFraction(band.to, win) - timeToFraction(band.from, win)) * 1000) : 0
  const ticks = axisTicks(win.from, win.to)
  const grid = ticks.slice(1, -1).map((t) => timeToFraction(t, win) * 1000)

  const strip = (values: number[], max: number, cls: string) => (
    <svg viewBox={`0 0 1000 ${H}`} preserveAspectRatio="none" aria-hidden="true" focusable="false">
      {grid.map((x) => (
        <line key={x} x1={x} x2={x} y1={0} y2={H} className="grid-line" />
      ))}
      {band && <rect className="band" x={bandX} y={0} width={bandW} height={H} />}
      <polyline className={cls} points={line(values, max)} />
    </svg>
  )

  return (
    <section className="chart" aria-label="Timeline">
      <div
        ref={area}
        className="strips"
        onPointerDown={onDown}
        onPointerMove={onMove}
        onPointerUp={onUp}
        onPointerCancel={() => setDrag(null)}
        role="img"
        aria-label={`Timeline. ${summary}`}
      >
        <p className="label">
          <b>Logs per {per}</b>
          <span>peak {peak.toLocaleString('en-US')}</span>
        </p>
        {strip(volumes, Math.max(peak, 1), 'vol')}
        <p className="label">
          <b>Error and fatal, % of logs per {per}</b>
          <span>{hasErrors ? `peak ${Math.round(peakShare)}%` : 'none'}</span>
        </p>
        {strip(shares, Math.max(peakShare, 10), 'err')}
      </div>
      <div className="axis" aria-hidden="true">
        {ticks.map((t) => (
          <span key={t}>{minuteUtc(t)}</span>
        ))}
      </div>
      <p className="key">
        {summary} Drag across either strip to select a time range{drag ? '; press Escape to cancel' : ''}.
      </p>
    </section>
  )
}
