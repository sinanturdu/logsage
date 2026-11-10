import { afterEach, describe, expect, it, vi } from 'vitest'
import { json } from '../test/util'
import { ApiError, CSRF_MESSAGE, api, errorFromResponse, errorMessage, request } from './client'
import * as alerts from '../alerts/api'
import { setGroupStatus } from '../groups/api'
import { createKey, revokeKey } from '../keys/api'
import { CSRF_ERROR } from '../test/csrf'

afterEach(() => vi.unstubAllGlobals())

// Why the K2 tests missed the DELETE bug: their fetch mocks answered every
// request without applying the server's CSRF rule. This test records the
// headers of every mutation the app makes, independently of the mocks.
describe('every mutation sends Content-Type: application/json', () => {
  const rule = { name: 'r', note: '', type: 'regression' as const, params: {}, interval: '60s', window: '300s', for: '0s', clear_for: '0s', repeat: '0s', enabled: true, channels: ['c'] }
  const calls: [string, () => Promise<unknown>][] = [
    ['login', () => api.login('a@example.com', 'x')],
    ['logout', () => api.logout()],
    ['create project', () => api.createProject('Shop', 'shop')],
    ['create key', () => createKey('shop', 'ingest', 'n', '')],
    ['revoke key', () => revokeKey('shop', 'lsk_x')],
    ['group status', () => setGroupStatus('shop', '0123456789abcdef', 'resolved', '')],
    ['create rule', () => alerts.createRule('shop', rule)],
    ['update rule (PUT)', () => alerts.updateRule('shop', 'r1', rule)],
    ['enable rule', () => alerts.setRuleEnabled('shop', 'r1', true)],
    ['disable rule', () => alerts.setRuleEnabled('shop', 'r1', false)],
    ['delete rule (DELETE)', () => alerts.deleteRule('shop', 'r1')],
    ['create channel', () => alerts.createChannel('shop', { name: 'n', kind: 'slack', url: 'https://x', include_sample: false })],
    ['update channel (PUT)', () => alerts.updateChannel('shop', 'c1', { name: 'n' })],
    ['rotate secret', () => alerts.rotateSecret('shop', 'c1')],
    ['delete channel (DELETE)', () => alerts.deleteChannel('shop', 'c1')],
    ['test send', () => alerts.testChannel('shop', 'c1')],
    ['change password', () => request('POST', '/api/auth/change-password', { body: {} })],
    ['sign out everywhere', () => request('POST', '/api/auth/logout-all', { body: {} })],
  ]
  it.each(calls)('%s', async (_, call) => {
    const seen: { method: string; type: string | null }[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_url: string, init?: RequestInit) => {
        seen.push({ method: init?.method ?? 'GET', type: new Headers(init?.headers).get('Content-Type') })
        return json(204)
      }),
    )
    await call()
    expect(seen.length).toBe(1)
    expect(seen[0]?.method).not.toBe('GET')
    expect(seen[0]?.type).toBe('application/json')
  })

  it('a GET sends no Content-Type', async () => {
    const f = vi.fn(async () => json(200, {}))
    vi.stubGlobal('fetch', f)
    await request('GET', '/x')
    expect(new Headers((f.mock.calls[0] as unknown as [string, RequestInit])[1].headers).get('Content-Type')).toBeNull()
  })
})

describe('403: CSRF rejection versus missing permission', () => {
  it('tells them apart by the server message', async () => {
    const csrf = await errorFromResponse(json(403, { error: CSRF_ERROR }))
    expect(csrf.kind).toBe('csrf')
    expect(errorMessage(csrf)).toBe(CSRF_MESSAGE)
    expect(CSRF_MESSAGE).toBe('The request was rejected by the browser security check. Reload the page and try again.')
    const role = await errorFromResponse(json(403, { error: 'forbidden' }))
    expect(role.kind).toBe('forbidden')
    expect(errorMessage(role)).toBe('You do not have permission to do this. Ask a project owner for access.')
    expect(new ApiError('forbidden', 403).kind).toBe('forbidden')
  })
})
