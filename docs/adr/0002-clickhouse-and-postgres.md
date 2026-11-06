# ADR 0002: ClickHouse for logs, PostgreSQL for relational data

Status: accepted

## Context
Logs are append-heavy, high-volume, and queried by time range with aggregation. Users, projects, rules, and incidents are small, relational, and transactional.

## Decision
Store logs and metrics in ClickHouse (MergeTree, partitioned by day, TTL retention). Store everything relational in PostgreSQL.

## Consequences
Right tool for each workload and fast aggregations. Two databases to operate and migrate; no cross-database joins (join in application code).
