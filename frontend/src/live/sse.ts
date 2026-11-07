// A text/event-stream parser and a fetch-based tail client (ADR 0019).
// EventSource is not used: it hides the HTTP status (so 401, 404, 400 and
// 429 cannot be told apart) and cannot set headers or Retry-After handling.

import { ApiError, errorFromResponse, notifySessionEnded } from '../api/client'

export type SseMessage = { event: string; data: string; id: string }

/**
 * Incremental parser for the WHATWG event stream format: lines end in CRLF,
 * LF or CR (also split across chunks), comments start with ":", data lines
 * join with "\n", and the last event id persists across messages.
 */
export class SseParser {
  private buf = ''
  private data: string[] = []
  private event = ''
  private afterCR = false
  /** The last id field seen; sent as Last-Event-ID on reconnect. */
  lastEventId = ''
  /** The last valid retry field, in milliseconds. */
  retry: number | undefined

  push(chunk: string): SseMessage[] {
    // A CR ended the previous chunk: an LF that starts this one is its pair.
    if (this.afterCR && chunk.startsWith('\n')) chunk = chunk.slice(1)
    this.afterCR = false
    this.buf += chunk
    const out: SseMessage[] = []
    let start = 0
    for (let i = 0; i < this.buf.length; i++) {
      const c = this.buf[i]
      if (c !== '\n' && c !== '\r') continue
      this.line(this.buf.slice(start, i), out)
      if (c === '\r') {
        if (i === this.buf.length - 1) this.afterCR = true
        else if (this.buf[i + 1] === '\n') i++
      }
      start = i + 1
    }
    this.buf = this.buf.slice(start)
    return out
  }

  private line(line: string, out: SseMessage[]) {
    if (line === '') {
      if (this.data.length > 0) out.push({ event: this.event || 'message', data: this.data.join('\n'), id: this.lastEventId })
      this.data = []
      this.event = ''
      return
    }
    if (line.startsWith(':')) return
    const colon = line.indexOf(':')
    const field = colon === -1 ? line : line.slice(0, colon)
    let value = colon === -1 ? '' : line.slice(colon + 1)
    if (value.startsWith(' ')) value = value.slice(1)
    switch (field) {
      case 'data':
        this.data.push(value)
        break
      case 'event':
        this.event = value
        break
      case 'id':
        if (!value.includes('\0')) this.lastEventId = value
        break
      case 'retry':
        if (/^\d+$/.test(value)) this.retry = Number(value)
        break
    }
  }
}

export type TailFilters = { levels: readonly string[]; services: readonly string[]; q: string; traceId: string }

/** The tail URL: the explorer's filters, never a time range. */
export function tailUrl(slug: string, f: TailFilters): string {
  const p = new URLSearchParams()
  if (f.levels.length > 0) p.set('level', f.levels.join(','))
  if (f.services.length > 0) p.set('service', f.services.join(','))
  if (f.q) p.set('q', f.q)
  if (f.traceId) p.set('trace_id', f.traceId)
  const qs = p.toString()
  return `/api/projects/${encodeURIComponent(slug)}/tail${qs ? `?${qs}` : ''}`
}

/** How a stream ended without an HTTP error. */
export type TailEnd = { kind: 'eof' | 'aborted'; lastEventId: string; retry: number | undefined }

/**
 * Opens the tail and calls onMessage for every event until the stream ends.
 * Resolves on end of stream or abort; rejects with ApiError for an HTTP
 * error status (401 also ends the session app-wide) or a network failure.
 */
export async function streamTail(opts: {
  url: string
  lastEventId: string
  signal: AbortSignal
  onOpen?: () => void
  onMessage: (m: SseMessage) => void
}): Promise<TailEnd> {
  const parser = new SseParser()
  parser.lastEventId = opts.lastEventId
  const end = (kind: TailEnd['kind']): TailEnd => ({ kind, lastEventId: parser.lastEventId, retry: parser.retry })
  const headers: Record<string, string> = { Accept: 'text/event-stream' }
  if (opts.lastEventId) headers['Last-Event-ID'] = opts.lastEventId
  let res: Response
  try {
    res = await fetch(opts.url, { method: 'GET', credentials: 'same-origin', headers, signal: opts.signal, cache: 'no-store' })
  } catch (err) {
    if (opts.signal.aborted) return end('aborted')
    throw err instanceof ApiError ? err : new ApiError('network', 0)
  }
  if (!res.ok) {
    const err = await errorFromResponse(res)
    if (err.kind === 'unauthorized') notifySessionEnded()
    throw err
  }
  if (!res.body) throw new ApiError('network', 0)
  opts.onOpen?.()
  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  // Cancel the read at once on abort, whatever the platform does with the body.
  const cancel = () => void reader.cancel().catch(() => {})
  opts.signal.addEventListener('abort', cancel, { once: true })
  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      for (const m of parser.push(decoder.decode(value, { stream: true }))) {
        if (opts.signal.aborted) return end('aborted')
        opts.onMessage(m)
      }
    }
  } catch {
    if (opts.signal.aborted) return end('aborted')
    throw new ApiError('network', 0)
  } finally {
    opts.signal.removeEventListener('abort', cancel)
  }
  return end(opts.signal.aborted ? 'aborted' : 'eof')
}
