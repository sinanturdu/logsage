# Changelog

All notable changes to LogSage. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
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
- Static `INGEST_API_KEY` / `QUERY_API_KEY`: honored only with `ALLOW_LEGACY_STATIC_KEYS=true` (default for now), mapped to the default project, with a startup warning.

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
