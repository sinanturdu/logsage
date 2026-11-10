-- v0.3 K2.2 (ADR 0024 addendum): a delivery failure is recorded as the
-- HTTP status code and a category from a closed list, never as text. The
-- old free-text last_error could hold part of a receiver's response body
-- (an internal page reached through a proxying public host), so it is
-- converted where the status code tells the category, then dropped.

ALTER TABLE alert_notifications
    ADD COLUMN error_category text NOT NULL DEFAULT ''
    CHECK (error_category IN ('', 'http_4xx', 'http_5xx', 'timeout', 'connect_error', 'tls_error',
                              'blocked_address', 'rate_limited', 'canceled', 'unknown'));

UPDATE alert_notifications SET error_category = CASE
        WHEN status = 'cancelled' THEN 'canceled'
        WHEN response_status = 429 THEN 'rate_limited'
        WHEN response_status BETWEEN 400 AND 499 THEN 'http_4xx'
        WHEN response_status BETWEEN 500 AND 599 THEN 'http_5xx'
        ELSE 'unknown'
    END
WHERE last_error <> '';

ALTER TABLE alert_notifications DROP COLUMN last_error;

