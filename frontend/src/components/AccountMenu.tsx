import { useCallback, useEffect, useId, useRef, useState, type FormEvent, type KeyboardEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router'
import { ApiError, api, errorMessage, request } from '../api/client'
import { useMe } from '../auth/session'
import { Dialog } from './Dialog'

/** The server's password rule (password.MinLength and MaxLength), in characters. */
export const PASSWORD_MIN = 12
export const PASSWORD_MAX = 128
export const LOGOUT_ALL_TEXT = 'This ends every session of your account, including this one. You will need to sign in again everywhere.'
export const PASSWORD_DONE = 'Your password was changed. Your other sessions were signed out; this one stays signed in.'

const chars = (s: string) => [...s].length

/** The first problem with a new password, or '' when it is acceptable. */
export function passwordProblem(next: string, confirm: string): string {
  if (chars(next) < PASSWORD_MIN) return `The new password needs at least ${PASSWORD_MIN} characters.`
  if (chars(next) > PASSWORD_MAX) return `The new password can have at most ${PASSWORD_MAX} characters.`
  if (next !== confirm) return 'The two new passwords do not match. Type the same password twice.'
  return ''
}

function ChangePasswordDialog({ onClose }: { onClose: () => void }) {
  const id = useId()
  // Passwords live only in these fields' state; they are never logged or stored.
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [done, setDone] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault()
    if (current === '') {
      setError('Enter your current password.')
      return
    }
    const problem = passwordProblem(next, confirm)
    if (problem) {
      setError(problem)
      return
    }
    setBusy(true)
    setError('')
    try {
      await request<undefined>('POST', '/api/auth/change-password', { body: { current_password: current, new_password: next } })
      setCurrent('')
      setNext('')
      setConfirm('')
      setDone(true)
    } catch (err) {
      setError(err instanceof ApiError && err.kind === 'forbidden' ? 'The current password is not correct. Try again.' : errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  if (done) {
    return (
      <Dialog title="Password changed" onClose={onClose}>
        <p role="status">{PASSWORD_DONE}</p>
        <div className="dialog-actions">
          <button type="button" className="button button-primary" onClick={onClose}>
            Close
          </button>
        </div>
      </Dialog>
    )
  }
  return (
    <Dialog title="Change password" onClose={onClose}>
      <form onSubmit={(e) => void submit(e)} noValidate>
        <label htmlFor={`${id}-cur`}>Current password</label>
        <input id={`${id}-cur`} type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} />
        <label htmlFor={`${id}-new`}>New password</label>
        <input
          id={`${id}-new`}
          type="password"
          autoComplete="new-password"
          aria-describedby={`${id}-rule`}
          value={next}
          onChange={(e) => setNext(e.target.value)}
        />
        <p className="field-hint" id={`${id}-rule`}>
          {PASSWORD_MIN} to {PASSWORD_MAX} characters.
        </p>
        <label htmlFor={`${id}-again`}>New password again</label>
        <input id={`${id}-again`} type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} />
        <p className="field-hint">Changing it signs out your other sessions.</p>
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
            {busy ? 'Changing…' : 'Change password'}
          </button>
        </div>
      </form>
    </Dialog>
  )
}

function LogoutAllDialog({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const cancel = useRef<HTMLButtonElement>(null)
  async function confirmed() {
    setBusy(true)
    try {
      await request<undefined>('POST', '/api/auth/logout-all', { body: {} })
      onDone()
    } catch (err) {
      setError(errorMessage(err))
      setBusy(false)
    }
  }
  return (
    <Dialog title="Sign out of all devices?" onClose={onClose} initialFocus={cancel}>
      <p>{LOGOUT_ALL_TEXT}</p>
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <div className="dialog-actions">
        <button ref={cancel} type="button" className="button button-quiet" onClick={onClose}>
          Cancel
        </button>
        <button type="button" className="button button-danger" disabled={busy} onClick={() => void confirmed()}>
          Sign out everywhere
        </button>
      </div>
    </Dialog>
  )
}

/**
 * The account menu (menu button pattern): the signed-in email, Change
 * password, Sign out of all devices and Sign out. Arrow keys move between
 * items; Escape closes the menu and returns focus to the button.
 */
export type AccountDialog = 'password' | 'all' | null

/** Sign-out and its aftermath (clear the cache, go to sign-in), shared with the palette. */
export function useSignOut() {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const ended = useCallback(() => {
    qc.clear()
    navigate('/login', { replace: true })
  }, [qc, navigate])
  const signOut = useCallback(async () => {
    try {
      await api.logout()
    } catch {
      // The cookie is cleared or already invalid either way; sign-in follows.
    }
    ended()
  }, [ended])
  return { signOut, ended }
}

/** The account dialogs, rendered by the shell so the palette can open them too. */
export function AccountDialogs({ dialog, onClose }: { dialog: AccountDialog; onClose: () => void }) {
  const { ended } = useSignOut()
  if (dialog === 'password') return <ChangePasswordDialog onClose={onClose} />
  if (dialog === 'all') return <LogoutAllDialog onClose={onClose} onDone={ended} />
  return null
}

export function AccountMenu({ onDialog }: { onDialog: (d: AccountDialog) => void }) {
  const me = useMe()
  const { signOut } = useSignOut()
  const [open, setOpen] = useState(false)
  const button = useRef<HTMLButtonElement>(null)
  const menu = useRef<HTMLDivElement>(null)
  const id = useId()

  const items = () => Array.from(menu.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? [])
  useEffect(() => {
    if (open) items()[0]?.focus()
  }, [open])
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      const t = e.target as Node
      if (!menu.current?.contains(t) && !button.current?.contains(t)) setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [open])

  const close = (refocus = true) => {
    setOpen(false)
    if (refocus) button.current?.focus()
  }
  const onMenuKey = (e: KeyboardEvent) => {
    const list = items()
    const at = list.indexOf(document.activeElement as HTMLElement)
    const go = (i: number) => {
      e.preventDefault()
      list[(i + list.length) % list.length]?.focus()
    }
    if (e.key === 'Escape') {
      e.preventDefault()
      e.stopPropagation()
      close()
    } else if (e.key === 'ArrowDown') go(at + 1)
    else if (e.key === 'ArrowUp') go(at - 1)
    else if (e.key === 'Home') go(0)
    else if (e.key === 'End') go(list.length - 1)
    else if (e.key === 'Tab') close(false)
  }
  const onButtonKey = (e: KeyboardEvent) => {
    if (e.key === 'ArrowDown' && !open) {
      e.preventDefault()
      setOpen(true)
    }
  }

  const pick = (what: 'password' | 'all') => {
    // The menu item goes away; the dialog returns focus to the Account button.
    button.current?.focus()
    setOpen(false)
    onDialog(what)
  }

  return (
    <div className="account">
      <button
        ref={button}
        type="button"
        className="button button-quiet"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={id}
        onClick={() => setOpen((o) => !o)}
        onKeyDown={onButtonKey}
      >
        Account
      </button>
      {open && (
        <div className="account-menu" id={id}>
          <p>Signed in as {me.data?.user.email}</p>
          <div ref={menu} role="menu" aria-label="Account" onKeyDown={onMenuKey}>
            <button type="button" role="menuitem" tabIndex={-1} className="menu-item" onClick={() => pick('password')}>
              Change password
            </button>
            <button type="button" role="menuitem" tabIndex={-1} className="menu-item" onClick={() => pick('all')}>
              Sign out of all devices
            </button>
            <button type="button" role="menuitem" tabIndex={-1} className="menu-item" onClick={() => void signOut()}>
              Sign out
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
