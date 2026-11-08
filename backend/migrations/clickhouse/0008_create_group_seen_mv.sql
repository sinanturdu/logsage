-- Error groups (ADR 0022): every insert into logs updates group_seen for its
-- grouped rows. Rows written before this view existed have fingerprint 0
-- (ADR 0022: no backfill), so there is nothing to backfill.
CREATE MATERIALIZED VIEW IF NOT EXISTS {{.Database}}.group_seen_mv TO {{.Database}}.group_seen AS
SELECT project_id, fingerprint, min(timestamp) AS first_seen, max(timestamp) AS last_seen
FROM {{.Database}}.logs
WHERE fingerprint != 0
GROUP BY project_id, fingerprint
