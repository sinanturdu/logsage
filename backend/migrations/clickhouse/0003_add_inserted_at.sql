-- inserted_at: when the processor started the insert attempt that wrote the
-- row (ADR 0015). The live tail reads rows in (inserted_at, event_id) order,
-- which follows visibility, unlike timestamp or ingested_at. Rows written
-- before this column existed get the fixed epoch, never a now()-style
-- default, so they never look new to a tail.
ALTER TABLE {{.Database}}.logs ADD COLUMN IF NOT EXISTS inserted_at DateTime64(3, 'UTC') DEFAULT toDateTime64(0, 3, 'UTC')
