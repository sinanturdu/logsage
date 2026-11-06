# Requirements

## Functional

| ID | Requirement | Milestone |
|----|-------------|-----------|
| F1 | Ingest logs via HTTPS JSON batch with per-project API key | v0.1 |
| F2 | Store logs with timestamp, level, service, message, attributes, trace id | v0.1 |
| F3 | Search logs by time range, level, service, free text, attribute | v0.1 |
| F4 | User registration/login, projects, API key create/revoke | v0.2 |
| F5 | Web dashboard: log explorer, volume and error-rate charts | v0.2 |
| F6 | Live log tail over WebSocket with filters | v0.3 |
| F7 | Group similar errors by fingerprint into error groups | v0.4 |
| F8 | Alert rules (threshold, rate, new error group) with Slack/email/webhook notification | v0.4 |
| F9 | Anomaly detection on error rate and latency | v0.5 |
| F10 | Incident lifecycle: open, acknowledge, resolve, timeline | v0.6 |
| F11 | AI root-cause suggestion using related logs and recent deploy events | v0.6 |
| F12 | Deploy event API (so incidents can be correlated with releases) | v0.6 |
| F13 | Public API + webhooks so external agents/tools can query and act | v1.0 |

## Non-functional

| ID | Requirement |
|----|-------------|
| N1 | Ingestion API p99 latency < 50 ms (accepts into queue, does not wait for storage) |
| N2 | Sustain 10,000 log events/second on a single developer-class machine (benchmarked) |
| N3 | No data loss on a processor crash: queue messages acknowledged only after storage write |
| N4 | Search over 24h of data returns in < 2 s for typical filters |
| N5 | API keys stored hashed; per-project data isolation enforced in every query |
| N6 | Configurable retention (default 30 days) |
| N7 | One-command local setup with Docker Compose |
| N8 | Structured logging, health endpoints, and Prometheus metrics for LogSage itself |

## Out of scope (for now)
Distributed tracing UI, billing, SSO, multi-region deployment.
