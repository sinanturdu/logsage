# ADR 0015: Live tail with a visibility-ordered cursor

Status: accepted (v0.2 package I1b)

## Context
The dashboard needs new logs as they arrive (B2 design), for one project, with the same filters as the explorer.

The hard part is not missing a row. ClickHouse rows do not become visible in `timestamp` or `ingested_at` order:
- The processor inserts batches and retries failed inserts with backoff.
- It drains backlogs and reclaims pending entries.
- Several processors can insert at once.

A row with an older timestamp or an older ingest time can therefore become visible after newer rows were already streamed. A cursor on either column would skip it silently.

## Decision

### Transport: Server-Sent Events
- `GET /api/projects/{slug}/tail` returns `text/event-stream`. The stream is one-way, works with the session cookie and needs only the standard library.
- The browser's `EventSource` reconnects by itself with `Last-Event-ID`.
- **WebSocket was not chosen:** it needs a library or a hand-written framing layer, has no built-in resume, and does not go through the cookie, CSRF and origin path of a plain GET.
- **Reading the Redis stream directly was rejected:** the API would see every project's events and filter them in process, which makes one bug a cross-project leak. It also loses the shared query builder, project scope and filter semantics, and cannot deliver what the processor dead-letters or rewrites.

### Visibility order: `inserted_at`
- ClickHouse migration 0003 adds `logs.inserted_at DateTime64(3, 'UTC')`. Old rows get a fixed epoch default, never a `now()` expression, so old rows never look new.
- Migration 0004 adds a minmax index on the column.
- The processor sets `inserted_at` on every insert attempt, not once per batch. A batch that succeeds after retries therefore carries the time of the attempt that became visible.
- Rows are read ordered by `(inserted_at, event_id)`.
- Tail statements go through the same `where()` as every other read: project scope, legacy `"default"` rows for the default project (ADR 0012), and the filters.

### Cursor, overlap and deduplication
Each stream keeps:
- **Position:** the highest delivered key `L`.
- **Delivered ids:** a bounded set (`TAIL_MAX_SEEN`, default 50,000), with every observed version of each and its send sequence number.

Each tick (`TAIL_POLL_INTERVAL`, default 1s):
1. **New rows:** rows after `L` in key order, at most `TAIL_BATCH` (200). A full batch polls again at once (drain).
2. **Late rows:** ClickHouse counts the distinct event ids in the overlap window `(L - TAIL_OVERLAP, L]`, and that number is compared with the delivered ids that have a version in the same window.
   - Delivered ids are a subset of what ClickHouse counts, so the two are equal exactly when nothing is missing there.
   - Only when they differ is the window fetched and the missing rows sent. This is a cheap count, not a re-read of the window on every tick.
3. **Duplicates:** event ids already delivered are skipped. A redelivered event id (a new version with a later `inserted_at`, after a lost ack) is therefore sent once.
4. **Pruning:** ids that left both the overlap window and the lateness window are forgotten.

Further rules:
- **Polling without FINAL:** tail polls read without `FINAL`; deduplication by event id replaces it.
- **Stream start:** a stream starts at the API's "now". Ids already visible in the overlap window below it are marked as delivered without being sent, so the stream shows new rows only.
- **The overlap bounds lateness:** `TAIL_OVERLAP` (default 30s) must exceed the time between the start of an insert attempt and its rows becoming visible, which is bounded by the processor's ClickHouse request timeout. Clock skew between the processor host and the API host also counts against it.

### Scan restriction
- Every tail query also has `timestamp >= now - TAIL_MAX_LATENESS` (default 10m) and `timestamp < now + 1h`.
- Without that, the `inserted_at` condition alone could not use the sorting key and the partitions (by `timestamp`), and would scan the table.
- **Consequence:** logs whose event time is older than `TAIL_MAX_LATENESS` when they arrive are not streamed live. They are still in `/logs`. This covers backfills and clients with clocks far behind.

### Resume and reset
- A log event's SSE `id` is an opaque cursor, unpadded base64url of `{v, s, t, e, n}`: version, stream token, the position `L` after that event, and the event's send sequence number. Decoding is strict: canonical encoding only, no unknown fields, validated token and event id.
- **Same instance:** on reconnect with `Last-Event-ID` within `TAIL_MAX_REPLAY` (5m), the instance restores the stream's saved state.
  - Ids sent after the client's last received event are un-marked, so they are sent again, and the position goes back to the cursor's.
  - The integration test checks this case: no gaps and no duplicates.
- **State gone** (another instance, a restart): the stream continues strictly after the cursor's position, and ids in the overlap below it count as delivered.
- **Cursor too old:** a cursor older than `TAIL_MAX_REPLAY` gets `event: reset`, and the stream starts fresh at "now". The client then refetches via `/logs`.
- **Overload:** `reset` is also sent when a window holds more rows than `TAIL_MAX_SEEN`.

### Protocol
- Every stream starts with `retry: 5000`.
- `event: log`: `data` is the same JSON object as one `/logs` event, `id` is the cursor.
- `event: status`: `{"state":"live"|"degraded"|"closed","reason":...}`.
  - A heartbeat is `{"state":...,"heartbeat":true}` every `TAIL_HEARTBEAT` (15s).
  - Close reasons are `max_duration`, `shutdown`, `session_ended` and `access_revoked`.
- `event: reset`: `{"reason":"cursor_too_old"|"overloaded"}`.
- Headers: `Content-Type: text/event-stream`, `Cache-Control: no-store` and `X-Accel-Buffering: no`. Every write is flushed.
- No storage error text appears in any event.

### Limits and lifecycle
- **Connection caps:** `TAIL_MAX_CONN_USER` (2), `TAIL_MAX_CONN_PROJECT` (10) and `TAIL_MAX_CONN_TOTAL` (50). Above them the answer is `429` with `Retry-After`. They are checked after membership and parameter validation and are in memory, per instance.
- **Maximum duration:** after `TAIL_MAX_DURATION` (1h) the stream closes, and the client reconnects with `Last-Event-ID`.
- **Auth re-check:** every `TAIL_AUTH_RECHECK` (30s) the session is checked again (revoked, expired, user disabled) along with the membership. If either fails, the stream gets `closed` and ends. PostgreSQL errors do not close streams.
- **Slow clients:** every write has a deadline (`TAIL_WRITE_TIMEOUT`, 10s, through `http.ResponseController`), so a client that does not read is dropped instead of buffered. The per-write deadline also replaces the server's 30s `WriteTimeout` for streams.
- **Shutdown:** `http.Server.RegisterOnShutdown` closes every stream with `closed` (reason `shutdown`), so a graceful shutdown does not wait for them. This is tested without OS signals.
- **Query cap:** each tick takes a slot of the per-user query cap (`API_MAX_CONCURRENT_QUERIES`, ADR 0014). A tick that finds no free slot is skipped, never answered with an error, so tails cannot starve a user's explorer queries and cannot be cut by them.
- **ClickHouse failure:** the connection stays open and the client gets `degraded`. Polls back off, doubling from `TAIL_POLL_INTERVAL` up to `TAIL_RETRY_MAX` (30s), with heartbeats continuing meanwhile. When storage answers again, the client gets `live` before any further rows, and the cursor continues with no gap.

## Measured (live check, development laptop, compose stack)
- 100 events at 5/s through ingest: 100 received, none missing, none duplicated.
- Latency from ingest accepted to received on the stream: p50 980 ms, p95 1.70 s, p99 1.88 s. That is about the processor's flush interval (1s) plus the poll interval (1s).
- Stopping ClickHouse for about 10s gave `degraded`, then `live`. The 14 events sent meanwhile all arrived afterwards.

## Not covered
- **Rows visible later than `TAIL_OVERLAP`** after their `inserted_at` are missed by a running stream; they are still in `/logs`. One cause is an insert that ClickHouse commits long after the client timed out; another is clock skew larger than the overlap.
- **Resume across instances or restarts** continues strictly after the cursor. A row that became visible late inside the overlap during the disconnect can be missed. With several API instances this needs sticky sessions or a shared resume store.
- **A redelivered event id** whose earlier version left the overlap window is delivered again.
- **Logs with an event time older than `TAIL_MAX_LATENESS`** at arrival are not streamed.
- **Limits are per instance:** the connection caps, the resume store and the query cap.

## Consequences
- The schema gains a column and an index. The processor writes `inserted_at`, so an old processor against a migrated table, or rows inserted by other tools, get the epoch default and never stream.
- A stream costs at least one ClickHouse query per second (two with the late check), bounded by the time window, the index and the caps.
- Clients must handle `reset` by refetching with `/logs`.
