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
- [x] **Package I1b:** live tail for the dashboard: Server-Sent Events with a visibility-ordered cursor (`inserted_at`), resume with Last-Event-ID, connection limits, auth re-check (ADR 0015)
- [x] **Package I2a:** dashboard foundation: Vite + React + TypeScript app, B2 design tokens and self-hosted fonts, login, app shell, project switcher, typed API client, frontend CI (ADR 0016)
- [x] **Package I2b:** log explorer (B2): URL state, two-strip timeline with brushing and accessible alternatives, facets, search, single-line rows with pagination; `scripts/dev.ps1` and `loadgen seed` for local development (ADR 0017)
- [x] **Package I2c-1:** row detail with Same trace and Copy as JSON, filter chips, Filters disclosure on narrow screens, zoom to selection, small-sample handling in the error share (ADR 0018)
- [x] **Package I2c-2:** live tail UI in the explorer (fetch-based SSE client, state machine with backoff, auto-pause), API keys screen with one-time reveal, account menu with change password and sign out of all devices (ADR 0019)
- [x] **Package I2c-3:** command palette (Ctrl K) with a fuzzy matcher, one shared shortcut definition and a "?" help, per-route titles, and polish: new-key dialog, chart text, live status and order, sentence-case labels, detail layout (ADR 0020)
- [x] **Package I2c-4:** the dashboard served from `cmd/api` (embedded build, SPA fallback, caching), strict security headers and CSP, runtime `/config.json` with `PUBLIC_INGEST_URL`, Docker frontend stage and root `.dockerignore`, legacy static keys off by default, v0.2.0 release notes (ADR 0021)
- [x] **v0.2.0 released** ([release notes](releases/v0.2.0.md))

## v0.3: Error groups, alerts and incidents
- [x] **Package J1:** error groups backend: fingerprinting in the processor (normalizer with a golden corpus), `template` column, `group_seen` view, groups list and detail, group status with audit rows, `fingerprint` filter and field (ADR 0022)
- [ ] **Package J2:** error groups UI
- [ ] **Package K:** alert rules and notifications
- [ ] **Package L:** incidents

## v0.4: Anomaly detection and AI root cause
- [ ] Python service: statistical baseline per service and level, evaluation notebook
- [ ] Deploy events and AI root-cause suggestions on incidents

## v0.5: MCP server and agent integration
- [ ] MCP server over the public API, so LLM agents can query logs, groups and incidents
- [ ] Public API docs and outgoing webhooks

## v1.0: Production hardening
- [ ] Dedicated read-only ClickHouse user for `cmd/api` and `cmd/query`
- [ ] Per-project quotas (ingest rate, retention, query budget)
- [ ] Email verification and password reset
- [ ] Prometheus metrics endpoint
- [ ] Backups and restore
- [ ] Scale tests
- [ ] Screenshots, README polish, demo video, one-command setup
- [ ] Remove the legacy static keys (`ALLOW_LEGACY_STATIC_KEYS`)
