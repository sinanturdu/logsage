import { useId, useRef, useState, type FormEvent, type KeyboardEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useParams } from 'react-router'
import { errorMessage } from '../api/client'
import { useProjects } from '../api/projects'
import { Dialog } from '../components/Dialog'
import { LogText } from '../components/LogText'
import { copyText } from '../explorer/clipboard'
import { COPY_FALLBACK } from '../explorer/RowDetail'
import { EXPIRY, MAX_KEY_NAME, createKey, keysKey, revokeKey, useKeys, type KeyInfo, type KeyKind } from './api'

export const EMPTY_KEYS = 'No API keys yet. Create an ingest key to start sending logs.'
export const ONCE_TEXT = 'This key is shown once. Copy it now and store it somewhere safe. LogSage keeps only a hash and cannot show it again.'
export const REVOKE_TEXT = 'Applications using this key will start getting 401 errors within about 30 seconds.'
export const VIEWER_TEXT = 'You can see this project’s keys. Only owners and admins can create or revoke them.'

const SHAPE = { active: '●', expired: '○', revoked: '■' } as const
const pad = (n: number) => String(n).padStart(2, '0')

/** "2026-11-07 16:40 UTC", with the full instant for the title. */
export function dateUtc(iso: string | null): { text: string; title: string } {
  if (!iso) return { text: '–', title: '' }
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return { text: '–', title: '' }
  return {
    text: `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())} ${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())} UTC`,
    title: d.toISOString(),
  }
}

function When({ iso, none = '–' }: { iso: string | null; none?: string }) {
  const d = dateUtc(iso)
  return iso ? <time dateTime={d.title} title={d.title}>{d.text}</time> : <>{none}</>
}

const INGEST_URL = 'http://localhost:8080/v1/ingest'
const stamp = (now: number) => `${new Date(now).toISOString().slice(0, 19)}Z`
const body = (slug: string, now: number, from: string) =>
  `{"events":[{"timestamp":"${stamp(now)}","level":"info","service":"${slug.replace(/[^a-z0-9-]/g, '')}-app","message":"hello from ${from}"}]}`

/** macOS / Linux: curl with a single-quoted body. Always the placeholder, never the key. */
export const curlExample = (slug: string, now: number) =>
  [
    `curl -X POST ${INGEST_URL} \\`,
    '  -H "X-API-Key: YOUR_KEY" -H "Content-Type: application/json" \\',
    `  -d '${body(slug, now, 'curl')}'`,
  ].join('\n')

/**
 * Windows PowerShell: the body goes to a temp file (ASCII, so no BOM), then
 * curl.exe sends it; the bare curl alias would run Invoke-WebRequest.
 */
export const powershellExample = (slug: string, now: number) =>
  [
    `$body = '${body(slug, now, 'PowerShell')}'`,
    "$file = Join-Path $env:TEMP 'logsage-event.json'",
    'Set-Content -Path $file -Value $body -Encoding ascii -NoNewline',
    `curl.exe -X POST ${INGEST_URL} -H "X-API-Key: YOUR_KEY" -H "Content-Type: application/json" --data-binary "@$file"`,
  ].join('\n')

const EXAMPLES = [
  { id: 'unix', label: 'macOS / Linux', text: curlExample },
  { id: 'ps', label: 'Windows PowerShell', text: powershellExample },
] as const

/** The test-log example in two tabs (WAI-ARIA tabs: arrow keys, Home, End). */
function ExampleTabs({ slug }: { slug: string }) {
  const id = useId()
  // The timestamp is taken when the example first renders.
  const [now] = useState(() => Date.now())
  const [active, setActive] = useState(0)
  const tabs = useRef<(HTMLButtonElement | null)[]>([])
  const select = (i: number) => {
    const n = (i + EXAMPLES.length) % EXAMPLES.length
    setActive(n)
    tabs.current[n]?.focus()
  }
  const onKey = (e: KeyboardEvent<HTMLDivElement>) => {
    const keys: Record<string, number> = { ArrowRight: active + 1, ArrowLeft: active - 1, Home: 0, End: EXAMPLES.length - 1 }
    const next = keys[e.key]
    if (next === undefined) return
    e.preventDefault()
    select(next)
  }
  return (
    <div className="examples">
      <div role="tablist" aria-label="Send a test log from" onKeyDown={onKey}>
        {EXAMPLES.map((x, i) => (
          <button
            key={x.id}
            ref={(el) => {
              tabs.current[i] = el
            }}
            type="button"
            role="tab"
            id={`${id}-tab-${x.id}`}
            aria-selected={i === active}
            aria-controls={`${id}-panel-${x.id}`}
            tabIndex={i === active ? 0 : -1}
            onClick={() => setActive(i)}
          >
            {x.label}
          </button>
        ))}
      </div>
      {EXAMPLES.map((x, i) => (
        <pre
          key={x.id}
          role="tabpanel"
          id={`${id}-panel-${x.id}`}
          aria-labelledby={`${id}-tab-${x.id}`}
          tabIndex={0}
          hidden={i !== active}
          className="curl"
        >
          {x.text(slug, now)}
        </pre>
      ))}
    </div>
  )
}

/** Selects the whole key, so one click or focus is enough to copy it by hand. */
function selectAll(el: HTMLElement) {
  const sel = window.getSelection()
  if (!sel) return
  sel.removeAllRanges()
  const r = document.createRange()
  r.selectNodeContents(el)
  sel.addRange(r)
}

export const KINDS = [
  { kind: 'ingest', label: 'Ingest', description: 'Sends logs to LogSage.' },
  { kind: 'query', label: 'Query', description: 'Reads logs through the API.' },
] as const

function CreateKeyDialog({ slug, onClose }: { slug: string; onClose: () => void }) {
  const qc = useQueryClient()
  const id = useId()
  const [kind, setKind] = useState<KeyKind>('ingest')
  const [name, setName] = useState('')
  const [expiry, setExpiry] = useState<(typeof EXPIRY)[number]['id']>('never')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  // The plaintext lives only here, and goes away with the dialog.
  const [created, setCreated] = useState<{ key: string; info: KeyInfo } | null>(null)
  const [copied, setCopied] = useState(false)
  const [note, setNote] = useState('')
  const nameInput = useRef<HTMLInputElement>(null)

  async function submit(e: FormEvent) {
    e.preventDefault()
    const trimmed = name.trim()
    if (trimmed === '') {
      setError('Enter a name, for example "checkout-prod".')
      nameInput.current?.focus()
      return
    }
    setBusy(true)
    setError('')
    try {
      const res = await createKey(slug, kind, trimmed, EXPIRY.find((x) => x.id === expiry)?.expiresIn ?? '')
      setCreated(res)
      void qc.invalidateQueries({ queryKey: keysKey(slug) })
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  if (created) {
    return (
      <Dialog title={`Your new ${created.info.kind} key`}>
        <p className="once">{ONCE_TEXT}</p>
        <p className="key-label" id={`${id}-key-label`}>
          Key
        </p>
        <div className="secret">
          <div
            className="key-block"
            role="textbox"
            aria-readonly="true"
            aria-multiline="true"
            aria-labelledby={`${id}-key-label`}
            tabIndex={0}
            spellCheck={false}
            onClick={(e) => selectAll(e.currentTarget)}
            onFocus={(e) => selectAll(e.currentTarget)}
          >
            {created.key}
          </div>
          <button type="button" className="button" onClick={async () => setNote((await copyText(created.key)) ? 'Copied the key.' : COPY_FALLBACK)}>
            Copy
          </button>
        </div>
        <p className="copied" role="status" aria-live="polite">
          {note}
        </p>
        {created.info.kind === 'ingest' && (
          <>
            <p>Send a test log. Replace YOUR_KEY with the key, and the address if your ingest API runs elsewhere.</p>
            <ExampleTabs slug={slug} />
          </>
        )}
        <label className="check">
          <input type="checkbox" checked={copied} onChange={(e) => setCopied(e.target.checked)} /> I copied the key
        </label>
        <div className="dialog-actions">
          <button type="button" className="button button-primary" disabled={!copied} onClick={onClose}>
            Done
          </button>
        </div>
      </Dialog>
    )
  }

  return (
    <Dialog title="Create an API key" onClose={onClose}>
      <form onSubmit={(e) => void submit(e)} noValidate>
        <fieldset>
          <legend>Kind</legend>
          {KINDS.map((k) => (
            <label key={k.kind} className="choice">
              <input
                type="radio"
                name={`${id}-kind`}
                value={k.kind}
                checked={kind === k.kind}
                aria-labelledby={`${id}-kname-${k.kind}`}
                aria-describedby={`${id}-kind-${k.kind}`}
                onChange={() => setKind(k.kind)}
              />
              <span>
                <b id={`${id}-kname-${k.kind}`}>{k.label}</b>{' '}
                <span className="choice-desc" id={`${id}-kind-${k.kind}`}>
                  {k.description}
                </span>
              </span>
            </label>
          ))}
        </fieldset>
        <label htmlFor={`${id}-name`}>Name</label>
        <input
          ref={nameInput}
          id={`${id}-name`}
          value={name}
          maxLength={MAX_KEY_NAME}
          required
          autoComplete="off"
          aria-invalid={error !== '' && name.trim() === ''}
          aria-describedby={`${id}-hint`}
          onChange={(e) => setName(e.target.value)}
        />
        <p className="field-hint" id={`${id}-hint`}>
          Where the key is used, for example &quot;checkout-prod&quot;. Up to {MAX_KEY_NAME} characters.
        </p>
        <label htmlFor={`${id}-exp`}>Expires</label>
        <select id={`${id}-exp`} value={expiry} onChange={(e) => setExpiry(e.target.value as typeof expiry)}>
          {EXPIRY.map((x) => (
            <option key={x.id} value={x.id}>
              {x.label}
            </option>
          ))}
        </select>
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <div className="dialog-actions">
          <button type="button" className="button button-quiet" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" className="button button-primary" disabled={busy}>
            {busy ? 'Creating…' : 'Create'}
          </button>
        </div>
      </form>
    </Dialog>
  )
}

function RevokeDialog({ slug, info, onClose }: { slug: string; info: KeyInfo; onClose: () => void }) {
  const qc = useQueryClient()
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const cancel = useRef<HTMLButtonElement>(null)
  async function revoke() {
    setBusy(true)
    setError('')
    try {
      await revokeKey(slug, info.prefix)
      await qc.invalidateQueries({ queryKey: keysKey(slug) })
      onClose()
    } catch (err) {
      setError(errorMessage(err))
      setBusy(false)
    }
  }
  return (
    <Dialog title="Revoke this key?" onClose={onClose} initialFocus={cancel}>
      <p>
        <span className="mono">{info.prefix}</span> (<LogText text={info.name} mono={false} />)
      </p>
      <p>{REVOKE_TEXT}</p>
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <div className="dialog-actions">
        <button ref={cancel} type="button" className="button button-quiet" onClick={onClose}>
          Cancel
        </button>
        <button type="button" className="button button-danger" disabled={busy} onClick={() => void revoke()}>
          {busy ? 'Revoking…' : 'Revoke key'}
        </button>
      </div>
    </Dialog>
  )
}

/** /projects/:slug/keys (ADR 0019). */
export function KeysPage() {
  const { slug = '' } = useParams()
  const projects = useProjects()
  const keys = useKeys(slug)
  const [creating, setCreating] = useState(false)
  const [revoking, setRevoking] = useState<KeyInfo | null>(null)
  const project = projects.data?.find((p) => p.slug === slug)

  if (projects.isSuccess && !project) {
    return (
      <div className="panel" role="alert">
        <h1>Project not found</h1>
        <p>
          There is no project <LogText text={slug} /> that you are a member of. Choose a project in the header.
        </p>
      </div>
    )
  }
  const canManage = project?.role === 'owner' || project?.role === 'admin'

  return (
    <div className="keys-page">
      <div className="keys-head">
        <h1>API keys</h1>
        {canManage && (
          <button type="button" className="button button-primary" onClick={() => setCreating(true)}>
            Create key
          </button>
        )}
      </div>
      {project && !canManage && <p className="notice">{VIEWER_TEXT}</p>}
      {keys.isPending ? (
        <p role="status">Loading keys…</p>
      ) : keys.isError ? (
        <div className="state-panel" role="alert">
          <p>{errorMessage(keys.error)}</p>
          <button type="button" className="button" onClick={() => void keys.refetch()}>
            Try again
          </button>
        </div>
      ) : keys.data.length === 0 ? (
        <p className="state-panel">{EMPTY_KEYS}</p>
      ) : (
        <div className="table-wrap">
          <table className="keys">
            <caption className="visually-hidden">API keys of this project, active first</caption>
            <thead>
              <tr>
                <th scope="col">Prefix</th>
                <th scope="col">Kind</th>
                <th scope="col">Name</th>
                <th scope="col">Created</th>
                <th scope="col">Expires</th>
                <th scope="col">Last used</th>
                <th scope="col">Status</th>
                {canManage && (
                  <th scope="col">
                    <span className="visually-hidden">Actions</span>
                  </th>
                )}
              </tr>
            </thead>
            <tbody>
              {keys.data.map((k) => (
                <tr key={k.prefix} className={`key-${k.status}`}>
                  <td className="mono">{k.prefix}</td>
                  <td>{k.kind}</td>
                  <td>
                    <LogText text={k.name} mono={false} />
                  </td>
                  <td>
                    <When iso={k.created_at} />
                  </td>
                  <td>
                    <When iso={k.expires_at} none="never" />
                  </td>
                  <td>
                    <When iso={k.last_used_at} none="never" />
                  </td>
                  <td className="status">
                    <span aria-hidden="true">{SHAPE[k.status]}</span> {k.status}
                  </td>
                  {canManage && (
                    <td>
                      {k.status === 'active' && (
                        <button type="button" className="button button-quiet" aria-label={`Revoke key ${k.prefix}`} onClick={() => setRevoking(k)}>
                          Revoke
                        </button>
                      )}
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {creating && <CreateKeyDialog slug={slug} onClose={() => setCreating(false)} />}
      {revoking && <RevokeDialog slug={slug} info={revoking} onClose={() => setRevoking(null)} />}
    </div>
  )
}
