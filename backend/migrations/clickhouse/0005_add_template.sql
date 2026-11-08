-- Error groups (ADR 0022): the normalized first line of grouped rows (warn,
-- error, fatal); empty for rows without a fingerprint. About 0.7% of the
-- message column's size on 100k rows.
ALTER TABLE {{.Database}}.logs ADD COLUMN IF NOT EXISTS template String DEFAULT '' CODEC(ZSTD(1)) AFTER fingerprint
