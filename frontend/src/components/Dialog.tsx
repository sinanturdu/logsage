import { useEffect, useId, useRef, type ReactNode } from 'react'

const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'

type Props = {
  title: string
  children: ReactNode
  /** Escape closes the dialog; omit to require an explicit button. */
  onClose?: () => void
  /** The element that gets focus first (defaults to the first focusable). */
  initialFocus?: React.RefObject<HTMLElement | null>
  /** Keeps the title for screen readers only. */
  titleHidden?: boolean
  className?: string
  /** A click or tap on the backdrop outside the panel closes the dialog. */
  closeOnBackdrop?: boolean
}

/**
 * A modal dialog: labelled by its title, focus moves in and is trapped
 * (Tab and Shift+Tab wrap), Escape closes it when allowed, and focus
 * returns to the element that opened it.
 */
export function Dialog({ title, children, onClose, initialFocus, titleHidden = false, className = '', closeOnBackdrop = false }: Props) {
  const box = useRef<HTMLDivElement>(null)
  const id = useId()
  const close = useRef(onClose)
  useEffect(() => {
    close.current = onClose
  })

  useEffect(() => {
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null
    const first = initialFocus?.current ?? box.current?.querySelector<HTMLElement>(FOCUSABLE) ?? box.current
    first?.focus({ preventScroll: true })
    return () => opener?.focus({ preventScroll: true })
    // Runs once per opening; the focus target is read at that moment.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') {
      e.stopPropagation()
      if (close.current) {
        e.preventDefault()
        close.current()
      }
      return
    }
    if (e.key !== 'Tab' || !box.current) return
    const items = Array.from(box.current.querySelectorAll<HTMLElement>(FOCUSABLE))
    if (items.length === 0) return
    const firstItem = items[0] as HTMLElement
    const lastItem = items[items.length - 1] as HTMLElement
    if (e.shiftKey && document.activeElement === firstItem) {
      e.preventDefault()
      lastItem.focus()
    } else if (!e.shiftKey && document.activeElement === lastItem) {
      e.preventDefault()
      firstItem.focus()
    }
  }

  return (
    <div
      className="dialog-backdrop"
      // The backdrop is not focusable: keep focus in the panel when it is pressed...
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) e.preventDefault()
      }}
      // ...and close on the click (mouse or tap) when that is allowed.
      onClick={(e) => {
        if (closeOnBackdrop && e.target === e.currentTarget) close.current?.()
      }}
    >
      <div ref={box} className={`dialog ${className}`.trim()} role="dialog" aria-modal="true" aria-labelledby={`${id}-t`} tabIndex={-1} onKeyDown={onKeyDown}>
        <h2 id={`${id}-t`} className={titleHidden ? 'visually-hidden' : undefined}>
          {title}
        </h2>
        {children}
      </div>
    </div>
  )
}
