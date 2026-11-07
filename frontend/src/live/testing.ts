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
