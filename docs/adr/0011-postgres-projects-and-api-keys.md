# ADR 0011: PostgreSQL for projects and hashed per-project API keys

Status: accepted (v0.2 package G)

## Context
v0.1 authenticates with one static key per API and scopes all data to the literal project `"default"`. v0.2 needs real projects and per-project keys that can be created, listed, revoked and expire, without storing secrets. Users and login come later (package H); keys and projects come first because ingest and query need them, and a project does not depend on having users.

## Decision

**Driver: `github.com/jackc/pgx/v5`, pinned to v5.7.4.** pgx is the maintained, idiomatic PostgreSQL driver for Go, with a built-in pool (`pgxpool`) and typed errors (`pgconn.PgError`). `database/sql` plus `lib/pq` was not chosen because `lib/pq` is in maintenance mode. v5.7.4 is the newest release whose `go.mod` declares a Go version (1.21) that leaves our `go 1.23` line untouched. v5.7.5 and v5.7.6 declare `go 1.23.0`, which `go get` writes into our `go.mod`, and v5.8+ need Go 1.24 or later. Raising the Go version is a separate decision (roadmap follow-up). Indirect modules: `jackc/pgpassfile`, `jackc/pgservicefile`, `jackc/puddle/v2`, `golang.org/x/crypto`, `golang.org/x/sync`, `golang.org/x/text`. No ORM and no migration library.

**Hand-written migrations.** Embedded `backend/migrations/postgres/NNNN_name.sql`, applied by `internal/storage/postgres.Migrate`:
- `schema_migrations(version, name, checksum, applied_at)`; each migration runs in its own transaction together with its row, so a failing file leaves nothing behind.
- A session advisory lock (`pg_advisory_lock`) serializes concurrent runners; the second one finds nothing to do.
- The checksum is the SHA-256 of the file with line endings normalized to `\n`, so a Windows checkout (CRLF via `core.autocrlf`) and Linux CI agree. An edited applied migration, a migration in the database that the binary does not know, or a pending migration older than the newest applied one fails startup without changes.

**Schema (migration 0001).**
- `projects`: `id uuid` (gen_random_uuid), `name`, `slug` (unique, `^[a-z0-9-]{3,40}$`, also enforced in Go), `created_at`, `disabled_at`.
- `api_keys`: `id`, `project_id` (FK, `ON DELETE CASCADE`), `kind` (`ingest` | `query`), `name`, `key_prefix` (unique), `key_hash` (`bytea`, exactly 32 bytes), `created_at`, `expires_at`, `revoked_at`, `last_used_at`; index on `project_id`.

**Key format:** `lsk_<ing|qry>_<prefix>_<secret>`, 60 characters, all from `crypto/rand`:
- `prefix`: 8 characters of lowercase base32 (40 random bits), public, stored in clear for lookup;
- `secret`: 32 bytes, base64url without padding (256 random bits), never stored.

The `lsk_` and kind markers make leaked keys easy to recognize in logs and by secret scanners. A prefix collision (unique index) makes the issuer generate a new key.

**Hashing: SHA-256 of the whole key.** Slow password hashes (bcrypt, argon2) exist because passwords have little entropy and can be brute-forced offline. A 256-bit random secret cannot be guessed even with a fast hash: a leaked hash does not help an attacker, and verification stays a single cheap operation on the ingest hot path. Verification looks the key up by prefix, then compares hashes with `crypto/subtle.ConstantTimeCompare`; an unknown prefix is compared against a dummy hash as well.

**Generic rejection.** Malformed, unknown, wrong secret, wrong kind, revoked, expired and disabled-project keys all return the same `keys.ErrInvalidKey` ("invalid API key"), so callers learn nothing about which check failed. Storage failures return a different error (the APIs should answer 503, not 401). Errors and logs never contain the key; tests check the database, logs and error messages for the plaintext and the secret.

**`last_used_at`** is written at most once per minute per key: an in-process cache skips the write, and the SQL `UPDATE ... WHERE last_used_at < now() - 1 minute` covers several processes.

**Admin CLI (`cmd/admin`)** creates projects and keys. A key is printed once on stdout (the warning goes to stderr, so it can be captured) and never again. The database comes from the environment only, and non-local hosts need `--allow-remote`.

**The `default` project.** Migration 0001 inserts the project with slug `default` and the fixed id `00000000-0000-0000-0000-000000000001`. All v0.1 ClickHouse rows carry `project_id = 'default'`, which is this project's **slug**. Package H decides whether ClickHouse keeps storing slugs (then `default` already matches) or switches to UUIDs (then `'default'` is read as an alias of the fixed id, or rewritten once). Either way the existing rows belong to a real project row.

## Consequences
- Ingest and query are unchanged in this package; the static `INGEST_API_KEY` / `QUERY_API_KEY` keep working until package H wires `keys.Verifier` into them.
- PostgreSQL is now part of the stack (compose service on a named volume, CI service for the integration tests).
- Keys cannot be recovered or shown again; losing one means creating a new one and revoking the old.
- A 40-bit prefix makes collisions rare but possible; the unique index and the retry handle them.
- Changing schema means adding a migration; editing an applied one is refused at startup.
