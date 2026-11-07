import { useId, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { Dialog } from '../components/Dialog'
import { defaultCommands, type Command } from './commands'
import { rank } from './fuzzy'
import { Highlight } from './Highlight'

type Props = {
  commands: readonly Command[]
  recent: readonly string[]
  onRun: (c: Command) => void
  onClose: () => void
}

export const countText = (n: number) => (n === 0 ? 'No commands match.' : n === 1 ? '1 command.' : `${n} commands.`)

/**
 * The command palette (WAI-ARIA combobox with a listbox, ADR 0020): the input
 * keeps focus and points at the active option with aria-activedescendant.
 * Arrow keys wrap, Home and End jump, Enter runs, Escape closes. Hovering
 * does not move the keyboard selection; a click runs the command.
 */
export function CommandPalette({ commands, recent, onRun, onClose }: Props) {
  const id = useId()
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)
  const input = useRef<HTMLInputElement>(null)
  const results = useMemo(
    () => (query.trim() === '' ? defaultCommands(commands, recent).map((item) => ({ item, positions: [] as number[] })) : rank(query, commands)),
    [query, commands, recent],
  )
  const current = Math.min(active, Math.max(0, results.length - 1))
  const optionId = (i: number) => `${id}-opt-${i}`

  const move = (i: number) => {
    if (results.length === 0) return
    const n = (i + results.length) % results.length
    setActive(n)
    document.getElementById(optionId(n))?.scrollIntoView?.({ block: 'nearest' })
  }
  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    const go: Record<string, number> = { ArrowDown: current + 1, ArrowUp: current - 1, Home: 0, End: results.length - 1 }
    if (e.key in go) {
      e.preventDefault()
      move(go[e.key] ?? 0)
    } else if (e.key === 'Enter') {
      e.preventDefault()
      const r = results[current]
      if (r) onRun(r.item)
    }
  }

  return (
    <Dialog title="Command palette" titleHidden className="palette" onClose={onClose} initialFocus={input}>
      <input
        ref={input}
        className="palette-input"
        type="text"
        role="combobox"
        aria-label="Type a command"
        aria-expanded={results.length > 0}
        aria-controls={`${id}-list`}
        aria-autocomplete="list"
        aria-activedescendant={results.length > 0 ? optionId(current) : undefined}
        placeholder="Type a command"
        autoComplete="off"
        spellCheck={false}
        value={query}
        onChange={(e) => {
          setQuery(e.target.value)
          setActive(0)
        }}
        onKeyDown={onKey}
      />
      <ul id={`${id}-list`} role="listbox" aria-label="Commands" className="palette-list">
        {results.map((r, i) => (
          <li
            key={r.item.id}
            id={optionId(i)}
            role="option"
            aria-selected={i === current}
            className="palette-option"
            onMouseDown={(e) => e.preventDefault()} // keep focus in the input
            onClick={() => onRun(r.item)}
          >
            <span className="label">
              <Highlight text={r.item.label} positions={r.positions} />
            </span>
            <span className="group">{r.item.group}</span>
          </li>
        ))}
      </ul>
      <p className="visually-hidden" role="status" aria-live="polite">
        {countText(results.length)}
      </p>
      {results.length === 0 && <p className="palette-empty">No commands match. Try fewer letters.</p>}
    </Dialog>
  )
}
