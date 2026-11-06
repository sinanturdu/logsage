-- Log events written by cmd/processor.
-- event_id is the Redis stream entry ID. A redelivered entry produces a row
-- with the same sorting key, so ReplacingMergeTree collapses duplicates on
-- merge; read with FINAL (or GROUP BY event_id) for exact results. See ADR 0008.
-- project_id is LowCardinality(String) until projects exist (v0.2).
-- TTL is applied only when the table is created; change it later with ALTER TABLE ... MODIFY TTL.
CREATE TABLE IF NOT EXISTS {{.Database}}.logs
(
    project_id  LowCardinality(String),
    event_id    String,
    timestamp   DateTime64(3, 'UTC'),
    level       LowCardinality(String),
    service     LowCardinality(String),
    message     String,
    fingerprint UInt64,
    trace_id    String,
    attributes  Map(String, String),
    ingested_at DateTime64(3, 'UTC') DEFAULT now64(3)
)
ENGINE = ReplacingMergeTree
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (project_id, service, timestamp, event_id)
TTL toDateTime(timestamp) + INTERVAL {{.TTLDays}} DAY
