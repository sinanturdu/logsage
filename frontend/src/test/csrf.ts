// The management API's CSRF rule (backend internal/api/security.go): every
// method other than GET and HEAD must send Content-Type: application/json,
// or the API answers 403. The shared fetch mocks apply the same rule, so a
// mutation without the header fails its test instead of passing silently
// (K2.2: DELETE had no Content-Type and every mock still answered 2xx).

export const CSRF_ERROR = 'csrf: rejected by the browser security check'

const violations: string[] = []

const headerOf = (init: RequestInit | undefined, name: string): string | null => {
  const h = init?.headers
  if (!h) return null
  if (h instanceof Headers) return h.get(name)
  if (Array.isArray(h)) return h.find(([k]) => k.toLowerCase() === name.toLowerCase())?.[1] ?? null
  const rec = h as Record<string, string>
  const key = Object.keys(rec).find((k) => k.toLowerCase() === name.toLowerCase())
  return key ? (rec[key] ?? null) : null
}

/**
 * The 403 the API would answer for this request, or null when it passes.
 * A refused request is recorded; setup.ts fails the test that made it.
 */
export function csrfGate(url: string, init: RequestInit | undefined): Response | null {
  const method = (init?.method ?? 'GET').toUpperCase()
  if (method === 'GET' || method === 'HEAD') return null
  const type = headerOf(init, 'Content-Type') ?? ''
  if (type.split(';')[0]?.trim().toLowerCase() === 'application/json') return null
  violations.push(`${method} ${url} without Content-Type: application/json`)
  return new Response(JSON.stringify({ error: CSRF_ERROR }), { status: 403, headers: { 'Content-Type': 'application/json' } })
}

/** Returns and clears the recorded refusals. */
export function takeCsrfViolations(): string[] {
  return violations.splice(0)
}
