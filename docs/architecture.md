# Architecture

## Overview
Modular monolith in Go plus one separate Python AI service. Ingestion is decoupled from storage by a queue so bursts do not overload the database.

```mermaid
flowchart LR
  A[Applications] -->|POST /v1/ingest| B[Ingestion API]
  B -->|XADD| C[(Redis Streams)]
  C -->|consumer group| D[Log Processor]
  C -->|consumer group| E[Alert Engine]
  D -->|batch insert| F[(ClickHouse)]
  E --> G[(PostgreSQL)]
  H[Query API] --> F
  H --> G
  H -->|WebSocket| I[Dashboard]
  J[AI Service] --> F
  J --> G
```

## Components
- **Ingestion API**: authenticates API key, validates batch, enforces rate limit and size limit, writes to Redis Stream, returns `202 Accepted`.
- **Log Processor** (`cmd/processor`): consumer group worker. Parses and validates entries, batches inserts into ClickHouse (by size or 1 s), acknowledges only after a successful write, dead-letters unusable entries. Fingerprinting comes later. See "Processor (v0.1)" below and ADR 0008.
- **Alert Engine**: evaluates rules on a sliding window; opens incidents and sends notifications.
- **Query API** (`cmd/query`): read-only REST search over ClickHouse (`GET /v1/logs`, v0.1); WebSocket live tail later. Always scoped by `project_id` through one query builder. See "Query API (v0.1 contract)" and ADR 0009.
- **AI Service**: anomaly detection and root-cause suggestions. Called by the backend over HTTP.
- **Dashboard**: React + TypeScript.

## Ingestion API (v0.1 contract)
```
POST /v1/ingest
X-API-Key: <project key>
Content-Type: application/json

{ "events": [
  { "timestamp": "2026-11-06T10:15:30.123Z", "level": "error", "service": "checkout",
    "message": "payment gateway timeout", "trace_id": "abc123",
    "attributes": { "env": "prod", "region": "eu-1" } } ] }
```
Responses (all bodies are JSON):

| Status | When | Body |
|--------|------|------|
| `202` | batch accepted | `{"accepted": N}` |
| `400` | malformed JSON, unknown field, trailing data, or an invalid event | `{"error": "events[3]: level must be one of ...", "index": 3}` (`index` only for per-event errors) |
| `401` | `X-API-Key` missing or wrong | `{"error": "missing or invalid API key"}` |
| `405` | method other than `POST` (`Allow: POST` is set) | `{"error": ...}` |
| `413` | body larger than the limit (by `Content-Length` or while reading) | `{"error": ...}` |
| `415` | `Content-Type` is not `application/json` (parameters such as `charset` are allowed) | `{"error": ...}` |
| `429` | rate limited (planned, not in v0.1 yet) | `{"error": ...}` |
| `503` | the publisher (queue) failed; the client should retry | `{"error": "events could not be accepted, retry later"}` |

Checks run in this order: method, API key, content type, body size, then JSON and event validation. The batch is all-or-nothing: if any event is invalid nothing is published.

Validation rules:
- `timestamp` required, RFC3339 (fractional seconds and offsets allowed; stored as UTC).
- `level` required, one of `debug`, `info`, `warn`, `error`, `fatal` (lowercase).
- `service` and `message` required and non-blank. `trace_id` and `attributes` optional.
- `attributes` is a flat object of string values.

Limits (env var, default): body `INGEST_MAX_BODY_BYTES` 1 MiB; events per batch `INGEST_MAX_EVENTS` 1000; message `INGEST_MAX_MESSAGE_BYTES` 8 KiB; attributes per event `INGEST_MAX_ATTRIBUTES` 32; attribute key `INGEST_MAX_ATTR_KEY_CHARS` 64 characters; attribute value `INGEST_MAX_ATTR_VALUE_CHARS` 1024 characters; `trace_id` `INGEST_MAX_TRACE_ID_LEN` 128 characters.

Authentication in v0.1 is a single static key from `INGEST_API_KEY`, compared in constant time and never logged. Per-project hashed keys replace it in v0.2.

### Publishing (v0.1)
`PUBLISHER` selects where accepted events go:
- `memory` (default): bounded in-process buffer for tests and local runs. Events are lost on restart.
- `redis`: one `XADD <REDIS_STREAM> MAXLEN ~ <REDIS_STREAM_MAXLEN> * ...` per event, with the whole batch sent in one pipeline under `REDIS_TIMEOUT`. Any failure returns `503`. Delivery is at-least-once (see ADR 0007).

Stream entry (default stream `logsage:events`):

| Field | Value |
|-------|-------|
| `payload` | event JSON: `timestamp` (RFC3339, UTC), `level`, `service`, `message`, `trace_id` (omitted if empty), `attributes` (omitted if empty) |
| `project_id` | `"default"` until projects exist (v0.2) |
| `ingested_at` | server receive time, RFC3339 UTC with fractional seconds |

Config: `REDIS_ADDR` (required for `redis`), `REDIS_STREAM` (`logsage:events`), `REDIS_STREAM_MAXLEN` (100000, approximate), `REDIS_TIMEOUT` (`2s`).

### Health endpoints
- `GET /healthz`: liveness only, always `200 {"status":"ok"}` while the process serves HTTP.
- `GET /readyz`: `200 {"status":"ready"}`, or `503 {"status":"unavailable"}` when `PUBLISHER=redis` and Redis `PING` fails within `REDIS_TIMEOUT`. Failure details are logged, not returned.

## Query API (v0.1 contract)
```
GET /v1/logs?from=2026-11-06T00:00:00Z&to=2026-11-06T12:00:00Z&level=error,fatal&service=checkout&q=timeout&limit=100
X-API-Key: <query key>
```
| Parameter | Rules |
|-----------|-------|
| `from`, `to` | Required, RFC3339. `from` inclusive, `to` exclusive, `from < to`, `to - from <= QUERY_MAX_WINDOW` (168h). Millisecond precision. |
| `level` | Optional, comma-separated subset of `debug,info,warn,error,fatal`. |
| `service` | Optional, exact match. |
| `q` | Optional, case-insensitive substring of `message` (UTF-8, at most `QUERY_MAX_TEXT_LEN` = 256 characters). |
| `trace_id` | Optional, exact match. |
| `limit` | Optional, 1..`QUERY_MAX_LIMIT` (1000), default 100. |
| `cursor` | Optional, `next_cursor` from the previous page of the same query, unchanged. |

Unknown or repeated parameters are rejected. Results are ordered newest first (`timestamp DESC, event_id DESC`) and deduplicated (`FINAL`).

`200`:
```json
{"events":[{"event_id":"1793993613811-0","timestamp":"2026-11-06T10:00:01.000Z","level":"info","service":"checkout",
  "message":"order created","trace_id":"t-1","attributes":{"env":"dev"}}],
 "next_cursor":"eyJ2IjoxLC...","took_ms":7}
```
`next_cursor` is `null` on the last page. Errors are `{"error":"..."}`: `400` (names the bad parameter), `401` (missing or wrong key, including the ingest key), `405` (not GET), `503` (ClickHouse unreachable or failing), `504` (query timed out), `500` (ClickHouse rejected the query). ClickHouse messages are never returned to clients.

Auth: `QUERY_API_KEY`, separate from `INGEST_API_KEY` (they must differ); constant-time comparison of SHA-256 digests, never logged.

Safety: every query is built by `query.Build`, which always adds `project_id = {project_id:String}` (`"default"` until v0.2). All values are server-side parameters (`{name:Type}` + `param_<name>`), never part of the SQL text. ClickHouse settings per query: `readonly=1`, `max_execution_time=QUERY_TIMEOUT` (10s), `max_result_rows=QUERY_MAX_LIMIT+1`.

Cost: queries without `service` read more of the primary index than queries with it (sorting key `(project_id, service, timestamp, event_id)`), and `q` scans every `message` in the time window. Both are bounded by `QUERY_MAX_WINDOW` and `QUERY_TIMEOUT` (ADR 0009).

Health: `GET /healthz` (liveness), `GET /readyz` (ClickHouse `/ping`). Listens on `QUERY_HTTP_ADDR` (`:8082`).

## Data model

### ClickHouse
Created by `backend/migrations/clickhouse` (applied idempotently by the processor on startup):
```sql
CREATE TABLE IF NOT EXISTS logsage.logs (
  project_id  LowCardinality(String),   -- "default" until projects exist (v0.2)
  event_id    String,                   -- Redis stream entry ID, used for deduplication
  timestamp   DateTime64(3, 'UTC'),
  level       LowCardinality(String),
  service     LowCardinality(String),
  message     String,
  fingerprint UInt64,                   -- 0 until fingerprinting lands
  trace_id    String,
  attributes  Map(String, String),
  ingested_at DateTime64(3, 'UTC') DEFAULT now64(3)
) ENGINE = ReplacingMergeTree
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (project_id, service, timestamp, event_id)
TTL toDateTime(timestamp) + INTERVAL 30 DAY;  -- CLICKHOUSE_TTL_DAYS
```
Delivery is at-least-once, so a redelivered entry can be inserted twice. Duplicates share the sorting key and collapse on merge. **Read with `FINAL`** (`SELECT ... FROM logs FINAL`) or dedupe by `event_id` when exact results matter.

## Processor (v0.1)
- Consumer group `logsage-processor` on `REDIS_STREAM`; consumer `PROCESSOR_CONSUMER` (default `hostname-pid`). The group is created at `0` with `MKSTREAM`.
- Loop: own pending entries first, then `XREADGROUP >` (`PROCESSOR_READ_COUNT`, `PROCESSOR_READ_BLOCK`), plus `XAUTOCLAIM` every `PROCESSOR_CLAIM_INTERVAL` for entries idle longer than `PROCESSOR_CLAIM_IDLE` (30s).
- Flush at `PROCESSOR_BATCH_SIZE` (1000) rows or `PROCESSOR_FLUSH_INTERVAL` (1s) as one `INSERT INTO logs FORMAT JSONEachRow` over HTTP (`CLICKHOUSE_URL`, port 8123), then `XACK`.
- Insert failure: no ack, no drop; retry with exponential backoff and jitter (`PROCESSOR_RETRY_INITIAL`..`PROCESSOR_RETRY_MAX`) and read nothing new meanwhile (backpressure shows up as stream lag).
- Unusable entries go to `PROCESSOR_DEAD_STREAM` (`logsage:events:dead`) with their original fields plus `original_id`, `reason` and `dead_lettered_at`, then get acked.
- Shutdown (SIGINT/SIGTERM): stop reading, flush and ack within `PROCESSOR_SHUTDOWN_TIMEOUT`.
- HTTP (`PROCESSOR_HTTP_ADDR`, default `:8081`): `GET /healthz` (liveness), `GET /readyz` (Redis `PING` and ClickHouse `/ping`), `GET /stats`:
  ```json
  {"stream_length":3,"group_pending":0,"group_lag":0,"processed_total":3,"failed_batches_total":0,"dead_lettered_total":0}
  ```
  `failed_batches_total` counts failed insert attempts. Stream fields are `null` (with `stream_error`) when Redis is down. The same figures are logged every `PROCESSOR_STATS_INTERVAL` (10s).

### PostgreSQL
`users`, `projects`, `api_keys` (hash only), `alert_rules`, `error_groups`, `incidents`, `incident_events`, `deploy_events`.

## Fingerprinting
Normalize the message (strip numbers, UUIDs, hex, timestamps, paths), hash the normalized template plus service plus level. Same template = same error group. Refined later with similarity clustering.

## Agent integration (planned, v1.0)
LogSage exposes a stable REST API and outgoing webhooks so external agents can query logs, read incidents, and post actions. A Model Context Protocol (MCP) server wrapper is planned so LLM agents can use LogSage as a tool. Design implication: every capability must be available through the API, not only the UI.

## Risks
| Risk | Mitigation |
|------|------------|
| Ingestion bursts overload storage | Queue + batched inserts, backpressure and `429` |
| High-cardinality attributes bloat ClickHouse | Attribute count/size limits, `Map` type, documented limits |
| Redelivery duplicates rows | `event_id` + `ReplacingMergeTree`, reads with `FINAL` (ADR 0008) |
| ClickHouse down or rejecting inserts | No ack, retry with backoff, backpressure into Redis; visible in `/stats` |
| Poisoned or oversized payloads | Size limits, strict validation, rate limit per key |
| Cross-project data leak | Mandatory `project_id` filter via a single query builder, tested (ADR 0009) |
| SQL injection through search parameters | Server-side query parameters only, `readonly=1`, injection tests |
| Expensive searches | Time-window, page-size and text-length limits, `max_execution_time`, `504` |
| False-positive anomalies | Start with simple statistical baselines, measure before adding ML |
