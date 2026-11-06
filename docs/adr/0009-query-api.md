# ADR 0009: Query API (GET /v1/logs)

Status: accepted

## Context
Logs land in `logsage.logs` (ADR 0008): a `ReplacingMergeTree` ordered by `(project_id, service, timestamp, event_id)`, delivered at-least-once. Clients need to search them by time range, level, service, text and trace ID, page through results, and never see another project's data. Search input is untrusted text that ends up in a SQL engine.

## Decision

**Separate binary and key.** `cmd/query` is read-only and authenticates with `QUERY_API_KEY`. The key is checked like ingest: SHA-256 of both sides, compared in constant time, never logged. The binary refuses to start without the key or when it equals `INGEST_API_KEY`, so a leaked write key cannot read logs and a read key cannot write. Separate binaries also let ingest and query scale and fail independently.

**One query builder, always project-scoped.** `query.Build` is the only code that produces search SQL. Its first `WHERE` condition is always `project_id = {project_id:String}`, with the value `"default"` until projects exist (v0.2). Tests build every combination of optional filters with hostile values and assert that the scope is present exactly once and is the only mention of `project_id`. Unknown query parameters (including `project_id`) are rejected with 400.

**Server-side parameters, never concatenation.** The SQL text consists only of constant fragments with `{name:Type}` placeholders. Values travel as `param_<name>` on the HTTP request. ClickHouse parses parameter values in its "Escaped" format, so the client escapes `\`, tab, newline, carriage return and NUL first; otherwise a search for `C:\temp` would contain a tab. Tests assert that the SQL is identical for benign and hostile values (quotes, comment markers, `UNION`, semicolons, placeholder syntax, 10k-character strings, Unicode, control characters).

**FINAL for deduplication.** Searches read `FROM logs FINAL`, so redelivered rows collapse even before background merges. Cost: ClickHouse merges overlapping parts at read time. That uses more CPU and memory and parallelises less, and it grows with the number of unmerged parts in the scanned range. The time-window limit keeps this bounded. If it becomes a bottleneck, the options are `do_not_merge_across_partitions_select_final` (safe here, because duplicates share a daily partition) or deduplicating with `LIMIT 1 BY event_id`.

**Keyset cursor.** Results are ordered `timestamp DESC, event_id DESC`. `next_cursor` is unpadded base64url of `{"v":1,"t":"<ms RFC3339 UTC>","id":"<event_id>"}`, taken from the last row of the page. The next page adds `(timestamp, event_id) < (cursor_ts, cursor_id)`, so rows with equal timestamps are neither repeated nor skipped. Pages are found by seeking, not by `OFFSET`. The query fetches `limit+1` rows to decide whether a next page exists. Decoding is strict: canonical encoding only, no unknown fields, a Redis-style ID, and a timestamp inside `[from, to)`. Anything else is 400. The cursor is not signed: tampering can only move the position inside the caller's own project and time range, because scope and range come from the request, not the cursor. `event_id` is compared as a string, which gives a consistent total order; that is all pagination needs.

**Protecting ClickHouse.** Every query runs with `readonly=1`, `max_execution_time=QUERY_TIMEOUT` (default 10s, `timeout_overflow_mode=throw`) and `max_result_rows=QUERY_MAX_LIMIT+1` (`result_overflow_mode=throw`). The range `to - from` is capped at `QUERY_MAX_WINDOW` (default 168h), the page size at `QUERY_MAX_LIMIT` (default 1000), and `q`/`service`/`trace_id` at `QUERY_MAX_TEXT_LEN` characters (default 256).

**Errors.** 400 names the bad parameter, then 401, 405, 503 (ClickHouse unreachable or failing), 504 (ClickHouse codes 159/160, or the client deadline), and 500 if ClickHouse rejects our own query. ClickHouse messages, SQL and addresses are logged at warn and never returned.

## Consequences
- **Scan cost depends on filters.** The sorting key is `(project_id, service, timestamp, event_id)`. With a `service` filter, ClickHouse can use the primary index for both service and time and read few granules. Without one, the time condition can only narrow each service's range inside the index, so more granules are read, roughly one band per service. Expect queries without `service` to scan noticeably more data. If that matters, the options are a skip index or projection on `timestamp`, or reordering the key, which would mean a new table.
- **Text search is a scan.** `q` uses `positionCaseInsensitiveUTF8(message, ...)`, which has no index. It reads every `message` in the matched time range. `QUERY_MAX_WINDOW` and `QUERY_TIMEOUT` bound the cost. A token or n-gram bloom-filter skip index can be added later without changing the API.
- Exact results need `FINAL` everywhere logs are read; any future endpoint must go through the same builder.
- `project_id` is fixed to `"default"` in v0.1. In v0.2 the project comes from the authenticated key, and the builder signature already requires it.
