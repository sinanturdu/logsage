import { LogText } from '../components/LogText'
import { useGroupTitle } from '../groups/api'
import { filterChips } from './detail'
import type { ExplorerState } from './model'

type Props = {
  slug: string
  state: ExplorerState
  onRemove: (next: Partial<ExplorerState>) => void
  onClear: () => void
}

/** Shortens a long trace id for display; the button label keeps it whole. */
const shown = (label: string) =>
  label.startsWith('trace ') && label.length > 20 ? `${label.slice(0, 14)}…` : label.startsWith('group: ') && label.length > 48 ? `${label.slice(0, 47)}…` : label

/** The group chip names the group by its template once it has loaded (else the first 8 hex). */
function useChipLabel(slug: string, fingerprint: string, fallback: string): string {
  const title = useGroupTitle(slug, fingerprint)
  return title.data ? `group: ${title.data}` : fallback
}

/**
 * One removable chip per active filter, above the list (ADR 0018). Labels
 * are text only; each chip is a button named "Remove filter <label>".
 */
export function FilterChips({ slug, state, onRemove, onClear }: Props) {
  const groupLabel = useChipLabel(slug, state.fingerprint, `group: ${state.fingerprint.slice(0, 8)}`)
  const chips = filterChips(state).map((c) => (c.id === 'fingerprint' ? { ...c, label: groupLabel } : c))
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
