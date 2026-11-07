import { render } from '@testing-library/react'
import { MemoryRouter, useLocation, useNavigate } from 'react-router'
import { vi } from 'vitest'
import { App, newQueryClient } from '../App'
import { ConfigContext, DEFAULT_CONFIG, type PublicConfig } from '../config'

export function json(status: number, body?: unknown, headers: Record<string, string> = {}): Response {
  if (status === 204) return new Response(null, { status })
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  })
}

type Handler = (init: RequestInit | undefined) => Response | Promise<Response>
export type Call = { key: string; init: RequestInit | undefined }

/**
 * Replaces fetch with a table of "METHOD /path" handlers. The table is
 * mutable, so a test can change an endpoint's answer halfway.
 */
export function mockApi(routes: Record<string, Handler>) {
  const calls: Call[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      const key = `${init?.method ?? 'GET'} ${url}`
      calls.push({ key, init })
      const h = routes[key]
      return h ? h(init) : json(404, { error: 'not found' })
    }),
  )
  return calls
}

export const alice = { user: { id: 'u1', email: 'alice@example.com' } }
export const project = (slug: string, name = slug) => ({
  id: `id-${slug}`,
  slug,
  name,
  role: 'owner',
  created_at: '2026-11-07T10:00:00Z',
  disabled: false,
})

function LocationProbe() {
  const l = useLocation()
  const navigate = useNavigate()
  return (
    <>
      <output data-testid="location">{l.pathname + l.search}</output>
      {/* The browser Back button, for tests on a memory router. */}
      <button type="button" data-testid="history-back" hidden onClick={() => void navigate(-1)} />
    </>
  )
}

export function renderApp(path: string, opts: { retries?: number; config?: PublicConfig } = { retries: 0 }) {
  const client = newQueryClient(opts)
  const utils = render(
    <ConfigContext.Provider value={opts.config ?? DEFAULT_CONFIG}>
      <MemoryRouter initialEntries={[path]}>
        <App client={client} />
        <LocationProbe />
      </MemoryRouter>
    </ConfigContext.Provider>,
  )
  return { ...utils, client }
}
