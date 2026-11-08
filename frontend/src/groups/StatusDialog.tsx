import { useId, useRef, useState, type FormEvent } from 'react'
import { Dialog } from '../components/Dialog'
import { MAX_NOTE } from './api'
import type { GroupStatus } from './model'

/** What each status action is called, and what it announces when done. */
export const ACTIONS: Record<GroupStatus, { verb: string; title: string; done: string; help: string }> = {
  resolved: {
    verb: 'Resolve',
    title: 'Resolve this group',
    done: 'Group resolved',
    help: 'Resolved groups leave the open list. If the group is seen again later, it is flagged as regressed.',
  },
  ignored: {
    verb: 'Ignore',
    title: 'Ignore this group',
    done: 'Group ignored',
    help: 'Ignored groups leave the open list and are not flagged when they are seen again.',
  },
  open: {
    verb: 'Reopen',
    title: 'Reopen this group',
    done: 'Group reopened',
    help: 'The group returns to the open list.',
  },
}

export const NOTE_TOO_LONG = `Notes are limited to ${MAX_NOTE} characters. Shorten the note to save it.`

/** The actions offered for a status: the two other statuses. */
export function actionsFor(status: GroupStatus): GroupStatus[] {
  return status === 'open' ? ['resolved', 'ignored'] : status === 'resolved' ? ['open', 'ignored'] : ['open', 'resolved']
}

type Props = {
  target: GroupStatus
  note: string
  onConfirm: (note: string) => void
  onClose: () => void
}

/** A small confirm dialog with an optional note (focus trap and Escape come from Dialog). */
export function StatusDialog({ target, note: initial, onConfirm, onClose }: Props) {
  const id = useId()
  const a = ACTIONS[target]
  const [note, setNote] = useState(initial)
  const field = useRef<HTMLTextAreaElement>(null)
  const length = [...note.trim()].length
  const tooLong = length > MAX_NOTE

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (tooLong) return
    onConfirm(note.trim())
  }

  return (
    <Dialog title={a.title} onClose={onClose} initialFocus={field}>
      <form onSubmit={submit} noValidate className="status-form">
        <p>{a.help}</p>
        <label htmlFor={`${id}-note`}>Note (optional)</label>
        <textarea
          ref={field}
          id={`${id}-note`}
          rows={3}
          value={note}
          onChange={(e) => setNote(e.target.value)}
          aria-invalid={tooLong || undefined}
          aria-describedby={`${id}-count${tooLong ? ` ${id}-err` : ''}`}
        />
        <p id={`${id}-count`} className="muted">
          {length} of {MAX_NOTE} characters
        </p>
        {tooLong && (
          <p id={`${id}-err`} className="field-error" role="alert">
            {NOTE_TOO_LONG}
          </p>
        )}
        <div className="dialog-actions">
          <button type="submit" className="button button-primary">
            {a.verb}
          </button>
          <button type="button" className="button" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </Dialog>
  )
}
