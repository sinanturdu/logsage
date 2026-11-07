import { LogText } from '../components/LogText'
import { filterChips } from './detail'
import type { ExplorerState } from './model'

type Props = {
  state: ExplorerState
  onRemove: (next: Partial<ExplorerState>) => void
  onClear: () => void
}

/** Shortens a long trace id for display; the button label keeps it whole. */
const shown = (label: string) => (label.startsWith('trace ') && label.length > 20 ? `${label.slice(0, 14)}…` : label)

/**
 * One removable chip per active filter, above the list (ADR 0018). Labels
 * are text only; each chip is a button named "Remove filter <label>".
 */
export function FilterChips({ state, onRemove, onClear }: Props) {
  const chips = filterChips(state)
  if (chips.length === 0) return null
  const hasFilter = chips.some((c) => c.id !== 'range')
  return (
    <div className="fchips" role="group" aria-label="Active filters">
      {chips.map((c) => (
        <button key={c.id} type="button" className="fchip" aria-label={`Remove filter ${c.label}`} title={c.label} onClick={() => onRemove(c.remove)}>
          <LogText text={shown(c.label)} mono={false} />
          <span aria-hidden="true"> ×</span>
        </button>
      ))}
      {hasFilter && (
        <button type="button" className="button button-quiet link" onClick={onClear}>
          Clear filters
        </button>
      )}
    </div>
  )
}
