# ADR 0026: Incidents backend (lifecycle, timeline, comments, opening and grouping from alerts)

Status: **draft** (v0.3 package L1, waiting for the owner's approval; no code yet)

## Context
K1 and K2 (ADR 0024, 0025) turn conditions into alert states and signed notifications. One real problem usually fires several rules at once: an error rate, new error groups, a log volume drop. Responders need one place per problem with a status, an owner, a timeline and the related error groups. L1 adds that backend. The UI comes in L2.

## Decision

### 1. Data model (migration 0007)
- **`incidents`:**
  - `id uuid`, `project_id`, `number int`: per project, unique `(project_id, number)`.
  - `title` (1–200), `severity` (`critical|high|medium|low`), `status` (`open|acknowledged|resolved`), `source` (`auto|manual`).
  - `assignee uuid NULL`: must be a member of the project, checked in the write transaction.
  - `summary` (≤ 2000), `services text[]` (≤ 20, the union of the linked alerts' services).
  - `opened_at`, `acknowledged_at`/`acknowledged_by`, `resolved_at`/`resolved_by`, `last_activity_at`, `recurrence_of uuid NULL`.
  - `all_alerts_resolved bool` (derived, kept up to date in the same transaction), `events_dropped int`, `version int` (optimistic concurrency).
- **`incident_counters (project_id pk, last int)`:** the next number is `UPDATE ... SET last = last + 1 RETURNING last` in the creating transaction. The row lock serializes creators of one project; there are no gaps and no duplicates, and a rolled-back transaction gives its number back.
- **`incident_alerts`:** `(incident_id, rule_id, state_key)` unique, plus `rule_name`, `services text[]`, `linked_at`, `closed_at NULL`, `close_reason` (`resolved|rule_disabled|rule_deleted|rule_changed`).
- **`incident_groups`:** `(incident_id, fingerprint)` unique, `source auto|manual`, `linked_at`, `linked_by NULL`. Auto links come from `new_group` and `regression` keys.
- **`incident_events`:**
  - Columns: `id bigint`, `incident_id`, `type`, `actor uuid NULL` (NULL is the system), `at`, `payload jsonb`, `body text NULL` (comments only, ≤ 2000), `count int` (default 1), `last_at`.
  - `type` is a closed list: `opened`, `alert_firing`, `alert_resolved`, `alert_closed`, `all_alerts_resolved`, `group_linked`, `group_unlinked`, `status_changed`, `severity_changed`, `assigned`, `comment`, `comment_deleted`, `reopened`.
  - **Payload:** each type has a fixed Go struct, decoded strictly. Payloads hold only ids, numbers, enums, rule names and normalized templates, never a raw log message or a sample.
  - **Comments:** the body lives in its own column. Deleting one sets `body = NULL` and adds `comment_deleted`, so the timeline keeps a "comment deleted" marker.
- **Rules** gain `open_incident bool DEFAULT false` and `incident_severity` (default `high`). Both are part of the rule body and do not change the rule version.
- **Limits:**
  - At most 1000 non-resolved incidents per project. The automatic path then links nothing, the alert is still delivered, and the evaluator counts it in `/stats`; manual create answers 409.
  - At most 1000 events per incident. After that only `events_dropped` grows, and the timeline says so.
  - Title 200, summary 2000, comment 2000; at most 50 groups per incident.
- **Retention:** resolved incidents older than `INCIDENT_RETENTION` (default 400 days) are deleted with their rows by the evaluator's daily prune.
- **Audit:** `audit_log` gets a row for every status change, severity, assignment, link and unlink, comment deletion, manual create and edit. The audit never holds comment text.

### 2. Opening and grouping (the risky part)
- **Function:** `incident.Correlate(alert, candidates, now, cfg)`. It is pure and table-tested, and returns `attach(id) | reopen(id) | create(recurrence_of?) | skip(reason)`.
- **When it runs:** inside the evaluator's `ApplyOutcome` transaction (the one that writes the state change, the `alert_events` row and the outbox rows), for each transition of a rule with `open_incident` into `firing`. The transaction first takes `pg_advisory_xact_lock` on the project's incidents.
- **An alert's services:** the rule's service filter, plus, for group keys, the services where the group was seen in the window (top 5; a small addition to the `new_group`/`regression` queries). A rule with neither is project-wide.
- **Order:**
  1. The rule and state key are already linked to a non-resolved incident: attach there.
  2. A non-resolved incident of the project that was active within `INCIDENT_MERGE_WINDOW` (30 min, by `last_activity_at`) shares at least one service: attach to the most recently active one.
  3. A project-wide rule attaches only to a non-resolved incident that this rule or another project-wide rule created (an incident whose services are empty).
  4. The rule and key were last linked to an incident resolved within `INCIDENT_REOPEN_WINDOW` (60 min): reopen it (`reopened` event, status `open`, resolution fields kept in the history).
  5. Otherwise create one. The title is `<rule name> in <services>` (or the rule name alone), cut to 200 characters; the severity is the rule's `incident_severity`. If an older incident existed for this rule and key, `recurrence_of` points to the latest resolved one, which the API shows as "recurrence of #N".
- **Alternatives:**
  - **One incident per rule:** simple and never merges unrelated problems. One outage then becomes 3–5 incidents (rate, new groups, volume), an incident storm, and nobody sees they are the same.
  - **Time window only** (everything within 30 min joins): no storms, but a mega-incident that merges unrelated problems in different services, which is wrong ownership and a wrong timeline.
  - **Chosen:** window **and** service overlap. The window bounds merging in time; services bound it in space.
- **What it can get wrong:**
  - Two problems in one shared service (for example `api`) inside 30 minutes merge. A responder can unlink groups and create a manual incident, but cannot move an alert in L1.
  - One problem whose rules filter disjoint services (`payments` versus `checkout`) splits into two incidents.
  - A project-wide rule never joins a service-scoped incident.
  - Each is visible and correctable, and none spreads across projects.
- **Flapping:** a repeat `alert_firing` or `alert_resolved` for the same incident, rule and key within 10 minutes of the previous one does not add an event. It increments `count` and `last_at` on it. Notifications are unchanged; they keep their own `repeat` logic.
- **Closing:**
  - There is no automatic resolve: a human resolves.
  - When a linked state resolves, its `incident_alerts.closed_at` is set. When every link is closed, `all_alerts_resolved` becomes true and one `all_alerts_resolved` event is written (once, until a link fires again).
  - A rule disabled or deleted while firing closes its links with `alert_closed` and the reason, in the transaction that already closes the states (`closeStates`).
  - A rule edit that resets its version closes them with `rule_changed`.
- **Idempotence and concurrency:**
  - The correlation is written in the same transaction as the outbox rows. A stale write (`ErrStale`) rolls back everything, so a second evaluator instance cannot add incidents or events.
  - The project lock serializes the automatic path against manual creates and status changes.

### 3. Suggested groups
- **What it is:** the detail response includes `suggested_groups`, computed live through the query service: the top 5 error groups by count in the incident's services (all services when empty) inside `[opened_at − 15 min, resolved_at or now]`, each with `is_new` and `regressed`.
- **Linking:** never automatic. `POST/DELETE .../groups/{fingerprint}` link and unlink them (`source manual`). Already linked groups are not suggested. A ClickHouse failure gives `suggested_groups: null` with a reason, never an error for the whole detail.
- **Toward v0.4:** this is the candidate set and the time window the root-cause work will rank (with deploys and anomalies).
- **What it must not claim:** the list is "groups active in the same services and time", not causes. The API field and the UI must not say "root cause" or "caused by".

### 4. Alert payload
- **The field:** `incident: {id, number, url}` is added as an optional field of payload version 1, so no version bump; receivers ignore unknown fields. `url` is `PUBLIC_DASHBOARD_URL/projects/{slug}/incidents/{number}` when that is set.
- **Signing and retries:** the field is decided inside `ApplyOutcome`, so the payload bytes are completed there before the outbox rows are inserted. They are stored once, signed at delivery time like everything else, and the same on every retry (the idempotency key does not change).
- **No incident:** without an incident (a rule without `open_incident`, a cap, a test send) the field is absent.

### 5. Members
- **Endpoint:** `GET /api/projects/{slug}/members` lists `user_id`, `email` and `role`, readable by every member. It is needed to pick an assignee.
- **Privacy:** viewers learn the emails of the people in projects they share. That is acceptable: they already work together and see each other's names in audit and incident history. There is no search across projects and nothing for non-members (404).
- **Changes:** adding and removing members stays in the admin CLI.

### 6. API (session auth; viewers read, owner and admin write; non-members 404; usual errors, limits, CSRF)
All under `/api/projects/{slug}/incidents`:
- **`GET` list:** `status`, `severity`, `assignee` (`me`, `none` or a user id), `service`, `from`/`to`, `q` (title contains, ≤ 200), `cursor` (`last_activity_at`, `id`), `limit`.
- **`GET summary`:** counts per status, for the nav badge.
- **`POST` create (manual):** `title`, `severity`, optional `summary`, `services` and `assignee`.
- **`GET {number}`:** the incident, linked alerts (with their current state), linked groups, `suggested_groups`, `recurrence_of`, and the first timeline page.
- **`GET {number}/events?before=`:** timeline pages, newest first.
- **`PATCH {number}`:** `title`, `severity` and `summary` with `version`; 409 on a stale version.
- **`POST {number}/acknowledge`, `/resolve`, `/reopen`:** each takes `{note?, version}`. The note becomes a comment; an invalid transition is 409.
- **`POST {number}/assign`** `{user_id}` and **`/unassign`**.
- **`POST {number}/comments`** `{body}`, and **`DELETE {number}/comments/{id}`:** the author, an owner or an admin.
- **`POST`/`DELETE {number}/groups/{fingerprint}`.**
- **Plus:** `GET /api/projects/{slug}/members`; rules accept `open_incident` and `incident_severity`.
- **`docs/architecture.md`:** an "Incidents (L1)" section (model, correlation, endpoints), the new tables, the payload field, `INCIDENT_*` settings, and the members endpoint.

### 7. Security and privacy
- Every user-authored string (title, summary, comment, note) is stored and returned as plain text. The dashboard renders it as text only, and tests store and return XSS payloads unchanged.
- Events never contain raw log messages or samples, only templates.
- In L1 a comment, acknowledge or resolve never notifies a channel.
- Every write is role-checked and audited. Project isolation is tested end to end: two projects with identical data and rules never share an incident, a counter or an event.
- Settings: `INCIDENT_MERGE_WINDOW`, `INCIDENT_REOPEN_WINDOW` and `INCIDENT_RETENTION`, read by `cmd/alerts`.

### 8. Not in L1
- Paging, escalation, on-call schedules, SLAs, a status page, postmortem templates, email, and machine-learning grouping.
- **Deploy events** come with v0.4. A deploy API (`POST /v1/deploys` with an ingest key: service, version, at) would add `deploy` timeline events to non-resolved incidents whose services and window match, and give the root-cause ranking a time anchor. The closed event list grows by one type then.

## Consequences
- An outage in one service area becomes one incident with its alerts and groups, without merging across services or projects.
- The evaluator's transaction does more work, still one transaction per rule evaluation, plus a per-project lock held only while correlating.
- Correlation mistakes stay visible and correctable (unlink, manual incidents), and the window settings tune them.
- The payload grows by one optional object; signed bodies and retries are unchanged.
