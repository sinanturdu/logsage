# ADR 0012: Project-scoped authentication for ingest and query

Status: accepted (v0.2 package H1)

## Context
Package G (ADR 0011) created projects and hashed per-project API keys in PostgreSQL, but ingest and query still accepted one static key each and wrote or read everything as project `"default"`. H1 makes the verified key decide the project end to end, without users or login (package H2).

## Decision

**One authenticator for both APIs** (`internal/auth.Service`). Ingest asks for kind `ingest`, query for kind `query`; a key of the other kind is a 401. Ingest keeps its check order 405 → 401 → 415 → 413 → 400 and the generic 401 body. Nothing in a request (body field, query parameter) can choose the project: ingest rejects unknown body fields, query rejects a `project_id` parameter, and both take the project only from the verified key.

**Verification cache.** Every request would otherwise cost a PostgreSQL round trip. The cache is in memory, bounded (`AUTH_CACHE_SIZE`, default 10,000, LRU eviction) and keyed by SHA-256 of the presented key plus the wanted kind, so the plaintext is never kept.
- Positive TTL `AUTH_CACHE_TTL` (30s): a verified key is trusted without a database check for this long. **A revoked key or a disabled project can keep working for up to this long.** That is the trade-off for not querying PostgreSQL on every request; lower the TTL if faster revocation matters more than load.
- Negative TTL `AUTH_NEGATIVE_TTL` (5s): rejected keys are remembered, so garbage keys cannot hammer PostgreSQL.
- Concurrent lookups for the same uncached key share one database call (no stampede).
- `last_used_at` keeps the once-a-minute throttle; with the cache it is written at most once per TTL.

**Failure modes: 401 vs 503.**
- 401 means the key itself is bad: missing, malformed, unknown, wrong secret, wrong kind, revoked, expired, or belonging to a disabled project. The body is always the same generic message.
- 503 means PostgreSQL is unreachable or failing and the key is not cached. It is never a 401, which would tell a client its key is wrong, and never a 200/202. A cached valid entry keeps working during an outage until its TTL expires; errors are not cached.
- `/readyz` of ingest and query also checks PostgreSQL when keys are database-backed.

**project_id mapping** (completes ADR 0011):
- New data is written with the project **UUID** as the string `project_id`, in the stream entry and in ClickHouse.
- v0.1 rows hold the legacy value `"default"`, which belongs to the default project (fixed id `00000000-0000-0000-0000-000000000001`).
- The query scope is always `(project_id = {project_id:String} OR project_id = {project_alias:String})`. The alias is `"default"` for the default project and equals the project id for every other project, so other projects never match legacy rows. The SQL text is identical for all projects (only parameters differ), and the OR on the first sorting-key column can still use the primary index. Nothing is rewritten in ClickHouse.
- The processor dead-letters entries whose `project_id` is neither a canonical UUID nor `"default"`; dead-lettered entries keep their original `project_id`.

**Legacy static keys** (`INGEST_API_KEY`, `QUERY_API_KEY`) work only while `ALLOW_LEGACY_STATIC_KEYS=true` (the default for now). They are mapped to the default project, must differ from each other, are checked in constant time, and need no database. A deprecation warning is logged at startup. Plan: default to `false` in v0.3 and remove them in v1.0. Without `DATABASE_URL` a service runs on legacy keys only; without either, it refuses to start.

## Not done yet
- Per-project rate limits and quotas (backpressure is still global).
- Users, login and project membership (package H2); the dashboard (package I).
- Cache invalidation on revoke: revocation takes effect after at most the positive TTL, not immediately.
- Key rotation helpers and audit logging of key use.

## Consequences
- Data isolation now depends on the key → project mapping, enforced in one authenticator and one query builder. End-to-end tests (`internal/e2e`) check it with identical data in two projects, for every filter, pagination and cross-project cursors.
- PostgreSQL is in the request path for uncached keys: an outage turns new or expired cache entries into 503s.
- In the live check, latency with database-backed keys at 10k events/s was p50 about 3 ms and p99 5.0–29.3 ms (median 7.0 ms, against 6.3 ms with static keys).
