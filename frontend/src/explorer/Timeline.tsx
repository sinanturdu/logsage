import { useEffect, useRef, useState, type PointerEvent, type ReactNode } from 'react'
import { minuteUtc, axisTicks, brushRange, bucketErrors, bucketLabel, bucketTotal, MIN_SHARE_SAMPLE, pixelToTime, SHARE_HIDDEN, shareOrNull, summarize, timeToFraction, type Bucket } from './time'

type Props = {
  buckets: Bucket[]
  bucketSeconds: number
  window: { from: number; to: number }
  selection: { from: number; to: number } | null
  onSelect: (from: number, to: number) => void
  /** "N logs in view, M in the selection". */
  scope: string
  /** Zoom to selection / Zoom out. */
  tools?: ReactNode
  /** Draw the error-share strip; off while only error and fatal are selected (it would be 100%). */
  showShare?: boolean
}

const H = 60

/**
 * Polyline point lists; a null value breaks the line, so a bucket without
 * enough logs for a share leaves a gap instead of a misleading spike.
 */
export function segments(values: readonly (number | null)[], max: number): string[] {
  const w = 1000 / Math.max(values.length, 1)
  const out: string[][] = []
  let cur: string[] | null = null
  values.forEach((v, i) => {
    if (v === null) {
      cur = null
      return
    }
    const x = (i + 0.5) * w
    const y = (H - 4 - (v / max) * (H - 10)).toFixed(1)
    if (!cur) {
      cur = []
      out.push(cur)
    }
    cur.push(`${x.toFixed(1)},${y}`)
  })
  // A lone point gets a short flat stroke so it stays visible.
  return out.map((pts) => {
    if (pts.length !== 1) return pts.join(' ')
    const [x = '0', y = '0'] = (pts[0] ?? '').split(',')
    return `${(Number(x) - w / 4).toFixed(1)},${y} ${(Number(x) + w / 4).toFixed(1)},${y}`
  })
}

/**
 * Two thin strips over one time axis (B2): logs per bucket and the error
 * plus fatal share. Dragging across either strip selects a range (snapped to
 * buckets, Escape cancels); the range presets and the custom range form are
 * the keyboard alternative, and the summary line states the peaks in words.
 */
export function Timeline({ buckets, bucketSeconds, window: win, selection, onSelect, scope, tools, showShare = true }: Props) {
  const area = useRef<HTMLDivElement>(null)
  const [drag, setDrag] = useState<{ start: number; end: number } | null>(null)
  const volumes = buckets.map(bucketTotal)
  const shares = buckets.map(shareOrNull)
  const peak = Math.max(0, ...volumes)
  const peakShare = Math.max(0, ...shares.map((v) => v ?? 0))
  const hasErrors = buckets.some((b) => shareOrNull(b) !== null && bucketErrors(b) > 0)
  const small = volumes.some((v) => v < MIN_SHARE_SAMPLE)
  const summary = summarize(buckets, bucketSeconds, { share: showShare })
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

  const strip = (values: (number | null)[], max: number, cls: string) => (
    <svg viewBox={`0 0 1000 ${H}`} preserveAspectRatio="none" aria-hidden="true" focusable="false">
      {grid.map((x) => (
        <line key={x} x1={x} x2={x} y1={0} y2={H} className="grid-line" />
      ))}
      {band && <rect className="band" x={bandX} y={0} width={bandW} height={H} />}
      {segments(values, max).map((pts) => (
        <polyline key={pts} className={cls} points={pts} />
      ))}
    </svg>
  )

  return (
    <section className="chart" aria-label="Timeline">
      <div className="chart-bar">
        <p className="scope">{scope}</p>
        {tools}
      </div>
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
        {showShare ? (
          <>
            <p className="label">
              <b>Error and fatal, % of logs per {per}</b>
              <span>{hasErrors ? `peak ${Math.round(peakShare)}%` : 'none'}</span>
            </p>
            {strip(shares, Math.max(peakShare, 10), 'err')}
          </>
        ) : (
          <p className="label share-hidden">{SHARE_HIDDEN}</p>
        )}
      </div>
      <div className="axis" aria-hidden="true">
        {ticks.map((t) => (
          <span key={t}>{minuteUtc(t)}</span>
        ))}
      </div>
      <p className="key">
        {summary} Drag across {showShare ? 'either strip' : 'the strip'} to select a time range{drag ? '; press Escape to cancel' : ''}.
      </p>
      <details className="about-chart">
        <summary>About this chart</summary>
        <p>
          The upper strip counts logs per {per}. The lower strip shows error and fatal logs as a percentage of each {per}&apos;s
          logs.
        </p>
        <p>
          Buckets with fewer than {MIN_SHARE_SAMPLE} logs are left out of the error share{small ? ' (this view has some)' : ''}, and
          the line breaks there: one error among two logs would otherwise show as a 50% spike.
        </p>
        <p>
          Drag across either strip to select a time range; the list, the facets and the counts then cover only that range. Press
          Escape while dragging to cancel. The range buttons and Custom do the same from the keyboard.
        </p>
      </details>
    </section>
  )
}
