# AGENTS.md: rules for AI coding agents working on LogSage

## Project
LogSage is a log and incident analysis platform. Read `docs/requirements.md` and `docs/architecture.md` before changing anything structural.

## Layout
- `backend/`: Go. Modular monolith: `cmd/`, `internal/{ingest,processor,query,alert,incident,auth,storage}`
- `ai-service/`: Python + FastAPI (anomaly detection, root-cause analysis)
- `frontend/`: React + TypeScript dashboard
- `infra/`: docker-compose and configs
- `docs/`: requirements, architecture, ADRs, roadmap

## Commands (update as the project grows)
- Start dependencies: `docker compose -f infra/docker-compose.yml up -d redis clickhouse` (no `.env` needed)
- Full stack in Docker: `docker compose --env-file .env -f infra/docker-compose.yml -f infra/docker-compose.app.yml --profile app up -d --build` (copy `.env.example` to `.env` first; keys and the ClickHouse password are required)
- Backend: `cd backend && go vet ./... && go build ./... && go test ./...`
  - Locally on Windows, run `go test ./...` without `-race` (the race detector crashes with the MinGW toolchain on PATH).
  - CI runs `go test ./... -race` on Linux, with Redis and ClickHouse services.
- Integration tests: `docker compose -f infra/docker-compose.yml up -d redis clickhouse`, then `cd backend && REDIS_TEST_ADDR=localhost:6379 CLICKHOUSE_TEST_URL=http://localhost:8123 CLICKHOUSE_TEST_USER=logsage CLICKHOUSE_TEST_PASSWORD=change-me go test ./... -count=1` (skipped when unset).
- Run ingest API: `cd backend && INGEST_API_KEY=dev-key go run ./cmd/ingest` (health check: `GET /healthz`; ingest: `POST /v1/ingest`)
- Run processor: `cd backend && REDIS_ADDR=localhost:6379 CLICKHOUSE_USER=logsage CLICKHOUSE_PASSWORD=change-me go run ./cmd/processor` (`GET :8081/healthz`, `/readyz`, `/stats`)
- Load generator: `cd backend && LOADGEN_INGEST_KEY=... go run ./cmd/loadgen ingest|demo`, `LOADGEN_QUERY_KEY=... go run ./cmd/loadgen query` (local targets only unless `--allow-remote`). No-loss check: `ingest --wait-drain http://127.0.0.1:8081/stats --verify` with `LOADGEN_CLICKHOUSE_USER/PASSWORD` set; add `--retry-on-429` to retry backpressure. Benchmark procedure in `docs/benchmarks.md`
- Ingest/query auth: with `DATABASE_URL` set they verify per-project keys (create with the admin CLI); `ALLOW_LEGACY_STATIC_KEYS=true` keeps `INGEST_API_KEY`/`QUERY_API_KEY` for the default project. End-to-end isolation tests: `go test ./internal/e2e` with `POSTGRES_TEST_URL`, `REDIS_TEST_ADDR` and `CLICKHOUSE_TEST_URL` set
- Admin CLI (projects, API keys, users, members): `docker compose -f infra/docker-compose.yml up -d postgres`, then `cd backend && DATABASE_URL='postgres://logsage:change-me@localhost:5432/logsage?sslmode=disable' go run ./cmd/admin migrate|create-project|list-projects|disable-project|create-key|list-keys|revoke-key|verify-key|create-user|set-password|disable-user|add-member|list-members` (keys only via stdin or `LOGSAGE_API_KEY`, passwords only via stdin or `LOGSAGE_PASSWORD`, never flags; non-local DB needs `--allow-remote`)
- PostgreSQL integration tests: add `POSTGRES_TEST_URL='postgres://logsage:change-me@localhost:5432/logsage?sslmode=disable'` (tests create and drop their own databases)
- Run management API: `cd backend && DATABASE_URL='postgres://logsage:change-me@localhost:5432/logsage?sslmode=disable' COOKIE_SECURE=false go run ./cmd/api` (`:8083/api/...`, `/healthz`, `/readyz`; POSTs need `Content-Type: application/json` and a same-host or `ALLOWED_ORIGINS` Origin). Tests: `go test ./internal/api ./cmd/api` with `POSTGRES_TEST_URL`
- Run query API: `cd backend && QUERY_API_KEY=dev-read-key CLICKHOUSE_USER=logsage CLICKHOUSE_PASSWORD=change-me go run ./cmd/query` (`GET :8082/v1/logs`, `/healthz`, `/readyz`); the query key must differ from the ingest key.

## Conventions
- Go: standard layout, small packages, `context.Context` first parameter, errors wrapped with `%w`, no global state, table-driven tests.
- Public behavior changes require a test. A task is not done until `go vet` and `go test` pass.
- Every significant technical decision gets an ADR in `docs/adr/` (next number, short, with consequences).
- Keep modules decoupled: packages communicate through interfaces defined by the consumer.

## Git workflow
- Never commit to `main` directly. One branch per task: `feat/...`, `fix/...`, `docs/...`.
- Conventional commits: `feat:`, `fix:`, `docs:`, `test:`, `refactor:`, `chore:`.
- Small, focused commits. Do not push or open a PR without the owner's approval.

## Hard rules
- Never commit secrets, `.env`, API keys, or real log data.
- Never log API keys or user credentials; store only hashed API keys.
- Do not add dependencies without stating why.
- Do not start a new milestone before the owner approves the previous one.
- When unsure about scope, ask instead of guessing.
