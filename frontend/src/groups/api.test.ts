import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, errorFromResponse } from '../api/client'
import { json } from '../test/util'
import { groupsError, isCursorChanged, LIST_CHANGED, mergeGroups, setGroupStatus } from './api'
import { group } from './testing'

afterEach(() => vi.unstubAllGlobals())


const err = (status: number, body: unknown, headers: Record<string, string> = {}) => errorFromResponse(json(status, body, headers))

describe('groups error mapping', () => {
  it('a 400 about the cursor means the list changed', async () => {
    const e = await err(400, { error: 'cursor: not valid for this query; repeat the same query with next_cursor' })
    expect(isCursorChanged(e)).toBe(true)
    expect(groupsError(e)).toEqual({ text: LIST_CHANGED, retry: false, reload: true })
  })

  it('other 400s show the server message', async () => {
    const e = await err(400, { error: 'q: at most 256 characters' })
    expect(isCursorChanged(e)).toBe(false)
    expect(groupsError(e)).toEqual({ text: 'Q: at most 256 characters.', retry: false })
  })

  it('404 depends on what was asked for', async () => {
    const e = await err(404, { error: 'not found' })
    expect(groupsError(e, 'group')).toEqual({ text: 'This group does not exist in this project.', retry: false, notFound: true })
    expect(groupsError(e).text).toMatch(/project was not found/)
  })

  it('429 uses Retry-After, 5xx and network are retryable', async () => {
    expect(groupsError(await err(429, { error: 'too many' }, { 'Retry-After': '7' }))).toEqual({ text: 'Too many requests. Try again in 7 seconds.', retry: true })
    expect(groupsError(await err(503, {})).retry).toBe(true)
    expect(groupsError(new ApiError('network', 0)).retry).toBe(true)
  })
})

describe('groups client', () => {
  it('posts the status and note as JSON', async () => {
    const fetch = vi.fn(async () => json(200, { group: { fingerprint: 'a', status: 'resolved', resolved_at: 'x', note: 'n', updated_by: 'u', updated_at: 'x' } }))
    vi.stubGlobal('fetch', fetch)
    const r = await setGroupStatus('my shop', '0123456789abcdef', 'resolved', 'fixed')
    expect(r.group.status).toBe('resolved')
    const [url, init] = fetch.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/projects/my%20shop/groups/0123456789abcdef/status')
    expect(init.method).toBe('POST')
    expect(JSON.parse(String(init.body))).toEqual({ status: 'resolved', note: 'fixed' })
  })

  it('merges pages without duplicate fingerprints', () => {
    const a = group('aaaaaaaaaaaaaaaa')
    const b = group('bbbbbbbbbbbbbbbb')
    const c = group('cccccccccccccccc')
    expect(mergeGroups([{ groups: [a, b] }, { groups: [b, c] }]).map((g) => g.fingerprint)).toEqual([a.fingerprint, b.fingerprint, c.fingerprint])
    expect(mergeGroups(undefined)).toEqual([])
  })
})
