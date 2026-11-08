import { forwardRef, useEffect, useId, useState, type KeyboardEvent } from 'react'
import type { Facets } from './api'
import { LEVELS, LEVEL_SHAPE, MAX_TEXT, toggleLevel, toggleService, validTraceId, type ExplorerState } from './model'

type Filters = Pick<ExplorerState, 'levels' | 'services' | 'q' | 'traceId'>

type Props = {
  filters: Filters
  facets: Facets | undefined
  onChange: (next: Partial<Filters>, opts?: { replace?: boolean }) => void
  onClear: () => void
  canClear: boolean
}

const fmt = (n: number | undefined) => (n === undefined ? '' : n.toLocaleString('en-US'))
export const SEARCH_TOO_LONG = `Search text is limited to ${MAX_TEXT} characters. Shorten it to search.`
export const TRACE_INVALID =
  'This trace id is not valid: it must be 32 hexadecimal characters with no dashes. Paste the full trace_id from a log.'

/** Search box: "/" focuses it (see ExplorerPage); 300 ms debounce, Enter searches now. */
type SearchProps = { value: string; onSearch: (q: string, immediate: boolean) => void; label?: string; placeholder?: string }

export const SearchBox = forwardRef<HTMLInputElement, SearchProps>(
  function SearchBox({ value, onSearch, label = 'Search messages', placeholder = 'Search messages, press /' }, ref) {
    const id = useId()
    const [text, setText] = useState(value)
    const [synced, setSynced] = useState(value)
    if (value !== synced) {
      // The URL changed elsewhere (back button, Clear filters): follow it.
      setSynced(value)
      setText(value)
    }
    const tooLong = text.length > MAX_TEXT
    useEffect(() => {
      if (tooLong || text === value) return
      const t = setTimeout(() => onSearch(text, false), 300)
      return () => clearTimeout(t)
    }, [text, value, tooLong, onSearch])
    const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
      if (e.key === 'Enter' && !tooLong) onSearch(text, true)
    }
    return (
      <div className="search">
        <label htmlFor={id} className="visually-hidden">
          {label}
        </label>
        <input
          ref={ref}
          id={id}
          type="search"
          placeholder={placeholder}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={onKeyDown}
          aria-invalid={tooLong || undefined}
          aria-describedby={tooLong ? `${id}-err` : undefined}
        />
        {tooLong && (
          <p className="field-error" id={`${id}-err`}>
            {SEARCH_TOO_LONG}
          </p>
        )}
      </div>
    )
  },
)

function TraceInput({ value, onApply }: { value: string; onApply: (v: string) => void }) {
  const id = useId()
  const [text, setText] = useState(value)
  const [synced, setSynced] = useState(value)
  const [invalid, setInvalid] = useState(false)
  if (value !== synced) {
    setSynced(value)
    setText(value)
    setInvalid(false)
  }
  const apply = () => {
    const v = text.trim().toLowerCase()
    if (v === value) return setInvalid(false)
    if (v !== '' && !validTraceId(v)) return setInvalid(true)
    setInvalid(false)
    onApply(v)
  }
  return (
    <div>
      <h2>
        <label htmlFor={id}>Trace id</label>
      </h2>
      <input
        id={id}
        data-field="trace"
        className="mono"
        value={text}
        spellCheck={false}
        autoComplete="off"
        placeholder="32 hex characters"
        onChange={(e) => setText(e.target.value)}
        onBlur={apply}
        onKeyDown={(e) => e.key === 'Enter' && apply()}
        aria-invalid={invalid || undefined}
        aria-describedby={invalid ? `${id}-err` : undefined}
      />
      {invalid && (
        <p className="field-error" id={`${id}-err`} role="alert">
          {TRACE_INVALID}
        </p>
      )}
    </div>
  )
}

/** The filter column of B2: levels and services with counts, trace id. */
export function FilterPanel({ filters, facets, onChange, onClear, canClear }: Props) {
  const allLevels = filters.levels.length === 0
  const allServices = filters.services.length === 0
  return (
    <div className="filters">
      <fieldset>
        <legend>Level</legend>
        {[...LEVELS].reverse().map((l) => (
          <label key={l} className={`facet level-${l}`}>
            <input
              type="checkbox"
              checked={allLevels || filters.levels.includes(l)}
              onChange={() => onChange({ levels: toggleLevel(filters.levels, l) })}
            />
            <span className="shape" aria-hidden="true">
              {LEVEL_SHAPE[l]}
            </span>
            <span className="word">{l}</span>
            <span className="count">{fmt(facets?.levels[l])}</span>
          </label>
        ))}
      </fieldset>
      <fieldset>
        <legend>
          Service <span className="legend-hint">{allServices ? '(all)' : `(${filters.services.length} selected)`}</span>
        </legend>
        {facets?.services.length === 0 && <p className="field-hint">No services in this range.</p>}
        {facets?.services.map((s) =>
          s.other ? (
            <p key="__other" className="facet facet-other" title="Services beyond the top 50 by count">
              <span className="word">other services</span>
              <span className="count">{fmt(s.count)}</span>
            </p>
          ) : (
            <label key={s.name} className="facet">
              <input
                type="checkbox"
                checked={filters.services.includes(s.name)}
                onChange={() => onChange({ services: toggleService(filters.services, s.name) })}
              />
              <span className="word">{s.name}</span>
              <span className="count">{fmt(s.count)}</span>
            </label>
          ),
        )}
        {filters.services
          .filter((name) => !facets?.services.some((s) => s.name === name))
          .map((name) => (
            <label key={name} className="facet">
              <input type="checkbox" checked onChange={() => onChange({ services: toggleService(filters.services, name) })} />
              <span className="word">{name}</span>
              <span className="count">0</span>
            </label>
          ))}
      </fieldset>
      <TraceInput value={filters.traceId} onApply={(traceId) => onChange({ traceId })} />
      <button type="button" className="button button-quiet clear" onClick={onClear} disabled={!canClear}>
        Clear filters
      </button>
    </div>
  )
}
