# Roadmap

## v0.1: Ingest and search
- [x] Go module, project layout, config loading, structured logging
- [x] `/healthz` endpoint
- [x] `POST /v1/ingest` with validation and API key check (static key first)
- [x] Redis Stream producer
- [x] Processor: consumer group, batch insert to ClickHouse, ack after write
- [x] ClickHouse migration for `logs`
- [x] `GET /v1/logs` search (time range, level, service, text)
- [x] Unit and integration tests, CI green
- [ ] Load test script and first benchmark numbers in docs

## v0.2: Accounts and dashboard
- [ ] PostgreSQL migrations, users, projects, hashed API keys
- [ ] Auth (JWT), project-scoped queries
- [ ] React dashboard: login, log explorer, charts

## v0.3: Live tail
- [ ] WebSocket streaming with filters

## v0.4: Error groups and alerts
- [ ] Fingerprinting, error groups UI
- [ ] Alert rules and notifications

## v0.5: Anomaly detection
- [ ] Python service, statistical baseline, evaluation notebook

## v0.6: Incidents and root cause
- [ ] Incident lifecycle, deploy events, AI root-cause suggestions

## v1.0: Release
- [ ] Public API docs, webhooks, MCP server, one-command setup, demo video, README polish
