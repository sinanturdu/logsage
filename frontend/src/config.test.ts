import { afterEach, describe, expect, it, vi } from 'vitest'
import { DEFAULT_CONFIG, loadConfig, normalizeIngestUrl, parseConfig } from './config'

const respond = (body: string, type = 'application/json', status = 200) =>
  vi.stubGlobal('fetch', vi.fn(async () => new Response(body, { status, headers: { 'Content-Type': type } })))

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('normalizeIngestUrl', () => {
  it.each([
    ['https://logs.example.com', 'https://logs.example.com'],
    ['https://logs.example.com/', 'https://logs.example.com'],
    ['http://localhost:8080', 'http://localhost:8080'],
    ['https://example.com/ingest//', 'https://example.com/ingest'],
    [' http://localhost:8080 ', 'http://localhost:8080'],
  ])('%s -> %s', (v, want) => expect(normalizeIngestUrl(v)).toBe(want))

  it.each(['', 'example.com', 'ftp://example.com', 'javascript:alert(1)', 'https://u:p@example.com', 'https://example.com/?a=1', 'https://example.com/?', 'https://example.com/#x', 42, null, {}])(
    'rejects %s',
    (v) => expect(normalizeIngestUrl(v)).toBeNull(),
  )
})

describe('parseConfig', () => {
  it('accepts the shape and rejects others', () => {
    expect(parseConfig({ ingestUrl: 'https://x.example/' })).toEqual({ ingestUrl: 'https://x.example' })
    expect(parseConfig({ ingest_url: 'https://x.example' })).toBeNull()
    expect(parseConfig([{ ingestUrl: 'https://x.example' }])).toBeNull()
    expect(parseConfig('https://x.example')).toBeNull()
    expect(parseConfig(null)).toBeNull()
  })
})

describe('loadConfig', () => {
  it('reads a valid config', async () => {
    respond('{"ingestUrl":"https://ingest.example.com/"}')
    const warn = vi.fn()
    expect(await loadConfig({ dev: true, warn })).toEqual({ ingestUrl: 'https://ingest.example.com' })
    expect(warn).not.toHaveBeenCalled()
    const init = vi.mocked(fetch).mock.calls[0]
    expect(init?.[0]).toBe('/config.json')
  })

  it.each([
    ['wrong shape', () => respond('{"url":"https://x.example"}'), 'unexpected shape'],
    ['HTML instead of JSON', () => respond('<!doctype html><title>LogSage</title>', 'text/html; charset=utf-8'), 'not JSON'],
    ['an error status', () => respond('{"error":"not found"}', 'application/json', 404), 'status 404'],
    ['malformed JSON', () => respond('{"ingestUrl":', 'application/json'), 'JSON'],
    ['a network error', () => vi.stubGlobal('fetch', vi.fn(async () => Promise.reject(new TypeError('Failed to fetch')))), 'Failed to fetch'],
  ])('falls back on %s, warning only in development', async (_name, setup, reason) => {
    setup()
    const warn = vi.fn()
    expect(await loadConfig({ dev: false, warn })).toEqual(DEFAULT_CONFIG)
    expect(warn).not.toHaveBeenCalled()
    expect(await loadConfig({ dev: true, warn })).toEqual(DEFAULT_CONFIG)
    expect(warn).toHaveBeenCalledTimes(1)
    expect(warn.mock.calls[0]?.[0]).toContain(reason)
  })

  it('gives up after the timeout', async () => {
    vi.useFakeTimers()
    vi.stubGlobal(
      'fetch',
      vi.fn(
        (_u: string, init: RequestInit) =>
          new Promise<Response>((_r, reject) => init.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))),
      ),
    )
    const warn = vi.fn()
    const p = loadConfig({ timeoutMs: 3000, dev: true, warn })
    await vi.advanceTimersByTimeAsync(3000)
    expect(await p).toEqual(DEFAULT_CONFIG)
    expect(warn.mock.calls[0]?.[0]).toContain('no answer within 3000 ms')
  })
})
