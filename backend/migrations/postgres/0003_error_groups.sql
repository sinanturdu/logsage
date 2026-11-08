-- Error group status (ADR 0022). Groups themselves are computed from the
-- ClickHouse logs table; a row here exists only after someone changed a
-- group's status, and no row means "open". The fingerprint is the uint64
-- stored as its two's-complement bigint (never 0: 0 means "not grouped").
-- Never edit this file after it has been applied anywhere; add a new
-- numbered migration instead.

CREATE TABLE error_groups (
    project_id  uuid        NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    fingerprint bigint      NOT NULL CHECK (fingerprint <> 0),
    status      text        NOT NULL CHECK (status IN ('open', 'ignored', 'resolved')),
    resolved_at timestamptz,
    note        text        NOT NULL DEFAULT '' CHECK (length(note) <= 500),
    updated_by  uuid        REFERENCES users (id) ON DELETE SET NULL,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, fingerprint),
    CHECK ((status = 'resolved') = (resolved_at IS NOT NULL))
);
