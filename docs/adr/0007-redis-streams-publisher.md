# ADR 0007: Redis Streams publisher for the ingest API

Status: accepted

## Context
ADR 0003 chose Redis Streams as the queue between ingestion and storage. The ingest handler already depends on a `Publisher` interface (ADR 0006). We now need a real implementation that is fast (one round trip per batch), bounded in memory, and testable without Redis.

## Decision
- **Dependency:** `github.com/redis/go-redis/v9`, pinned to **v9.18.0**. The standard library has no Redis client, and writing a RESP client with pipelining, connection pooling and timeouts by hand is not worth the risk. go-redis is the official Redis client for Go. v9.18.0 is the newest release that still supports our Go 1.23 toolchain (v9.19+ requires Go 1.24 or later). It brings three small indirect modules: `cespare/xxhash`, `dgryski/go-rendezvous`, `go.uber.org/atomic`. Upgrading go-redis is tied to raising the Go version.
- **Selection:** `PUBLISHER=memory|redis`, with memory as the default. Unit tests keep using the memory publisher.
- **One entry per event**, all XADDs of a batch sent in **one pipeline**. Fields:
  - `payload`: the validated event as JSON (`timestamp` RFC3339 UTC, `level`, `service`, `message`, `trace_id`, `attributes`)
  - `project_id`: `"default"` until projects exist (v0.2)
  - `ingested_at`: server time in UTC, RFC3339 with fractional seconds, the same for the whole batch

  Entry IDs are assigned by Redis (`*`).
- **Approximate trimming:** `XADD ... MAXLEN ~ REDIS_STREAM_MAXLEN` (default 100000). Exact trimming (`MAXLEN =`) is slower because Redis must split radix-tree nodes on every write. `~` trims whole nodes, so the stream can be somewhat longer than the cap, but it is never shorter.
- **Timeouts:** every Publish/Ready call runs under `REDIS_TIMEOUT` (default 2s), and go-redis is configured to honor context deadlines. Any error returns `503` from the handler.
- **Readiness:** `GET /readyz` pings Redis. `/healthz` stays liveness only. The binary starts even if Redis is down, so an orchestrator can see `503` on `/readyz` instead of a crash loop.
- **Testability:** the publisher talks to Redis through a small internal interface, so unit tests use a fake. Integration tests run against real Redis when `REDIS_TEST_ADDR` is set, which CI does.

## Consequences
- **At-least-once delivery.** A pipeline is not a transaction. If it fails part-way or times out after Redis applied it, some events of the batch are already in the stream while the client gets `503` and retries. Consumers must tolerate duplicates (the processor will deduplicate or accept them; ClickHouse inserts are idempotent only if we make them so).
- **Data loss window is bounded by trimming.** If consumers fall behind by more than about `REDIS_STREAM_MAXLEN` entries, the oldest unread entries are trimmed. The cap must be sized for the expected consumer lag, and lag should be monitored once the processor exists.
- **Durability depends on Redis persistence.** docker-compose runs Redis with AOF (`appendonly yes`, default `appendfsync everysec`), so up to about one second of acknowledged events can be lost on a Redis crash.
- `project_id` is a placeholder. Multi-tenant routing (per-project keys, or a field the processor filters on) comes with v0.2 accounts.
