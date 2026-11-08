-- Alerts (ADR 0024): notification channels, rules, their evaluation state,
-- every state transition, and the delivery outbox. Secrets and webhook URLs
-- are stored sealed (AES-256-GCM, see internal/alert/secret); no column
-- holds them in clear text.
-- Never edit this file after it has been applied anywhere; add a new
-- numbered migration instead.

CREATE TABLE notification_channels (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id       uuid        NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name             text        NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    kind             text        NOT NULL CHECK (kind IN ('webhook', 'slack')),
    url_sealed       bytea       NOT NULL,
    url_host         text        NOT NULL, -- scheme and host only, for display and logs
    secret_sealed    bytea,                -- webhook signing secret; null for slack
    include_sample   boolean     NOT NULL DEFAULT false,
    created_by       uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, name),
    CHECK ((kind = 'webhook') = (secret_sealed IS NOT NULL))
);

CREATE TABLE alert_rules (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id    uuid        NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name          text        NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    type          text        NOT NULL CHECK (type IN ('error_rate', 'log_volume', 'new_group', 'regression', 'new_groups_per_hour')),
    params        jsonb       NOT NULL CHECK (jsonb_typeof(params) = 'object'),
    interval_s    integer     NOT NULL CHECK (interval_s BETWEEN 30 AND 3600),
    window_s      integer     NOT NULL CHECK (window_s BETWEEN 60 AND 86400),
    for_s         integer     NOT NULL DEFAULT 0 CHECK (for_s BETWEEN 0 AND 86400),
    clear_for_s   integer     NOT NULL CHECK (clear_for_s BETWEEN 0 AND 3600),
    repeat_s      integer     NOT NULL DEFAULT 0 CHECK (repeat_s = 0 OR repeat_s BETWEEN 300 AND 604800),
    enabled       boolean     NOT NULL DEFAULT true,
    note          text        NOT NULL DEFAULT '' CHECK (length(note) <= 500),
    version       integer     NOT NULL DEFAULT 1, -- bumped when what the rule measures changes
    next_eval_at  timestamptz NOT NULL DEFAULT now(),
    created_by    uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);
CREATE INDEX alert_rules_due ON alert_rules (next_eval_at) WHERE enabled;

CREATE TABLE alert_rule_channels (
    rule_id    uuid NOT NULL REFERENCES alert_rules (id) ON DELETE CASCADE,
    channel_id uuid NOT NULL REFERENCES notification_channels (id) ON DELETE CASCADE,
    PRIMARY KEY (rule_id, channel_id)
);
CREATE INDEX alert_rule_channels_channel ON alert_rule_channels (channel_id);

-- One row per rule and dedup key: "rule" for the rate, volume and guard
-- rules, "fp:<hex>" for new_group, "fp:<hex>:<resolved_at>" for regression.
CREATE TABLE alert_states (
    rule_id          uuid             NOT NULL REFERENCES alert_rules (id) ON DELETE CASCADE,
    dedup_key        text             NOT NULL CHECK (length(dedup_key) <= 200),
    state            text             NOT NULL CHECK (state IN ('ok', 'pending', 'firing', 'resolved')),
    since            timestamptz      NOT NULL,
    clear_since      timestamptz,
    last_value       double precision NOT NULL DEFAULT 0,
    fired_at         timestamptz,
    last_notified_at timestamptz,
    notify_count     integer          NOT NULL DEFAULT 0,
    fingerprint      bigint,
    template         text             NOT NULL DEFAULT '',
    version          integer          NOT NULL DEFAULT 1, -- optimistic check against a second writer
    PRIMARY KEY (rule_id, dedup_key)
);

CREATE TABLE alert_events (
    id           bigint           GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id   uuid             NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    rule_id      uuid             REFERENCES alert_rules (id) ON DELETE SET NULL,
    rule_name    text             NOT NULL,
    rule_type    text             NOT NULL,
    dedup_key    text             NOT NULL,
    from_state   text             NOT NULL,
    to_state     text             NOT NULL,
    value        double precision NOT NULL,
    threshold    double precision NOT NULL,
    window_start timestamptz      NOT NULL,
    window_end   timestamptz      NOT NULL,
    fingerprint  bigint,
    template     text             NOT NULL DEFAULT '',
    reason       text             NOT NULL DEFAULT '',
    at           timestamptz      NOT NULL DEFAULT now()
);
CREATE INDEX alert_events_project_at ON alert_events (project_id, at DESC, id DESC);
CREATE INDEX alert_events_rule_at ON alert_events (rule_id, at DESC);

CREATE TABLE alert_notifications (
    id              bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id      uuid        NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    event_id        bigint      REFERENCES alert_events (id) ON DELETE SET NULL,
    channel_id      uuid        REFERENCES notification_channels (id) ON DELETE SET NULL,
    channel_name    text        NOT NULL,
    idempotency_key text        NOT NULL UNIQUE,
    event_type      text        NOT NULL CHECK (event_type IN ('alert.firing', 'alert.resolved', 'alert.test')),
    payload         jsonb       NOT NULL, -- never a secret or a webhook URL
    status          text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sending', 'sent', 'failed', 'cancelled')),
    attempts        integer     NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    claimed_by      text,
    claimed_until   timestamptz,
    response_status integer,
    last_error      text        NOT NULL DEFAULT '' CHECK (length(last_error) <= 300),
    test            boolean     NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now(),
    sent_at         timestamptz
);
CREATE INDEX alert_notifications_due ON alert_notifications (next_attempt_at) WHERE status = 'pending';
CREATE INDEX alert_notifications_claimed ON alert_notifications (claimed_until) WHERE status = 'sending';
CREATE INDEX alert_notifications_project ON alert_notifications (project_id, created_at DESC, id DESC);
