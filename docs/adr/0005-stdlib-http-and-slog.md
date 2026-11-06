# ADR 0005: Standard library for HTTP routing and logging

Status: accepted

## Context
The backend needs an HTTP router and structured logging. Since Go 1.22, `net/http.ServeMux` supports method and path-parameter patterns (`GET /v1/logs/{id}`), and `log/slog` (Go 1.21) provides leveled, structured JSON logging.

## Decision
Use `net/http` for routing and `log/slog` for logging. No third-party web framework or logging library. Configuration is read from environment variables by `internal/config`.

## Consequences
Zero dependencies for the skeleton, nothing to keep patched, and handlers stay plain `http.Handler`. Middleware (request ID, rate limiting, auth) is written by hand. If routing needs outgrow `ServeMux`, a router compatible with `http.Handler` (for example `chi`) can be introduced with a new ADR.
