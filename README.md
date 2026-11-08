# LogSage

[![CI](https://github.com/sinanturdu/logsage/actions/workflows/ci.yml/badge.svg)](https://github.com/sinanturdu/logsage/actions/workflows/ci.yml)

LogSage is a log and incident analysis platform. Applications send their logs to an ingest API; LogSage stores them in ClickHouse and lets you search them. Later milestones group similar errors, detect anomalies and suggest likely root causes when an incident opens ("mini Datadog + AI").

> **Status: v0.2 (accounts and dashboard).** Ingest, storage and search, plus accounts, projects with roles, hashed per-project API keys and a web dashboard (log explorer, timeline, row detail with Same trace, live tail, API keys, command palette) served by the API. Error groups, alerting and AI features are planned: see [docs/roadmap.md](docs/roadmap.md) and the [v0.2.0 release notes](docs/releases/v0.2.0.md).

## Architecture

```mermaid
flowchart LR
  A[Applications] -->|POST /v1/ingest + API key| B[Ingest API<br/>cmd/ingest]
  B -->|XADD| C[(Redis Stream)]
  C -->|consumer group| D[Processor<br/>cmd/processor]
  D -->|batched INSERT, ack after write| F[(ClickHouse)]
  Q[Query API<br/>cmd/query] -->|parameterized SELECT ... FINAL| F
  U[Clients] -->|GET /v1/logs + read key| Q
  C -. planned .-> E[Alert engine]
  F -. planned .-> J[AI service]
  Q -. planned .-> I[Dashboard]
```

- **Ingest** validates batches, authenticates with a static key (constant-time comparison) and publishes to a Redis Stream.
- **Processor** reads through a consumer group, writes batches to ClickHouse, acks only after the write succeeds, dead-letters unusable entries and deduplicates redeliveries (`event_id` + `ReplacingMergeTree`).
- **Query** is read-only search with a separate key. Every query is scoped to a project, values are sent as server-side parameters, and pagination uses a keyset cursor.

Details: [docs/architecture.md](docs/architecture.md) and the decision records in [docs/adr](docs/adr).

## Quick start

On Windows (Docker Desktop; Go 1.23+ only for the demo data), from PowerShell:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev.ps1 up
```

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev.ps1 seed
```

Open http://localhost:8083 and sign in with the dev login that `up` printed (also in `.dev\login.txt`). `scripts\dev.ps1 down` stops everything and deletes the data. More in [Run the dashboard locally](#run-the-dashboard-locally).

### With Docker Compose (any OS)

Requirements: Docker with Compose v2.

```bash
cp .env.example .env    # then replace the change-me keys and password in .env
```

```bash
docker compose --env-file .env -f infra/docker-compose.yml -f infra/docker-compose.app.yml --profile app up -d --build
```

```bash
cd backend && LOADGEN_INGEST_KEY=<an ingest key> go run ./cmd/loadgen demo --duration 30s
```

This starts Redis, ClickHouse, PostgreSQL and the LogSage services. Their ports are bound to 127.0.0.1 only: ingest on `:8080`, processor stats on `:8081`, query on `:8082`, and the management API with the dashboard on `:8083` (set `COOKIE_SECURE=false` in `.env` to log in over plain http). Compose refuses to start the app profile if the ClickHouse or PostgreSQL password is missing from `.env`. Create a user, a project and an ingest key with the admin CLI (see [Development](#development)), or in the dashboard's API keys screen. The static `INGEST_API_KEY` works only with `ALLOW_LEGACY_STATIC_KEYS=true` (deprecated, removed in v0.3). The third command sends a realistic stream of events for 30 seconds; it needs Go 1.23+, or send single events with curl as below.

To stop: `docker compose --env-file .env -f infra/docker-compose.yml -f infra/docker-compose.app.yml --profile app down` (add `-v` to delete the stored data).

## Run the dashboard locally

Requirements: Docker Desktop, Go 1.23+ (for the demo data) and Node 24 (`frontend/.nvmrc`). On Windows, one script does everything from PowerShell, with no admin rights and no Git Bash:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev.ps1 up
```

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev.ps1 seed
```

Then open http://localhost:8083/projects/demo/logs (the api serves the built dashboard). For hot-reload frontend work, also run `scripts\dev.ps1 web` and use http://localhost:5173; it proxies `/api` and `/config.json` to the api. Click an error row from the incident to see its full message, attributes and the Same trace cascade across `payments`, `checkout` and `api`; brush the timeline and use "Zoom to selection" for finer buckets. Press `l` (or the Live button) to follow new logs while `go run ./cmd/loadgen demo --rate 2` sends some, and use API keys in the header to create an ingest key; it is shown once. Open Error groups in the header: the incident's groups lead the list with the New badge; open one to resolve it, then send more incident logs (`go run ./cmd/loadgen demo --incident`) to see it flagged as Regressed.

Keyboard shortcuts (press `?` in the dashboard for the same list):

| Keys | Where | What |
|---|---|---|
| `Ctrl K` / `⌘ K` | anywhere, also while typing | open the command palette |
| `?` | anywhere | show keyboard shortcuts |
| `/` | log explorer | focus the search box |
| `j` / `k` | log explorer | select the next / previous log |
| `Enter` / `Space` | log explorer | expand or collapse the selected log |
| `Escape` | log explorer | close the detail, then clear the selection; closes menus and dialogs |
| `t` | log explorer | filter by the selected log's trace |
| `l` | live tail | turn the live tail on or off |
| `r` | live tail | reconnect now while it waits to retry |
| `/` | error groups | focus the search box |
| `j` / `k` | error groups | select the next / previous group |
| `Enter` | error groups | open the selected group |

- `up` creates `.env` if it is missing (random secrets, `COOKIE_SECURE=false`) and starts the stack.
- It then runs the migrations and creates a dev user, the project `demo` and an ingest key, unless they already exist.
- The dev login is printed once and kept in `.dev/login.txt`. `.dev/` is local-only and git-ignored, like `.env`.
- `seed` sends about 70,000 events: an hour of history, plus an incident in the last 15 minutes in which payment gateway timeouts in `payments` cascade into `checkout` and `api` errors.
- `web` runs the dashboard dev server, which proxies `/api` to the API on `:8083`.
- `status` shows the services and where the dev login is.
- `down` stops everything and deletes the data, then asks before removing `.dev/`.

Every command is safe to run again.

On macOS or Linux, or from Git Bash, run the same steps by hand:
1. Copy `.env.example` to `.env`, replace the `change-me` values and set `COOKIE_SECURE=false`.
2. Start the stack with `docker compose --env-file .env -f infra/docker-compose.yml -f infra/docker-compose.app.yml --profile app up -d --build`.
3. Create the user, project and key with the admin CLI as in [AGENTS.md](AGENTS.md). Pass the password through stdin, and prefix `docker compose exec` with `MSYS_NO_PATHCONV=1` in Git Bash.
4. Seed with `cd backend && LOADGEN_INGEST_KEY=<key> go run ./cmd/loadgen seed`.
5. Start the dashboard with `cd frontend && npm ci && npm run dev`.

## API examples

Send events (bash, curl):

```bash
curl -X POST http://127.0.0.1:8080/v1/ingest \
  -H "X-API-Key: $INGEST_API_KEY" -H "Content-Type: application/json" \
  -d '{"events":[{"timestamp":"2026-11-06T10:15:30Z","level":"error","service":"checkout","message":"payment gateway timeout","trace_id":"abc123","attributes":{"env":"prod"}}]}'
# {"accepted":1}
```

Search (bash, curl). The query key is different from the ingest key:

```bash
curl -H "X-API-Key: $QUERY_API_KEY" \
  "http://127.0.0.1:8082/v1/logs?from=2026-11-06T00:00:00Z&to=2026-11-07T00:00:00Z&level=error,fatal&service=checkout&q=timeout&limit=50"
# {"events":[{"event_id":"...","timestamp":"2026-11-06T10:15:30.000Z","level":"error",...}],"next_cursor":null,"took_ms":7}
```

Windows PowerShell (`curl.exe`, body from a file to avoid quoting problems, and the URL in quotes because it contains `&`):

```powershell
curl.exe -X POST http://127.0.0.1:8080/v1/ingest -H "X-API-Key: $env:INGEST_API_KEY" -H "Content-Type: application/json" --data-binary "@event.json"
curl.exe -H "X-API-Key: $env:QUERY_API_KEY" "http://127.0.0.1:8082/v1/logs?from=2026-11-06T00:00:00Z&to=2026-11-07T00:00:00Z&level=error"
```

Pass `next_cursor` back as `&cursor=...` to get the next page. Ingest answers `429` with `Retry-After` when the queue backlog is at its limit (nothing is written; retry with backoff) and `503` when the queue is unavailable. Every parameter, limit and status code is documented in [docs/architecture.md](docs/architecture.md). Health endpoints: `GET /healthz` and `GET /readyz` on each service; ingest and the processor also serve `GET /stats` (accepted, rejected and failed counts; stream length, pending, lag, oldest unprocessed age, trimmed).

## Benchmarks

Measured on a Windows laptop with everything in Docker Desktop. This is not a production benchmark; read [docs/benchmarks.md](docs/benchmarks.md) for the conditions and caveats.

| | Result | Target |
|---|---|---|
| Ingest throughput (accepted) | 15k–70k events/s for 1–64 concurrent senders | N2: 10k events/s ✓ |
| End-to-end through the processor | 20k–27k events/s under load, 41k events/s draining a backlog | N2 ✓ |
| Ingest p99 at a steady 10k events/s | p50 about 3 ms; p99 5.9–7.9 ms in 8 of 8 runs after the no-loss fix (earlier runs on a nearly full disk: 7 of 14 above 50 ms) | N1: < 50 ms ✓ in the re-run |
| Bursts at maximum rate | accepted == stored, lost 0; `429` backpressure above 500k queued events | no silent loss ✓ |
| 24h search, 1M rows | p99 76 ms with a service filter, ≤ 342 ms without | N4: < 2 s ✓ (for this dataset) |

The first benchmark found that bursts faster than the processor lost events silently (stream trimming). That is fixed: see [ADR 0010](docs/adr/0010-no-loss-under-bursts.md).

## Development

Go 1.23+ for the backend. Infrastructure only (without `.env`):

```bash
docker compose -f infra/docker-compose.yml up -d redis clickhouse
```

```bash
cd backend && go vet ./... && go build ./... && go test ./...
```

Integration tests run when these variables are set (CI sets them):

```bash
cd backend && REDIS_TEST_ADDR=localhost:6379 CLICKHOUSE_TEST_URL=http://localhost:8123 CLICKHOUSE_TEST_USER=logsage CLICKHOUSE_TEST_PASSWORD=change-me go test ./... -count=1
```

Projects and per-project API keys. Ingest and query accept these keys, and each key only sees and writes its own project (the static `INGEST_API_KEY`/`QUERY_API_KEY` are deprecated and work only with `ALLOW_LEGACY_STATIC_KEYS=true`, for the default project):

```bash
docker compose -f infra/docker-compose.yml up -d postgres
cd backend
export DATABASE_URL='postgres://logsage:change-me@localhost:5432/logsage?sslmode=disable'
go run ./cmd/admin migrate
go run ./cmd/admin create-project --name "Demo app" --slug demo
KEY=$(go run ./cmd/admin create-key --project demo --kind ingest --name ci --expires-in 720h)   # shown once
go run ./cmd/admin list-keys --project demo
printf '%s\n' "$KEY" | go run ./cmd/admin verify-key          # valid project=demo kind=ingest
go run ./cmd/admin revoke-key --prefix <8-char prefix from list-keys>
```

Use the keys like this (a revoked key or disabled project keeps working for up to `AUTH_CACHE_TTL`, 30 s by default):

```bash
INGEST_KEY=$(go run ./cmd/admin create-key --project demo --kind ingest --name app)
QUERY_KEY=$(go run ./cmd/admin create-key --project demo --kind query --name dashboard)
curl -X POST http://127.0.0.1:8080/v1/ingest -H "X-API-Key: $INGEST_KEY" -H "Content-Type: application/json" -d @events.json
curl -H "X-API-Key: $QUERY_KEY" "http://127.0.0.1:8082/v1/logs?from=2026-11-07T00:00:00Z&to=2026-11-08T00:00:00Z"
```

Users, the management API and the dashboard read endpoints (`cmd/api`, `:8083`; contract in [docs/architecture.md](docs/architecture.md), design in ADR 0013, 0014 and 0015). Passwords are read from `LOGSAGE_PASSWORD` or stdin, never flags:

```bash
printf '%s\n' 'a long passphrase here' | go run ./cmd/admin create-user --email you@example.com
go run ./cmd/admin add-member --project demo --email you@example.com --role owner
COOKIE_SECURE=false CLICKHOUSE_URL=http://localhost:8123 CLICKHOUSE_DB=logsage CLICKHOUSE_USER=logsage \
  CLICKHOUSE_PASSWORD=change-me go run ./cmd/api   # COOKIE_SECURE=false only for local plain http
curl -c jar.txt -H "Content-Type: application/json" -H "Origin: http://localhost:8083" \
  -d '{"email":"you@example.com","password":"a long passphrase here"}' http://localhost:8083/api/auth/login
curl -b jar.txt http://localhost:8083/api/projects
# Live tail (ADR 0015): Server-Sent Events of new logs, same filters, resumable with Last-Event-ID
curl -N -b jar.txt "http://localhost:8083/api/projects/demo/tail?level=error,fatal"
# Dashboard reads (ADR 0014): logs, histogram and facets of a project you are a member of
curl -b jar.txt "http://localhost:8083/api/projects/demo/histogram?from=2026-11-07T00:00:00Z&to=2026-11-08T00:00:00Z&service=payments,checkout"
curl -b jar.txt "http://localhost:8083/api/projects/demo/facets?from=2026-11-07T00:00:00Z&to=2026-11-08T00:00:00Z&level=error"
# Error groups (ADR 0022): groups in a window, one group, and its status (owner or admin)
curl -b jar.txt "http://localhost:8083/api/projects/demo/groups?from=2026-11-07T00:00:00Z&to=2026-11-08T00:00:00Z&sort=count"
curl -b jar.txt -H "Content-Type: application/json" -H "Origin: http://localhost:8083"   -d '{"status":"resolved","note":"fixed in 1.4.2"}' http://localhost:8083/api/projects/demo/groups/<fingerprint>/status
```

Running the services without Docker, the load generator and all commands are described in [AGENTS.md](AGENTS.md). Configuration is read from environment variables; `.env.example` lists every variable with its default.

## Security

- **Browser headers:** the dashboard is served with a strict Content-Security-Policy (same-origin scripts, styles, fonts and requests only; no `unsafe-inline`, no `unsafe-eval`, no framing). Every response also carries `nosniff`, `Referrer-Policy: no-referrer`, a Permissions-Policy, COOP, CORP and `X-Frame-Options: DENY` ([ADR 0021](docs/adr/0021-serve-dashboard-csp-runtime-config.md)).
- **TLS:** LogSage does not terminate TLS. In production, put a reverse proxy or load balancer with TLS in front of `cmd/api` (`:8083`) and ingest (`:8080`), and set `COOKIE_SECURE=true`. That gives `__Host-` cookies and turns on HSTS. Keep the ports bound to 127.0.0.1 or a private network behind the proxy.
- **Keys and passwords:** API keys are stored as hashes and shown once; passwords use argon2id; sessions are server-side; nothing secret is kept in browser storage. Report security issues privately to the maintainer rather than in a public issue.

## Documentation

- [Requirements](docs/requirements.md) · [Architecture](docs/architecture.md) · [Decision records](docs/adr)
- [Roadmap](docs/roadmap.md) · [Changelog](CHANGELOG.md) · [v0.2.0 release notes](docs/releases/v0.2.0.md) · [v0.1.0 release notes](docs/releases/v0.1.0.md)
- [Benchmarks](docs/benchmarks.md) · [Claude Code workflow](docs/claude-code-workflow.md)

## Tech stack

Go (standard library, go-redis, pgx, x/crypto) · Redis Streams · ClickHouse · PostgreSQL · React + TypeScript (Vite) · Docker Compose · GitHub Actions. Planned: Python + FastAPI AI service.
