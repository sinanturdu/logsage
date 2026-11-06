# LogSage

[![CI](https://github.com/sinanturdu/logsage/actions/workflows/ci.yml/badge.svg)](https://github.com/sinanturdu/logsage/actions/workflows/ci.yml)

LogSage is a log and incident analysis platform. Applications send their logs to an ingest API; LogSage stores them in ClickHouse and lets you search them. Later milestones group similar errors, detect anomalies and suggest likely root causes when an incident opens ("mini Datadog + AI").

> **Status: v0.1 (ingest and search).** Ingest, queue, processor, storage and search work end to end and run with one Docker Compose command. Accounts, the dashboard, alerting and AI features are planned: see [docs/roadmap.md](docs/roadmap.md) and the [v0.1.0 release notes](docs/releases/v0.1.0.md).

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

Requirements: Docker with Compose v2. On Windows use Docker Desktop.

```bash
cp .env.example .env    # then replace the change-me keys and password in .env
```

```bash
docker compose --env-file .env -f infra/docker-compose.yml -f infra/docker-compose.app.yml --profile app up -d --build
```

```bash
cd backend && LOADGEN_INGEST_KEY=<INGEST_API_KEY from .env> go run ./cmd/loadgen demo --duration 30s
```

This starts Redis, ClickHouse, PostgreSQL (unused until v0.2) and the three LogSage services. Their ports are bound to 127.0.0.1 only: ingest on `:8080`, processor stats on `:8081`, query on `:8082`. Compose refuses to start the app profile if a key or the ClickHouse password is missing from `.env`. The third command sends a realistic stream of events for 30 seconds; it needs Go 1.23+, or send single events with curl as below.

To stop: `docker compose --env-file .env -f infra/docker-compose.yml -f infra/docker-compose.app.yml --profile app down` (add `-v` to delete the stored data).

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

Projects and per-project API keys (v0.2 foundation; ingest and query still use the static keys until package H):

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

Running the services without Docker, the load generator and all commands are described in [AGENTS.md](AGENTS.md). Configuration is read from environment variables; `.env.example` lists every variable with its default.

## Documentation

- [Requirements](docs/requirements.md) · [Architecture](docs/architecture.md) · [Decision records](docs/adr)
- [Roadmap](docs/roadmap.md) · [Changelog](CHANGELOG.md) · [v0.1.0 release notes](docs/releases/v0.1.0.md)
- [Benchmarks](docs/benchmarks.md) · [Claude Code workflow](docs/claude-code-workflow.md)

## Tech stack

Go (standard library plus go-redis) · Redis Streams · ClickHouse · Docker Compose · GitHub Actions. Planned: PostgreSQL, React + TypeScript dashboard, Python + FastAPI AI service.
