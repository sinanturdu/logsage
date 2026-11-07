import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, onSessionEnded } from '../api/client'
import { SseParser, streamTail, tailUrl, type SseMessage } from './sse'
import { sseResponse } from './testing'

/** Feeds the text in the given chunk sizes and collects the messages. */
function feed(p: SseParser, text: string, sizes: number[] = [text.length]): SseMessage[] {
  const out: SseMessage[] = []
  let i = 0
  let k = 0
  while (i < text.length) {
    const n = sizes[k++ % sizes.length] ?? text.length
    out.push(...p.push(text.slice(i, i + n)))
    i += n
  }
  return out
}

const STREAM = 'retry: 5000\n\n: hello\nid: c1\nevent: log\ndata: {"a":1}\n\nevent: status\ndata: {"state":"live"}\n\n'

describe('SseParser', () => {
  it('parses events, ids, comments and retry', () => {
    const p = new SseParser()
    expect(feed(p, STREAM)).toEqual([
      { event: 'log', data: '{"a":1}', id: 'c1' },
      { event: 'status', data: '{"state":"live"}', id: 'c1' }, // the id persists
    ])
    expect(p.retry).toBe(5000)
    expect(p.lastEventId).toBe('c1')
  })

  it.each([1, 2, 3, 7])('gives the same result for chunks of %i characters', (n) => {
    expect(feed(new SseParser(), STREAM, [n])).toEqual(feed(new SseParser(), STREAM))
  })

  it('accepts CRLF, CR and LF, including a CRLF split across chunks', () => {
    const crlf = STREAM.replace(/\n/g, '\r\n')
    const cr = STREAM.replace(/\n/g, '\r')
    const want = feed(new SseParser(), STREAM)
    expect(feed(new SseParser(), crlf, [1])).toEqual(want)
    expect(feed(new SseParser(), crlf, [5, 1, 9])).toEqual(want)
    expect(feed(new SseParser(), cr, [4])).toEqual(want)
  })

  it('joins multi-line data, defaults the event name and handles field edge cases', () => {
    const p = new SseParser()
    expect(feed(p, 'data: a\ndata:b\ndata\n\n')).toEqual([{ event: 'message', data: 'a\nb\n', id: '' }])
    expect(feed(p, 'event: x\n\n')).toEqual([]) // no data, no event; the name resets
    expect(feed(p, 'data: y\n\n')).toEqual([{ event: 'message', data: 'y', id: '' }])
    feed(p, 'retry: soon\nid: a\0b\n\n')
    expect(p.retry).toBeUndefined()
    expect(p.lastEventId).toBe('')
    expect(feed(p, 'data: incomplete')).toEqual([]) // not dispatched until the blank line
  })
})

describe('tailUrl', () => {
  it('sends the filters and never a time range', () => {
    expect(tailUrl('shop', { levels: [], services: [], q: '', traceId: '' })).toBe('/api/projects/shop/tail')
    expect(tailUrl('shop', { levels: ['error', 'fatal'], services: ['api', 'checkout'], q: 'time out', traceId: 'a'.repeat(32) })).toBe(
      `/api/projects/shop/tail?level=error%2Cfatal&service=api%2Ccheckout&q=time+out&trace_id=${'a'.repeat(32)}`,
    )
  })
})

describe('streamTail', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('streams messages, sends Last-Event-ID and reports the cursor at the end', async () => {
    const fetchMock = vi.fn<(u: string, i?: RequestInit) => Promise<Response>>(async () => sseResponse([STREAM.slice(0, 20), STREAM.slice(20), 'id: c2\nevent: log\ndata: {}\n\n']))
    vi.stubGlobal('fetch', fetchMock)
    const got: string[] = []
    const end = await streamTail({ url: '/t', lastEventId: 'c0', signal: new AbortController().signal, onMessage: (m) => got.push(m.event) })
    expect(got).toEqual(['log', 'status', 'log'])
    expect(end).toEqual({ kind: 'eof', lastEventId: 'c2', retry: 5000 })
    const headers = fetchMock.mock.calls[0]?.[1]?.headers as Record<string, string>
    expect(headers['Last-Event-ID']).toBe('c0')
  })

  it('omits Last-Event-ID on the first connection', async () => {
    const fetchMock = vi.fn<(u: string, i?: RequestInit) => Promise<Response>>(async () => sseResponse([]))
    vi.stubGlobal('fetch', fetchMock)
    await streamTail({ url: '/t', lastEventId: '', signal: new AbortController().signal, onMessage: () => {} })
    expect((fetchMock.mock.calls[0]?.[1]?.headers as Record<string, string>)['Last-Event-ID']).toBeUndefined()
  })

  it.each([
    [400, 'invalid'],
    [404, 'not_found'],
    [429, 'rate_limited'],
    [503, 'server'],
  ])('rejects %i as %s', async (status, kind) => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: 'q is too long' }), { status, headers: { 'Retry-After': '7' } })))
    const err = (await streamTail({ url: '/t', lastEventId: '', signal: new AbortController().signal, onMessage: () => {} }).catch((e: unknown) => e)) as ApiError
    expect(err.kind).toBe(kind)
    if (status === 400) expect(err.serverMessage).toBe('q is too long')
    if (status === 429) expect(err.retryAfter).toBe(7)
  })

  it('ends the session on 401 and reports a network failure', async () => {
    const ended = vi.fn()
    const off = onSessionEnded(ended)
    vi.stubGlobal('fetch', vi.fn(async () => new Response('', { status: 401 })))
    await expect(streamTail({ url: '/t', lastEventId: '', signal: new AbortController().signal, onMessage: () => {} })).rejects.toMatchObject({ kind: 'unauthorized' })
    expect(ended).toHaveBeenCalledTimes(1)
    off()
    vi.stubGlobal('fetch', vi.fn(async () => Promise.reject(new TypeError('Failed to fetch'))))
    await expect(streamTail({ url: '/t', lastEventId: '', signal: new AbortController().signal, onMessage: () => {} })).rejects.toMatchObject({ kind: 'network' })
  })

  it('closes at once when aborted', async () => {
    let signal: AbortSignal | undefined
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_u: string, i?: RequestInit) => {
        signal = i?.signal ?? undefined
        return sseResponse(['event: log\ndata: {}\n\n'], { open: true })
      }),
    )
    const ac = new AbortController()
    const got: string[] = []
    const p = streamTail({ url: '/t', lastEventId: '', signal: ac.signal, onMessage: (m) => got.push(m.event) })
    await vi.waitFor(() => expect(got).toEqual(['log']))
    ac.abort()
    await expect(p).resolves.toMatchObject({ kind: 'aborted' })
    expect(signal?.aborted).toBe(true)
  })
})
