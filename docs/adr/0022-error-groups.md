# ADR 0022: Error groups (fingerprints, groups API, group status)

Status: **draft** (v0.3 package J1), waiting for the owner's approval before any code.

## Context
v0.3 starts with error groups: many log lines with the same cause should be read as one problem, with counts, first and last seen, and a status the team sets.
- The `logs` table already has a `fingerprint UInt64` column, written as `0` today (processor `entry.go`). The architecture note ("hash the normalized template plus service plus level") was never implemented.
- The dashboard reads through one query service (ADR 0014) and the processor writes in batches (ADR 0005).

This ADR fixes the fingerprint rule, where groups and their status live, and the API. The groups UI (J2), alerts (K) and incidents (L) build on it.

## Decision (proposed)

### 1. Fingerprint key
`fingerprint = first 8 bytes (big-endian) of SHA-256(FingerprintVersion ‖ service ‖ keyTemplate ‖ errorType)`, fields separated by `0x1F`. A result of `0` becomes `1`, because `0` means "not grouped".
- **`template`:** the normalized first line of the message (section 2), after the line is cut to 512 bytes.
- **`keyTemplate`:** the template with every `<n>`, `<id>` and `<uuid>` collapsed to `<*>`.
  - This makes the key independent of how a particular id happened to look. An all-digit hex id becomes `<n>` and a mixed one becomes `<id>`, but both key as `<*>`.
  - Without the collapse, the demo data split into 460 templates instead of 37.
  - The readable `template` keeps the distinct placeholders.
- **`errorType`:** the value of the attribute `error.type`, or else `exception.type`, trimmed and cut to 128 bytes. Empty when absent.
- **`FingerprintVersion`:** a constant (`"1"`). Changing any rule means bumping it, and the golden tests fail until the expected fingerprints are updated on purpose.
- **Level is not in the key.** An escalation of the same message from warn to error stays one group, and the group lists the levels it has seen.

**Which rows get a fingerprint (owner's choice):**
- **Proposed:** only `warn`, `error` and `fatal` rows. `debug` and `info` keep `0`.
  - Normalizing costs about 42 µs per message in the prototype (one core handles about 24k messages/s). The processor runs at 20–27k events/s, so normalizing everything would cost about one core.
  - Warn and above are about 20% of the demo data, which brings it to about 8 µs per event on average.
  - Groups of `info` lines are not "error groups".
- **The alternative:** fingerprint all levels, at about one core.

**Trade-offs:**
- **Service in the key:** the same error in two services is two groups. That is right for ownership, wrong for a shared library bug. The detail view can show "same template in other services" later.
- **First line only:** multi-line stack traces group by their first line.
- **64 bits:** a collision is negligible at our scale (about 1 in 10^10 for 10^5 groups per project). SHA-256 is in the standard library, and FNV would do too. Truncated SHA-256 is chosen for a good spread with no dependency.

### 2. Normalization rules
- **Preparation:** cut to the first line, then to 512 bytes on a rune boundary. Invalid UTF-8 becomes U+FFFD, and control characters become spaces.
- **Rules,** in this order, all Go `regexp` (RE2, linear time):
  1. HTTP status codes are protected: a 4xx or 5xx after `returned`, `status`, `code` or `HTTP/x` (but not when a noun follows: "returned 503 results" is a count), at the start of the line before a capitalized reason phrase, and the status in a `METHOD path STATUS` request line.
  2. Quoted strings: one that looks like an identifier (no spaces, at most 64 characters, such as `"users_email_key"`, `'user_id'` or `"String.length()"`) is kept, because it is usually a column, key or method name. Anything else becomes `"<str>"`, such as user input with spaces. A quoted request line is kept so its path gets normalized.
  3. ISO timestamps become `<ts>`, dates `<date>` and clock times `<time>`.
  4. UUIDs become `<uuid>`, and emails `<email>`.
  5. URLs keep the scheme, host and path, and lose the query and fragment.
  6. Kubernetes pod names (`api-7d9f8b6c4-x2kqp`) become `api-<id>`.
  7. IPv4 (with an optional port) and IPv6 become `<ip>`.
  8. In paths, segments with digits become `<n>` or `<id>` and keep their file extension (`/var/data/exports/<n>.json`). `v1` and `v2` are kept.
  9. Base64-like tokens of 24 or more characters with letters and digits become `<token>`.
  10. Hex: `0x…` and runs of 8 or more hex characters become `<id>`, and so do 6–7 character hex runs that mix letters and digits.
  11. Numbers with units become `<n>ms`, `<n> ms`, `<n>us`, `<n>kb`; percentages become `<n>%`.
  12. Prefixed ids: `ord_7Q2K9M` becomes `ord_<id>`, `db-3` becomes `db-<n>` and `worker-12` becomes `worker-<n>`.
  13. Mixed words where a digit is followed by a letter (`a1b2c3`, at least 5 characters) become `<id>`. Words that only end in digits stay (`psycopg2`, `sha256`, `local1`), plus a small keep-list (`utf8`, `ipv6`, `x86_64`, `int64`, and so on).
  14. Plain numbers become `<n>`.
  15. Whitespace is collapsed and trimmed, and the protected status codes are restored.
- **Invariants,** each enforced by tests:
  - **Deterministic:** the same input always gives the same output.
  - **Idempotent:** `normalize(normalize(x)) == normalize(x)`. This held for the whole corpus.
  - **Never panics:** on empty, huge, binary or invalid-UTF-8 input.
  - **Bounded time:** the input is cut to 512 bytes first. The worst case measured was a 60 KB message full of tokens at about 0.37 ms; the typical case is about 42 µs.

### 3. Evaluation
- **Corpus:** 76 messages: every loadgen template, plus Go, Java, Python, HTTP, SQL, timeout, OOM, auth, validation, disk, rate-limit and IPv6 lines, junk and an empty message. The full table is at the end; it becomes the golden test.
- **Demo data:** `loadgen seed`, 69,346 events, 51,255 distinct raw messages.
  - **Templates:** 37 normalized key templates for 18 generator templates.
  - **Deliberate splits:** the panic splits into 3 handler names and "loaded config" into 3 file names. With those, the generator's 18 templates are 22 meaningful ones.
  - **One-off splits:** the other 15 templates are singletons from ids that happen to be all letters (`ord_bcfdae`, `client facade`), 15 events in all (0.02%).
  - **Groups:** 127 groups (service × template); 112 without the singletons. 39 groups contain error or fatal rows.
  - **The incident** forms 4 large groups: payments timeout, checkout 504, api 502 and payments retry.
- **Over-merged:**
  - Python `Traceback (most recent call last):`: every traceback in a service becomes one group. Fix: rely on `exception.type` (part of the key), or, if you allow it, take the last line when the first line is exactly this header.
  - Access logs with 2xx statuses: the status becomes `<n>`, so 200 and 304 merge.
  - `exit code 137` and `exit code 1` merge.
- **Under-merged:**
  - Ids that are all letters (`facade`, `ord_bcfdae`), at 0.02% of the demo.
  - Quoted values that look like identifiers: `ValueError: … 'abc'` and `'xyz'` stay separate, and so do partition tables (`"orders_2026_11"`).
  - An unquoted query string in a path (`/v1/orders?page=2` becomes `/v1/<id>`). I will strip `?…` before the path rule.
  - All of these are accepted for v1; the golden tests pin the behavior.

### 4. Storage
- **No backfill.** Existing rows keep `fingerprint = 0` and are shown as "not grouped". That covers `debug` and `info` rows and anything ingested before v0.3.
- **New column `template String`** (ClickHouse migration 0005): the readable template, written with the fingerprint, and empty when it is 0.
  - Measured on 100k rows: 17 KiB compressed, against 2.2 MiB for `message`, about 0.7%.
  - It gives group titles without recomputation and allows `q` to filter on the template.
- **Counts:** they come from the base table with `FINAL`, the same semantics as `/logs`. Measured: 20,621 raw rows against 20,000 real events with 3% redelivered duplicates, so without `FINAL` counts inflate.
- **Materialized view `group_seen`** (AggregatingMergeTree, `min`/`max` of `timestamp` per `(project_id, fingerprint)`) for all-time first and last seen.
  - `min` and `max` are not affected by duplicates; counts never come from the view.
  - Measured on 100k rows: the view reads 200 rows (4 ms), against 83k rows (13 ms) for the base table with `FINAL`.
  - At 100k rows the base table would do. The view keeps `is_new` and `regressed` independent of how much log data is retained, so I propose it now, while there is nothing to backfill.
  - It gets a TTL on `last_seen` equal to the log retention.
- **Fingerprint filter:** a `bloom_filter` skip index on `fingerprint`, to be measured during the implementation.
- **The other measurements** (100k rows, 24 h window): the groups query with `FINAL` took 18 ms (14 ms for 1 h), and a 24-bucket sparkline for 50 groups took 16 ms.
- **PostgreSQL `error_groups`** (migration 0003): `project_id`, `fingerprint BIGINT` (the uint64 bits), `status` (`open`, `ignored` or `resolved`), `resolved_at`, `note` (at most 500 characters), `updated_by`, `updated_at`, with the primary key `(project_id, fingerprint)`.
  - A row is created on the first status change; no row means `open`.
  - Status filtering reads the project's rows (small) and passes fingerprint lists to ClickHouse as parameters.

### 5. API
All endpoints use session auth and need the viewer role or higher; non-members get a 404. They go through the shared query service with the same limits and error bodies as `/logs`, the project scope comes from the one builder, and the per-user concurrent-query cap applies.
- **`GET /api/projects/{slug}/groups`**
  - **Parameters:** `from` and `to` (required), `level`, `service`, `q` (on the template), `status`, `sort` (`count`, `last_seen` or `first_seen`, newest or largest first), `limit` (at most 100), `cursor` (opaque: an offset plus a hash of the query, at most 1,000 groups deep).
  - **Each item:**
    - `fingerprint` (16-hex) and `title` (the template);
    - `sample` (the latest message) and `error_type`;
    - `levels` and `services`;
    - `count` (in the window), `first_seen` and `last_seen` (all-time, from the view);
    - `sparkline` (24 buckets) and `status`;
    - `is_new` (first seen inside the window) and `regressed` (resolved, and seen after `resolved_at`).
- **`GET /api/projects/{slug}/groups/{fingerprint}`**
  - Counts by service and by level, a time series (with the `/histogram` bucket rules), the latest 5 sample logs, `first_seen`, `last_seen`, `status`, `note`, `resolved_at` and `updated_by`.
  - An unknown or wrongly formatted fingerprint gets a 404 or a 400.
- **`POST /api/projects/{slug}/groups/{fingerprint}/status`**
  - Owner and admin only; viewers get a 403.
  - Body: `{"status":"open|ignored|resolved","note":"…"}`.
  - It writes an `audit_log` row with the action `group.status`, upserts `error_groups`, and sets `resolved_at = now` when the status becomes resolved.
- **Additive changes:**
  - An optional `fingerprint` (16 hex) filter on `/logs`, `/histogram`, `/facets` and `/tail`, and on `/v1/logs`.
  - A `fingerprint` field on log items: a hex string, empty when not grouped.
- **`docs/architecture.md`:**
  - Replace the Fingerprinting section with these rules.
  - Add the `template` column, the view and `error_groups` to the schemas.
  - Add the three endpoints and the new filter and field.
  - Add the processor step "fingerprint at parse time, before the flush".

### 6. Risks and what this package does not do
**Risks:**
- **Rule changes regroup everything:** bumping the version splits old and new rows. This is mitigated by the golden tests and the version constant; a migration path for statuses (old to new fingerprint) is out of scope.
- **Processor CPU:** about 8 µs per event with warn and above. This is measured in the regression check of this package.
- **Hot groups dominate:** a hot group can dominate the sort by count; `last_seen` and `is_new` help.
- **Per-instance limits:** the same as the other read endpoints.

**Not in this package:** ML or clustering by similarity, stack trace parsing beyond the first line, merging or splitting groups by hand, group assignment and comments, notifications, the groups UI (J2), and backfilling old rows.

## Appendix: golden corpus (prototype output, 74 non-empty cases plus an empty one)

| Message (first line) | Template |
|---|---|
| `GET /api/v1/orders/482913 completed in 47ms` | `GET /api/v1/orders/<n> completed in <n>ms` |
| `user 31337 logged in from 10.12.200.7` | `user <n> logged in from <ip>` |
| `order 3fa9c0d1 created with 3 items, total 128.07 EUR` | `order <id> created with <n> items, total <n> EUR` |
| `order beefcafe created with 3 items, total 128.07 EUR` | `order <id> created with <n> items, total <n> EUR` |
| `search query returned 1520 results in 41ms` | `search query returned <n> results in <n>ms` |
| `email 9b2e44c1a7f0 queued for user 1290` | `email <id> queued for user <n>` |
| `cache miss for key product:55123` | `cache miss for key product:<n>` |
| `loaded config from /etc/app/flags.yaml in 312us` | `loaded config from /etc/app/flags.yaml in <n>us` |
| `slow query on table orders took 2921ms (threshold 500ms)` | `slow query on table orders took <n>ms (threshold <n>ms)` |
| `retrying request to payment gateway, attempt 2 of 3` | `retrying request to payment gateway, attempt <n> of <n>` |
| `rate limit at 93% for client 8a1f0c` | `rate limit at <n>% for client <id>` |
| `rate limit at 93% for client facade` | `rate limit at <n>% for client facade` |
| `payment 7c1e0b9a42 declined: gateway timeout after 7086ms` | `payment <id> declined: gateway timeout after <n>ms` |
| `failed to write /var/data/exports/5078.json: disk quota exceeded` | `failed to write /var/data/exports/<n>.json: disk quota exceeded` |
| `connection to db-3.internal:5432 refused` | `connection to db-<n>.internal:<n> refused` |
| `panic: runtime error: nil pointer dereference in handler CreateOrder` | `panic: runtime error: nil pointer dereference in handler CreateOrder` |
| `panic: runtime error: nil pointer dereference in handler Refund` | `panic: runtime error: nil pointer dereference in handler Refund` |
| `upstream bank-api returned 503 for transaction 0f3c9a77b2` | `upstream bank-api returned 503 for transaction <id>` |
| `circuit breaker open for bank-api, 37 requests rejected` | `circuit breaker open for bank-api, <n> requests rejected` |
| `payment gateway timeout after 5000 ms (psp-eu-2)` | `payment gateway timeout after <n> ms (psp-eu-<n>)` |
| `retrying charge for ord_7Q2K9M, attempt 3 of 3` | `retrying charge for ord_<id>, attempt <n> of <n>` |
| `charge failed: upstream payments returned 504` | `charge failed: upstream payments returned 504` |
| `POST /v1/checkout 502 in 5151 ms` | `POST /v1/checkout 502 in <n> ms` |
| `panic: runtime error: index out of range [5] with length 3` | `panic: runtime error: index out of range [<n>] with length <n>` |
| `panic: runtime error: invalid memory address or nil pointer dereference` | `panic: runtime error: invalid memory address or nil pointer dereference` |
| `http: panic serving 10.0.3.14:53412: runtime error: slice bounds out of range [:12] with capacity 8` | `http: panic serving <ip>: runtime error: slice bounds out of range [:<n>] with capacity <n>` |
| `context deadline exceeded (Client.Timeout exceeded while awaiting headers)` | `context deadline exceeded (Client.Timeout exceeded while awaiting headers)` |
| `dial tcp 10.0.4.21:6379: connect: connection refused` | `dial tcp <ip>: connect: connection refused` |
| `read tcp 172.17.0.3:41234->172.17.0.5:5432: i/o timeout` | `read tcp <ip>-><ip>: i/o timeout` |
| `pq: duplicate key value violates unique constraint "users_email_key"` | `pq: duplicate key value violates unique constraint "users_email_key"` |
| `json: cannot unmarshal string into Go struct field Order.items.qty of type int` | `json: cannot unmarshal string into Go struct field Order.items.qty of type int` |
| `open /tmp/upload-3829121.part: no such file or directory` | `open /tmp/<id>.part: no such file or directory` |
| `java.lang.NullPointerException: Cannot invoke "String.length()" because "<local1>" is null` | `java.lang.NullPointerException: Cannot invoke "String.length()" because "<local1>" is null` |
| `java.lang.IllegalStateException: Connection pool exhausted (max=50, active=50, idle=0)` | `java.lang.IllegalStateException: Connection pool exhausted (max=<n>, active=<n>, idle=<n>)` |
| `org.postgresql.util.PSQLException: ERROR: deadlock detected` | `org.postgresql.util.PSQLException: ERROR: deadlock detected` |
| `java.net.SocketTimeoutException: Read timed out after 30000 ms` | `java.net.SocketTimeoutException: Read timed out after <n> ms` |
| `Exception in thread "main" java.lang.OutOfMemoryError: Java heap space` | `Exception in thread "main" java.lang.OutOfMemoryError: Java heap space` |
| `Caused by: java.sql.SQLTransientConnectionException: HikariPool-1 - Connection is not available, request timed` | `Caused by: java.sql.SQLTransientConnectionException: HikariPool-<n> - Connection is not available, request timed out after <n>ms.` |
| `Traceback (most recent call last):` | `Traceback (most recent call last):` |
| `KeyError: 'user_id'` | `KeyError: 'user_id'` |
| `ValueError: invalid literal for int() with base 10: 'abc'` | `ValueError: invalid literal for int() with base <n>: 'abc'` |
| `ValueError: invalid literal for int() with base 10: 'xyz'` | `ValueError: invalid literal for int() with base <n>: 'xyz'` |
| `ConnectionResetError: [Errno 104] Connection reset by peer` | `ConnectionResetError: [Errno <n>] Connection reset by peer` |
| `redis.exceptions.TimeoutError: Timeout reading from socket` | `redis.exceptions.TimeoutError: Timeout reading from socket` |
| `psycopg2.OperationalError: could not connect to server: Connection refused` | `psycopg2.OperationalError: could not connect to server: Connection refused` |
| `GET /api/v2/users/8f14e45f-ceea-467a-9575-1f5b3c1e2a9b 404 in 3 ms` | `GET /api/v2/users/<uuid> 404 in <n> ms` |
| `POST /api/v1/payments 500 in 120ms` | `POST /api/v1/payments 500 in <n>ms` |
| `POST /api/v1/payments 503 in 98ms` | `POST /api/v1/payments 503 in <n>ms` |
| `10.1.2.3 - - [07/Nov/2026:10:15:30 +0000] "GET /index.html HTTP/1.1" 200 512` | `<ip> - - [<n>/Nov/<n>:<time> +<n>] "GET /index.html HTTP/<id>" <n> <n>` |
| `upstream timed out (110: Connection timed out) while reading response header from upstream, client: 192.168.1.` | `upstream timed out (<n>: Connection timed out) while reading response header from upstream, client: <ip>, server: api.example.com, request: "GET /v1/<id> HTTP/<id>"` |
| `request to https://api.stripe.com/v1/charges?amount=5000&currency=eur failed with status 429` | `request to https://api.stripe.com/v1/charges failed with status 429` |
| `request to https://api.stripe.com/v1/refunds/re_3NqX2b failed with status 429` | `request to https://api.stripe.com/v1/refunds/<id> failed with status 429` |
| `ERROR: relation "orders_2026_11" does not exist at character 15` | `ERROR: relation "orders_2026_11" does not exist at character <n>` |
| `too many connections for role "app"` | `too many connections for role "app"` |
| `could not obtain lock on row in relation "accounts"` | `could not obtain lock on row in relation "accounts"` |
| `Lost connection to MySQL server at 'reading initial communication packet', system error: 0` | `Lost connection to MySQL server at '<str>', system error: <n>` |
| `timeout after 30s waiting for lock /var/lock/job-17.lock` | `timeout after <n>s waiting for lock /var/lock/<id>.lock` |
| `Out of memory: Killed process 18213 (java) total-vm:8123456kB, anon-rss:4012348kB` | `Out of memory: Killed process <n> (java) total-vm:<n>kb, anon-rss:<n>kb` |
| `OOMKilled: container api-7d9f8b6c4-x2kqp exceeded memory limit 512Mi` | `OOMKilled: container api-<id> exceeded memory limit <id>` |
| `disk usage at 97% on /dev/sda1` | `disk usage at <n>% on /dev/<id>` |
| `write /data/wal/000000010000000A00000042: no space left on device` | `write /data/wal/<id>: no space left on device` |
| `invalid token for user alice@example.com: signature mismatch` | `invalid token for user <email>: signature mismatch` |
| `login failed for user bob@example.com from 203.0.113.9: wrong password` | `login failed for user <email> from <ip>: wrong password` |
| `permission denied: role viewer cannot revoke key lsk_ing_hyqay6m3` | `permission denied: role viewer cannot revoke key lsk_ing_<id>` |
| `validation failed: field "email" must be a valid address` | `validation failed: field "email" must be a valid address` |
| `validation failed: field "amount" must be positive, got -12.50` | `validation failed: field "amount" must be positive, got -<n>` |
| `JWT expired at 2026-11-07T10:15:30Z, now 2026-11-07T10:20:01Z` | `JWT expired at <ts>, now <ts>` |
| `rate limited: 1200 requests in 60s exceeds quota of 1000 for api key 4f9c2e1a8b7d6c5e` | `rate limited: <n> requests in <n>s exceeds quota of <n> for api key <id>` |
| `429 Too Many Requests: retry after 17 seconds` | `429 Too Many Requests: retry after <n> seconds` |
| `connection from [2001:db8::8a2e:370:7334]:443 reset` | `connection from [<ip>]:<n> reset` |
| `cache key session:7f3c2a91 evicted after 3600s` | `cache key session:<id> evicted after <n>s` |
| `job 7f3c2a91-2b4d-4c1e-9a77-0e5f6d3b2a19 failed after 3 retries` | `job <uuid> failed after <n> retries` |
| `worker-12 crashed with exit code 137` | `worker-<n> crashed with exit code <n>` |
| ` �� binary   junk` | `� binary junk` |
