# ADR 0014: Dashboard read API on the management API

Status: accepted (v0.2 package I1)

## Context
The dashboard (design B2, `docs/design/dashboard.md`) needs three reads per screen:
- a log page;
- a timeline of volume and error share;
- level and service counts for the filter column.

Browsers authenticate with the session cookie (ADR 0013), not with API keys, and must never hold a query key. `/v1/logs` (`cmd/query`, ADR 0009/0012) already validates and scopes searches; a second copy of that logic would drift.

## Decision

### One read path
- `query.Service` owns parameter parsing, validation, the SQL builders, the ClickHouse settings and error mapping.
- `/v1/logs` (API key) is a thin handler in front of it. The new session endpoints in `cmd/api` call the same service through a consumer-side interface (`api.LogReader`).
- Every builder starts from one `where()` that refuses an empty scope and always emits the project condition and the time range. The scope comes only from the verified key or from the caller's membership, never from a request parameter (`project_id` is an unknown parameter, `400`).

### Endpoints (session, viewer or higher)
- `GET /api/projects/{slug}/logs`: same parameters, limits, errors and response as `/v1/logs`.
- `GET /api/projects/{slug}/histogram`.
- `GET /api/projects/{slug}/facets`.

Access is checked in this order:
1. Membership. Non-members and unknown slugs get `404` before any parameter is read.
2. Parameter validation: `400`, with the `/v1/logs` messages.
3. The per-user cap: `429`.
4. The query.

ClickHouse text never reaches a client (`query.Classify`). Disabled projects stay readable by their members (history), while their keys stop working (ADR 0013).

### Service lists
`service=a,b` (also on `/v1/logs`):
- At most 20 names, each validated like before, duplicates dropped, sent as one `Array(String)` parameter.
- One name keeps the v0.1 statement `service = {service:String}`.
- A comma now always separates names, so a service whose name contains a comma cannot be filtered on.
- ClickHouse reads an array parameter with one level of escapes, after the client has already escaped backslashes and control characters. Quotes inside a name are therefore doubled, not backslash-escaped. The first version got this wrong; integration tests with hostile names found it.

### Histogram
- Buckets of `10s, 30s, 1m, 5m, 15m, 1h, 6h, 1d`, aligned to the Unix epoch (each size divides a day, so they are also aligned to UTC days).
- Automatic size: the smallest giving at most 240 buckets.
- An explicit `bucket` must be from the list and give at most 500 buckets.
- The server returns every bucket from the one holding `from` up to `to`, zero-filled. Counts are per level, so the client can draw volume and the error-plus-fatal share from one call.

### Facets
- Level counts apply every filter except `level`; service counts apply every filter except `service`. The sidebar thus shows what selecting another value would give.
- `total` applies every filter.
- Services are sorted by count: the top 50, plus `{"name":"other","other":true}` for the rest when truncated.
- It costs two queries: levels, and services with window totals.

### Limits
- The `QUERY_*` limits apply unchanged (window, limit, text length, timeout).
- Queries keep `readonly=1`, `max_execution_time` and `max_result_rows`: 501 rows for a histogram, 51 for service facets.
- `API_MAX_CONCURRENT_QUERIES` (default 4) caps the reads one user runs at once. A request above it gets `429` with `Retry-After: 1`. The cap is in memory and per instance, like the login limiter; invalid requests are rejected before they take a slot.

### FINAL
All three read `FROM logs FINAL`, like `/v1/logs`. The opt-in measurement `TestFinalCost` (`LOGSAGE_FINAL_COST=1`) used 100,000 rows over 24 hours plus 1% redelivered rows in a separate insert. Medians of 7 runs over HTTP on the development laptop, ClickHouse 24.8:

| Statement | with FINAL | without |
|---|---|---|
| logs page, 24 h, limit 100 | 16.9 ms | 16.4 ms |
| logs page, level and service list | 9.6 ms | 9.9 ms |
| histogram, 24 h, 15 min buckets | 9.9 ms | 11.8 ms |
| facets: levels | 6.9 ms | 7.4 ms |
| facets: services | 7.9 ms | 8.4 ms |

At this size the difference is within run-to-run noise: background merges had already collapsed most parts. FINAL cost grows with the number of unmerged parts and the window, so it is re-measured at the N2 volume before optimizing (the roadmap already lists `do_not_merge_across_partitions_select_final`). Nothing is optimized now.

### Configuration
- `cmd/api` now needs `CLICKHOUSE_URL`, `CLICKHOUSE_DB`, `CLICKHOUSE_USER` and `CLICKHOUSE_PASSWORD` set explicitly (fail-fast: the defaults are never right for a deployment).
- `/readyz` checks PostgreSQL and ClickHouse.
- The compose `api` service gets the ClickHouse secrets and no Redis.

## Not done
- A dedicated read-only ClickHouse user for `cmd/api` and `cmd/query`. Today they share the processor's user, and only `readonly=1` per query keeps them read-only. **Follow-up.**
- Live tail. It is the next package (I1b), because it needs a different transport: Server-Sent Events with a heartbeat, a rate cap, dropped-row notices and a connection limit per user. Its backpressure and lifetime questions differ from request/response reads, and it should not hold up the explorer.
- Caching facets and histograms, and a shared (Redis) concurrency cap across instances.

## Consequences
- One place to change validation, scoping and error mapping. A fix to either API's parameters applies to both.
- `cmd/api` depends on ClickHouse: if ClickHouse is down, `/readyz` fails and the read endpoints answer `503`, while login and key management still work.
- The facet exclusion rule costs a second ClickHouse query per facets call.
