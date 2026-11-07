-- Users, sessions, project membership and the audit log (ADR 0013).
-- Never edit this file after it has been applied anywhere; add a new
-- numbered migration instead.

CREATE TABLE users (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    email         text        NOT NULL CHECK (length(email) BETWEEN 3 AND 254 AND email = btrim(email) AND position('@' IN email) > 1),
    -- argon2id PHC string; the password itself is never stored.
    password_hash text        NOT NULL CHECK (password_hash LIKE '$argon2id$%'),
    created_at    timestamptz NOT NULL DEFAULT now(),
    disabled_at   timestamptz
);

CREATE UNIQUE INDEX users_email_lower_key ON users (lower(email));

CREATE TABLE sessions (
    id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- SHA-256 of the random cookie token. The token itself is never stored.
    token_hash          bytea       NOT NULL CHECK (octet_length(token_hash) = 32),
    created_at          timestamptz NOT NULL,
    last_seen_at        timestamptz NOT NULL,
    idle_expires_at     timestamptz NOT NULL,
    absolute_expires_at timestamptz NOT NULL,
    revoked_at          timestamptz,
    user_agent          text        NOT NULL DEFAULT '' CHECK (length(user_agent) <= 256)
);

CREATE UNIQUE INDEX sessions_token_hash_key ON sessions (token_hash);
CREATE INDEX sessions_user_id_active_idx ON sessions (user_id) WHERE revoked_at IS NULL;
CREATE INDEX sessions_absolute_expires_at_idx ON sessions (absolute_expires_at);

CREATE TABLE project_members (
    project_id uuid        NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role       text        NOT NULL CHECK (role IN ('owner', 'admin', 'viewer')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, user_id)
);

CREATE INDEX project_members_user_id_idx ON project_members (user_id);

-- The default project keeps created_by NULL and has no members until an
-- operator runs: admin add-member --project default --email ... --role owner
ALTER TABLE projects ADD COLUMN created_by uuid REFERENCES users (id) ON DELETE SET NULL;

-- Security-relevant events. detail never holds a secret (no passwords,
-- tokens, keys or hashes).
CREATE TABLE audit_log (
    id      bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at      timestamptz NOT NULL DEFAULT now(),
    user_id uuid        REFERENCES users (id) ON DELETE SET NULL,
    action  text        NOT NULL CHECK (action ~ '^[a-z_.]{1,64}$'),
    target  text        NOT NULL DEFAULT '' CHECK (length(target) <= 300),
    detail  text        NOT NULL DEFAULT '' CHECK (length(detail) <= 500)
);

CREATE INDEX audit_log_at_idx ON audit_log (at);
CREATE INDEX audit_log_user_id_at_idx ON audit_log (user_id, at);
