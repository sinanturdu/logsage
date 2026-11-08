-- Error groups (ADR 0022): all-time first and last seen per group, filled by
-- the view in 0008. Only min and max live here: redelivered duplicates
-- cannot change them, while counts always come from logs with FINAL. Rows
-- expire with the logs' retention after the group was last seen.
CREATE TABLE IF NOT EXISTS {{.Database}}.group_seen
(
    project_id  LowCardinality(String),
    fingerprint UInt64,
    first_seen  SimpleAggregateFunction(min, DateTime64(3, 'UTC')),
    last_seen   SimpleAggregateFunction(max, DateTime64(3, 'UTC'))
)
ENGINE = AggregatingMergeTree
ORDER BY (project_id, fingerprint)
TTL toDateTime(last_seen) + INTERVAL {{.TTLDays}} DAY
