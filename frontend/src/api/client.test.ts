import { describe, expect, it, vi } from 'vitest'
import { json, mockApi } from '../test/util'
import { ApiError, api, errorMessage, onSessionEnded, request } from './client'

async function failure(p: Promise<unknown>): Promise<ApiError> {
  try {
    await p
  } catch (e) {
    if (e instanceof ApiError) return e
    throw e
  }
  throw new Error('expected an ApiError')
}

describe('request', () => {
  it('sends JSON with same-origin credentials and returns the body', async () => {
    const calls = mockApi({ 'POST /api/projects': () => json(201, { project: { slug: 'shop' } }) })
    const got = await api.createProject('Shop', 'shop')
    expect(got).toEqual({ project: { slug: 'shop' } })
    const init = calls[0]?.init
    expect(init?.credentials).toBe('same-origin')
    expect((init?.headers as Record<string, string>)['Content-Type']).toBe('application/json')
    expect(init?.body).toBe('{"name":"Shop","slug":"shop"}')
  })

  it('returns undefined for 204', async () => {
    mockApi({ 'POST /api/auth/logout': () => json(204) })
    await expect(api.logout()).resolves.toBeUndefined()
  })

  it.each([
    [400, 'invalid'],
    [401, 'unauthorized'],
    [403, 'forbidden'],
    [404, 'not_found'],
    [409, 'conflict'],
    [413, 'invalid'],
    [429, 'rate_limited'],
    [500, 'server'],
    [503, 'server'],
  ])('maps %i to %s', async (status, kind) => {
    mockApi({ 'GET /api/projects': () => json(status, { error: 'x' }) })
    const err = await failure(api.listProjects())
    expect(err.kind).toBe(kind)
    expect(err.status).toBe(status)
  })

  it('reads Retry-After on 429, defaulting to 1 second', async () => {
    mockApi({ 'GET /api/projects': () => json(429, { error: 'too many requests' }, { 'Retry-After': '7' }) })
    const err = await failure(api.listProjects())
    expect(err.retryAfter).toBe(7)
    expect(errorMessage(err)).toBe('Too many requests. Try again in 7 seconds.')
    mockApi({ 'GET /api/projects': () => json(429, { error: 'too many requests' }) })
    expect((await failure(api.listProjects())).retryAfter).toBe(1)
  })

  it('keeps the server text of a 400 only', async () => {
    mockApi({ 'POST /api/projects': () => json(400, { error: 'invalid slug: use 3-40 lowercase letters, digits or dashes' }) })
    const err = await failure(api.createProject('A', 'A'))
    expect(errorMessage(err)).toBe('Invalid slug: use 3-40 lowercase letters, digits or dashes.')
    mockApi({ 'GET /api/projects': () => json(500, { error: 'secret internal detail' }) })
    const err500 = await failure(api.listProjects())
    expect(err500.serverMessage).toBeUndefined()
    expect(errorMessage(err500)).toBe('The LogSage API failed (status 500). Try again in a minute.')
  })

  it('turns a failed fetch into a network error', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new TypeError('Failed to fetch'))))
    const err = await failure(api.me())
    expect(err.kind).toBe('network')
    expect(errorMessage(err)).toMatch(/^Cannot reach the LogSage API/)
  })

  it('tolerates a non-JSON error body', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('<html>bad gateway</html>', { status: 502 })))
    expect((await failure(api.me())).kind).toBe('server')
  })

  it('reports an ended session on 401, except for the login request', async () => {
    const ended = vi.fn()
    const off = onSessionEnded(ended)
    mockApi({ 'GET /api/auth/me': () => json(401, { error: 'unauthorized' }), 'POST /api/auth/login': () => json(401, { error: 'unauthorized' }) })
    await failure(api.login('a@b.c', 'wrong password'))
    expect(ended).not.toHaveBeenCalled()
    await failure(api.me())
    expect(ended).toHaveBeenCalledTimes(1)
    off()
    await failure(request('GET', '/api/auth/me'))
    expect(ended).toHaveBeenCalledTimes(1)
  })
})

describe('errorMessage', () => {
  it('says what happened and how to fix it, for every kind', () => {
    expect(errorMessage(new ApiError('unauthorized', 401))).toBe('Your session ended. Sign in again to continue.')
    expect(errorMessage(new ApiError('not_found', 404))).toMatch(/not found/)
    expect(errorMessage(new ApiError('forbidden', 403))).toMatch(/permission/)
    expect(errorMessage(new Error('boom'))).toMatch(/^Something unexpected happened/)
    for (const kind of ['network', 'conflict', 'invalid', 'server'] as const) {
      expect(errorMessage(new ApiError(kind, 0))).not.toMatch(/sorry|apolog/i)
    }
  })
})
