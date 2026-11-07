// Test helpers for the live tail.

/** A streamed response that sends the chunks, then ends (or stays open). */
export function sseResponse(chunks: string[], opts: { open?: boolean; status?: number } = {}): Response {
  const enc = new TextEncoder()
  const body = new ReadableStream<Uint8Array>({
    start(c) {
      for (const ch of chunks) c.enqueue(enc.encode(ch))
      if (!opts.open) c.close()
    },
  })
  return new Response(body, { status: opts.status ?? 200, headers: { 'Content-Type': 'text/event-stream' } })
}

/** An open event stream the test writes to; it errors when the request is aborted. */
export function controlledStream(signal?: AbortSignal) {
  const enc = new TextEncoder()
  let ctl!: ReadableStreamDefaultController<Uint8Array>
  let closed = false
  const body = new ReadableStream<Uint8Array>({
    start(c) {
      ctl = c
    },
  })
  signal?.addEventListener('abort', () => {
    if (!closed) {
      closed = true
      ctl.error(new DOMException('aborted', 'AbortError'))
    }
  })
  const send = (text: string) => {
    if (!closed) ctl.enqueue(enc.encode(text))
  }
  return {
    response: new Response(body, { status: 200, headers: { 'Content-Type': 'text/event-stream' } }),
    send,
    log: (e: object, id: string) => send(`id: ${id}
event: log
data: ${JSON.stringify(e)}

`),
    status: (d: object) => send(`event: status
data: ${JSON.stringify(d)}

`),
    close: () => {
      if (!closed) {
        closed = true
        ctl.close()
      }
    },
  }
}
