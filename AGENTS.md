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
- Full stack in Docker: `docker compose --env-file .env -f infra/docker-compose.yml -f infra/docker-compose.app.yml --profile app up -d --build` (copy `.env.example` to `.env` first; the ClickHouse and PostgreSQL passwords are required). The api serves the dashboard on http://localhost:8083. Image by hand: `docker build -f backend/Dockerfile .` (context: the repo root; the allow-list `/.dockerignore` must keep `.env`, `.dev`, keys, `node_modules`, `dist` and `.git` out)
- Local dev on Windows (PowerShell 5.1 or pwsh, no Git Bash): `powershell -ExecutionPolicy Bypass -File .\scripts\dev.ps1 up|seed|web|status|down` (creates `.env` and `.dev/` if missing, both git-ignored; never commit them). Demo data only: `cd backend && LOADGEN_INGEST_KEY=... go run ./cmd/loadgen seed` (about 70k events: an hour plus an incident in the last 15 min)
- Frontend (Node from `frontend/.nvmrc`): `cd frontend && npm ci`, then `npm run dev` (http://localhost:5173, proxies `/api` to `:8083`; needs `COOKIE_SECURE=false` on the API), `npm run lint`, `npm run typecheck`, `npm test`, `npm run build`. Exact versions only, lockfile committed; no browser binaries in tests (jsdom). Render untrusted text with `<LogText>`; no browser storage (lint rules). Explorer URL model (ADR 0017, 0018): `range` or `from`/`to`, `level`, `service`, `q`, `trace_id`, `zoom=1` (absolute ranges only); `live=1` for the live tail (ADR 0019). Shared test helpers in `frontend/src/explorer/testing.ts` and `frontend/src/live/testing.ts` (streamed `fetch` responses). Never put a new API key or password in React Query, the URL, storage or logs. Keyboard shortcuts live only in `frontend/src/shortcuts.ts` (handlers and the ? help read it); palette commands are the pure `commandsFor` in `frontend/src/palette/commands.ts` (ADR 0020)
- Backend: `cd backend && go vet ./... && go build ./... && go test ./...`
  - Locally on Windows, run `go test ./...` without `-race` (the race detector crashes with the MinGW toolchain on PATH).
  - CI runs `go test ./... -race` on Linux, with Redis and ClickHouse services.
- Integration tests: `docker compose -f infra/docker-compose.yml up -d redis clickhouse`, then `cd backend && REDIS_TEST_ADDR=localhost:6379 CLICKHOUSE_TEST_URL=http://localhost:8123 CLICKHOUSE_TEST_USER=logsage CLICKHOUSE_TEST_PASSWORD=change-me go test ./... -count=1` (skipped when unset).
- Run ingest API: `cd backend && INGEST_API_KEY=dev-key go run ./cmd/ingest` (health check: `GET /healthz`; ingest: `POST /v1/ingest`)
- Run processor: `cd backend && REDIS_ADDR=localhost:6379 CLICKHOUSE_USER=logsage CLICKHOUSE_PASSWORD=change-me go run ./cmd/processor` (`GET :8081/healthz`, `/readyz`, `/stats`)
- Load generator: `cd backend && LOADGEN_INGEST_KEY=... go run ./cmd/loadgen ingest|demo`, `LOADGEN_QUERY_KEY=... go run ./cmd/loadgen query` (local targets only unless `--allow-remote`). No-loss check: `ingest --wait-drain http://127.0.0.1:8081/stats --verify` with `LOADGEN_CLICKHOUSE_USER/PASSWORD` set; add `--retry-on-429` to retry backpressure. Benchmark procedure in `docs/benchmarks.md`
- Ingest/query auth: with `DATABASE_URL` set they verify per-project keys (create with the admin CLI or the dashboard); `ALLOW_LEGACY_STATIC_KEYS=true` (off by default since v0.2.0, removed in v0.3 package M) keeps `INGEST_API_KEY`/`QUERY_API_KEY` for the default project; tests that need it set it explicitly. End-to-end isolation tests: `go test ./internal/e2e` with `POSTGRES_TEST_URL`, `REDIS_TEST_ADDR` and `CLICKHOUSE_TEST_URL` set
- Admin CLI (projects, API keys, users, members): `docker compose -f infra/docker-compose.yml up -d postgres`, then `cd backend && DATABASE_URL='postgres://logsage:change-me@localhost:5432/logsage?sslmode=disable' go run ./cmd/admin migrate|create-project|list-projects|disable-project|create-key|list-keys|revoke-key|verify-key|create-user|set-password|disable-user|add-member|list-members` (keys only via stdin or `LOGSAGE_API_KEY`, passwords only via stdin or `LOGSAGE_PASSWORD`, never flags; non-local DB needs `--allow-remote`)
- PostgreSQL integration tests: add `POSTGRES_TEST_URL='postgres://logsage:change-me@localhost:5432/logsage?sslmode=disable'` (tests create and drop their own databases)
- Run management API: `cd backend && DATABASE_URL='postgres://logsage:change-me@localhost:5432/logsage?sslmode=disable' CLICKHOUSE_URL=http://localhost:8123 CLICKHOUSE_DB=logsage CLICKHOUSE_USER=logsage CLICKHOUSE_PASSWORD=change-me COOKIE_SECURE=false go run ./cmd/api` (`:8083/api/...` including `GET /api/projects/{slug}/logs|histogram|facets`, `/healthz`, `/readyz`; POSTs need `Content-Type: application/json` and a same-host or `ALLOWED_ORIGINS` Origin). Tests: `go test ./internal/api ./cmd/api` with `POSTGRES_TEST_URL` and `CLICKHOUSE_TEST_URL`
- Live tail latency: `cd backend && LOADGEN_INGEST_KEY=... go run ./cmd/loadgen tail --project <slug> --cookie-file jar.txt --events 100 --rate 5` (session from a curl cookie jar or `LOADGEN_SESSION`; reports p50/p95/p99 and missing events). Stream by hand: `curl -N -b jar.txt http://127.0.0.1:8083/api/projects/<slug>/tail`
- Measure `FINAL` cost (opt-in, 100k rows): `cd backend && LOGSAGE_FINAL_COST=1 CLICKHOUSE_TEST_URL=http://localhost:8123 CLICKHOUSE_TEST_USER=logsage CLICKHOUSE_TEST_PASSWORD=change-me go test ./internal/query -run TestFinalCost -v -count=1`
- Error groups (ADR 0022): the normalizer is `backend/internal/fingerprint`; `go test ./internal/fingerprint` compares every corpus case with `testdata/golden.json`. A deliberate rule change: run with `-update`, review the diff, and bump `fingerprint.Version`. Fuzz: `go test ./internal/fingerprint -run '^$' -fuzz FuzzNormalize -fuzztime 30s`. Endpoints `GET /api/projects/{slug}/groups`, `GET .../groups/{fingerprint}`, `POST .../groups/{fingerprint}/status`; filter `fingerprint=<16 hex>` on the log reads. Dashboard (ADR 0023): `frontend/src/groups/` (URL model in `model.ts`, client in `api.ts`, test helpers in `testing.ts`); render templates with `TemplateText` (text nodes only)
- Run query API: `cd backend && QUERY_API_KEY=dev-read-key CLICKHOUSE_USER=logsage CLICKHOUSE_PASSWORD=change-me go run ./cmd/query` (`GET :8082/v1/logs`, `/healthz`, `/readyz`); the query key must differ from the ingest key.

## Dashboard serving and security (ADR 0021)
- `internal/web` embeds `backend/internal/web/dist`; only its `.gitkeep` is committed, never build output. Without a build, pages answer with a short 404; use the Vite dev server.
- Keep the HTML CSP free of `unsafe-inline` and `unsafe-eval`: no inline scripts, no `style` attributes or React `style` props, no external origins. Any new resource type needs a CSP change and a check in the browser console.
- `PUBLIC_INGEST_URL` reaches the dashboard only through `/config.json`; never hard-code the ingest address in the frontend.

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
