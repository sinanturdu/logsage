# ADR 0013: Users, sessions and the management API

Status: accepted (v0.2 package H2)

## Context
Projects and API keys were managed only through `cmd/admin` (ADR 0011, 0012). The dashboard (package I) needs people to log in, see their projects and manage keys. This adds users, browser sessions, project roles and an HTTP management API (`cmd/api`, port 8083), with stdlib only.

## Decision

**Server-side sessions, not JWT.** Login creates a row in `sessions` and sets a cookie with an opaque 32-byte random token; only its SHA-256 is stored. Every request looks the session up, so logout, logout-all, password change and disabling a user take effect on the next request. A JWT would need a deny list for the same guarantees, adds a signing-key secret and a library, and its claims would go stale (roles, disabled users). The cost is one indexed PostgreSQL lookup per API request, acceptable for a management API.
- Cookie: `HttpOnly`, `SameSite=Lax`, `Path=/`, no `Domain`, `Max-Age` = absolute TTL. With `COOKIE_SECURE=true` (default) it is `Secure` and named `__Host-logsage_session`; local plain http needs `COOKIE_SECURE=false` (name `logsage_session`, warning at startup).
- Idle TTL `SESSION_IDLE_TTL` (12h) slides with activity; absolute TTL `SESSION_ABSOLUTE_TTL` (7d) never extends. `last_seen_at`/`idle_expires_at` are written at most once a minute per session, so the idle limit is precise to about a minute.
- Every login creates a new session (no fixation); a session presented to the login request is revoked. Change-password needs the current password, keeps the current session and revokes all others. `admin set-password` and `disable-user` revoke all sessions; a disabled user's sessions also fail through the user join.

**Passwords: argon2id** from `golang.org/x/crypto/argon2`, the only new direct dependency. pgx already pulled x/crypto v0.31.0 indirectly; it is now direct at **v0.33.0**, the newest release whose go.mod keeps `go 1.23` (v0.34+ declare `go 1.23.0` or newer, which would change our go directive). Later releases fix ssh and other packages, not argon2. (`go get` also moved x/sync to v0.11.0 and x/text to v0.22.0, both indirect.)
- Parameters (OWASP minimum is m=19 MiB, t=2, p=1), measured on the development laptop (one hash):

  | m | t | p | time |
  |---|---|---|------|
  | 19 MiB | 2 | 1 | 31 ms |
  | 46 MiB | 1 | 1 | 43 ms |
  | 64 MiB | 2 | 2 | 72 ms |
  | **64 MiB** | **3** | **2** | **96 ms (default)** |
  | 64 MiB | 4 | 2 | 132 ms |

  Defaults: `ARGON2_MEMORY_KIB=65536`, `ARGON2_ITERATIONS=3`, `ARGON2_PARALLELISM=2`; config rejects memory below 19 MiB.
- PHC strings (`$argon2id$v=19$m=..,t=..,p=..$salt$hash`), 16-byte salt from `crypto/rand`, 32-byte key, constant-time comparison. A stored hash with other parameters is re-hashed after a successful login (compare-and-set, so a concurrent password change wins).
- `ARGON2_MAX_CONCURRENT` (4) bounds hashes in flight, so a login flood costs at most 4 × 64 MiB; waiters give up with the request context.
- Unknown emails verify against a dummy hash, so timing does not reveal registered emails; wrong password, unknown email and disabled user all return the same `401`.
- Policy: 12–128 characters, not equal to the email, no composition rules (NIST SP 800-63B). Passwords come from JSON bodies, `LOGSAGE_PASSWORD` or stdin, never flags; they and their hashes are never logged or returned.

**CSRF.** Every state-changing request (anything but GET/HEAD/OPTIONS) must have `Content-Type: application/json` and an `Origin` (else `Referer`) that is listed in `ALLOWED_ORIGINS` or names the request host; otherwise `403`. Browsers cannot send JSON cross-origin without a CORS preflight, and `SameSite=Lax` already keeps the cookie off cross-site POSTs; the origin check covers old browsers and same-site siblings.

**CORS** only for the explicit `ALLOWED_ORIGINS` list (default empty, `*` rejected at startup): the exact origin is echoed with `Access-Control-Allow-Credentials: true` and `Vary: Origin`; preflights allow GET/POST and `Content-Type`.

**Login throttling** (`LOGIN_*`): an in-memory limiter keyed by client IP and by SHA-256 of the lower-cased email. `LOGIN_MAX_FAILURES` (5) free failures, then each failure blocks the key for `LOGIN_DELAY_BASE` (1s) doubling up to `LOGIN_DELAY_MAX` (15m); blocked requests get `429` with `Retry-After`, even with the right password. Failures are forgotten after a quiet period of `LOGIN_DELAY_MAX`. A success resets the email key, not the IP key. The table is an LRU of `LOGIN_LIMITER_SIZE` (10,000) keys. Limits are **per instance** (N replicas allow N times the attempts) and the client IP is `RemoteAddr`: `X-Forwarded-For` is not trusted, so behind a proxy all clients share the proxy's IP key until trusted-proxy support exists. Change-password uses the same limiter on the user's email key.

**Roles** (`project_members`): `owner` (everything, including disable), `admin` (keys), `viewer` (read the project and key list). The creator of a project becomes its owner. A non-member gets `404` for every project endpoint, never `403`, so project slugs cannot be probed; members lacking a role right get `403`. The default project has no members; an operator assigns one with `admin add-member --project default --email E --role owner`.

**Audit log** (`audit_log`): login success/failure, logout, logout-all, password change (and failed attempts), registration, project create/disable, key create/revoke, and the admin CLI's user and member commands. Failed logins store `user_id` when the account exists and otherwise only a truncated SHA-256 of the typed email (people type passwords into the email field). No secret ever goes into `detail`.

**Registration** (`POST /api/auth/register`) is off by default (`REGISTRATION_ENABLED=false`, `404`). When on, it reveals whether an email is registered (`409`); conflicts count against the IP limiter.

**API shape.** JSON only, 16 KiB body limit, unknown fields rejected, `Cache-Control: no-store` and `X-Content-Type-Options: nosniff` on every response, generic error bodies (`{"error":"unauthorized"}`); `400` messages are fixed strings from our validation. PostgreSQL errors are logged and answered `503`. Keys are issued through the same `Store.IssueKey` as the admin CLI and returned once.

## Not done
- Email verification, password reset, MFA, account lockout notifications.
- Per-project rate limits and quotas.
- Trusted-proxy handling for client IPs; a shared (Redis) login limiter across instances.
- Session listing per user and an audit log API; cleanup of expired session rows (indexed by `absolute_expires_at` for a later job).
- Key revocation is still subject to the ingest/query `AUTH_CACHE_TTL` (ADR 0012).

## Consequences
- The management API depends on PostgreSQL for every request; `/readyz` reports it.
- Login costs about 100 ms of CPU and 64 MiB per attempt; the semaphore and limiter bound the damage of a flood, per instance.
- Tests cover the role matrix, CSRF/CORS, cookie attributes, limiter, expiry with a fake clock, fixation, isolation between users, and that passwords, tokens and keys never appear in any table or log.
