-- v0.3 L1: incidents (ADR 0026). An incident groups the alerts of one
-- problem; the timeline is a closed list of event types whose payloads hold
-- ids, enums, rule names and normalized templates only, never a raw log
-- message. Comments keep their text in their own column.

ALTER TABLE alert_rules
    ADD COLUMN open_incident boolean NOT NULL DEFAULT false,
    ADD COLUMN incident_severity text NOT NULL DEFAULT 'high'
    CHECK (incident_severity IN ('critical', 'high', 'medium', 'low'));

-- Per-project numbers: the creating transaction increments the row, so
-- concurrent creators wait on its lock and get distinct numbers without gaps.
CREATE TABLE incident_counters (
    project_id uuid    PRIMARY KEY REFERENCES projects (id) ON DELETE CASCADE,
    last       integer NOT NULL DEFAULT 0
);

CREATE TABLE incidents (
    id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id          uuid        NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    number              integer     NOT NULL,
    title               text        NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    severity            text        NOT NULL CHECK (severity IN ('critical', 'high', 'medium', 'low')),
    status              text        NOT NULL CHECK (status IN ('open', 'acknowledged', 'resolved')),
    source              text        NOT NULL CHECK (source IN ('auto', 'manual')),
    assignee            uuid        REFERENCES users (id) ON DELETE SET NULL,
    summary             text        NOT NULL DEFAULT '' CHECK (length(summary) <= 2000),
    services            text[]      NOT NULL DEFAULT '{}' CHECK (cardinality(services) <= 20),
    opened_at           timestamptz NOT NULL,
    acknowledged_at     timestamptz,
    acknowledged_by     uuid        REFERENCES users (id) ON DELETE SET NULL,
    resolved_at         timestamptz,
    resolved_by         uuid        REFERENCES users (id) ON DELETE SET NULL,
    last_activity_at    timestamptz NOT NULL,
    recurrence_of       uuid        REFERENCES incidents (id) ON DELETE SET NULL,
    all_alerts_resolved boolean     NOT NULL DEFAULT false,
    event_count         integer     NOT NULL DEFAULT 0,
    events_dropped      integer     NOT NULL DEFAULT 0,
    created_by          uuid        REFERENCES users (id) ON DELETE SET NULL,
    version             integer     NOT NULL DEFAULT 1,
    UNIQUE (project_id, number)
);
CREATE INDEX incidents_project_activity ON incidents (project_id, last_activity_at DESC, id DESC);
CREATE INDEX incidents_project_open ON incidents (project_id, status) WHERE status <> 'resolved';

-- Alert states linked to an incident: one row per rule and state key.
CREATE TABLE incident_alerts (
    incident_id  uuid        NOT NULL REFERENCES incidents (id) ON DELETE CASCADE,
    rule_id      uuid        REFERENCES alert_rules (id) ON DELETE SET NULL,
    rule_key     text        NOT NULL, -- the rule id as text: kept after the rule is deleted
    state_key    text        NOT NULL CHECK (length(state_key) <= 200),
    rule_name    text        NOT NULL,
    services     text[]      NOT NULL DEFAULT '{}',
    linked_at    timestamptz NOT NULL,
    closed_at    timestamptz,
    close_reason text        NOT NULL DEFAULT '' CHECK (close_reason IN ('', 'resolved', 'rule_disabled', 'rule_deleted', 'rule_changed')),
    PRIMARY KEY (incident_id, rule_key, state_key)
);
CREATE INDEX incident_alerts_rule ON incident_alerts (rule_key, state_key);

CREATE TABLE incident_groups (
    incident_id uuid        NOT NULL REFERENCES incidents (id) ON DELETE CASCADE,
    fingerprint bigint      NOT NULL,
    template    text        NOT NULL DEFAULT '',
    source      text        NOT NULL CHECK (source IN ('auto', 'manual')),
    linked_at   timestamptz NOT NULL,
    linked_by   uuid        REFERENCES users (id) ON DELETE SET NULL,
    PRIMARY KEY (incident_id, fingerprint)
);

CREATE TABLE incident_events (
    id          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    incident_id uuid        NOT NULL REFERENCES incidents (id) ON DELETE CASCADE,
    type        text        NOT NULL CHECK (type IN ('opened', 'alert_firing', 'alert_resolved', 'alert_closed', 'all_alerts_resolved',
                                                     'group_linked', 'group_unlinked', 'status_changed', 'severity_changed', 'assigned',
                                                     'comment', 'comment_deleted', 'reopened')),
    actor       uuid        REFERENCES users (id) ON DELETE SET NULL, -- NULL: the system
    at          timestamptz NOT NULL,
    last_at     timestamptz NOT NULL,
    count       integer     NOT NULL DEFAULT 1, -- flapping repeats counted on one event
    payload     jsonb       NOT NULL DEFAULT '{}',
    body        text        CHECK (body IS NULL OR length(body) <= 2000), -- comments only; NULL once deleted
    -- Flapping: the rule and key of alert events, to find the previous one.
    alert_key   text        NOT NULL DEFAULT ''
);
CREATE INDEX incident_events_timeline ON incident_events (incident_id, id DESC);
CREATE INDEX incident_events_alert ON incident_events (incident_id, alert_key, type, id DESC) WHERE alert_key <> '';
