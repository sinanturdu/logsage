# Changelog

All notable changes to LogSage. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- Error groups backend (v0.3 package J1, ADR 0022):
  - The processor fingerprints `warn`, `error` and `fatal` rows: a normalized template of the first line plus service and error type. A golden corpus test pins every template and fingerprint.
  - ClickHouse: a `template` column, a `bloom_filter` index on `fingerprint`, and the `group_seen` table with its materialized view for first and last seen.
  - PostgreSQL: `error_groups` for the group status and note; every change writes an audit row.
  - `GET /api/projects/{slug}/groups` (counts, sparklines, `is_new`, `regressed`, status filter, sorts, cursor), `GET .../groups/{fingerprint}` and `POST .../groups/{fingerprint}/status` (owner and admin).
  - An optional `fingerprint` filter on `/logs`, `/histogram`, `/facets`, `/tail` and `/v1/logs`, and a `fingerprint` field on log events.
  - Python tracebacks group by their exception line (the last non-empty line, found in the last 4 KiB), also when chained.
  - Exit codes, exit statuses, signals and SQLSTATE codes are kept in templates (`exit code 137` and `exit code 1` are two groups).
  - The processor caches templates of repeated messages (at most 8,192 entries, about 9 MiB). Cost against v0.2: [docs/benchmarks.md](docs/benchmarks.md#j1-processor-cost-2026-11-08).
  - `loadgen ingest --grouped-share` sets the share of `warn`, `error` and `fatal` events.

### Notes
- Rows ingested before this version, and `debug` and `info` rows, are not grouped (`fingerprint = 0`); there is no backfill.

## [v0.2.1] - 2026-11-08

Patch release: dashboard fixes only. Release notes: [docs/releases/v0.2.1.md](docs/releases/v0.2.1.md).

### Fixed
- Command palette:
  - It could not be closed once focus had left its panel (for example after a click outside). The owner hit this after opening it with Ctrl K.
  - Escape now closes it from anywhere, and Ctrl K / Cmd K toggles it. A backdrop click or tap, a visible Close button and any route change close it too.
  - Focus returns to where it was, and opening twice never stacks two palettes.
  - A footer explains the keys.
- The palette's empty list offers up to 8 commands for the context instead of only navigation.
- The error-share strip is replaced by one line while only error and fatal levels are selected (it would be 100% everywhere), and the summary matches.

## [v0.2.0] - 2026-11-07

Second milestone: accounts and dashboard. Release notes: [docs/releases/v0.2.0.md](docs/releases/v0.2.0.md).

### Upgrade notes
- **Breaking:** the legacy static keys are off by default. `INGEST_API_KEY` and `QUERY_API_KEY` are ignored unless `ALLOW_LEGACY_STATIC_KEYS=true`. To keep the old behavior while clients move to per-project keys, set it to `true` in `.env` (compose passes it through); the static keys are removed in v0.3.
- Deploy order: run the PostgreSQL migrations, then the processor (ClickHouse migrations 0003/0004 add `logs.inserted_at`), then `cmd/api`, ingest and query. Rows stored before the upgrade never appear in a live tail.
- `service=a,b` is now a list (at most 20 names); a service name containing a comma can no longer be searched exactly.
- The Docker build context is the repository root: `docker build -f backend/Dockerfile .`.

### Added
- The dashboard is served by `cmd/api` (ADR 0021):
  - It is embedded at build time from `backend/internal/web/dist` (the Docker image's Node stage fills it).
  - Only GET and HEAD are allowed, with a single-page-app fallback.
  - Hashed assets are cached for a year as immutable; `index.html` gets `no-cache`.
  - Unknown `/api/` paths get a JSON 404, and traversal attempts get a plain 404.
  - `WEB_ENABLED` turns it off.
- `GET /config.json` with the public ingest address (`PUBLIC_INGEST_URL`, validated at start-up). The dashboard reads it before rendering and uses it in the new-key examples.
- Security headers on every response:
  - The dashboard gets a strict Content-Security-Policy without `unsafe-inline` or `unsafe-eval`.
  - Everything gets `nosniff`, `Referrer-Policy: no-referrer`, a Permissions-Policy, COOP, CORP and `X-Frame-Options: DENY`.
  - HSTS is sent only with `COOKIE_SECURE=true` (TLS in front).
- `scripts/dev.ps1 up` builds the dashboard into the image and prints `Dashboard: http://localhost:8083`; `web` stays for hot-reload frontend work.
- Root `.dockerignore` (allow-list) that keeps `.env`, `.dev`, keys, `node_modules`, `dist` and `.git` out of the build. CI builds the image from the root and checks it.
- Command palette (ADR 0020):
  - Opens with `Ctrl K` or `⌘ K` (also while typing) or the Commands button.
  - It is a combobox with a listbox, a focus trap and a polite result count.
  - Commands cover navigation, project switching, time ranges, filters, the live tail, refresh, zoom, copy link, account actions and help, each only when it applies.
  - A fuzzy matcher in the repo highlights matched letters.
  - Recent commands are kept in memory.
- Keyboard shortcuts help (`?`), generated from the one shortcut definition that the handlers use.
- Per-route document titles ("Logs - demo - LogSage"), announced politely on navigation.
- The new-key dialog's test-log example has macOS / Linux and Windows PowerShell tabs (the latter uses `curl.exe` with a temp file).

### Changed
- New-key dialog: the kind name appears once with a short description, and the key is shown in full in a wrapping, selectable block.
- Explorer timeline: the summary starts with the peaks (the total stays in the scope line), and the notes moved into an "About this chart" disclosure.
- Live tail: the status says "Receiving new logs". While live, each batch is sorted by event time and goes on top ("latest arrivals first"); turning Live off restores strict newest-first order.
- Row detail: zone headings are sentence case, and a lone attribute group flows in columns.
- Live tail in the log explorer (ADR 0019):
  - A Live toggle (also `l`, `live=1` in the URL) streams new logs through a fetch-based SSE client with Last-Event-ID.
  - Backoff starts at 5 s and doubles up to 30 s, with a countdown and Retry now (`r`). A 401 returns to sign-in, a 404 or a 400 stops with a plain reason, and a 429 waits for Retry-After.
  - It shows a degraded state and recovers from it.
  - New logs are batched per frame, de-duplicated and capped at 1,000 rows.
  - The list auto-pauses while you read, with "N new logs" and Resume.
  - Screen readers get a polite summary at most every 5 seconds.
  - The timeline refreshes every 10 seconds while Live is on.
- API keys screen at `/projects/:slug/keys` with a Logs / API keys nav:
  - The list puts active keys first, with UTC times and status in words.
  - Owners and admins create keys in a dialog that reveals the key once, with Copy and a placeholder curl example, and revoke them after a confirmation.
  - Viewers get the list read-only.
- Account menu: Change password (12 to 128 characters; other sessions are signed out), Sign out of all devices, and Sign out, as a keyboard menu.
- Log explorer row detail (ADR 0018): Enter, Space or a click expands a row (treegrid, Escape collapses) to show the full message, `trace_id` and `span_id` with copy buttons, Request and Context attributes by a fixed grouping rule, "Filter by this trace" (also `t`) and "Copy as JSON" with a clipboard fallback.
- Same trace: the logs that share the trace within 10 minutes of the log, loaded lazily, cancelled on collapse, 5 shown then "Show all N", the current log marked; clicking one shows it in the open detail.
- Filter chips above the list, one removable chip per active filter plus Clear filters, and a "Filters (N)" disclosure below 900px.
- Zoom to selection and Zoom out (`zoom=1` in the URL, Back works), and a scope line ("N logs in view, M in the selection").

### Changed
- The error-share strip leaves out buckets with fewer than 5 logs (the line breaks there, with a note), and the summary line ignores them for the peak share.
- Log explorer at `/projects/:slug/logs` (ADR 0017): range and filters in the URL (relative or absolute UTC ranges), a two-strip SVG timeline with brushing, presets, a custom UTC range and a summary line, level and service facets with counts, trace id validation, debounced search with `/`, single-line B2 rows through `<LogText>` with `j`/`k`/Escape selection, Load more with de-duplication and a 1,000-row cap, and plain-language empty and error states (400, 429 with Retry-After, 503 or network, 404).
- `scripts/dev.ps1` (Windows PowerShell 5.1 and pwsh): `up`, `seed`, `web`, `status` and `down` for one-command local development, with a dev user, a project and an ingest key created idempotently.
- `loadgen seed`: an hour of demo history with an incident in the last 15 minutes (payments timeouts cascading into checkout and api errors), at most 100k events.

### Fixed
- Dashboard: a second request failing with 401 no longer replaces the "session ended" notice after sign-out elsewhere.
- Dashboard foundation in `frontend/` (ADR 0016): Vite + React + TypeScript (strict), B2 design tokens with self-hosted Red Hat fonts, login with plain-language errors, return to the requested URL after a 401, app shell with project switcher, `Ctrl K` hint and account menu, empty state with project creation, typed API client with central error mapping, `<LogText>` for text-only rendering of untrusted log data, lint rules against HTML injection and browser storage, Vitest tests, and a `frontend` CI job.
- Live tail `GET /api/projects/{slug}/tail` on `cmd/api` (ADR 0015): Server-Sent Events with the read endpoints' filters, in visibility order with late-visible rows and redeliveries handled, resume with `Last-Event-ID` (reset when older than `TAIL_MAX_REPLAY`), heartbeats, `degraded`/`live` on ClickHouse errors, connection limits per user/project/total, auth re-check, per-write deadlines and close on shutdown. Settings `TAIL_*`.
- ClickHouse migrations 0003/0004: `logs.inserted_at` (fixed epoch for old rows) with a minmax index; the processor sets it per insert attempt.
- `loadgen tail`: end-to-end live tail latency (p50/p95/p99) and missing events.
- Dashboard read endpoints on `cmd/api` (ADR 0014): `GET /api/projects/{slug}/logs`, `/histogram` (per-level counts in buckets of 10s to 1d, zero-filled) and `/facets` (levels without the level filter, services without the service filter, top 50 plus other) for members with a session; non-members get `404`. Per-user cap `API_MAX_CONCURRENT_QUERIES` (default 4, `429` + `Retry-After`).
- Log search accepts a comma-separated `service` list (at most 20 names), also on `/v1/logs`.
- `cmd/api` needs `CLICKHOUSE_URL`, `CLICKHOUSE_DB`, `CLICKHOUSE_USER` and `CLICKHOUSE_PASSWORD`, uses the `QUERY_*` limits and checks ClickHouse in `/readyz`; the compose `api` service gets the ClickHouse settings (no Redis).
- Users, server-side sessions and the management API `cmd/api` on `:8083` (ADR 0013): login, logout, logout-all, me, change-password, optional registration (`REGISTRATION_ENABLED`, off by default), projects with `owner`/`admin`/`viewer` roles, key list/create (shown once)/revoke. Non-members get `404`. Session cookie `HttpOnly`, `SameSite=Lax`, `__Host-` prefix and `Secure` with `COOKIE_SECURE=true` (default); idle (12h) and absolute (7d) TTLs.
- Passwords hashed with argon2id (PHC strings, rehash on login, bounded concurrency, dummy verification for unknown emails); policy 12–128 characters, not the email. New direct dependency `golang.org/x/crypto` v0.33.0 (was indirect via pgx).
- CSRF (JSON content type plus trusted `Origin`/`Referer`), CORS only for `ALLOWED_ORIGINS`, per-IP and per-email login throttling with `429` + `Retry-After` (per instance), audit log of security events.
- PostgreSQL migration 0002: `users`, `sessions` (token hash only), `project_members`, `projects.created_by`, `audit_log`.
- `cmd/admin`: `create-user`, `set-password`, `disable-user`, `add-member`, `list-members`; key creation shared with the API.
- Config: `API_HTTP_ADDR`, `SESSION_IDLE_TTL`, `SESSION_ABSOLUTE_TTL`, `COOKIE_SECURE`, `ALLOWED_ORIGINS`, `REGISTRATION_ENABLED`, `ARGON2_*`, `LOGIN_*`. Compose: `api` service (app profile, 127.0.0.1:8083); the image contains `cmd/api`.
- Ingest and query authenticate with per-project keys from PostgreSQL (ADR 0012); the verified key decides the project end to end: stream entries and ClickHouse rows carry the project UUID, queries are scoped to it (the default project also matches legacy `"default"` rows). One shared authenticator with a bounded SHA-256-keyed cache (`AUTH_CACHE_TTL`, `AUTH_NEGATIVE_TTL`, `AUTH_CACHE_SIZE`), single lookup per key, `503` when PostgreSQL cannot verify an uncached key, `/readyz` checks PostgreSQL. The processor dead-letters entries without a valid project id.
- End-to-end isolation tests (`internal/e2e`) against PostgreSQL, Redis and ClickHouse.
- Compose: one-shot `migrate` service; ingest and query depend on it and on PostgreSQL. The image contains `cmd/admin`.

### Deprecated
- Static `INGEST_API_KEY` / `QUERY_API_KEY`: honored only with `ALLOW_LEGACY_STATIC_KEYS=true` (off by default since v0.2.0), mapped to the default project, with a startup warning. Removed in v0.3 (package M).

- PostgreSQL foundation for v0.2 (ADR 0011): `github.com/jackc/pgx/v5` v5.7.4; hand-written embedded migrations with checksums (line-ending independent) and an advisory lock; `projects` and `api_keys` tables plus the `default` project that v0.1 data belongs to.
- Per-project API keys `lsk_<ing|qry>_<prefix>_<secret>`: only the prefix and SHA-256 are stored, constant-time verification with one generic rejection error, throttled `last_used_at`.
- `cmd/admin`: `migrate`, `create-project`, `list-projects`, `disable-project`, `create-key` (key shown once), `list-keys`, `revoke-key`, `verify-key`.
- Config: `DATABASE_URL` (or `POSTGRES_*`), `DATABASE_MAX_CONNS`, `DATABASE_CONNECT_TIMEOUT`, `DATABASE_QUERY_TIMEOUT`.
- Compose: PostgreSQL on a named volume with a healthcheck. CI: PostgreSQL service and `POSTGRES_TEST_URL` for the integration tests.

## [v0.1.0] - unreleased

First milestone: ingest and search. Release notes: [docs/releases/v0.1.0.md](docs/releases/v0.1.0.md).

### Added
- Go backend skeleton: environment config with fail-fast validation, structured logging (`log/slog`), `/healthz` and `/readyz` on every service.
- `POST /v1/ingest` (`cmd/ingest`): strict JSON batch validation with configurable limits, static API key compared in constant time, JSON errors with the failing event index.
- Redis Streams publisher (one pipelined `XADD` per batch, no length trimming), selectable with `PUBLISHER=memory|redis`.
- Processor (`cmd/processor`): consumer group with own-pending recovery and `XAUTOCLAIM`, batched `INSERT ... FORMAT JSONEachRow` over HTTP, ack after write, retry with backoff and backpressure, dead-letter stream, `/stats`, graceful shutdown.
- ClickHouse schema (`logsage.logs`, `ReplacingMergeTree` keyed by `event_id`) applied idempotently on startup.
- `GET /v1/logs` (`cmd/query`): time range, level, service, text and trace filters; keyset cursor pagination; separate read key; single project-scoped query builder with server-side parameters, `readonly=1` and execution limits.
- Docker image for the three services and a Compose `app` profile (`infra/docker-compose.app.yml`) for a one-command local stack.
- `cmd/loadgen`: realistic ingest load, query benchmark scenarios and a demo stream with a simulated incident.
- Benchmarks on a local Docker setup ([docs/benchmarks.md](docs/benchmarks.md)) and ADRs 0001–0009.
- CI: vet, build and race-enabled tests with Redis and ClickHouse services; Docker image build and Compose config validation.

### Fixed
- No silent log loss under bursts (ADR 0010): `XADD` no longer uses `MAXLEN` (which trimmed unread entries, 22.9% of a burst in the benchmark); the processor trims only entries every consumer group acknowledged (`XTRIM MINID`, `PROCESSOR_TRIM_INTERVAL`); ingest answers `429` with `Retry-After` at `INGEST_MAX_STREAM_LEN`; Redis runs with a 1 GB `noeviction` cap so overload becomes `503`, never `202`. `REDIS_STREAM_MAXLEN` is removed (startup error if set). New: ingest `/stats`, processor `trimmed_total` and `oldest_unprocessed_age_s`, loadgen `--retry-on-429` and `--verify`.
- Ingest HTTP shutdown is bounded like the other services.
- ClickHouse and Redis data moved to named Docker volumes: bind mounts from a Windows filesystem broke ClickHouse part renames and stalled Redis persistence.
- Processor HTTP shutdown is bounded (force-close after 5 s).

### Known limitations
See [docs/releases/v0.1.0.md](docs/releases/v0.1.0.md#known-limitations).
