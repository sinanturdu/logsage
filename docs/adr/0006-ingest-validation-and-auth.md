# ADR 0006: Ingest API validation and static-key authentication

Status: accepted

## Context
v0.1 needs `POST /v1/ingest` before accounts, Redis or ClickHouse exist. The API must reject poisoned or oversized payloads and must not leak the API key, while keeping the contract stable for later milestones.

## Decision
- One static key from `INGEST_API_KEY`. Both the configured key and the `X-API-Key` header are hashed with SHA-256 and compared with `crypto/subtle.ConstantTimeCompare`, so timing does not depend on content or length. The key is never logged. `cmd/ingest` refuses to start without it.
- Strict decoding: unknown fields, trailing data after the top-level object, and non-string attribute values are `400`.
- Batches are all-or-nothing. The first invalid event rejects the batch, and the error names its `index`.
- Checks run in a fixed order (method, key, content type, size, body) so unauthenticated clients never get the body parsed.
- Limits are config values (`INGEST_MAX_*`) with documented defaults.
- The handler depends on a `Publisher` interface it defines. A publisher failure returns `503` so clients retry. An in-memory publisher (bounded) serves tests and local runs until the Redis Streams producer exists.

## Consequences
Clients get clear, index-specific errors and cannot smuggle extra fields into storage. Adding a field to the contract requires a server change first. A single shared key gives no per-project isolation; v0.2 replaces it with hashed per-project keys behind the same header. Events accepted by the in-memory publisher are lost on restart, which is acceptable only until the queue lands.
