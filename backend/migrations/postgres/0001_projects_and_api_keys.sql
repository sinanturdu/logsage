-- Projects and per-project API keys (ADR 0011).
-- Applied once by internal/storage/postgres.Migrate inside a transaction and
-- recorded in schema_migrations with a checksum: never edit this file after it
-- has been applied anywhere; add a new numbered migration instead.

CREATE TABLE projects (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text        NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
    slug        text        NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9-]{3,40}$'),
    created_at  timestamptz NOT NULL DEFAULT now(),
    disabled_at timestamptz
);

CREATE TABLE api_keys (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id   uuid        NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    kind         text        NOT NULL CHECK (kind IN ('ingest', 'query')),
    name         text        NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
    -- Public lookup part of the key: 8 lowercase base32 characters.
    key_prefix   text        NOT NULL CHECK (key_prefix ~ '^[a-z2-7]{8}$'),
    -- SHA-256 of the full key. The key itself is never stored.
    key_hash     bytea       NOT NULL CHECK (octet_length(key_hash) = 32),
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz,
    revoked_at   timestamptz,
    last_used_at timestamptz
);

CREATE UNIQUE INDEX api_keys_key_prefix_key ON api_keys (key_prefix);
CREATE INDEX api_keys_project_id_idx ON api_keys (project_id);

-- The project that v0.1 data belongs to: ClickHouse rows carry
-- project_id = 'default', which is this project's slug. The id is fixed so
-- code and later migrations can refer to it.
INSERT INTO projects (id, name, slug)
VALUES ('00000000-0000-0000-0000-000000000001', 'Default project', 'default');
