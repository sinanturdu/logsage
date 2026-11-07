import { useEffect, useId, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router'
import { api } from '../api/client'
import { useMe } from '../auth/session'

export function AccountMenu() {
  const me = useMe()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [open, setOpen] = useState(false)
  const button = useRef<HTMLButtonElement>(null)
  const id = useId()

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setOpen(false)
        button.current?.focus()
      }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [open])

  async function signOut() {
    try {
      await api.logout()
    } catch {
      // The cookie is cleared or already invalid either way; sign-in follows.
    }
    qc.clear()
    navigate('/login', { replace: true })
  }

  return (
    <div className="account">
      <button
        ref={button}
        type="button"
        className="button button-quiet"
        aria-expanded={open}
        aria-controls={id}
        onClick={() => setOpen((o) => !o)}
      >
        Account
      </button>
      {open && (
        <div className="account-menu" id={id}>
          <p>Signed in as {me.data?.user.email}</p>
          <button type="button" className="button" onClick={() => void signOut()}>
            Sign out
          </button>
        </div>
      )}
    </div>
  )
}
