# ADR 0008: Processor design (Redis Stream to ClickHouse)

Status: accepted

## Context
The ingest API appends events to the Redis Stream `logsage:events` (ADR 0007). Requirement N3 says no data may be lost when the processor crashes. ClickHouse prefers large, infrequent inserts. Redelivery is unavoidable with at-least-once queues, so it must not create duplicate rows.

## Decision

**Consumer group.** `cmd/processor` reads with `XREADGROUP` in group `logsage-processor`. The consumer name is `PROCESSOR_CONSUMER`, or `hostname-pid` when unset. The group is created at ID `0` with `MKSTREAM`. On startup the processor first re-reads its own pending entries (`XREADGROUP ... 0`). Every `PROCESSOR_CLAIM_INTERVAL` it runs `XAUTOCLAIM` for entries idle longer than `PROCESSOR_CLAIM_IDLE` (30s), so work left by crashed or restarted consumers is taken over. Because the default consumer name changes on every restart, recovery relies on `XAUTOCLAIM`.

**Batching and ack.** A batch is flushed when it holds `PROCESSOR_BATCH_SIZE` (1000) rows or `PROCESSOR_FLUSH_INTERVAL` (1s) has passed since its first row. Entries are acked (`XACK`) only after ClickHouse accepted the insert. Delivery is therefore **at-least-once**: a crash between insert and ack, or an ack failure, redelivers entries that are already stored.

**Deduplication.** The Redis entry ID is stored as `event_id`. `logs` is a `ReplacingMergeTree` with `ORDER BY (project_id, service, timestamp, event_id)`. A redelivered entry yields a row with an identical sorting key, so the copies collapse when parts merge. Queries that need exact results read with `FINAL`, or aggregate with `uniqExact(event_id)` or `GROUP BY event_id`.

Alternatives considered:
- *Plain MergeTree plus ClickHouse insert deduplication* (block hashes): this only catches byte-identical retries of the same batch. A redelivered entry is usually inserted in a different batch, so duplicates would remain.
- *Exactly-once with a transactional outbox or a seen-ID table*: requires a second store and a lookup on every insert. That is more moving parts than v0.1 needs.
- *Dedup at query time only (`GROUP BY event_id` everywhere)*: always correct, but every query pays for it. With `ReplacingMergeTree` the duplicates also disappear from storage over time.

Trade-offs: until a merge runs, plain `SELECT count()` can over-count by the number of redelivered rows, and `FINAL` costs some read performance. Duplicates collapse only within a partition, which is fine because a duplicate has the same `timestamp` and therefore the same daily partition. If the Redis stream were deleted and recreated, IDs could in theory repeat, but a collision would also need the same project, service and timestamp.

**Dead-letter.** Entries that cannot be parsed or fail validation (bad JSON, wrong level, missing fields, trimmed entries without fields) are copied to `PROCESSOR_DEAD_STREAM` (`logsage:events:dead`, capped with approximate `MAXLEN`). Each copy keeps the original fields plus `original_id`, `reason` and `dead_lettered_at`. The entry is then acked so it does not block the pipeline. If writing the dead-letter fails, the entry stays pending and is retried after it is claimed. Unknown payload fields are ignored, so a newer ingest version does not send everything to the dead-letter stream.

**Failure handling and backpressure.** When an insert fails (ClickHouse unreachable, timeout, or a rejected batch), the batch is neither acked nor dropped. The processor retries with exponential backoff from `PROCESSOR_RETRY_INITIAL`, capped at `PROCESSOR_RETRY_MAX`, with equal jitter (uniform in `[d/2, d)`). While it retries it reads nothing new, so unprocessed entries pile up in Redis as stream lag rather than in processor memory. A batch that ClickHouse keeps rejecting stalls the pipeline visibly (`failed_batches_total` grows, lag grows) instead of losing data. Validation before insert is meant to keep this from happening.

**ClickHouse over HTTP.** The processor uses the HTTP interface (port 8123) with `net/http` and `INSERT INTO logs FORMAT JSONEachRow`. There is no new dependency. Requests have a timeout (`CLICKHOUSE_TIMEOUT`) and credentials go in `X-ClickHouse-User`/`X-ClickHouse-Key` headers. Timestamps are sent as `YYYY-MM-DD hh:mm:ss.sss` in UTC into `DateTime64(3, 'UTC')` columns. Compared with the native client (`clickhouse-go`, TCP 9000):
- *For HTTP:* no dependency; easy to debug with curl; works through ordinary HTTP proxies and load balancers; one request per batch is all we need.
- *Against HTTP:* JSON is larger and slower to parse than the native columnar format; no typed results or connection-level features (native compression, streaming blocks); errors come back as text. At our batch sizes (≤1000 rows per request) this overhead is small. If inserts become a bottleneck, switching to the native client or `RowBinary` can happen behind the `Store` interface.

**Schema.** Embedded SQL files in `backend/migrations/clickhouse` (`CREATE DATABASE/TABLE IF NOT EXISTS`) are applied on every start. `project_id` is `LowCardinality(String)` until projects exist; ingest writes `"default"`. `fingerprint` is 0 until fingerprinting lands. TTL comes from `CLICKHOUSE_TTL_DAYS`, but it is only applied when the table is created; changing it later requires `ALTER TABLE logs MODIFY TTL ...`.

**Shutdown.** On SIGINT/SIGTERM (or context cancel) the processor stops reading, then flushes and acks the current batch within `PROCESSOR_SHUTDOWN_TIMEOUT`. Anything not flushed stays pending in Redis and is claimed later.

## Consequences
- No silent loss: every entry is either stored and acked, dead-lettered and acked, or still pending and visible in `/stats` (`group_pending`, `group_lag`).
- Readers must use `FINAL` (or dedupe by `event_id`) for exact counts. The Query API (`GET /v1/logs`) has to do this.
- Consumer names from `hostname-pid` leave stale consumers in the group after restarts. They hold no pending entries once claimed; periodic `XGROUP DELCONSUMER` cleanup is a follow-up.
- Throughput is bounded by one insert in flight per processor. Scaling out means more processor instances in the same group.
