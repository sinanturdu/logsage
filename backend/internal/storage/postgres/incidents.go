package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sinanturdu/logsage/backend/internal/alert"
	"github.com/sinanturdu/logsage/backend/internal/incident"
)

// Incidents (ADR 0026). Every write runs in one transaction under the
// project's incident lock, so the evaluator's automatic path and people's
// changes never interleave; numbers come from incident_counters in the same
// transaction.

var (
	// ErrTransition is a status change that the workflow does not allow.
	ErrTransition = errors.New("status change not allowed")
	// ErrNotMember is an assignee who is not a member of the project.
	ErrNotMember = errors.New("not a member of the project")
	// ErrForbidden is a comment deletion by someone who may not delete it.
	ErrForbidden = errors.New("not allowed")
)

const incidentCols = `i.id::text, i.project_id::text, i.number, i.title, i.severity, i.status, i.source, i.assignee::text, coalesce(u.email, ''), i.summary, i.services,
	i.opened_at, i.acknowledged_at, i.acknowledged_by::text, i.resolved_at, i.resolved_by::text, i.last_activity_at, r.number, i.all_alerts_resolved,
	i.events_dropped, i.version,
	(SELECT count(*) FROM incident_alerts a WHERE a.incident_id = i.id), (SELECT count(*) FROM incident_groups g WHERE g.incident_id = i.id)`

const incidentFrom = ` FROM incidents i LEFT JOIN users u ON u.id = i.assignee LEFT JOIN incidents r ON r.id = i.recurrence_of`

func scanIncident(row pgx.Row) (incident.Incident, error) {
	var in incident.Incident
	var sev, status string
	err := row.Scan(&in.ID, &in.ProjectID, &in.Number, &in.Title, &sev, &status, &in.Source, &in.Assignee, &in.AssigneeEmail, &in.Summary, &in.Services,
		&in.OpenedAt, &in.AcknowledgedAt, &in.AcknowledgedBy, &in.ResolvedAt, &in.ResolvedBy, &in.LastActivityAt, &in.RecurrenceOf, &in.AllAlertsResolved,
		&in.EventsDropped, &in.Version, &in.AlertCount, &in.GroupCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return incident.Incident{}, ErrNotFound
	}
	in.Severity, in.Status = incident.Severity(sev), incident.Status(status)
	if in.Services == nil {
		in.Services = []string{}
	}
	return in, err
}

// lockIncidents serializes incident writes of one project.
func lockIncidents(ctx context.Context, tx pgx.Tx, projectID string) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('incidents:' || $1, 0))", projectID)
	if err != nil {
		return fmt.Errorf("postgres: incident lock: %w", err)
	}
	return nil
}

func nextNumber(ctx context.Context, tx pgx.Tx, projectID string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `INSERT INTO incident_counters (project_id, last) VALUES ($1, 1)
		ON CONFLICT (project_id) DO UPDATE SET last = incident_counters.last + 1 RETURNING last`, projectID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("postgres: incident number: %w", err)
	}
	return n, nil
}

func openCount(ctx context.Context, tx pgx.Tx, projectID string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, "SELECT count(*) FROM incidents WHERE project_id = $1 AND status <> 'resolved'", projectID).Scan(&n)
	return n, err
}

// insertIncident creates the row, its number and the opened event.
func insertIncident(ctx context.Context, tx pgx.Tx, projectID string, n incident.New, source string, actor *string, recurrenceOf *string, at time.Time, opened incident.OpenedPayload) (string, int, error) {
	num, err := nextNumber(ctx, tx, projectID)
	if err != nil {
		return "", 0, err
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO incidents (project_id, number, title, severity, status, source, summary, services, opened_at, last_activity_at, recurrence_of, created_by)
		VALUES ($1, $2, $3, $4, 'open', $5, $6, $7, $8, $8, $9::uuid, $10) RETURNING id::text`,
		projectID, num, n.Title, string(n.Severity), source, n.Summary, incident.MergeServices(nil, n.Services), at, recurrenceOf, actor).Scan(&id)
	if err != nil {
		return "", 0, fmt.Errorf("postgres: insert incident: %w", err)
	}
	if _, err := addEvent(ctx, tx, id, incident.EvOpened, actor, at, opened, nil, ""); err != nil {
		return "", 0, err
	}
	return id, num, nil
}

// addEvent appends a timeline event, or counts it as dropped past the cap.
// It also moves the incident's last activity. It returns the event id (0
// when dropped).
func addEvent(ctx context.Context, tx pgx.Tx, incidentID string, t incident.EventType, actor *string, at time.Time, payload any, body *string, alertKey string) (int64, error) {
	raw, err := incident.EncodePayload(t, payload)
	if err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `UPDATE incidents SET event_count = event_count + 1, last_activity_at = greatest(last_activity_at, $2)
		WHERE id::text = $1 AND event_count < $3`, incidentID, at, incident.MaxEvents)
	if err != nil {
		return 0, fmt.Errorf("postgres: count event: %w", err)
	}
	if tag.RowsAffected() == 0 {
		_, err := tx.Exec(ctx, "UPDATE incidents SET events_dropped = events_dropped + 1, last_activity_at = greatest(last_activity_at, $2) WHERE id::text = $1", incidentID, at)
		return 0, err
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO incident_events (incident_id, type, actor, at, last_at, payload, body, alert_key)
		VALUES ($1::uuid, $2, $3::uuid, $4, $4, $5, $6, $7) RETURNING id`, incidentID, string(t), actor, at, raw, body, alertKey).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("postgres: insert incident event: %w", err)
	}
	return id, nil
}

// addAlertEvent is addEvent for alert_firing and alert_resolved, with the
// flapping rule: a repeat within incident.FlapWindow only counts on the
// previous event of the same rule, key and type.
func addAlertEvent(ctx context.Context, tx pgx.Tx, incidentID string, t incident.EventType, at time.Time, p incident.AlertPayload) error {
	key := p.RuleID + "/" + p.Key
	var prevID int64
	var prevAt time.Time
	err := tx.QueryRow(ctx, `SELECT id, last_at FROM incident_events WHERE incident_id::text = $1 AND alert_key = $2 AND type = $3 ORDER BY id DESC LIMIT 1`,
		incidentID, key, string(t)).Scan(&prevID, &prevAt)
	have := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("postgres: previous alert event: %w", err)
	}
	if incident.Collapse(prevAt, have, at) {
		raw, err := incident.EncodePayload(t, p)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE incident_events SET count = count + 1, last_at = $2, payload = $3 WHERE id = $1", prevID, at, raw); err != nil {
			return fmt.Errorf("postgres: count alert event: %w", err)
		}
		_, err = tx.Exec(ctx, "UPDATE incidents SET last_activity_at = greatest(last_activity_at, $2) WHERE id::text = $1", incidentID, at)
		return err
	}
	_, err = addEvent(ctx, tx, incidentID, t, nil, at, p, nil, key)
	return err
}

// refreshAllResolved recomputes all_alerts_resolved and writes one
// all_alerts_resolved event when it becomes true.
func refreshAllResolved(ctx context.Context, tx pgx.Tx, incidentID string, at time.Time) error {
	var was, now bool
	err := tx.QueryRow(ctx, `SELECT all_alerts_resolved,
			EXISTS (SELECT 1 FROM incident_alerts a WHERE a.incident_id = i.id) AND NOT EXISTS (SELECT 1 FROM incident_alerts a WHERE a.incident_id = i.id AND a.closed_at IS NULL)
		FROM incidents i WHERE id::text = $1`, incidentID).Scan(&was, &now)
	if err != nil {
		return fmt.Errorf("postgres: all alerts resolved: %w", err)
	}
	if was == now {
		return nil
	}
	if _, err := tx.Exec(ctx, "UPDATE incidents SET all_alerts_resolved = $2 WHERE id::text = $1", incidentID, now); err != nil {
		return err
	}
	if now {
		_, err = addEvent(ctx, tx, incidentID, incident.EvAllAlertsResolved, nil, at, incident.EmptyPayload{}, nil, "")
	}
	return err
}

// --- manual writes ---

// CreateIncident creates a manual incident (409 at the open incident cap).
func (s *Store) CreateIncident(ctx context.Context, projectID string, n incident.New, actor string, a AuditEntry) (incident.Incident, error) {
	var out incident.Incident
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if err := lockIncidents(ctx, tx, projectID); err != nil {
			return err
		}
		c, err := openCount(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if c >= incident.MaxOpenIncidents {
			return fmt.Errorf("incidents: %w", ErrLimit)
		}
		now := time.Now().UTC()
		id, num, err := insertIncident(ctx, tx, projectID, n, "manual", &actor, nil, now, incident.OpenedPayload{Source: "manual"})
		if err != nil {
			return err
		}
		if n.Assignee != "" {
			if err := assign(ctx, tx, projectID, id, &n.Assignee, actor, now); err != nil {
				return err
			}
		}
		a.Target += "#" + strconv.Itoa(num)
		if err := audit(ctx, tx, a); err != nil {
			return err
		}
		out, err = scanIncident(tx.QueryRow(ctx, `SELECT `+incidentCols+incidentFrom+` WHERE i.id::text = $1`, id))
		return err
	})
	return out, err
}

// forUpdate locks the project's incidents and returns the incident.
func forUpdate(ctx context.Context, tx pgx.Tx, projectID string, number int) (incident.Incident, error) {
	if err := lockIncidents(ctx, tx, projectID); err != nil {
		return incident.Incident{}, err
	}
	return scanIncident(tx.QueryRow(ctx, `SELECT `+incidentCols+incidentFrom+` WHERE i.project_id = $1 AND i.number = $2`, projectID, number))
}

// write runs fn on the incident under the lock, checks the version when
// version > 0, bumps it, audits and returns the incident as it is after.
func (s *Store) write(ctx context.Context, projectID string, number, version int, a AuditEntry, fn func(tx pgx.Tx, in incident.Incident, now time.Time) error) (incident.Incident, error) {
	var out incident.Incident
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		in, err := forUpdate(ctx, tx, projectID, number)
		if err != nil {
			return err
		}
		if version > 0 && in.Version != version {
			return alert.ErrStale
		}
		now := time.Now().UTC()
		if err := fn(tx, in, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE incidents SET version = version + 1 WHERE id::text = $1", in.ID); err != nil {
			return err
		}
		if a.Action != "" {
			if err := audit(ctx, tx, a); err != nil {
				return err
			}
		}
		out, err = scanIncident(tx.QueryRow(ctx, `SELECT `+incidentCols+incidentFrom+` WHERE i.id::text = $1`, in.ID))
		return err
	})
	return out, err
}

// UpdateIncident changes the title, severity or summary (version checked).
func (s *Store) UpdateIncident(ctx context.Context, projectID string, number, version int, p incident.Patch, actor string, a AuditEntry) (incident.Incident, error) {
	return s.write(ctx, projectID, number, version, a, func(tx pgx.Tx, in incident.Incident, now time.Time) error {
		title, summary, sev := in.Title, in.Summary, in.Severity
		if p.Title != nil {
			title = *p.Title
		}
		if p.Summary != nil {
			summary = *p.Summary
		}
		if p.Severity != nil {
			sev = *p.Severity
		}
		if _, err := tx.Exec(ctx, "UPDATE incidents SET title = $2, summary = $3, severity = $4, last_activity_at = greatest(last_activity_at, $5) WHERE id::text = $1",
			in.ID, title, summary, string(sev), now); err != nil {
			return fmt.Errorf("postgres: update incident: %w", err)
		}
		if sev != in.Severity {
			_, err := addEvent(ctx, tx, in.ID, incident.EvSeverityChanged, &actor, now, incident.SeverityPayload{From: in.Severity, To: sev}, nil, "")
			return err
		}
		return nil
	})
}

// ChangeStatus applies acknowledge, resolve or reopen (version checked);
// a note becomes a comment.
func (s *Store) ChangeStatus(ctx context.Context, projectID string, number, version int, action, note, actor string, a AuditEntry) (incident.Incident, error) {
	return s.write(ctx, projectID, number, version, a, func(tx pgx.Tx, in incident.Incident, now time.Time) error {
		to, ok := incident.Transition(in.Status, action)
		if !ok {
			return fmt.Errorf("%s from %s: %w", action, in.Status, ErrTransition)
		}
		var err error
		switch to {
		case incident.StatusAcknowledged:
			_, err = tx.Exec(ctx, "UPDATE incidents SET status = $2, acknowledged_at = $3, acknowledged_by = $4 WHERE id::text = $1", in.ID, string(to), now, actor)
		case incident.StatusResolved:
			_, err = tx.Exec(ctx, "UPDATE incidents SET status = $2, resolved_at = $3, resolved_by = $4 WHERE id::text = $1", in.ID, string(to), now, actor)
		default:
			_, err = tx.Exec(ctx, "UPDATE incidents SET status = $2, resolved_at = NULL, resolved_by = NULL WHERE id::text = $1", in.ID, string(to))
		}
		if err != nil {
			return fmt.Errorf("postgres: change status: %w", err)
		}
		if action == "reopen" {
			_, err = addEvent(ctx, tx, in.ID, incident.EvReopened, &actor, now, incident.ReopenedPayload{}, nil, "")
		} else {
			_, err = addEvent(ctx, tx, in.ID, incident.EvStatusChanged, &actor, now, incident.StatusPayload{From: in.Status, To: to}, nil, "")
		}
		if err != nil {
			return err
		}
		if strings.TrimSpace(note) != "" {
			n := strings.TrimSpace(note)
			_, err = addEvent(ctx, tx, in.ID, incident.EvComment, &actor, now, incident.CommentPayload{}, &n, "")
		}
		return err
	})
}

func assign(ctx context.Context, tx pgx.Tx, projectID, incidentID string, userID *string, actor string, now time.Time) error {
	if userID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM project_members WHERE project_id = $1 AND user_id::text = $2)", projectID, *userID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return ErrNotMember
		}
	}
	if _, err := tx.Exec(ctx, "UPDATE incidents SET assignee = $2::uuid WHERE id::text = $1", incidentID, userID); err != nil {
		return fmt.Errorf("postgres: assign: %w", err)
	}
	p := incident.AssignedPayload{}
	if userID != nil {
		p.UserID = *userID
	}
	_, err := addEvent(ctx, tx, incidentID, incident.EvAssigned, &actor, now, p, nil, "")
	return err
}

// Assign sets the assignee (a project member) or clears it (userID nil).
func (s *Store) Assign(ctx context.Context, projectID string, number int, userID *string, actor string, a AuditEntry) (incident.Incident, error) {
	return s.write(ctx, projectID, number, 0, a, func(tx pgx.Tx, in incident.Incident, now time.Time) error {
		return assign(ctx, tx, projectID, in.ID, userID, actor, now)
	})
}

// AddComment adds a comment; it never notifies a channel.
func (s *Store) AddComment(ctx context.Context, projectID string, number int, body, actor string) (incident.Event, error) {
	var ev incident.Event
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		in, err := forUpdate(ctx, tx, projectID, number)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		id, err := addEvent(ctx, tx, in.ID, incident.EvComment, &actor, now, incident.CommentPayload{}, &body, "")
		if err != nil {
			return err
		}
		if id == 0 {
			return fmt.Errorf("events: %w", ErrLimit)
		}
		evs, err := readEvents(ctx, tx, in.ID, id+1, 1)
		if err != nil || len(evs) == 0 {
			return fmt.Errorf("postgres: read comment: %w", err)
		}
		ev = evs[0]
		return nil
	})
	return ev, err
}

// DeleteComment removes a comment's text (the author, or a manager when
// moderate is set) and records comment_deleted.
func (s *Store) DeleteComment(ctx context.Context, projectID string, number int, eventID int64, actor string, moderate bool, a AuditEntry) error {
	_, err := s.write(ctx, projectID, number, 0, a, func(tx pgx.Tx, in incident.Incident, now time.Time) error {
		var author *string
		err := tx.QueryRow(ctx, `SELECT actor::text FROM incident_events WHERE id = $1 AND incident_id::text = $2 AND type = 'comment' AND body IS NOT NULL`,
			eventID, in.ID).Scan(&author)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if !moderate && (author == nil || *author != actor) {
			return ErrForbidden
		}
		if _, err := tx.Exec(ctx, "UPDATE incident_events SET body = NULL WHERE id = $1", eventID); err != nil {
			return err
		}
		_, err = addEvent(ctx, tx, in.ID, incident.EvCommentDeleted, &actor, now, incident.CommentPayload{CommentID: eventID}, nil, "")
		return err
	})
	return err
}

// LinkGroup links an error group by hand (at most incident.MaxGroups).
func (s *Store) LinkGroup(ctx context.Context, projectID string, number int, fp uint64, template, actor string, a AuditEntry) (incident.Incident, error) {
	return s.write(ctx, projectID, number, 0, a, func(tx pgx.Tx, in incident.Incident, now time.Time) error {
		added, err := linkGroup(ctx, tx, in.ID, fp, template, "manual", &actor, now)
		if err != nil {
			return err
		}
		if !added {
			return fmt.Errorf("group: %w", ErrConflict)
		}
		return nil
	})
}

func linkGroup(ctx context.Context, tx pgx.Tx, incidentID string, fp uint64, template, source string, actor *string, now time.Time) (bool, error) {
	var n int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM incident_groups WHERE incident_id::text = $1", incidentID).Scan(&n); err != nil {
		return false, err
	}
	if n >= incident.MaxGroups {
		if source == "auto" {
			return false, nil // the automatic path stops linking at the cap
		}
		return false, fmt.Errorf("groups: %w", ErrLimit)
	}
	tag, err := tx.Exec(ctx, `INSERT INTO incident_groups (incident_id, fingerprint, template, source, linked_at, linked_by)
		VALUES ($1::uuid, $2, $3, $4, $5, $6::uuid) ON CONFLICT DO NOTHING`, incidentID, fpToDB(fp), template, source, now, actor)
	if err != nil {
		return false, fmt.Errorf("postgres: link group: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	_, err = addEvent(ctx, tx, incidentID, incident.EvGroupLinked, actor, now,
		incident.GroupPayload{Fingerprint: fmt.Sprintf("%016x", fp), Template: template, Source: source}, nil, "")
	return true, err
}

// UnlinkGroup removes a group link.
func (s *Store) UnlinkGroup(ctx context.Context, projectID string, number int, fp uint64, actor string, a AuditEntry) (incident.Incident, error) {
	return s.write(ctx, projectID, number, 0, a, func(tx pgx.Tx, in incident.Incident, now time.Time) error {
		var template, source string
		err := tx.QueryRow(ctx, "DELETE FROM incident_groups WHERE incident_id::text = $1 AND fingerprint = $2 RETURNING template, source", in.ID, fpToDB(fp)).Scan(&template, &source)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		_, err = addEvent(ctx, tx, in.ID, incident.EvGroupUnlinked, &actor, now,
			incident.GroupPayload{Fingerprint: fmt.Sprintf("%016x", fp), Template: template, Source: source}, nil, "")
		return err
	})
}

// --- reads ---

// IncidentCursor pages the list by last activity, newest first.
type IncidentCursor struct {
	At time.Time
	ID string
}

// ListIncidents lists a project's incidents, most recently active first.
func (s *Store) ListIncidents(ctx context.Context, projectID string, f incident.Filter, after *IncidentCursor, limit int) ([]incident.Incident, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	where := []string{"i.project_id = $1"}
	args := []any{projectID}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
	}
	if len(f.Status) > 0 {
		st := make([]string, len(f.Status))
		for i, v := range f.Status {
			st[i] = string(v)
		}
		add("i.status = ANY(?)", st)
	}
	if len(f.Severity) > 0 {
		sv := make([]string, len(f.Severity))
		for i, v := range f.Severity {
			sv[i] = string(v)
		}
		add("i.severity = ANY(?)", sv)
	}
	switch f.Assignee {
	case "":
	case "none":
		where = append(where, "i.assignee IS NULL")
	default:
		add("i.assignee::text = ?", f.Assignee)
	}
	if f.Service != "" {
		add("? = ANY(i.services)", f.Service)
	}
	if f.From != nil {
		add("i.opened_at >= ?", *f.From)
	}
	if f.To != nil {
		add("i.opened_at < ?", *f.To)
	}
	if f.Q != "" {
		add("strpos(lower(i.title), lower(?)) > 0", f.Q)
	}
	if after != nil {
		args = append(args, after.At, after.ID)
		where = append(where, fmt.Sprintf("(i.last_activity_at, i.id::text) < ($%d, $%d)", len(args)-1, len(args)))
	}
	args = append(args, min(max(limit, 1), 100))
	rows, err := s.pool.Query(ctx, `SELECT `+incidentCols+incidentFrom+` WHERE `+strings.Join(where, " AND ")+
		fmt.Sprintf(" ORDER BY i.last_activity_at DESC, i.id::text DESC LIMIT $%d", len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: list incidents: %w", err)
	}
	defer rows.Close()
	out := []incident.Incident{}
	for rows.Next() {
		in, err := scanIncident(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan incident: %w", err)
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// IncidentSummary counts a project's incidents per status.
func (s *Store) IncidentSummary(ctx context.Context, projectID string) (incident.Summary, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	var out incident.Summary
	err := s.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'open'), count(*) FILTER (WHERE status = 'acknowledged'), count(*) FILTER (WHERE status = 'resolved')
		FROM incidents WHERE project_id = $1`, projectID).Scan(&out.Open, &out.Acknowledged, &out.Resolved)
	return out, err
}

// IncidentDetail is an incident with its links.
type IncidentDetail struct {
	Incident incident.Incident
	Alerts   []incident.LinkedAlert
	Groups   []incident.LinkedGroup
}

// Incident reads one incident by number with its alerts and groups.
func (s *Store) Incident(ctx context.Context, projectID string, number int) (IncidentDetail, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	in, err := scanIncident(s.pool.QueryRow(ctx, `SELECT `+incidentCols+incidentFrom+` WHERE i.project_id = $1 AND i.number = $2`, projectID, number))
	if err != nil {
		return IncidentDetail{}, err
	}
	d := IncidentDetail{Incident: in, Alerts: []incident.LinkedAlert{}, Groups: []incident.LinkedGroup{}}
	rows, err := s.pool.Query(ctx, `SELECT a.rule_id::text, a.rule_name, a.state_key, a.services, a.linked_at, a.closed_at, a.close_reason, coalesce(st.state, '')
		FROM incident_alerts a LEFT JOIN alert_states st ON st.rule_id = a.rule_id AND st.dedup_key = a.state_key
		WHERE a.incident_id::text = $1 ORDER BY a.linked_at, a.rule_name, a.state_key`, in.ID)
	if err != nil {
		return IncidentDetail{}, fmt.Errorf("postgres: incident alerts: %w", err)
	}
	for rows.Next() {
		var la incident.LinkedAlert
		if err := rows.Scan(&la.RuleID, &la.RuleName, &la.StateKey, &la.Services, &la.LinkedAt, &la.ClosedAt, &la.CloseReason, &la.State); err != nil {
			rows.Close()
			return IncidentDetail{}, err
		}
		d.Alerts = append(d.Alerts, la)
	}
	rows.Close()
	rows, err = s.pool.Query(ctx, `SELECT fingerprint, template, source, linked_at, linked_by::text FROM incident_groups WHERE incident_id::text = $1 ORDER BY linked_at, fingerprint`, in.ID)
	if err != nil {
		return IncidentDetail{}, fmt.Errorf("postgres: incident groups: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var g incident.LinkedGroup
		var fp int64
		if err := rows.Scan(&fp, &g.Template, &g.Source, &g.LinkedAt, &g.LinkedBy); err != nil {
			return IncidentDetail{}, err
		}
		g.Fingerprint = fmt.Sprintf("%016x", fpFromDB(fp))
		d.Groups = append(d.Groups, g)
	}
	return d, rows.Err()
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func readEvents(ctx context.Context, q querier, incidentID string, before int64, limit int) ([]incident.Event, error) {
	if before <= 0 {
		before = 1<<63 - 1
	}
	rows, err := q.Query(ctx, `SELECT e.id, e.type, e.actor::text, coalesce(u.email, ''), e.at, e.last_at, e.count, e.payload, e.body
		FROM incident_events e LEFT JOIN users u ON u.id = e.actor WHERE e.incident_id::text = $1 AND e.id < $2 ORDER BY e.id DESC LIMIT $3`,
		incidentID, before, min(max(limit, 1), MaxHistory))
	if err != nil {
		return nil, fmt.Errorf("postgres: incident events: %w", err)
	}
	defer rows.Close()
	out := []incident.Event{}
	for rows.Next() {
		var e incident.Event
		var t string
		var raw []byte
		if err := rows.Scan(&e.ID, &t, &e.Actor, &e.ActorEmail, &e.At, &e.LastAt, &e.Count, &raw, &e.Body); err != nil {
			return nil, err
		}
		e.Type = incident.EventType(t)
		e.Payload = json.RawMessage(raw)
		out = append(out, e)
	}
	return out, rows.Err()
}

// IncidentEvents pages an incident's timeline, newest first.
func (s *Store) IncidentEvents(ctx context.Context, incidentID string, before int64, limit int) ([]incident.Event, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	return readEvents(ctx, s.pool, incidentID, before, limit)
}

// ProjectMember is a member as the members endpoint shows it.
type ProjectMember struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Role   Role   `json:"role"`
}

// ProjectMembers lists a project's members (enabled users only).
func (s *Store) ProjectMembers(ctx context.Context, projectID string) ([]ProjectMember, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	rows, err := s.pool.Query(ctx, `SELECT u.id::text, u.email, m.role FROM project_members m JOIN users u ON u.id = m.user_id
		WHERE m.project_id = $1 AND u.disabled_at IS NULL ORDER BY lower(u.email)`, projectID)
	if err != nil {
		return nil, fmt.Errorf("postgres: project members: %w", err)
	}
	defer rows.Close()
	out := []ProjectMember{}
	for rows.Next() {
		var m ProjectMember
		var r string
		if err := rows.Scan(&m.UserID, &m.Email, &r); err != nil {
			return nil, err
		}
		m.Role = Role(r)
		out = append(out, m)
	}
	return out, rows.Err()
}
