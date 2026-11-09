import { Choice } from '../components/Choice'
import { useId, useRef, useState, type FormEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Dialog } from '../components/Dialog'
import { LogText } from '../components/LogText'
import { copyText } from '../explorer/clipboard'
import { COPY_FALLBACK } from '../explorer/RowDetail'
import {
  alertsKey,
  channelUrlError,
  createChannel,
  deleteChannel,
  rotateSecret,
  updateChannel,
  writeError,
  type Channel,
  type ChannelKind,
  type Rule,
} from './api'
import { LIMITS } from './model'
import { NODE_EXAMPLE, PYTHON_EXAMPLE } from './signature'

export const SECRET_ONCE = 'This signing secret is shown once. Copy it now and store it where your receiver can read it. LogSage cannot show it again.'
export const SAMPLE_WARNING =
  'Log messages can contain secrets or personal data. With this on, one recent matching message is sent to the receiver with every notification.'
export const KINDS: { kind: ChannelKind; label: string; description: string }[] = [
  { kind: 'webhook', label: 'Webhook', description: 'A signed JSON POST to your https endpoint.' },
  { kind: 'slack', label: 'Slack', description: 'A message through a Slack incoming webhook URL.' },
]
export const kindLabel = (k: ChannelKind) => (k === 'slack' ? 'Slack' : 'Webhook')

/** Selects the whole block, so one click or focus is enough to copy it by hand. */
function selectAll(el: HTMLElement) {
  const sel = window.getSelection()
  if (!sel) return
  sel.removeAllRanges()
  const r = document.createRange()
  r.selectNodeContents(el)
  sel.addRange(r)
}

/**
 * The signing secret, once: a read-only wrapping block with Copy, and Done
 * only after "I copied the secret". The secret lives in this component's
 * props and goes away when the dialog closes.
 */
export function SecretDialog({ title, secret, onDone }: { title: string; secret: string; onDone: () => void }) {
  const id = useId()
  const [copied, setCopied] = useState(false)
  const [note, setNote] = useState('')
  return (
    <Dialog title={title}>
      <p className="once">{SECRET_ONCE}</p>
      <p className="key-label" id={`${id}-label`}>
        Signing secret
      </p>
      <div className="secret">
        <div
          className="key-block"
          role="textbox"
          aria-readonly="true"
          aria-multiline="true"
          aria-labelledby={`${id}-label`}
          tabIndex={0}
          spellCheck={false}
          data-testid="signing-secret"
          onClick={(e) => selectAll(e.currentTarget)}
          onFocus={(e) => selectAll(e.currentTarget)}
        >
          {secret}
        </div>
        <button type="button" className="button" onClick={async () => setNote((await copyText(secret)) ? 'Copied the secret.' : COPY_FALLBACK)}>
          Copy
        </button>
      </div>
      <p className="copied" role="status" aria-live="polite">
        {note}
      </p>
      <details className="disclosure verify">
        <summary>How to verify the signature</summary>
        <p>
          Every delivery has the header <code>X-LogSage-Signature: t=&lt;unix seconds&gt;,v1=&lt;hex&gt;</code>. The value <code>v1</code> is the HMAC-SHA256 of{' '}
          <code>t</code>, a dot and the raw body, keyed with the secret. Reject a wrong signature and a timestamp older than 5 minutes; use the{' '}
          <code>X-LogSage-Delivery</code> header to drop repeats.
        </p>
        <h3>Node.js</h3>
        <pre className="curl" tabIndex={0}>
          {NODE_EXAMPLE}
        </pre>
        <h3>Python</h3>
        <pre className="curl" tabIndex={0}>
          {PYTHON_EXAMPLE}
        </pre>
      </details>
      <label className="check">
        <input type="checkbox" checked={copied} onChange={(e) => setCopied(e.target.checked)} /> I copied the secret
      </label>
      <div className="dialog-actions">
        <button type="button" className="button button-primary" disabled={!copied} onClick={onDone}>
          Done
        </button>
      </div>
    </Dialog>
  )
}

/** Create a channel, or edit its name, URL and sample setting. */
export function ChannelFormDialog({ slug, channel, onClose, onCreated }: { slug: string; channel?: Channel; onClose: () => void; onCreated?: () => void }) {
  const qc = useQueryClient()
  const id = useId()
  const [kind, setKind] = useState<ChannelKind>(channel?.kind ?? 'webhook')
  const [name, setName] = useState(channel?.name ?? '')
  const [url, setUrl] = useState('')
  const [sample, setSample] = useState(channel?.include_sample ?? false)
  const [errors, setErrors] = useState<{ name?: string; url?: string; form?: string }>({})
  const [busy, setBusy] = useState(false)
  const [secret, setSecret] = useState<string | null>(null)
  const nameInput = useRef<HTMLInputElement>(null)
  const urlInput = useRef<HTMLInputElement>(null)

  async function submit(e: FormEvent) {
    e.preventDefault()
    const n = name.trim()
    const u = url.trim()
    const errs: typeof errors = {}
    if (n === '') errs.name = 'Enter a name, for example "ops".'
    else if (Array.from(n).length > LIMITS.channelName) errs.name = `Use at most ${LIMITS.channelName} characters.`
    if (!channel && u === '') errs.url = 'Enter the https URL that receives the notifications.'
    else if (u !== '' && !/^https?:\/\//i.test(u)) errs.url = 'Enter a full URL that starts with https://.'
    setErrors(errs)
    if (errs.name) return nameInput.current?.focus()
    if (errs.url) return urlInput.current?.focus()
    setBusy(true)
    try {
      if (channel) {
        await updateChannel(slug, channel.id, { name: n, include_sample: sample, ...(u && { url: u }) })
        void qc.invalidateQueries({ queryKey: alertsKey(slug) })
        onClose()
        return
      }
      const res = await createChannel(slug, { name: n, kind, url: u, include_sample: sample })
      void qc.invalidateQueries({ queryKey: alertsKey(slug) })
      onCreated?.()
      if (res.secret) setSecret(res.secret)
      else onClose()
    } catch (err) {
      const urlMsg = channelUrlError(err)
      if (urlMsg) {
        setErrors({ url: urlMsg })
        urlInput.current?.focus()
      } else setErrors({ form: writeError(err) })
      setBusy(false)
    }
  }

  if (secret) return <SecretDialog title="Your new webhook channel" secret={secret} onDone={onClose} />

  return (
    <Dialog title={channel ? 'Edit channel' : 'Create a channel'} onClose={onClose} {...(channel && { initialFocus: nameInput })}>
      <form onSubmit={(e) => void submit(e)} noValidate>
        {!channel && (
          <fieldset className="choices">
            <legend>Type</legend>
            {KINDS.map((k) => (
              <Choice key={k.kind} name={`${id}-kind`} value={k.kind} checked={kind === k.kind} label={k.label} description={k.description} onChange={() => setKind(k.kind)} />
            ))}
          </fieldset>
        )}
        <div className="afield">
          <label htmlFor={`${id}-name`}>Name</label>
          <input
            ref={nameInput}
            id={`${id}-name`}
            value={name}
            autoComplete="off"
            aria-invalid={errors.name ? true : undefined}
            aria-describedby={errors.name ? `${id}-name-error` : undefined}
            onChange={(e) => setName(e.target.value)}
          />
          {errors.name && (
            <p className="field-error" id={`${id}-name-error`}>
              {errors.name}
            </p>
          )}
        </div>
        <div className="afield">
          <label htmlFor={`${id}-url`}>{channel ? 'New URL (optional)' : 'URL'}</label>
          <input
            ref={urlInput}
            id={`${id}-url`}
            type="url"
            value={url}
            autoComplete="off"
            spellCheck={false}
            placeholder={kind === 'slack' ? 'https://hooks.slack.com/services/…' : 'https://hooks.example.com/logsage'}
            aria-invalid={errors.url ? true : undefined}
            aria-describedby={`${id}-url-hint${errors.url ? ` ${id}-url-error` : ''}`}
            onChange={(e) => setUrl(e.target.value)}
          />
          <p className="field-hint" id={`${id}-url-hint`}>
            {channel
              ? `Now sends to ${channel.url_host}. Leave empty to keep the current URL; it is stored encrypted and never shown again.`
              : 'A public https address. It is stored encrypted; afterwards only its host is shown.'}
          </p>
          {errors.url && (
            <p className="field-error" id={`${id}-url-error`}>
              {errors.url}
            </p>
          )}
        </div>
        <label className="check">
          <input type="checkbox" role="switch" checked={sample} aria-describedby={`${id}-sample-warning`} onChange={(e) => setSample(e.target.checked)} /> Include one
          sample log message
        </label>
        <p className="once" id={`${id}-sample-warning`}>
          {SAMPLE_WARNING}
        </p>
        {errors.form && (
          <p className="form-error" role="alert">
            {errors.form}
          </p>
        )}
        <div className="dialog-actions">
          <button type="button" className="button button-quiet" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" className="button button-primary" disabled={busy}>
            {busy ? 'Saving…' : channel ? 'Save channel' : 'Create channel'}
          </button>
        </div>
      </form>
    </Dialog>
  )
}

/** Rotate a webhook secret: confirm, then show the new one once. */
export function RotateDialog({ slug, channel, onClose }: { slug: string; channel: Channel; onClose: () => void }) {
  const qc = useQueryClient()
  const [secret, setSecret] = useState<string | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const cancel = useRef<HTMLButtonElement>(null)
  if (secret) return <SecretDialog title="Your new signing secret" secret={secret} onDone={onClose} />
  async function rotate() {
    setBusy(true)
    setError('')
    try {
      const res = await rotateSecret(slug, channel.id)
      void qc.invalidateQueries({ queryKey: alertsKey(slug) })
      setSecret(res.secret)
    } catch (err) {
      setError(writeError(err))
      setBusy(false)
    }
  }
  return (
    <Dialog title="Rotate the signing secret?" onClose={onClose} initialFocus={cancel}>
      <p>
        <LogText text={channel.name} mono={false} /> gets a new secret now. Deliveries after this are signed with the new one, so update your receiver right away.
      </p>
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <div className="dialog-actions">
        <button ref={cancel} type="button" className="button button-quiet" onClick={onClose}>
          Cancel
        </button>
        <button type="button" className="button button-danger" disabled={busy} onClick={() => void rotate()}>
          {busy ? 'Rotating…' : 'Rotate secret'}
        </button>
      </div>
    </Dialog>
  )
}

/** Delete a channel; names the rules that use it. */
export function DeleteChannelDialog({ slug, channel, rules, onClose }: { slug: string; channel: Channel; rules: readonly Rule[]; onClose: () => void }) {
  const qc = useQueryClient()
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const cancel = useRef<HTMLButtonElement>(null)
  const using = rules.filter((r) => r.channels.includes(channel.id))
  async function remove() {
    setBusy(true)
    setError('')
    try {
      await deleteChannel(slug, channel.id)
      await qc.invalidateQueries({ queryKey: alertsKey(slug) })
      onClose()
    } catch (err) {
      setError(writeError(err))
      setBusy(false)
    }
  }
  return (
    <Dialog title="Delete this channel?" onClose={onClose} initialFocus={cancel}>
      <p>
        <LogText text={channel.name} mono={false} /> stops getting notifications, and its pending deliveries are cancelled. Its delivery history stays.
      </p>
      {using.length > 0 ? (
        <>
          <p>
            {using.length === 1 ? 'This rule uses it' : `These ${using.length} rules use it`} and will stop notifying this channel:
          </p>
          <ul className="named-list">
            {using.map((r) => (
              <li key={r.id}>
                <LogText text={r.name} mono={false} />
                {r.channels.length === 1 && <span className="muted"> (no other channel: edit it to choose one)</span>}
              </li>
            ))}
          </ul>
        </>
      ) : (
        <p>No rule uses it.</p>
      )}
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <div className="dialog-actions">
        <button ref={cancel} type="button" className="button button-quiet" onClick={onClose}>
          Cancel
        </button>
        <button type="button" className="button button-danger" disabled={busy} onClick={() => void remove()}>
          {busy ? 'Deleting…' : 'Delete channel'}
        </button>
      </div>
    </Dialog>
  )
}
