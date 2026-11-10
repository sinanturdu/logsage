import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, errorFromResponse, request } from '../api/client'
import { json } from '../test/util'
import { NOT_ALLOWED_ADDRESS, URL_CREDENTIALS, alertsProblem, channelUrlError, firingRules, isAlertsOff, mergePages, writeError } from './api'
import { NODE_EXAMPLE, PYTHON_EXAMPLE } from './signature'
import { OFF, firingState } from './testing'

afterEach(() => vi.unstubAllGlobals())

const err = (status: number, body?: unknown, headers: Record<string, string> = {}) => errorFromResponse(json(status, body, headers))

describe('alerts error mapping', () => {
  it('tells the off state from other 503s', async () => {
    const off = await errorFromResponse(OFF())
    expect(isAlertsOff(off)).toBe(true)
    expect(alertsProblem(off)).toEqual({ text: 'Alerts are turned off on this server.', retry: false, off: true })
    const key = await err(503, { error: 'alerts are not configured: ALERTS_SECRET_KEY is missing or not 32 bytes base64' })
    expect(isAlertsOff(key)).toBe(true)
    const other = await err(503, { error: 'database unavailable' })
    expect(isAlertsOff(other)).toBe(false)
    expect(alertsProblem(other)).toEqual({ text: 'The LogSage API failed (status 503). Try again in a minute.', retry: true })
    expect(alertsProblem(await err(500))).toMatchObject({ retry: true })
    expect(alertsProblem(new ApiError('network', 0))).toMatchObject({ retry: true })
  })

  it('maps 429, 404 and write errors', async () => {
    expect(alertsProblem(await err(429, { error: 'slow down' }, { 'Retry-After': '7' }))).toEqual({ text: 'Too many requests. Try again in 7 seconds.', retry: true })
    expect(alertsProblem(await err(404), 'rule')).toMatchObject({ notFound: true, retry: false })
    expect(alertsProblem(await err(404))).toMatchObject({ retry: false })
    expect(writeError(await err(400, { error: 'window: must be between 1m and 24h, in whole seconds' }))).toBe('Must be between 1m and 24h, in whole seconds.')
    expect(writeError(await err(409, { error: 'rules: at most 50 per project' }))).toBe('Rules: at most 50 per project.')
    expect(writeError(await err(409, { error: 'conflict' }))).toBe('Conflict.')
    expect(writeError(await errorFromResponse(OFF()))).toBe('Alerts are turned off on this server.')
  })

  it('turns SSRF and URL refusals into plain sentences', async () => {
    const msg = async (e: string) => channelUrlError(await err(400, { error: e }))
    expect(await msg('url: host resolves to a private or reserved address')).toBe(NOT_ALLOWED_ADDRESS)
    expect(await msg('url: host is not allowed')).toBe(NOT_ALLOWED_ADDRESS)
    expect(await msg('url: must use https')).toBe(NOT_ALLOWED_ADDRESS)
    expect(await msg('url: must not contain a user name or password')).toBe(URL_CREDENTIALS)
    expect(await msg('url: port is not allowed')).toBe(`${NOT_ALLOWED_ADDRESS} Port is not allowed.`)
    expect(await msg('url: host cannot be resolved')).toBe(`${NOT_ALLOWED_ADDRESS} Host cannot be resolved.`)
    expect(await msg('url: not a valid URL')).toContain('Enter a valid https URL')
    expect(await msg('name: 1 to 100 characters')).toBeNull()
    expect(channelUrlError(await err(500))).toBeNull()
  })

  it('sends PUT and DELETE with the JSON body', async () => {
    const f = vi.fn(async () => json(204))
    vi.stubGlobal('fetch', f)
    await request('PUT', '/x', { body: { a: 1 } })
    await request('DELETE', '/y')
    expect(f.mock.calls.map((c) => (c as unknown as [string, RequestInit])[1].method)).toEqual(['PUT', 'DELETE'])
  })

  it('counts firing rules, not keys, and merges pages without repeats', () => {
    expect(firingRules([firingState('a'), firingState('a', { dedup_key: 'fp:1' }), firingState('b'), { ...firingState('c'), state: 'pending' }])).toBe(2)
    expect(mergePages([{ events: [{ id: 3 }, { id: 2 }], next_before: 2 }, { events: [{ id: 2 }, { id: 1 }], next_before: null }], 'events').map((e) => e.id)).toEqual([3, 2, 1])
  })
})

// The backend's known vectors (internal/alert/secret TestSignKnownVectors).
const VECTORS = [
  { secret: 'lsw_test-secret', header: 't=1762600000,v1=e9aff3fd84d7701a427ffd4f71cf0ed4549ac234efdd790edb9efd08c7f72f59', body: '{"version":1}', now: 1762600000 },
  { secret: 'key', header: 't=1700000000,v1=0f1cc1f811f42fd12af9618acf321769899fa521fe07a642f70a61785e130770', body: '', now: 1700000000 },
]

const bytes = (s: string) => new TextEncoder().encode(s)

// Node built-ins, loaded at run time (the dashboard has no Node type definitions).
const load = (m: string) => import(/* @vite-ignore */ m) as Promise<unknown>
type SpawnSync = (cmd: string, args: string[], opts?: { encoding: 'utf8' }) => { status: number | null; stdout: string; stderr: string }
const nodeCrypto = await load('node:crypto')
const { spawnSync } = (await load('node:child_process')) as { spawnSync: SpawnSync }

describe('signature examples', () => {
  it('the Node example accepts the backend vectors and rejects tampering and old timestamps', () => {
    const require = (m: string) => (m === 'node:crypto' ? nodeCrypto : undefined)
    const verify = new Function('require', `${NODE_EXAMPLE}\nreturn verifyLogSage`)(require) as (s: string, h: string, b: Uint8Array, now?: number) => boolean
    for (const v of VECTORS) {
      expect(verify(v.secret, v.header, bytes(v.body), v.now)).toBe(true)
      expect(verify(v.secret, v.header, bytes(v.body), v.now + 300)).toBe(true)
      expect(verify(v.secret, v.header, bytes(v.body), v.now + 301)).toBe(false) // older than 5 minutes
      expect(verify(`${v.secret}x`, v.header, bytes(v.body), v.now)).toBe(false)
      expect(verify(v.secret, v.header, bytes(`${v.body} `), v.now)).toBe(false)
      expect(verify(v.secret, v.header.replace(/t=(\d+)/, (_, t: string) => `t=${Number(t) + 1}`), bytes(v.body), v.now)).toBe(false)
    }
    expect(verify('k', 't=1700000000', bytes(''), 1700000000)).toBe(false)
    expect(verify('k', 't=1700000000,v1=zz', bytes(''), 1700000000)).toBe(false)
    expect(NODE_EXAMPLE).not.toMatch(/lsw_[A-Za-z0-9]{8}/) // a placeholder, never a real secret
  })

  const python = ['python3', 'python'].find((p) => spawnSync(p, ['--version']).status === 0)
  it.skipIf(!python)('the Python example accepts the backend vectors and rejects tampering and old timestamps', () => {
    const cases = VECTORS.flatMap((v) => [
      [v.secret, v.header, v.body, v.now, true],
      [v.secret, v.header, v.body, v.now + 301, false],
      [`${v.secret}x`, v.header, v.body, v.now, false],
      [v.secret, v.header, `${v.body} `, v.now, false],
    ])
    const script = `${PYTHON_EXAMPLE}\nimport json, sys\nfor s, h, b, n, want in json.loads(sys.argv[1]):\n    assert verify_logsage(s, h, b.encode(), n) == want, (s, h, b, n, want)\nprint("ok")\n`
    const r = spawnSync(python as string, ['-I', '-c', script, JSON.stringify(cases)], { encoding: 'utf8' })
    expect(r.stderr).toBe('')
    expect(r.stdout.trim()).toBe('ok')
  })
})

describe('delivery failures in plain sentences', () => {
  it('uses only the status code and the category', async () => {
    const { failureSentence } = await import('./api')
    const s = (error_category: Parameters<typeof failureSentence>[0]['error_category'], response_status: number | null = null) => failureSentence({ error_category, response_status })
    expect(s('http_4xx', 400)).toBe('The receiver answered with HTTP 400.')
    expect(s('http_5xx', 503)).toBe('The receiver answered with HTTP 503.')
    expect(s('unknown', 302)).toBe('The receiver answered with HTTP 302.')
    expect(s('timeout')).toBe('The receiver did not answer in time.')
    expect(s('connect_error')).toBe('LogSage could not connect to the receiver.')
    expect(s('tls_error')).toBe("The receiver's TLS certificate could not be verified.")
    expect(s('blocked_address')).toBe('This address is not allowed any more.')
    expect(s('rate_limited', 429)).toBe('The receiver answered with HTTP 429 (too many requests).')
    expect(s('rate_limited')).toContain('limit of sends per minute')
    expect(s('canceled')).toBe('Cancelled: the rule or the channel changed.')
    expect(s('unknown')).toBe('The delivery failed.')
    expect(s('')).toBe('')
  })
})
