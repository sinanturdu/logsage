package postgres

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sinanturdu/logsage/backend/internal/alert"
	"github.com/sinanturdu/logsage/backend/internal/fingerprint"
)

// Alerts storage (ADR 0024). Channel URLs and secrets arrive sealed; this
// file never sees them in clear text.

var (
	// ErrLimit means a per-project limit (rules, channels) is reached.
	ErrLimit = errors.New("limit reached")
	// ErrRateLimited means too many test sends; RetryAfter says how long to wait.
	ErrRateLimited = errors.New("rate limited")
	// ErrBadChannel means a rule names a channel of another project or none.
	ErrBadChannel = errors.New("unknown channel")
)

// RateLimitError carries how long to wait.
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string { return "rate limited" }
func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

// NewID returns a random UUID (version 4); channel ids are made before the
// row exists because they are part of the sealed data's additional data.
func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("postgres: id: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func audit(ctx context.Context, tx pgx.Tx, a AuditEntry) error {
	var uid *string
	if a.UserID != "" {
		uid = &a.UserID
	}
	_, err := tx.Exec(ctx, "INSERT INTO audit_log (user_id, action, target, detail) VALUES ($1, $2, $3, $4)",
		uid, a.Action, clip(a.Target, 300), clip(a.Detail, 500))
	if err != nil {
		return fmt.Errorf("postgres: audit: %w", err)
	}
	return nil
}

func (s *Store) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit: %w", err)
	}
	return nil
}

func uniqueName(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// --- channels ---

const channelCols = `c.id::text, c.name, c.kind, c.url_host, c.include_sample, c.created_at, c.updated_at`

func scanChannel(row pgx.Row) (alert.Channel, error) {
	var c alert.Channel
	err := row.Scan(&c.ID, &c.Name, &c.Kind, &c.URLHost, &c.IncludeSample, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return alert.Channel{}, ErrNotFound
	}
	return c, err
}

// CreateChannel inserts a channel (its id made with NewID and used in the
// sealed data) and the audit row, refusing more than the per-project limit.
func (s *Store) CreateChannel(ctx context.Context, c alert.SealedChannel, userID string, a AuditEntry) (alert.Channel, error) {
	var out alert.Channel
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT 1 FROM projects WHERE id = $1 FOR UPDATE", c.ProjectID); err != nil {
			return fmt.Errorf("postgres: lock project: %w", err)
		}
		var n int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM notification_channels WHERE project_id = $1", c.ProjectID).Scan(&n); err != nil {
			return fmt.Errorf("postgres: count channels: %w", err)
		}
		if n >= alert.MaxChannelsPerProject {
			return fmt.Errorf("channels: %w", ErrLimit)
		}
		var uid *string
		if userID != "" {
			uid = &userID
		}
		var err error
		out, err = scanChannel(tx.QueryRow(ctx, `INSERT INTO notification_channels AS c (id, project_id, name, kind, url_sealed, url_host, secret_sealed, include_sample, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING `+channelCols,
			c.ID, c.ProjectID, c.Name, c.Kind, c.URL, c.URLHost, c.Secret, c.IncludeSample, uid))
		if uniqueName(err) {
			return fmt.Errorf("channel %q: %w", c.Name, ErrConflict)
		}
		if err != nil {
			return fmt.Errorf("postgres: create channel: %w", err)
		}
		return audit(ctx, tx, a)
	})
	return out, err
}

// ListChannels returns a project's channels by name.
func (s *Store) ListChannels(ctx context.Context, projectID string) ([]alert.Channel, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	rows, err := s.pool.Query(ctx, `SELECT `+channelCols+` FROM notification_channels c WHERE c.project_id = $1 ORDER BY c.name`, projectID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list channels: %w", err)
	}
	defer rows.Close()
	out := []alert.Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan channel: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Channel returns one channel of a project, or ErrNotFound.
func (s *Store) Channel(ctx context.Context, projectID, id string) (alert.Channel, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	c, err := scanChannel(s.pool.QueryRow(ctx, `SELECT `+channelCols+` FROM notification_channels c WHERE c.project_id = $1 AND c.id::text = $2`, projectID, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return alert.Channel{}, fmt.Errorf("postgres: channel: %w", err)
	}
	return c, err
}

// ChannelUpdate changes a channel; nil fields stay.
type ChannelUpdate struct {
	Name          *string
	IncludeSample *bool
	URL           []byte  // sealed; nil keeps the URL
	URLHost       *string // with URL
	Secret        []byte  // sealed; nil keeps the secret (rotate)
}

// UpdateChannel applies u and writes the audit row.
func (s *Store) UpdateChannel(ctx context.Context, projectID, id string, u ChannelUpdate, a AuditEntry) (alert.Channel, error) {
	var out alert.Channel
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = scanChannel(tx.QueryRow(ctx, `UPDATE notification_channels c SET
				name = coalesce($3, c.name),
				include_sample = coalesce($4, c.include_sample),
				url_sealed = coalesce($5, c.url_sealed),
				url_host = coalesce($6, c.url_host),
				secret_sealed = CASE WHEN c.kind = 'webhook' THEN coalesce($7, c.secret_sealed) ELSE NULL END,
				updated_at = now()
			WHERE c.project_id = $1 AND c.id::text = $2 RETURNING `+channelCols,
			projectID, id, u.Name, u.IncludeSample, u.URL, u.URLHost, u.Secret))
		if uniqueName(err) {
			return fmt.Errorf("channel: %w", ErrConflict)
		}
		if err != nil {
			return err
		}
		return audit(ctx, tx, a)
	})
	return out, err
}

// DeleteChannel removes a channel; its pending deliveries are cancelled.
func (s *Store) DeleteChannel(ctx context.Context, projectID, id string, a AuditEntry) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE alert_notifications SET status = 'cancelled', last_error = 'channel deleted'
			WHERE project_id = $1 AND channel_id::text = $2 AND status IN ('pending', 'sending')`, projectID, id); err != nil {
			return fmt.Errorf("postgres: cancel deliveries: %w", err)
		}
		tag, err := tx.Exec(ctx, "DELETE FROM notification_channels WHERE project_id = $1 AND id::text = $2", projectID, id)
		if err != nil {
			return fmt.Errorf("postgres: delete channel: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return audit(ctx, tx, a)
	})
}

// SealedChannel returns a channel with its sealed URL and secret (delivery
// and test sends only).
func (s *Store) SealedChannel(ctx context.Context, projectID, id string) (alert.SealedChannel, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	var c alert.SealedChannel
	err := s.pool.QueryRow(ctx, `SELECT c.id::text, c.name, c.kind, c.url_host, c.include_sample, c.created_at, c.updated_at, c.project_id::text, c.url_sealed, c.secret_sealed
		FROM notification_channels c WHERE c.project_id = $1 AND c.id::text = $2`, projectID, id).
		Scan(&c.ID, &c.Name, &c.Kind, &c.URLHost, &c.IncludeSample, &c.CreatedAt, &c.UpdatedAt, &c.ProjectID, &c.URL, &c.Secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return alert.SealedChannel{}, ErrNotFound
	}
	if err != nil {
		return alert.SealedChannel{}, fmt.Errorf("postgres: sealed channel: %w", err)
	}
	return c, nil
}

// --- rules ---

const ruleCols = `r.id::text, r.project_id::text, p.slug, r.name, r.note, r.type, r.params, r.interval_s, r.window_s, r.for_s, r.clear_for_s,
	r.repeat_s, r.enabled, r.version, r.next_eval_at,
	coalesce((SELECT array_agg(rc.channel_id::text ORDER BY rc.channel_id) FROM alert_rule_channels rc WHERE rc.rule_id = r.id), '{}')`

func scanRule(row pgx.Row) (alert.Rule, error) {
	var r alert.Rule
	var params []byte
	var interval, window, forS, clearFor, repeat int
	err := row.Scan(&r.ID, &r.ProjectID, &r.ProjectSlug, &r.Name, &r.Note, &r.Type, &params, &interval, &window, &forS, &clearFor,
		&repeat, &r.Enabled, &r.Version, &r.NextEvalAt, &r.Channels)
	if errors.Is(err, pgx.ErrNoRows) {
		return alert.Rule{}, ErrNotFound
	}
	if err != nil {
		return alert.Rule{}, err
	}
	if err := json.Unmarshal(params, &r.Params); err != nil {
		return alert.Rule{}, fmt.Errorf("decode rule params: %w", err)
	}
	sec := func(n int) time.Duration { return time.Duration(n) * time.Second }
	r.Interval, r.Window, r.For, r.ClearFor, r.Repeat = sec(interval), sec(window), sec(forS), sec(clearFor), sec(repeat)
	return r, nil
}

func (s *Store) checkChannels(ctx context.Context, tx pgx.Tx, projectID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	var n int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM notification_channels WHERE project_id = $1 AND id::text = ANY($2)", projectID, ids).Scan(&n); err != nil {
		return fmt.Errorf("postgres: check channels: %w", err)
	}
	if n != len(dedupe(ids)) {
		return ErrBadChannel
	}
	return nil
}

func dedupe(ids []string) []string {
	seen := map[string]bool{}
	out := ids[:0:0]
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func setChannels(ctx context.Context, tx pgx.Tx, ruleID string, ids []string) error {
	if _, err := tx.Exec(ctx, "DELETE FROM alert_rule_channels WHERE rule_id::text = $1", ruleID); err != nil {
		return fmt.Errorf("postgres: rule channels: %w", err)
	}
	for _, id := range dedupe(ids) {
		if _, err := tx.Exec(ctx, "INSERT INTO alert_rule_channels (rule_id, channel_id) VALUES ($1, $2)", ruleID, id); err != nil {
			return fmt.Errorf("postgres: rule channels: %w", err)
		}
	}
	return nil
}

func ruleByID(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, projectID, id string) (alert.Rule, error) {
	return scanRule(q.QueryRow(ctx, `SELECT `+ruleCols+` FROM alert_rules r JOIN projects p ON p.id = r.project_id
		WHERE r.project_id = $1 AND r.id::text = $2`, projectID, id))
}

// CreateRule inserts a validated rule with its channels and the audit row.
func (s *Store) CreateRule(ctx context.Context, r alert.Rule, userID string, a AuditEntry) (alert.Rule, error) {
	params, err := json.Marshal(r.Params)
	if err != nil {
		return alert.Rule{}, fmt.Errorf("postgres: encode params: %w", err)
	}
	var out alert.Rule
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT 1 FROM projects WHERE id = $1 FOR UPDATE", r.ProjectID); err != nil {
			return fmt.Errorf("postgres: lock project: %w", err)
		}
		var n int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM alert_rules WHERE project_id = $1", r.ProjectID).Scan(&n); err != nil {
			return fmt.Errorf("postgres: count rules: %w", err)
		}
		if n >= alert.MaxRulesPerProject {
			return fmt.Errorf("rules: %w", ErrLimit)
		}
		if err := s.checkChannels(ctx, tx, r.ProjectID, r.Channels); err != nil {
			return err
		}
		var uid *string
		if userID != "" {
			uid = &userID
		}
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO alert_rules (project_id, name, type, params, interval_s, window_s, for_s, clear_for_s, repeat_s, enabled, note, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id::text`,
			r.ProjectID, r.Name, string(r.Type), params, secs(r.Interval), secs(r.Window), secs(r.For), secs(r.ClearFor), secs(r.Repeat), r.Enabled, r.Note, uid).Scan(&id)
		if uniqueName(err) {
			return fmt.Errorf("rule %q: %w", r.Name, ErrConflict)
		}
		if err != nil {
			return fmt.Errorf("postgres: create rule: %w", err)
		}
		if err := setChannels(ctx, tx, id, r.Channels); err != nil {
			return err
		}
		a.Target += "/" + id
		if err := audit(ctx, tx, a); err != nil {
			return err
		}
		out, err = ruleByID(ctx, tx, r.ProjectID, id)
		return err
	})
	return out, err
}

func secs(d time.Duration) int { return int(d / time.Second) }

// ListRules returns a project's rules by name.
func (s *Store) ListRules(ctx context.Context, projectID string) ([]alert.Rule, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	rows, err := s.pool.Query(ctx, `SELECT `+ruleCols+` FROM alert_rules r JOIN projects p ON p.id = r.project_id
		WHERE r.project_id = $1 ORDER BY r.name`, projectID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list rules: %w", err)
	}
	defer rows.Close()
	out := []alert.Rule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan rule: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Rule returns one rule of a project, or ErrNotFound.
func (s *Store) Rule(ctx context.Context, projectID, id string) (alert.Rule, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	r, err := ruleByID(ctx, s.pool, projectID, id)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return alert.Rule{}, fmt.Errorf("postgres: rule: %w", err)
	}
	return r, err
}

// Closer builds the final notifications of a rule that is disabled or
// deleted while firing (the payload lives in the alert package).
type Closer func(r alert.Rule, channels []alert.Channel, notices []alert.Notice) ([]alert.NewNotification, error)

// closeStates ends a rule's pending and firing states with an event naming
// the reason and deletes its states. With cancel, the rule's other
// undelivered notifications are cancelled and, when close builds them, a
// final alert.resolved notification per channel is queued for every firing
// state (disable and delete; an edit sends nothing).
func closeStates(ctx context.Context, tx pgx.Tx, r alert.Rule, reason string, cancel bool, close Closer) error {
	rows, err := tx.Query(ctx, `SELECT dedup_key, state, since, clear_since, last_value, fired_at, last_notified_at, notify_count,
		coalesce(fingerprint, 0), template, version FROM alert_states WHERE rule_id::text = $1 AND state IN ('pending', 'firing')
		ORDER BY dedup_key FOR UPDATE`, r.ID)
	if err != nil {
		return fmt.Errorf("postgres: read states: %w", err)
	}
	var states []alert.State
	for rows.Next() {
		var st alert.State
		var fp int64
		if err := rows.Scan(&st.DedupKey, &st.State, &st.Since, &st.ClearSince, &st.LastValue, &st.FiredAt, &st.LastNotifiedAt, &st.NotifyCount,
			&fp, &st.Template, &st.Version); err != nil {
			rows.Close()
			return fmt.Errorf("postgres: scan state: %w", err)
		}
		st.Fingerprint = fpFromDB(fp)
		states = append(states, st)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	ids := make([]int64, len(states))
	for i, st := range states {
		to := alert.StateOK
		if st.State == alert.StateFiring {
			to = alert.StateResolved
		}
		if err := tx.QueryRow(ctx, `INSERT INTO alert_events (project_id, rule_id, rule_name, rule_type, dedup_key, from_state, to_state, value, threshold, window_start, window_end, fingerprint, template, reason)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 0, now(), now(), $9, $10, $11) RETURNING id`,
			r.ProjectID, r.ID, r.Name, string(r.Type), st.DedupKey, string(st.State), string(to), st.LastValue, nullFP(st.Fingerprint), st.Template, reason).Scan(&ids[i]); err != nil {
			return fmt.Errorf("postgres: close state: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, "DELETE FROM alert_states WHERE rule_id::text = $1", r.ID); err != nil {
		return fmt.Errorf("postgres: delete states: %w", err)
	}
	if !cancel {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE alert_notifications n SET status = 'cancelled', last_error = $2
		FROM alert_events e WHERE n.event_id = e.id AND e.rule_id::text = $1 AND n.status = 'pending'`, r.ID, reason); err != nil {
		return fmt.Errorf("postgres: cancel deliveries: %w", err)
	}
	if close == nil {
		return nil
	}
	notices := alert.CloseNotices(states, time.Now().UTC())
	if len(notices) == 0 {
		return nil
	}
	channels, err := channelsOfRule(ctx, tx, r.ID)
	if err != nil {
		return err
	}
	notes, err := close(r, channels, notices)
	if err != nil {
		return err
	}
	for _, n := range notes {
		var eventID *int64
		if n.Transition >= 0 && n.Transition < len(ids) {
			eventID = &ids[n.Transition]
		}
		if _, err := tx.Exec(ctx, `INSERT INTO alert_notifications (project_id, event_id, channel_id, channel_name, idempotency_key, event_type, payload)
			VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (idempotency_key) DO NOTHING`,
			r.ProjectID, eventID, n.ChannelID, n.ChannelName, n.IdempotencyKey, n.Event, n.Payload); err != nil {
			return fmt.Errorf("postgres: final notification: %w", err)
		}
	}
	return nil
}

func channelsOfRule(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, ruleID string) ([]alert.Channel, error) {
	rows, err := q.Query(ctx, `SELECT `+channelCols+` FROM notification_channels c JOIN alert_rule_channels rc ON rc.channel_id = c.id
		WHERE rc.rule_id::text = $1 ORDER BY c.name`, ruleID)
	if err != nil {
		return nil, fmt.Errorf("postgres: rule channels: %w", err)
	}
	defer rows.Close()
	var out []alert.Channel
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func measures(a, b alert.Rule) bool {
	pa, _ := json.Marshal(a.Params)
	pb, _ := json.Marshal(b.Params)
	return a.Type != b.Type || string(pa) != string(pb) || a.Window != b.Window
}

// UpdateRule replaces a rule. When what it measures changes (type, params
// or window) its version goes up and its states are closed ("rule changed",
// no notification), so it starts from ok.
func (s *Store) UpdateRule(ctx context.Context, r alert.Rule, a AuditEntry) (alert.Rule, error) {
	params, err := json.Marshal(r.Params)
	if err != nil {
		return alert.Rule{}, fmt.Errorf("postgres: encode params: %w", err)
	}
	var out alert.Rule
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		old, err := scanRule(tx.QueryRow(ctx, `SELECT `+ruleCols+` FROM alert_rules r JOIN projects p ON p.id = r.project_id
			WHERE r.project_id = $1 AND r.id::text = $2 FOR UPDATE OF r`, r.ProjectID, r.ID))
		if err != nil {
			return err
		}
		if err := s.checkChannels(ctx, tx, r.ProjectID, r.Channels); err != nil {
			return err
		}
		changed := measures(old, r)
		if changed {
			if err := closeStates(ctx, tx, old, "rule_changed", false, nil); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE alert_rules SET name = $3, type = $4, params = $5, interval_s = $6, window_s = $7, for_s = $8, clear_for_s = $9,
				repeat_s = $10, note = $11, version = version + $12, updated_at = now(), next_eval_at = least(next_eval_at, now() + make_interval(secs => $6::int))
			WHERE project_id = $1 AND id::text = $2`,
			r.ProjectID, r.ID, r.Name, string(r.Type), params, secs(r.Interval), secs(r.Window), secs(r.For), secs(r.ClearFor), secs(r.Repeat), r.Note, b2i(changed))
		if uniqueName(err) {
			return fmt.Errorf("rule %q: %w", r.Name, ErrConflict)
		}
		if err != nil {
			return fmt.Errorf("postgres: update rule: %w", err)
		}
		if err := setChannels(ctx, tx, r.ID, r.Channels); err != nil {
			return err
		}
		if err := audit(ctx, tx, a); err != nil {
			return err
		}
		out, err = ruleByID(ctx, tx, r.ProjectID, r.ID)
		return err
	})
	return out, err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// SetRuleEnabled enables or disables a rule. Disabling closes its states
// (reason rule_disabled): each firing state gets a final alert.resolved
// notification per channel, and the rule's other undelivered ones are
// cancelled.
func (s *Store) SetRuleEnabled(ctx context.Context, projectID, id string, enabled bool, close Closer, a AuditEntry) (alert.Rule, error) {
	var out alert.Rule
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		old, err := scanRule(tx.QueryRow(ctx, `SELECT `+ruleCols+` FROM alert_rules r JOIN projects p ON p.id = r.project_id
			WHERE r.project_id = $1 AND r.id::text = $2 FOR UPDATE OF r`, projectID, id))
		if err != nil {
			return err
		}
		if !enabled && old.Enabled {
			if err := closeStates(ctx, tx, old, alert.ReasonRuleDisabled, true, close); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE alert_rules SET enabled = $3, updated_at = now(),
			next_eval_at = CASE WHEN $3 THEN now() ELSE next_eval_at END, version = version + CASE WHEN $3 <> enabled THEN 1 ELSE 0 END
			WHERE project_id = $1 AND id::text = $2`, projectID, id, enabled); err != nil {
			return fmt.Errorf("postgres: enable rule: %w", err)
		}
		if err := audit(ctx, tx, a); err != nil {
			return err
		}
		out, err = ruleByID(ctx, tx, projectID, id)
		return err
	})
	return out, err
}

// DeleteRule closes the rule's states like a disable (reason rule_deleted,
// final resolved notifications) and deletes it; its events and
// notifications stay (rule_id becomes null).
func (s *Store) DeleteRule(ctx context.Context, projectID, id string, close Closer, a AuditEntry) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		old, err := scanRule(tx.QueryRow(ctx, `SELECT `+ruleCols+` FROM alert_rules r JOIN projects p ON p.id = r.project_id
			WHERE r.project_id = $1 AND r.id::text = $2 FOR UPDATE OF r`, projectID, id))
		if err != nil {
			return err
		}
		if err := closeStates(ctx, tx, old, alert.ReasonRuleDeleted, true, close); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM alert_rules WHERE id::text = $1", id); err != nil {
			return fmt.Errorf("postgres: delete rule: %w", err)
		}
		return audit(ctx, tx, a)
	})
}

// --- evaluator ---

// DueRules returns enabled rules of enabled projects whose next evaluation
// is due, the longest waiting first.
func (s *Store) DueRules(ctx context.Context, now time.Time, limit int) ([]alert.Rule, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	rows, err := s.pool.Query(ctx, `SELECT `+ruleCols+` FROM alert_rules r JOIN projects p ON p.id = r.project_id
		WHERE r.enabled AND p.disabled_at IS NULL AND r.next_eval_at <= $1 ORDER BY r.next_eval_at, r.id LIMIT $2`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: due rules: %w", err)
	}
	defer rows.Close()
	var out []alert.Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan rule: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RuleStates returns every stored state of a rule by dedup key.
func (s *Store) RuleStates(ctx context.Context, ruleID string) (map[string]alert.State, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	rows, err := s.pool.Query(ctx, `SELECT dedup_key, state, since, clear_since, last_value, fired_at, last_notified_at, notify_count,
		coalesce(fingerprint, 0), template, version FROM alert_states WHERE rule_id::text = $1`, ruleID)
	if err != nil {
		return nil, fmt.Errorf("postgres: rule states: %w", err)
	}
	defer rows.Close()
	out := map[string]alert.State{}
	for rows.Next() {
		var st alert.State
		var fp int64
		if err := rows.Scan(&st.DedupKey, &st.State, &st.Since, &st.ClearSince, &st.LastValue, &st.FiredAt, &st.LastNotifiedAt, &st.NotifyCount,
			&fp, &st.Template, &st.Version); err != nil {
			return nil, fmt.Errorf("postgres: scan state: %w", err)
		}
		st.Fingerprint = fpFromDB(fp)
		out[st.DedupKey] = st
	}
	return out, rows.Err()
}

// RuleChannels returns the channels of a rule, for building payloads.
func (s *Store) RuleChannels(ctx context.Context, ruleID string) ([]alert.Channel, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	return channelsOfRule(ctx, s.pool, ruleID)
}

// ResolvedGroups lists a project's resolved groups (the regression rule).
func (s *Store) ResolvedGroups(ctx context.Context, projectID string) ([]alert.ResolvedGroup, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	rows, err := s.pool.Query(ctx, `SELECT fingerprint, resolved_at FROM error_groups WHERE project_id = $1 AND status = 'resolved'
		ORDER BY resolved_at DESC LIMIT 10000`, projectID)
	if err != nil {
		return nil, fmt.Errorf("postgres: resolved groups: %w", err)
	}
	defer rows.Close()
	var out []alert.ResolvedGroup
	for rows.Next() {
		var fp int64
		var g alert.ResolvedGroup
		if err := rows.Scan(&fp, &g.ResolvedAt); err != nil {
			return nil, err
		}
		g.Fingerprint = fpFromDB(fp)
		out = append(out, g)
	}
	return out, rows.Err()
}

func nullFP(fp uint64) *int64 {
	if fp == 0 {
		return nil
	}
	v := fpToDB(fp)
	return &v
}

// ApplyOutcome writes one evaluation in one transaction: the rule's next
// evaluation time (only if the rule is still enabled at the version that
// was evaluated), the changed states (each with an optimistic version
// check), one event per transition and the outbox rows. Anything changed
// under it returns ErrStale and writes nothing.
func (s *Store) ApplyOutcome(ctx context.Context, r alert.Rule, win alert.Window, out alert.Outcome, notes []alert.NewNotification, next time.Time) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "UPDATE alert_rules SET next_eval_at = $3 WHERE id::text = $1 AND version = $2 AND enabled", r.ID, r.Version, next)
		if err != nil {
			return fmt.Errorf("postgres: advance rule: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return alert.ErrStale
		}
		for _, st := range out.States {
			var tag pgconn.CommandTag
			if st.Version == 0 {
				tag, err = tx.Exec(ctx, `INSERT INTO alert_states (rule_id, dedup_key, state, since, clear_since, last_value, fired_at, last_notified_at, notify_count, fingerprint, template, version)
					VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 1) ON CONFLICT (rule_id, dedup_key) DO NOTHING`,
					r.ID, st.DedupKey, string(st.State), st.Since, st.ClearSince, st.LastValue, st.FiredAt, st.LastNotifiedAt, st.NotifyCount, nullFP(st.Fingerprint), st.Template)
			} else {
				tag, err = tx.Exec(ctx, `UPDATE alert_states SET state = $3, since = $4, clear_since = $5, last_value = $6, fired_at = $7, last_notified_at = $8,
						notify_count = $9, fingerprint = $10, template = $11, version = version + 1
					WHERE rule_id::text = $1 AND dedup_key = $2 AND version = $12`,
					r.ID, st.DedupKey, string(st.State), st.Since, st.ClearSince, st.LastValue, st.FiredAt, st.LastNotifiedAt, st.NotifyCount, nullFP(st.Fingerprint), st.Template, st.Version)
			}
			if err != nil {
				return fmt.Errorf("postgres: write state: %w", err)
			}
			if tag.RowsAffected() != 1 {
				return alert.ErrStale
			}
		}
		ids := make([]int64, len(out.Transitions))
		for i, t := range out.Transitions {
			if err := tx.QueryRow(ctx, `INSERT INTO alert_events (project_id, rule_id, rule_name, rule_type, dedup_key, from_state, to_state, value, threshold, window_start, window_end, fingerprint, template, reason)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) RETURNING id`,
				r.ProjectID, r.ID, r.Name, string(r.Type), t.Key, string(t.From), string(t.To), t.Value, t.Threshold, win.From, win.To,
				nullFP(t.Fingerprint), t.Template, t.Reason).Scan(&ids[i]); err != nil {
				return fmt.Errorf("postgres: write event: %w", err)
			}
		}
		for _, n := range notes {
			var eventID *int64
			if n.Transition >= 0 && n.Transition < len(ids) {
				eventID = &ids[n.Transition]
			}
			if _, err := tx.Exec(ctx, `INSERT INTO alert_notifications (project_id, event_id, channel_id, channel_name, idempotency_key, event_type, payload)
				VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (idempotency_key) DO NOTHING`,
				r.ProjectID, eventID, n.ChannelID, n.ChannelName, n.IdempotencyKey, n.Event, n.Payload); err != nil {
				return fmt.Errorf("postgres: write notification: %w", err)
			}
		}
		return nil
	})
}

// --- outbox ---

// ClaimNotifications takes up to n due rows for worker until lease ends,
// skipping rows other workers hold. Rows whose channel is gone are cancelled.
func (s *Store) ClaimNotifications(ctx context.Context, worker string, n int, now time.Time, lease time.Duration) ([]alert.Claimed, error) {
	var out []alert.Claimed
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE alert_notifications SET status = 'cancelled', last_error = 'channel deleted'
			WHERE status = 'pending' AND channel_id IS NULL`); err != nil {
			return fmt.Errorf("postgres: cancel orphans: %w", err)
		}
		rows, err := tx.Query(ctx, `UPDATE alert_notifications n SET status = 'sending', claimed_by = $1, claimed_until = $3::timestamptz + make_interval(secs => $4)
			WHERE n.id IN (SELECT id FROM alert_notifications WHERE status = 'pending' AND next_attempt_at <= $3 AND channel_id IS NOT NULL
				ORDER BY next_attempt_at, id LIMIT $2 FOR UPDATE SKIP LOCKED)
			RETURNING n.id, n.project_id::text, n.event_type, n.idempotency_key, n.payload, n.attempts, n.test, n.channel_id::text`,
			worker, n, now, lease.Seconds())
		if err != nil {
			return fmt.Errorf("postgres: claim: %w", err)
		}
		type claim struct {
			c       alert.Claimed
			channel string
		}
		var claims []claim
		for rows.Next() {
			var c claim
			if err := rows.Scan(&c.c.ID, &c.c.ProjectID, &c.c.Event, &c.c.IdempotencyKey, &c.c.Payload, &c.c.Attempts, &c.c.Test, &c.channel); err != nil {
				rows.Close()
				return fmt.Errorf("postgres: scan claim: %w", err)
			}
			claims = append(claims, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, c := range claims {
			err := tx.QueryRow(ctx, `SELECT c.id::text, c.name, c.kind, c.url_host, c.include_sample, c.created_at, c.updated_at, c.project_id::text, c.url_sealed, c.secret_sealed
				FROM notification_channels c WHERE c.id::text = $1`, c.channel).
				Scan(&c.c.Channel.ID, &c.c.Channel.Name, &c.c.Channel.Kind, &c.c.Channel.URLHost, &c.c.Channel.IncludeSample, &c.c.Channel.CreatedAt,
					&c.c.Channel.UpdatedAt, &c.c.Channel.ProjectID, &c.c.Channel.URL, &c.c.Channel.Secret)
			if err != nil {
				return fmt.Errorf("postgres: claim channel: %w", err)
			}
			out = append(out, c.c)
		}
		return nil
	})
	return out, err
}

// DeliveryResult is how an attempt ended.
type DeliveryResult struct {
	Status         string // sent, pending (retry), failed or a deferral back to pending
	ResponseStatus *int
	LastError      string
	NextAttemptAt  time.Time
	CountAttempt   bool // false for a rate-limit deferral
}

// FinishNotification records an attempt of a row this worker holds.
func (s *Store) FinishNotification(ctx context.Context, id int64, worker string, r DeliveryResult) error {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	_, err := s.pool.Exec(ctx, `UPDATE alert_notifications SET status = $3, response_status = $4, last_error = $5, next_attempt_at = $6,
			attempts = attempts + $7, claimed_by = NULL, claimed_until = NULL, sent_at = CASE WHEN $3 = 'sent' THEN now() ELSE sent_at END
		WHERE id = $1 AND claimed_by = $2 AND status = 'sending'`,
		id, worker, r.Status, r.ResponseStatus, clip(r.LastError, 300), r.NextAttemptAt, b2i(r.CountAttempt))
	if err != nil {
		return fmt.Errorf("postgres: finish notification: %w", err)
	}
	return nil
}

// ReleaseClaims returns a worker's unfinished rows to pending (shutdown).
func (s *Store) ReleaseClaims(ctx context.Context, worker string) error {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	_, err := s.pool.Exec(ctx, `UPDATE alert_notifications SET status = 'pending', claimed_by = NULL, claimed_until = NULL
		WHERE claimed_by = $1 AND status = 'sending'`, worker)
	return err
}

// RequeueExpired returns rows whose lease ended (a crashed worker) to pending.
func (s *Store) RequeueExpired(ctx context.Context, now time.Time) (int64, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	tag, err := s.pool.Exec(ctx, `UPDATE alert_notifications SET status = 'pending', claimed_by = NULL, claimed_until = NULL
		WHERE status = 'sending' AND claimed_until < $1`, now)
	if err != nil {
		return 0, fmt.Errorf("postgres: requeue: %w", err)
	}
	return tag.RowsAffected(), nil
}

// TestSendLimit is how many test sends a channel allows per window.
const (
	TestSendLimit  = 5
	TestSendWindow = 10 * time.Minute
)

// QueueTestNotification adds a test delivery for a channel (at most
// TestSendLimit per TestSendWindow) and the audit row.
func (s *Store) QueueTestNotification(ctx context.Context, projectID string, ch alert.Channel, key string, payload []byte, now time.Time, a AuditEntry) (int64, error) {
	var id int64
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT 1 FROM notification_channels WHERE id::text = $1 FOR UPDATE", ch.ID); err != nil {
			return fmt.Errorf("postgres: lock channel: %w", err)
		}
		var n int
		var oldest *time.Time
		if err := tx.QueryRow(ctx, `SELECT count(*), min(created_at) FROM alert_notifications WHERE channel_id::text = $1 AND test AND created_at > $2`,
			ch.ID, now.Add(-TestSendWindow)).Scan(&n, &oldest); err != nil {
			return fmt.Errorf("postgres: count tests: %w", err)
		}
		if n >= TestSendLimit {
			wait := time.Minute
			if oldest != nil {
				wait = max(time.Second, (oldest.Add(TestSendWindow).Sub(now) + time.Second - 1).Truncate(time.Second))
			}
			return &RateLimitError{RetryAfter: wait}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO alert_notifications (project_id, channel_id, channel_name, idempotency_key, event_type, payload, test, created_at)
			VALUES ($1, $2, $3, $4, 'alert.test', $5, true, $6) RETURNING id`, projectID, ch.ID, ch.Name, key, payload, now).Scan(&id); err != nil {
			return fmt.Errorf("postgres: queue test: %w", err)
		}
		return audit(ctx, tx, a)
	})
	return id, err
}

// --- history ---

// MaxHistory is the page size of events and notifications.
const MaxHistory = 100

// AlertEvents returns a project's transitions, newest first, before the
// given id (0: from the newest), optionally for one rule.
func (s *Store) AlertEvents(ctx context.Context, projectID, ruleID string, before int64, limit int) ([]alert.EventRecord, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	if before <= 0 {
		before = 1<<63 - 1
	}
	rows, err := s.pool.Query(ctx, `SELECT id, rule_id::text, rule_name, rule_type, dedup_key, from_state, to_state, value, threshold, window_start, window_end,
			coalesce(fingerprint, 0), template, reason, at
		FROM alert_events WHERE project_id = $1 AND id < $2 AND ($3 = '' OR rule_id::text = $3) ORDER BY id DESC LIMIT $4`,
		projectID, before, ruleID, min(max(limit, 1), MaxHistory))
	if err != nil {
		return nil, fmt.Errorf("postgres: events: %w", err)
	}
	defer rows.Close()
	out := []alert.EventRecord{}
	for rows.Next() {
		var e alert.EventRecord
		var fp int64
		if err := rows.Scan(&e.ID, &e.RuleID, &e.RuleName, &e.RuleType, &e.DedupKey, &e.From, &e.To, &e.Value, &e.Threshold, &e.WindowStart, &e.WindowEnd,
			&fp, &e.Template, &e.Reason, &e.At); err != nil {
			return nil, err
		}
		e.Fingerprint = fingerprint.Hex(fpFromDB(fp))
		out = append(out, e)
	}
	return out, rows.Err()
}

// AlertStates returns a project's pending and firing states.
func (s *Store) AlertStates(ctx context.Context, projectID string) ([]alert.StateRecord, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	rows, err := s.pool.Query(ctx, `SELECT s.rule_id::text, r.name, s.dedup_key, s.state, s.since, s.last_value, s.fired_at, coalesce(s.fingerprint, 0), s.template
		FROM alert_states s JOIN alert_rules r ON r.id = s.rule_id
		WHERE r.project_id = $1 AND s.state IN ('pending', 'firing') ORDER BY s.since DESC LIMIT 1000`, projectID)
	if err != nil {
		return nil, fmt.Errorf("postgres: states: %w", err)
	}
	defer rows.Close()
	out := []alert.StateRecord{}
	for rows.Next() {
		var st alert.StateRecord
		var fp int64
		if err := rows.Scan(&st.RuleID, &st.RuleName, &st.DedupKey, &st.State, &st.Since, &st.Value, &st.FiredAt, &fp, &st.Template); err != nil {
			return nil, err
		}
		st.Fingerprint = fingerprint.Hex(fpFromDB(fp))
		out = append(out, st)
	}
	return out, rows.Err()
}

// Notifications returns a project's deliveries, newest first.
func (s *Store) Notifications(ctx context.Context, projectID, channelID, status string, before int64, limit int) ([]alert.NotificationRecord, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	if before <= 0 {
		before = 1<<63 - 1
	}
	rows, err := s.pool.Query(ctx, `SELECT id, channel_id::text, channel_name, event_id, event_type, status, attempts, response_status, last_error, test,
			idempotency_key, created_at, next_attempt_at, sent_at
		FROM alert_notifications WHERE project_id = $1 AND id < $2 AND ($3 = '' OR channel_id::text = $3) AND ($4 = '' OR status = $4)
		ORDER BY id DESC LIMIT $5`, projectID, before, channelID, status, min(max(limit, 1), MaxHistory))
	if err != nil {
		return nil, fmt.Errorf("postgres: notifications: %w", err)
	}
	defer rows.Close()
	out := []alert.NotificationRecord{}
	for rows.Next() {
		var n alert.NotificationRecord
		if err := rows.Scan(&n.ID, &n.ChannelID, &n.ChannelName, &n.EventID, &n.Event, &n.Status, &n.Attempts, &n.ResponseStatus, &n.LastError, &n.Test,
			&n.IdempotencyKey, &n.CreatedAt, &n.NextAttemptAt, &n.SentAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// PruneAlerts deletes events, finished deliveries and resolved group states
// older than before (retention).
func (s *Store) PruneAlerts(ctx context.Context, before time.Time) error {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	for _, q := range []string{
		"DELETE FROM alert_notifications WHERE status IN ('sent', 'failed', 'cancelled') AND created_at < $1",
		"DELETE FROM alert_events e WHERE at < $1 AND NOT EXISTS (SELECT 1 FROM alert_notifications n WHERE n.event_id = e.id)",
		"DELETE FROM alert_states WHERE state IN ('resolved', 'ok') AND since < $1 AND dedup_key LIKE 'fp:%'",
	} {
		if _, err := s.pool.Exec(ctx, q, before); err != nil {
			return fmt.Errorf("postgres: prune: %w", err)
		}
	}
	return nil
}

// --- single writer ---

// EvaluatorLock is the advisory lock key of the alert evaluator.
const EvaluatorLock int64 = 0x4c6f6753616765 // "LogSage"

// TryEvaluatorLock takes the evaluator lock if no other instance holds it.
// It returns nil, nil when another instance has it.
func (s *Store) TryEvaluatorLock(ctx context.Context) (*HeldLock, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("postgres: acquire: %w", err)
	}
	var ok bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", EvaluatorLock).Scan(&ok); err != nil {
		conn.Release()
		return nil, fmt.Errorf("postgres: advisory lock: %w", err)
	}
	if !ok {
		conn.Release()
		return nil, nil
	}
	return &HeldLock{conn: conn}, nil
}

// HeldLock keeps the connection that holds the lock.
type HeldLock struct {
	conn interface {
		Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
		Ping(context.Context) error
		Release()
	}
}

// Alive reports whether the lock's connection still answers (the lock is
// lost with the connection).
func (l *HeldLock) Alive(ctx context.Context) bool { return l.conn.Ping(ctx) == nil }

// Release unlocks and returns the connection.
func (l *HeldLock) Release(ctx context.Context) {
	_, _ = l.conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", EvaluatorLock)
	l.conn.Release()
}
