import { useEffect, useRef, useState } from 'react'
import { useLocation } from 'react-router'

const APP = 'LogSage'

function decode(s: string): string {
  try {
    return decodeURIComponent(s)
  } catch {
    return s
  }
}

/** The document title for a path: "<page> - <project> - LogSage". */
export function titleFor(pathname: string): string {
  if (pathname === '/login') return `Sign in - ${APP}`
  if (pathname === '/') return `Projects - ${APP}`
  const m = /^\/projects\/([^/]+)\/(logs|keys)\/?$/.exec(pathname)
  if (m) return `${m[2] === 'logs' ? 'Logs' : 'API keys'} - ${decode(m[1] ?? '')} - ${APP}`
  return `Page not found - ${APP}`
}

/**
 * Keeps document.title in step with the route and, after the first page,
 * announces the new page politely (a client-side navigation is silent
 * otherwise). Query string changes (filters) are not page changes.
 */
export function RouteTitle() {
  const { pathname } = useLocation()
  const title = titleFor(pathname)
  const [spoken, setSpoken] = useState('')
  const first = useRef(true)
  useEffect(() => {
    document.title = title
    if (first.current) {
      first.current = false
      return
    }
    const id = setTimeout(() => setSpoken(title), 50)
    return () => clearTimeout(id)
  }, [title])
  return (
    <p className="visually-hidden" role="status" aria-live="polite" data-testid="route-announcer">
      {spoken}
    </p>
  )
}
