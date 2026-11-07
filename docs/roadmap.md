# Roadmap

## v0.1: Ingest and search
- [x] Go module, project layout, config loading, structured logging
- [x] `/healthz` endpoint
- [x] `POST /v1/ingest` with validation and API key check (static key first)
- [x] Redis Stream producer
- [x] Processor: consumer group, batch insert to ClickHouse, ack after write
- [x] ClickHouse migration for `logs`
- [x] `GET /v1/logs` search (time range, level, service, text)
- [x] Unit and integration tests, CI green
- [x] Load test script and first benchmark numbers in docs ([benchmarks](benchmarks.md))

## Known follow-ups
Found while building and benchmarking v0.1 (see [benchmarks](benchmarks.md) and the [v0.1.0 known limitations](releases/v0.1.0.md#known-limitations)).

- [x] **Ingest backpressure / no silent loss:** publish without `MAXLEN`, safe `XTRIM MINID` in the processor, `429` at `INGEST_MAX_STREAM_LEN`, Redis `noeviction` (ADR 0010).
- [ ] Tighten the soft backpressure limit if overshoot matters: shorter refresh interval, or an atomic check-and-add in Lua; account for several ingest instances.
- [ ] Adaptive `Retry-After` (from the processor's drain rate) instead of a fixed 1 s.
- [ ] Rename `failed_batches_total` to `failed_attempts_total` (it counts failed insert attempts).
- [ ] Periodic cleanup of stale Redis consumers (`XGROUP DELCONSUMER`); `hostname-pid` names pile up after restarts.
- [ ] Processor recreates the consumer group when it gets `NOGROUP` (today the stream or group being deleted while it runs needs a restart).
- [ ] Route go-redis internal logs through `slog`.
- [ ] Decide a Go version upgrade for the module and CI (go-redis is pinned to v9.18.0 because v9.19+ needs Go 1.24+).
- [ ] Revisit strict rejection of unknown JSON fields for SDK clients.
- [ ] Evaluate query speedups from the benchmark results: a skip index or projection on `timestamp` (queries without `service` read the whole window; N4 would not hold at N2 volume), `do_not_merge_across_partitions_select_final` for `FINAL`, a token bloom filter for text search.
- [ ] Processor throughput: overlap reading the next batch with the insert, more consumers, fewer allocations in parse/encode (serial loop at about 24.5 ms per 1000-event batch).
- [x] Re-measure N1 (ingest p99 < 50 ms): 8 of 8 runs under 50 ms after the no-loss fix ([benchmarks](benchmarks.md#2026-11-07-after-the-no-loss-fix)).
- [ ] Review Redis `appendfsync` and AOF rewrite settings for large backlogs (the earlier N1 tail coincided with big AOF rewrites).

## v0.2: Accounts and dashboard
- [x] **Package G:** PostgreSQL foundation: migrations, projects, hashed per-project API keys, admin CLI (ADR 0011)
- [x] **Package H1:** ingest and query verify per-project keys; the key decides the project end to end; isolation tests (ADR 0012)
- [x] **Package H2:** users, server-side sessions, project roles and the management API (`cmd/api`); admin CLI user and member commands (ADR 0013)
- [x] **Package I0:** dashboard design brief; chosen direction B2 (`docs/design/dashboard.md`)
- [x] **Package I1:** dashboard read API on `cmd/api`: session-authenticated logs, histogram and facets through one shared query service; multi-value service filter; per-user query cap (ADR 0014)
- [ ] **Package I1b (next):** live tail for the dashboard (Server-Sent Events, filters, rate cap)
- [ ] **Package I2:** React dashboard: login, log explorer (B2), timeline, keys
- [ ] Dedicated read-only ClickHouse user for `cmd/api` and `cmd/query`

## v0.3: Live tail
- [ ] WebSocket streaming with filters

## v0.4: Error groups and alerts
- [ ] Fingerprinting, error groups UI
- [ ] Alert rules and notifications

## v0.5: Anomaly detection
- [ ] Python service, statistical baseline, evaluation notebook

## v0.6: Incidents and root cause
- [ ] Incident lifecycle, deploy events, AI root-cause suggestions

## v1.0: Release
- [ ] Public API docs, webhooks, MCP server, one-command setup, demo video, README polish
