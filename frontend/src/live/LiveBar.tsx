import { describe as words, type LiveState } from './machine'
import { countText } from './useLiveList'

// Shapes back up the words; color is never the only signal.
const SHAPE: Record<LiveState['kind'], string> = {
  off: '',
  connecting: '◌',
  live: '●',
  degraded: '▲',
  reconnecting: '○',
  stopped: '■',
}

type Props = {
  live: boolean
  state: LiveState
  now: number
  paused: boolean
  buffered: number
  onToggle: () => void
  onRetry: () => void
  onResume: () => void
}

/** The Live toggle and the live tail status in the list header (ADR 0019). */
export function LiveBar({ live, state, now, paused, buffered, onToggle, onRetry, onResume }: Props) {
  const text = words(state, now)
  // Announced once per change; the visible countdown is not a live region.
  const spoken = !live ? '' : state.kind === 'reconnecting' ? `Live tail disconnected: ${state.reason}.` : words(state, now)
  return (
    <div className="live-bar">
      <p className="visually-hidden" role="status" aria-live="polite">
        {spoken}
      </p>
      <button type="button" className="button live-toggle" aria-pressed={live} onClick={onToggle} title="Live tail (l)">
        <span aria-hidden="true">{live ? '●' : '○'}</span> Live
      </button>
      {live && text && (
        <p className={`live-state live-${state.kind}`}>
          <span className="shape" aria-hidden="true">
            {SHAPE[state.kind]}
          </span>{' '}
          {text}
        </p>
      )}
      {state.kind === 'reconnecting' && (
        <button type="button" className="button" onClick={onRetry}>
          Retry now
        </button>
      )}
      {live && paused && (
        <>
          <p className="live-paused">Paused while you read; new logs wait here.</p>
          <button type="button" className="button button-primary" onClick={onResume}>
            {buffered > 0 ? countText(buffered) : 'Resume'}
          </button>
        </>
      )}
    </div>
  )
}
