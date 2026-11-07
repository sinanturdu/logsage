-- Lets live tail polls skip granules whose rows were all inserted before
-- the cursor (ADR 0015).
ALTER TABLE {{.Database}}.logs ADD INDEX IF NOT EXISTS idx_inserted_at inserted_at TYPE minmax GRANULARITY 1
