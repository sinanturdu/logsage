import { useId, useMemo, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { ApiError } from '../api/client'
import { Dialog } from '../components/Dialog'
import { LogText } from '../components/LogText'
import { useFacets } from '../explorer/api'
import { alertsKey, createRule, updateRule, writeError, type Channel, type Rule } from './api'
import {
  DURATION_UNITS,
  LEVELS,
  LIMITS,
  RULE_TYPES,
  TYPE_HINT,
  TYPE_LABEL,
  emptyRuleForm,
  fieldForServerMessage,
  formFromRule,
  previewSentences,
  ruleBody,
  typeFields,
  validateRule,
  type DurationValue,
  type FieldErrors,
  type FieldName,
  type RuleForm,
} from './model'

const DAY = 86_400_000

type FieldProps = { id: string; label: string; error: string | undefined; hint?: ReactNode; children: ReactNode }

/** A labelled field with its hint and its error (described by both). */
function Field({ id, label, error, hint, children }: FieldProps) {
  return (
    <div className="afield">
      <label htmlFor={id}>{label}</label>
      {children}
      {hint && (
        <p className="field-hint" id={`${id}-hint`}>
          {hint}
        </p>
      )}
      {error && (
        <p className="field-error" id={`${id}-error`}>
          {error}
        </p>
      )}
    </div>
  )
}

const describedBy = (id: string, error: string | undefined, hint: boolean) =>
  [hint ? `${id}-hint` : '', error ? `${id}-error` : ''].filter(Boolean).join(' ') || undefined

function DurationInput({
  id,
  value,
  onChange,
  error,
  hint,
  units = ['s', 'm', 'h'],
}: {
  id: string
  value: DurationValue
  onChange: (v: DurationValue) => void
  error: string | undefined
  hint: boolean
  units?: readonly DurationValue['unit'][]
}) {
  return (
    <span className="duration">
      <input
        id={id}
        inputMode="numeric"
        value={value.amount}
        aria-invalid={error ? true : undefined}
        aria-describedby={describedBy(id, error, hint)}
        onChange={(e) => onChange({ ...value, amount: e.target.value })}
      />
      <select aria-label="Unit" value={value.unit} onChange={(e) => onChange({ ...value, unit: e.target.value as DurationValue['unit'] })}>
        {DURATION_UNITS.filter((u) => units.includes(u.id)).map((u) => (
          <option key={u.id} value={u.id}>
            {u.label}
          </option>
        ))}
      </select>
    </span>
  )
}

/** Service choices from the facets of the last 24 hours, plus any already chosen. */
function useServiceChoices(slug: string, chosen: readonly string[]) {
  const [win] = useState(() => {
    const to = Date.now()
    return { from: to - DAY, to }
  })
  const facets = useFacets(slug, { levels: [], services: [], q: '', traceId: '' }, win)
  const names = facets.data?.services.filter((s) => !s.other).map((s) => s.name) ?? []
  return { names: [...new Set([...names, ...chosen])].sort(), loading: facets.isPending, failed: facets.isError }
}

type Props = { slug: string; channels: readonly Channel[]; rule?: Rule; onClose: () => void; onSaved?: (r: Rule) => void }

/** Create or edit a rule: only the chosen type's fields, a live preview, the server's limits. */
export function RuleFormDialog({ slug, channels, rule, onClose, onSaved }: Props) {
  const qc = useQueryClient()
  const id = useId()
  const [form, setForm] = useState<RuleForm>(() => (rule ? formFromRule(rule) : emptyRuleForm()))
  const [errors, setErrors] = useState<FieldErrors>({})
  const [formError, setFormError] = useState('')
  const [busy, setBusy] = useState(false)
  const nameInput = useRef<HTMLInputElement>(null)
  const services = useServiceChoices(slug, form.services)
  const fields = typeFields(form.type)
  const set = <K extends keyof RuleForm>(k: K, v: RuleForm[K]) => setForm((f) => ({ ...f, [k]: v }))
  const toggle = (k: 'services' | 'levels' | 'channels', v: string) =>
    setForm((f) => ({ ...f, [k]: f[k].includes(v) ? f[k].filter((x) => x !== v) : [...f[k], v] }))
  const channelNames = useMemo(() => channels.filter((c) => form.channels.includes(c.id)).map((c) => c.name), [channels, form.channels])
  const preview = previewSentences(form, channelNames)
  const fid = (f: FieldName) => `${id}-${f}`

  async function submit(e: FormEvent) {
    e.preventDefault()
    const errs = validateRule(form)
    setErrors(errs)
    setFormError('')
    const first = Object.keys(errs)[0] as FieldName | undefined
    if (first) {
      document.getElementById(fid(first))?.focus()
      return
    }
    setBusy(true)
    try {
      const body = ruleBody(form)
      const res = rule ? await updateRule(slug, rule.id, body) : await createRule(slug, body)
      void qc.invalidateQueries({ queryKey: alertsKey(slug) })
      onSaved?.(res.rule)
      onClose()
    } catch (err) {
      const msg = writeError(err)
      const field = err instanceof ApiError && err.kind === 'invalid' ? fieldForServerMessage(err.serverMessage ?? '') : null
      if (field) {
        setErrors({ [field]: msg })
        document.getElementById(fid(field))?.focus()
      } else setFormError(msg)
      setBusy(false)
    }
  }

  const err = (f: FieldName) => errors[f]
  return (
    <Dialog title={rule ? 'Edit alert rule' : 'Create an alert rule'} onClose={onClose} initialFocus={nameInput} className="rule-dialog">
      <form onSubmit={(e) => void submit(e)} noValidate>
        <Field id={fid('name')} label="Name" error={err('name')}>
          <input
            ref={nameInput}
            id={fid('name')}
            value={form.name}
            autoComplete="off"
            aria-invalid={err('name') ? true : undefined}
            aria-describedby={describedBy(fid('name'), err('name'), false)}
            onChange={(e) => set('name', e.target.value)}
          />
        </Field>
        <fieldset className="choices" aria-describedby={err('type') ? `${fid('type')}-error` : undefined}>
          <legend>Type</legend>
          {RULE_TYPES.map((t) => (
            <label key={t} className="choice">
              <input
                type="radio"
                name={`${id}-type`}
                id={t === form.type ? fid('type') : undefined}
                value={t}
                checked={form.type === t}
                aria-describedby={`${id}-type-${t}`}
                onChange={() => set('type', t)}
              />
              <span>
                <b>{TYPE_LABEL[t]}</b>{' '}
                <span className="choice-desc" id={`${id}-type-${t}`}>
                  {TYPE_HINT[t]}
                </span>
              </span>
            </label>
          ))}
          {err('type') && (
            <p className="field-error" id={`${fid('type')}-error`}>
              {err('type')}
            </p>
          )}
        </fieldset>

        <div className="field-row">
          {fields.includes('threshold') && (
            <Field id={fid('threshold')} label="Threshold (%)" error={err('threshold')} hint="Share of error and fatal logs among all logs. 0.1 to 100.">
              <input
                id={fid('threshold')}
                inputMode="decimal"
                value={form.threshold}
                aria-invalid={err('threshold') ? true : undefined}
                aria-describedby={describedBy(fid('threshold'), err('threshold'), true)}
                onChange={(e) => set('threshold', e.target.value)}
              />
            </Field>
          )}
          {fields.includes('minLogs') && (
            <Field id={fid('minLogs')} label="Minimum logs" error={err('minLogs')} hint="A smaller sample never fires. 10 to 1,000,000.">
              <input
                id={fid('minLogs')}
                inputMode="numeric"
                value={form.minLogs}
                aria-invalid={err('minLogs') ? true : undefined}
                aria-describedby={describedBy(fid('minLogs'), err('minLogs'), true)}
                onChange={(e) => set('minLogs', e.target.value)}
              />
            </Field>
          )}
          {fields.includes('op') && (
            <Field id={fid('op')} label="Direction" error={err('op')} hint="Fires when the count is above or below the number.">
              <select id={fid('op')} aria-describedby={describedBy(fid('op'), err('op'), true)} value={form.op} onChange={(e) => set('op', e.target.value === 'below' ? 'below' : 'above')}>
                <option value="above">Above</option>
                <option value="below">Below</option>
              </select>
            </Field>
          )}
          {fields.includes('count') && (
            <Field id={fid('count')} label="Count" error={err('count')} hint={form.op === 'below' ? 'Below 1 means no logs at all (silence).' : '0 to 1,000,000,000.'}>
              <input
                id={fid('count')}
                inputMode="numeric"
                value={form.count}
                aria-invalid={err('count') ? true : undefined}
                aria-describedby={describedBy(fid('count'), err('count'), true)}
                onChange={(e) => set('count', e.target.value)}
              />
            </Field>
          )}
          {fields.includes('minEvents') && (
            <Field id={fid('minEvents')} label="Minimum events" error={err('minEvents')} hint="Events of the new group in the window. 1 to 1,000,000.">
              <input
                id={fid('minEvents')}
                inputMode="numeric"
                value={form.minEvents}
                aria-invalid={err('minEvents') ? true : undefined}
                aria-describedby={describedBy(fid('minEvents'), err('minEvents'), true)}
                onChange={(e) => set('minEvents', e.target.value)}
              />
            </Field>
          )}
          {fields.includes('maxGroups') && (
            <Field id={fid('maxGroups')} label="Max new groups" error={err('maxGroups')} hint="Per hour; fires above this number. The window is always 1 hour. 1 to 10,000.">
              <input
                id={fid('maxGroups')}
                inputMode="numeric"
                value={form.maxGroups}
                aria-invalid={err('maxGroups') ? true : undefined}
                aria-describedby={describedBy(fid('maxGroups'), err('maxGroups'), true)}
                onChange={(e) => set('maxGroups', e.target.value)}
              />
            </Field>
          )}
          {fields.includes('window') && (
            <Field id={fid('window')} label="Window" error={err('window')} hint="How far back the condition looks. 1 minute to 24 hours.">
              <DurationInput id={fid('window')} value={form.window} onChange={(v) => set('window', v)} error={err('window')} hint units={['m', 'h']} />
            </Field>
          )}
        </div>

        {fields.includes('levels') && (
          <fieldset className="choices inline-choices">
            <legend>Levels (none chosen: all levels)</legend>
            {LEVELS.map((l) => (
              <label key={l} className="check">
                <input type="checkbox" checked={form.levels.includes(l)} onChange={() => toggle('levels', l)} /> {l}
              </label>
            ))}
          </fieldset>
        )}
        {fields.includes('services') && (
          <fieldset className="choices inline-choices" aria-describedby={err('services') ? `${fid('services')}-error` : undefined}>
            <legend>Services (none chosen: all services)</legend>
            {services.loading ? (
              <p className="field-hint">Loading services…</p>
            ) : services.names.length === 0 ? (
              <p className="field-hint">{services.failed ? 'Services could not be loaded; the rule covers all services.' : 'No services in the last 24 hours.'}</p>
            ) : (
              services.names.map((s, i) => (
                <label key={s} className="check">
                  <input type="checkbox" id={i === 0 ? fid('services') : undefined} checked={form.services.includes(s)} onChange={() => toggle('services', s)} />{' '}
                  <LogText text={s} />
                </label>
              ))
            )}
            {err('services') && (
              <p className="field-error" id={`${fid('services')}-error`}>
                {err('services')}
              </p>
            )}
          </fieldset>
        )}

        <div className="field-row">
          <Field id={fid('for')} label="For" error={err('for')} hint="How long the condition must hold to fire. 0 to 24 hours.">
            <DurationInput id={fid('for')} value={form.for} onChange={(v) => set('for', v)} error={err('for')} hint />
          </Field>
          <Field id={fid('clearFor')} label="Clear for" error={err('clearFor')} hint="How long it must be clear to resolve. 0 uses the check interval; at most 1 hour.">
            <DurationInput id={fid('clearFor')} value={form.clearFor} onChange={(v) => set('clearFor', v)} error={err('clearFor')} hint />
          </Field>
          <Field id={fid('interval')} label="Check every" error={err('interval')} hint="30 seconds to 1 hour.">
            <DurationInput id={fid('interval')} value={form.interval} onChange={(v) => set('interval', v)} error={err('interval')} hint />
          </Field>
        </div>
        <label className="check">
          <input type="checkbox" checked={form.repeatOn} onChange={(e) => set('repeatOn', e.target.checked)} /> Notify again while firing
        </label>
        {form.repeatOn && (
          <Field id={fid('repeat')} label="Repeat every" error={err('repeat')} hint="5 minutes to 7 days.">
            <DurationInput id={fid('repeat')} value={form.repeat} onChange={(v) => set('repeat', v)} error={err('repeat')} hint units={['m', 'h', 'd']} />
          </Field>
        )}

        <fieldset className="choices" aria-describedby={err('channels') ? `${fid('channels')}-error` : undefined}>
          <legend>Notify</legend>
          {channels.length === 0 ? (
            <p className="field-hint">No channels yet. Create one on the Channels tab first.</p>
          ) : (
            channels.map((c, i) => (
              <label key={c.id} className="check">
                <input type="checkbox" id={i === 0 ? fid('channels') : undefined} checked={form.channels.includes(c.id)} onChange={() => toggle('channels', c.id)} />{' '}
                <LogText text={c.name} mono={false} /> <span className="muted">({c.kind === 'slack' ? 'Slack' : 'Webhook'})</span>
              </label>
            ))
          )}
          {err('channels') && (
            <p className="field-error" id={`${fid('channels')}-error`}>
              {err('channels')}
            </p>
          )}
        </fieldset>

        <Field id={fid('note')} label="Note (optional)" error={err('note')} hint={`Shown in notifications, for example what to check first. Up to ${LIMITS.note} characters.`}>
          <textarea
            id={fid('note')}
            rows={2}
            value={form.note}
            aria-invalid={err('note') ? true : undefined}
            aria-describedby={describedBy(fid('note'), err('note'), true)}
            onChange={(e) => set('note', e.target.value)}
          />
        </Field>

        {/* Not a live region: it changes on every keystroke. */}
        <div className="preview" role="group" aria-label="Preview" data-testid="rule-preview">
          <p>
            <LogText text={preview.fires} mono={false} />
          </p>
          <p>
            <LogText text={preview.resolves} mono={false} />
          </p>
        </div>

        {formError && (
          <p className="form-error" role="alert">
            {formError}
          </p>
        )}
        <div className="dialog-actions">
          <button type="button" className="button button-quiet" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" className="button button-primary" disabled={busy}>
            {busy ? 'Saving…' : rule ? 'Save rule' : 'Create rule'}
          </button>
        </div>
      </form>
    </Dialog>
  )
}
