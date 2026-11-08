# ADR 0024: Alerts backend (rules, evaluator, webhook notifications)

Status: **draft** (v0.3 package K1), waiting for the owner's approval before any code.

## Context
v0.3 has error groups (ADR 0022, 0023). Package K adds alerts: rules over the logs and groups of a project, evaluated on a schedule, with notifications to webhooks. K1 is the backend only; the UI is K2. Everything must be project-scoped, deterministic and safe to run next to the query API. Notifications leave the system, so the outbound HTTP client is the main security risk (SSRF).

## Decision (proposed)

### 1. Rule types
Every rule has:
- `name`: 1–100 characters.
- `note`: at most 500.
- `enabled`.
- `interval`: how often it is evaluated; 30 s to 1 h.
- `window`: 1 min to 24 h, and at most 7 days for group rules' look-back.
- `for`: 0 to 24 h; the condition must hold this long before it fires.
- `repeat`: 0 (never) or 5 min to 7 days; while firing, notify again after this interval.
- `channels`: 1–10 channel ids of the same project.

**Limits:** at most 50 rules and 10 channels per project.

| Type | Parameters | Condition |
|---|---|---|
| `error_rate` | `threshold_percent` 0.1–100, `min_logs` 10–1,000,000 (default 50), optional `services` (≤ 20) | (error + fatal) / all logs in the window ≥ threshold, and all logs ≥ `min_logs`. A small sample never fires. |
| `log_volume` | `op` `above` or `below`, `count` 0–10^9, optional `services`, optional `levels` | Count in the window `>` or `<` the count. `below` with `count` 1 is "no logs for N minutes" (silence). |
| `new_group` | `min_events` 1–10^6 (default 1), optional `services` | A group whose all-time first seen (`group_seen`) is inside the window and that has ≥ `min_events` events in it. It fires once per fingerprint. |
| `regression` | optional `services` | A group whose status is `resolved` with `last_seen > resolved_at`, and which has at least one event in the window. It fires once per fingerprint per resolution. |
| `new_groups_per_hour` | `max_groups` 1–10,000 | The number of groups first seen in the last hour (the window is fixed at 1 h) is > `max_groups`. This is the guard against group explosions from unique letter-only ids (ADR 0022 risk). |

Validation errors are 400 with the fixed messages of the other endpoints (for example "window: must be between 1m and 24h").

### 2. Evaluation
- **A separate binary `cmd/alerts`** (compose service `alerts`, app profile) runs the evaluator and the outbox worker. `cmd/api` only stores rules and channels. The binary reuses the `query.Service` (project scope, `readonly=1`, `max_execution_time`, row limits) and the PostgreSQL store.
- **Schedule:** every `ALERTS_TICK` (5 s), the evaluator loads the enabled rules of enabled projects that are due (`next_eval_at <= now`), then evaluates at most `ALERTS_QUERIES_PER_CYCLE` (default 200) of them.
  - A worker pool of `ALERTS_CONCURRENCY` (default 2) runs the evaluations.
  - Rules not reached wait for the next tick, oldest due first, so nothing starves.
  - Its ClickHouse queries carry `log_comment = 'alerts'`. A separate ClickHouse user with a lower `max_concurrent_queries_for_user` is recommended in v1.0 (the read-only user item); until then the low concurrency protects the query API.
- **Window end with lateness grace:** a rule evaluated at `now` looks at `[end - window, end)` with `end = now - ALERTS_LATENESS` (default 60 s, allowed 0–10 min).
  - Logs reach ClickHouse after ingest, the stream and the processor flush (normally under 2 s, more under backpressure). The grace keeps a slow pipeline from looking like silence or a rate drop.
  - Windows use **event time** (`timestamp`), the same as the explorer, so the numbers match what a user sees.
  - `inserted_at` is only used to report pipeline lag in the evaluator's `/stats`, not to decide conditions.
  - **Trade-off:** logs with timestamps older than the grace that arrive late are counted only if their window is evaluated again. Silence rules (`below`) are therefore evaluated with the grace, and the docs say so.
- **Determinism:** the evaluator takes an injected `clock` (`Now()`, timers), and each rule's evaluation is a pure function `(rule, query results, previous state, now) -> (new state, events, notifications)`. Tests drive it with a fake clock and fake query results.
- **Single writer:** at startup and before each cycle the evaluator holds a session-level PostgreSQL advisory lock (`pg_try_advisory_lock(<fixed key>)`) on a dedicated connection.
  - A second instance does not get the lock; it stays idle and retries every tick, so it is a hot standby.
  - State changes and outbox rows are written in one transaction per rule, which also checks `alert_states.version` (optimistic), so even a lock lost between check and write cannot double-fire.
- **Health:** `GET :8084/healthz`, `/readyz` (PostgreSQL and ClickHouse) and `/stats`: whether this instance holds the lock, the last cycle's duration, rules evaluated and skipped, query errors, and the outbox counts by state.

### 3. State machine and storage
Each rule has one state per dedup key.
- The **dedup key** is `rule` for the rate, volume and guard rules.
- For `new_group` it is `rule + fingerprint`, and for `regression` it is `rule + fingerprint + resolved_at`, so each new group or each regression fires once.

```
ok --cond--> pending --cond held for `for`--> firing --cond false for `clear_for`--> resolved --> ok
       \--for = 0-------------------------------^        ^-- repeat: re-notify every `repeat` while firing
pending --cond false--> ok (no notification)
```
- **Resolve hysteresis:**
  - `error_rate` resolves when the share drops below `threshold * (1 - hysteresis)`, with hysteresis 10% (configurable 0–50%).
  - `log_volume` resolves on a 10% band the other way.
  - All rules need the condition false for `clear_for` (default equal to the interval, at most 1 h) before resolving, so a flapping value does not send firing and resolved every cycle.
- **Group rules:** `new_group` and `regression` states go to `resolved` once their window has passed. A group that is new once is not new again.
- **Query error:** the state does not change. An `eval_error` event is recorded (rate limited) and shown in `/stats`; no notification is sent.
- **Edits:**
  - Changing the type, query parameters or window resets the rule's states to `ok`. A firing state is closed with a `resolved` event marked `reason: "rule changed"`, and **no** notification is sent; the docs say so.
  - Changing the name, note, channels or repeat keeps the state.
- **Disable or delete while firing:** the open states are closed with `reason: "rule disabled"` or `"rule deleted"`, and no resolved notification is sent. Pending outbox rows for that rule are marked `cancelled`. Deleting a rule keeps its events (`ON DELETE SET NULL` for the rule id, with the rule name copied into the event).
- **Channel deleted:** its pending outbox rows are cancelled, and rules keep their other channels. A rule left with no channel keeps evaluating, and its events record "no channel".
- **Restart:** state lives in PostgreSQL, so a restarted evaluator continues. A rule that was firing stays firing, with no new notification until `repeat`. `next_eval_at` is recomputed, and missed cycles are not replayed.

**PostgreSQL migration 0004:**
- **`notification_channels`:**
  - `id`, `project_id` (foreign key, cascade), `name`, `kind` (`webhook` or `slack`);
  - `url_encrypted`, the `secret_encrypted` HMAC key, `include_sample bool`;
  - `created_by`, `created_at`, `updated_at`, `disabled_at`;
  - unique `(project_id, name)`.
- **`alert_rules`:**
  - `id`, `project_id`, `name`, `type`, `params jsonb`, validated in Go with a `CHECK (jsonb_typeof(params) = 'object')`;
  - `interval_s`, `window_s`, `for_s`, `clear_for_s`, `repeat_s`, `enabled`, `note`, `version int`;
  - `next_eval_at`, `created_by`, `created_at`, `updated_at`; unique `(project_id, name)`.
- **`alert_rule_channels`:** `(rule_id, channel_id)`, cascading on both.
- **`alert_states`:** `(rule_id, dedup_key)` as the primary key, `state`, `since`, `last_value`, `last_notified_at`, `fired_at`, `version`.
- **`alert_events`:** `id`, `project_id`, `rule_id` NULL, `rule_name`, `dedup_key`, `from_state`, `to_state`, `value`, `threshold`, `window_start`, `window_end`, `fingerprint` NULL, `reason`, `at`. Every transition is recorded.
- **`alert_notifications` (the outbox):**
  - `id`, `project_id`, `event_id`, `channel_id`, `idempotency_key` (unique);
  - `payload jsonb`, which holds no secrets;
  - `status` (`pending`, `sending`, `sent`, `failed` or `cancelled`), `attempts`, `next_attempt_at`, `claimed_by`, `claimed_until`;
  - `last_error` (short, no URL), `sent_at`, `response_status`, `test bool`.
- **`audit_log`:** rows for `alert_rule.create/update/delete/enable/disable` and `channel.create/update/delete/test`, with no secrets or URLs in `detail`.
- **Retention:** events and sent or failed outbox rows older than `ALERTS_RETENTION` (30 days) are deleted by the evaluator once an hour.

### 4. Channels
- **`webhook`:** a POST of the JSON payload with these headers:
  - `Content-Type: application/json` and `User-Agent: LogSage-Alerts/1`;
  - `X-LogSage-Event` (`alert.firing`, `alert.resolved` or `alert.test`);
  - `X-LogSage-Delivery` (the idempotency key);
  - `X-LogSage-Signature: t=<unix seconds>,v1=<hex HMAC-SHA256(secret, "<t>.<body>")>`.
  - The docs give receivers verification code: reject `|now - t| > 5 min` and compare with a constant-time check.
- **`slack`:** a POST of `{"text": "..."}` to a Slack-compatible incoming-webhook URL. The text escapes `&`, `<` and `>` (Slack's rules), and templates and names are put in a code span. There is no signature, since Slack does not verify one; the URL itself is the secret.
- **Secrets:** the server generates the HMAC secret: 32 random bytes from `crypto/rand`, base64url, prefixed `lsw_`.
  - It is returned once, in the create response (and by an explicit "rotate secret" action).
  - It is stored encrypted with AES-256-GCM: a random 12-byte nonce per encryption, and the project id and channel id as additional data, so a ciphertext cannot be moved to another row.
  - The webhook URL is encrypted the same way, since it often holds a token (Slack).
- **The key:** `ALERTS_SECRET_KEY` is 32 bytes, base64. It is required by `cmd/api` and `cmd/alerts` when alerts are enabled (`ALERTS_ENABLED=true`), and startup fails without it.
  - **Rotation:** ciphertexts carry a 1-byte key id. `ALERTS_SECRET_KEY_PREVIOUS` would be accepted for decryption, and an admin command would re-encrypt. In K1 only key id 1 exists and there is no re-encryption command; the format leaves room for it.
- **Never logged:** secrets, URLs (only scheme and host are logged, never path, query or userinfo), and response bodies (only the status code and a capped, sanitized first 200 bytes go into `last_error`).

### 5. SSRF protection (`internal/alert/outbound`)
- **URL rules:**
  - Only `https`. Plain `http` is allowed only to `localhost`, `127.0.0.1` or `[::1]`, and only with `ALERTS_ALLOW_INSECURE_LOCAL=true` (off by default; for dev and tests).
  - Rejected: userinfo, fragments, empty hosts, hosts that are IP literals in non-canonical spellings (decimal `2130706433`, octal `0177.0.0.1`, hex `0x7f.1`, short forms `127.1`, mixed), and anything `net/netip` does not parse exactly.
  - Ports other than 443 (or 80 for the insecure-local case) only if listed in `ALERTS_ALLOWED_PORTS`.
  - At most 2048 characters.
- **Address rules, applied to every resolved address:**
  - **Blocked IPv4:** unspecified, loopback, RFC 1918, link-local 169.254/16 (cloud metadata), CGNAT 100.64/10, 0/8, 192.0.0/24, documentation (192.0.2/24, 198.51.100/24, 203.0.113/24), benchmarking 198.18/15, multicast, reserved 240/4 and broadcast.
  - **Blocked IPv6:** unspecified, loopback, link-local fe80::/10, unique-local fc00::/7, multicast, documentation 2001:db8::/32, the ORCHID ranges, and discard 100::/64.
  - **Embedded IPv4:** IPv4-mapped (`::ffff:a.b.c.d`), IPv4-compatible, NAT64 (`64:ff9b::/96` and `64:ff9b:1::/48`), 6to4 (`2002::/16`) and Teredo (`2001::/32`) are decoded and the embedded IPv4 is checked too. Unknown special-purpose ranges are blocked.
- **No rebinding:** a custom `DialContext` resolves the host once (through an injectable resolver), checks **all** answers, and rejects the host if any answer is blocked. It then dials the first allowed IP directly.
  - TLS keeps `ServerName` = the original host, with normal certificate verification.
  - There is no second lookup, so DNS rebinding cannot swap the address.
  - Proxies from the environment are ignored (`Proxy: nil`).
- **Request limits:**
  - No redirects (`CheckRedirect` returns an error; a 3xx is a failed attempt).
  - Timeouts: connect 3 s, TLS 5 s, response headers 5 s, total per attempt 10 s.
  - Response body read up to 4 KiB and then discarded; no cookies (no jar); no credentials or `Authorization` header.
  - HTTP/2 allowed; keep-alives off, so a connection never outlives its validation.
- **Validated twice:** when a channel is created or edited, by URL rules plus resolution, so the user gets a clear 400 ("url: host resolves to a private address"). Again on every delivery, because DNS can change.
- **Tests:**
  - A table of hostile URLs, every class and spelling above.
  - A fake resolver that answers differently on each call (rebinding).
  - A test server that redirects to 127.0.0.1, sleeps past the timeouts, or streams a huge body.
  - An IPv6 literal with a zone (`fe80::1%eth0`, rejected).
- **Remaining risks:**
  - An allowed public host that proxies internally (out of our hands).
  - Time-of-check differences between the validation at create and at send (mitigated by validating at send).
  - DNS answers for IPv6 on a host without IPv6 routes (only an availability issue).
  - A future change that forgets the client (mitigated: the outbound package is the only place that builds the transport, and a test checks that `cmd/alerts` uses it).

### 6. Payload (version 1)
```json
{"version":1,"event":"alert.firing","idempotency_key":"…","sent_at":"…","test":false,
 "project":"demo","rule":{"id":"…","name":"Checkout errors","type":"error_rate"},
 "state":"firing","value":12.5,"threshold":5,"unit":"percent",
 "window":{"from":"…","to":"…","seconds":300},"fired_at":"…","resolved_at":null,
 "group":{"fingerprint":"6fde77d96a33b9a3","template":"payment gateway timeout after <n> ms"},
 "sample":null,"url":"https://logsage.example.com/projects/demo/groups/6fde…"}
```
- `group` is only present for group rules, and `url` only when `PUBLIC_DASHBOARD_URL` is set.
- `sample` is a raw message, and only when the channel's `include_sample` is on (off by default, because logs may hold secrets). It is cut to 1 KiB.
- `idempotency_key` = `sha256(rule id, dedup key, transition, fired_at, repeat number)`, hex-encoded. A redelivery has the same key, so receivers can deduplicate.

### 7. Outbox delivery
- **At least once:**
  - The state change and its outbox rows are committed together.
  - The worker claims due rows with `SELECT … WHERE status='pending' AND next_attempt_at <= now() ORDER BY next_attempt_at LIMIT n FOR UPDATE SKIP LOCKED`, sets `status='sending'`, `claimed_by` and `claimed_until = now()+60s`, and commits.
  - It then sends outside the transaction and writes the result.
  - Rows stuck in `sending` past `claimed_until` (a crashed worker) return to `pending`.
- **Retries:**
  - **Backoff:** 10 s × 2^attempt with ±20% jitter, capped at 1 h.
  - After 8 attempts (about 40 min) the row is `failed`. 4xx responses other than 408 and 429 fail at once, because they will not change.
- **Limits:**
  - At most 30 deliveries per channel per minute (a token bucket in the worker); a row over the limit is pushed back without counting an attempt.
  - Concurrency `ALERTS_DELIVERY_CONCURRENCY` (default 4).
- **Shutdown:** stop claiming, finish or abandon in-flight sends within 10 s, and return unfinished `sending` rows to `pending`.
- **History:** visible through the API (section 8).

### 8. API (cmd/api, session auth)
Viewers read; owners and admins write; non-members get 404. POSTs need the JSON content type and an allowed origin. Errors and limits follow the other endpoints, and every write leaves an audit row.

| Endpoint | Purpose |
|---|---|
| `GET /api/projects/{slug}/alerts/rules`, `POST …/rules` | List the rules (each with its current state summary) or create one. |
| `GET/PUT/DELETE …/rules/{id}`, `POST …/rules/{id}/enable`, `…/disable` | Read, replace, delete, enable or disable a rule. |
| `GET …/alerts/channels`, `POST …/channels` | List the channels (the URL shown as scheme plus host only, never the secret) or create one (the response holds the secret once). |
| `GET/PUT/DELETE …/channels/{id}`, `POST …/channels/{id}/rotate-secret` | Read, edit, delete a channel or rotate its secret (shown once). |
| `POST …/channels/{id}/test` | Send a test notification (`"test": true`, event `alert.test`), at most 5 per channel per 10 min (429 with `Retry-After`). It goes through the outbox and returns the notification id. |
| `GET …/alerts/states` | The current states that are pending or firing. |
| `GET …/alerts/events?rule=&from=&to=&cursor=` | The transitions, newest first. |
| `GET …/alerts/notifications?channel=&status=&cursor=` | The delivery history: status, attempts, response status and the short `last_error`. |

**`docs/architecture.md` changes:**
- An "Alerts (K1)" section: rule types, evaluation, state machine, delivery and SSRF.
- The new tables in the PostgreSQL schema.
- The `cmd/alerts` component and its health endpoints.
- The new endpoints.
- The new environment variables: `ALERTS_*` and `PUBLIC_DASHBOARD_URL`.
- The `log_comment` tag on alert queries.

### 9. Risks and what K1 does not do
**Risks:**
- **SSRF:** see section 5.
- **ClickHouse load:** 50 rules per project times many projects. This is mitigated by the per-cycle budget, the low concurrency and minimum interval, and window queries that use the primary index (`project_id`, `timestamp`); a dedicated ClickHouse user is planned in v1.0.
- **Clock skew** between client timestamps and server time affects windows exactly as it affects the explorer.
- **Missed notifications:** a webhook that is down past 8 attempts misses that notification. It stays visible as `failed` in the history.

**Not in K1:**
- Email.
- Escalation policies and on-call.
- Maintenance windows and muting. They would later be a `silences` table (matching rule ids and dedup-key patterns with a time range) that the evaluator checks before writing outbox rows. Events would still be recorded, so nothing is lost.
- Anomaly detection (v0.4).
- The alerts UI (K2), and incidents (L).

## Consequences
To be completed after approval.
