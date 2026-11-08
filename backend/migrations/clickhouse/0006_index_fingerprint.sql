-- Error groups (ADR 0022): skip granules without the fingerprint asked for
-- (fingerprint filter on /logs, /histogram, /facets, the tail and groups).
ALTER TABLE {{.Database}}.logs ADD INDEX IF NOT EXISTS idx_fingerprint fingerprint TYPE bloom_filter(0.01) GRANULARITY 4
