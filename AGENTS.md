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
- Start dependencies: `docker compose -f infra/docker-compose.yml up -d`
- Backend: `cd backend && go vet ./... && go build ./... && go test ./... -race`
- Run ingest API: `cd backend && go run ./cmd/ingest` (health check: `GET /healthz`)

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
