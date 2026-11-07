// Package e2e holds end-to-end tests that run ingest, processor and query
// against real PostgreSQL, Redis and ClickHouse. They are skipped unless
// POSTGRES_TEST_URL, REDIS_TEST_ADDR and CLICKHOUSE_TEST_URL are set.
package e2e
