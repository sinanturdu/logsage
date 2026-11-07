// Package auth authenticates API requests: per-project keys from PostgreSQL
// (through internal/keys) with a bounded verification cache, and the
// deprecated static keys mapped to the default project. Users and sessions
// come in v0.2 package H2. See ADR 0012.
package auth
