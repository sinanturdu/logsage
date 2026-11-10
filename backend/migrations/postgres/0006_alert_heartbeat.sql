-- v0.3 K2.2: the evaluator's heartbeat per rule. last_evaluated_at is the
-- last successful evaluation; last_eval_error is the category of the last
-- attempt when it failed (a closed list, never an error text), '' after a
-- success. The dashboard says "Not being checked" when a rule is overdue.
ALTER TABLE alert_rules
    ADD COLUMN last_evaluated_at timestamptz,
    ADD COLUMN last_eval_error text NOT NULL DEFAULT ''
    CHECK (last_eval_error IN ('', 'query_timeout', 'query_rejected', 'store_unavailable', 'unknown'));
