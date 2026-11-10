package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sinanturdu/logsage/backend/internal/alert"
	"github.com/sinanturdu/logsage/backend/internal/incident"
)

// IncidentRef is the incident a transition or a state key belongs to.
type IncidentRef struct {
	ID     string
	Number int
}

// SetIncidentConfig sets the correlation windows used by ApplyOutcome
// (INCIDENT_MERGE_WINDOW, INCIDENT_REOPEN_WINDOW); the default otherwise.
func (s *Store) SetIncidentConfig(cfg incident.Config) { s.incidents = cfg }

func (s *Store) incidentConfig() incident.Config {
	if s.incidents.MergeWindow == 0 {
		return incident.DefaultConfig
	}
	return s.incidents
}

// correlateOutcome links the outcome's transitions of a rule with
// open_incident to incidents, inside the evaluator's transaction (ADR 0026
// section 2). It returns the incident of each transition into firing, and
// of each key that is linked to a non-resolved incident (for repeats).
func correlateOutcome(ctx context.Context, tx pgx.Tx, r alert.Rule, out alert.Outcome, cfg incident.Config, now time.Time) (map[int]IncidentRef, map[string]IncidentRef, error) {
	byTransition := map[int]IncidentRef{}
	byKey := map[string]IncidentRef{}
	if len(out.Transitions) == 0 && !hasRepeat(out) {
		return byTransition, byKey, nil
	}
	if !r.OpenIncident {
		// Links made before open_incident was turned off still close.
		var linked bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM incident_alerts WHERE rule_key = $1 AND closed_at IS NULL)", r.ID).Scan(&linked); err != nil || !linked {
			return byTransition, byKey, err
		}
	}
	if err := lockIncidents(ctx, tx, r.ProjectID); err != nil {
		return nil, nil, err
	}
	for i, t := range out.Transitions {
		switch {
		case t.To == alert.StateFiring && r.OpenIncident:
			ref, ok, err := linkFiring(ctx, tx, r, t, cfg, now)
			if err != nil {
				return nil, nil, err
			}
			if ok {
				byTransition[i] = ref
				byKey[t.Key] = ref
			}
		case t.To == alert.StateResolved && t.From == alert.StateFiring:
			refs, err := closeLink(ctx, tx, r, t.Key, "resolved", &t, now)
			if err != nil {
				return nil, nil, err
			}
			if len(refs) > 0 {
				byTransition[i] = refs[0] // the resolved notice names the incident too
			}
		}
	}
	// Repeats: the key's open incident, if any.
	for _, n := range out.Notices {
		if n.Transition >= 0 || n.Event != alert.EventFiring {
			continue
		}
		if _, ok := byKey[n.Key]; ok {
			continue
		}
		var ref IncidentRef
		err := tx.QueryRow(ctx, `SELECT i.id::text, i.number FROM incident_alerts a JOIN incidents i ON i.id = a.incident_id
			WHERE a.rule_key = $1 AND a.state_key = $2 AND i.status <> 'resolved' ORDER BY a.linked_at DESC LIMIT 1`, r.ID, n.Key).Scan(&ref.ID, &ref.Number)
		if err == nil {
			byKey[n.Key] = ref
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, fmt.Errorf("postgres: repeat incident: %w", err)
		}
	}
	return byTransition, byKey, nil
}

func hasRepeat(out alert.Outcome) bool {
	for _, n := range out.Notices {
		if n.Transition < 0 {
			return true
		}
	}
	return false
}

// alertServices: the rule's service filter plus where a group key was seen.
func alertServices(r alert.Rule, t alert.Transition) []string {
	return incident.MergeServices(slices.Clone(r.Params.Services), t.Services)
}

func linkFiring(ctx context.Context, tx pgx.Tx, r alert.Rule, t alert.Transition, cfg incident.Config, now time.Time) (IncidentRef, bool, error) {
	a := incident.Alert{RuleID: r.ID, Key: t.Key, RuleName: r.Name, Services: alertServices(r, t)}
	rows, err := tx.Query(ctx, `SELECT i.id::text, i.number, i.status, i.services, i.last_activity_at, i.resolved_at,
			EXISTS (SELECT 1 FROM incident_alerts a WHERE a.incident_id = i.id AND a.rule_key = $2 AND a.state_key = $3)
		FROM incidents i WHERE i.project_id = $1 AND i.status <> 'resolved'`, r.ProjectID, r.ID, t.Key)
	if err != nil {
		return IncidentRef{}, false, fmt.Errorf("postgres: incident candidates: %w", err)
	}
	var open []incident.Candidate
	for rows.Next() {
		var c incident.Candidate
		var st string
		if err := rows.Scan(&c.ID, &c.Number, &st, &c.Services, &c.LastActivity, &c.ResolvedAt, &c.LinkedHere); err != nil {
			rows.Close()
			return IncidentRef{}, false, err
		}
		c.Status = incident.Status(st)
		open = append(open, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return IncidentRef{}, false, err
	}
	var last *incident.Candidate
	var c incident.Candidate
	var st string
	err = tx.QueryRow(ctx, `SELECT i.id::text, i.number, i.status, i.resolved_at FROM incident_alerts a JOIN incidents i ON i.id = a.incident_id
		WHERE a.rule_key = $1 AND a.state_key = $2 AND i.project_id = $3 ORDER BY i.opened_at DESC, i.number DESC LIMIT 1`, r.ID, t.Key, r.ProjectID).Scan(&c.ID, &c.Number, &st, &c.ResolvedAt)
	switch {
	case err == nil:
		c.Status = incident.Status(st)
		last = &c
	case !errors.Is(err, pgx.ErrNoRows):
		return IncidentRef{}, false, fmt.Errorf("postgres: last incident: %w", err)
	}
	d := incident.Correlate(a, open, last, len(open), now, cfg)
	var ref IncidentRef
	switch d.Kind {
	case incident.Skip:
		return IncidentRef{}, false, nil
	case incident.Attach:
		ref.ID = d.IncidentID
	case incident.Reopen:
		ref.ID = d.IncidentID
		if _, err := tx.Exec(ctx, `UPDATE incidents SET status = 'open', resolved_at = NULL, resolved_by = NULL, version = version + 1 WHERE id::text = $1`, ref.ID); err != nil {
			return IncidentRef{}, false, fmt.Errorf("postgres: reopen incident: %w", err)
		}
		if _, err := addEvent(ctx, tx, ref.ID, incident.EvReopened, nil, now, incident.ReopenedPayload{RuleID: r.ID}, nil, ""); err != nil {
			return IncidentRef{}, false, err
		}
	case incident.Create:
		var recurrence *string
		opened := incident.OpenedPayload{Source: "auto", RuleID: r.ID, RuleName: r.Name}
		if d.RecurrenceOf != "" {
			recurrence = &d.RecurrenceOf
			opened.RecurrenceOf = last.Number
		}
		sev := incident.Severity(r.IncidentSeverity)
		if sev == "" {
			sev = incident.High
		}
		id, _, err := insertIncident(ctx, tx, r.ProjectID, incident.New{Title: incident.Title(r.Name, a.Services), Severity: sev, Services: a.Services}, "auto", nil, recurrence, now, opened)
		if err != nil {
			return IncidentRef{}, false, err
		}
		ref.ID = id
	}
	if err := tx.QueryRow(ctx, "SELECT number FROM incidents WHERE id::text = $1", ref.ID).Scan(&ref.Number); err != nil {
		return IncidentRef{}, false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO incident_alerts (incident_id, rule_id, rule_key, state_key, rule_name, services, linked_at)
		VALUES ($1::uuid, $2::uuid, $2, $3, $4, $5, $6)
		ON CONFLICT (incident_id, rule_key, state_key) DO UPDATE SET closed_at = NULL, close_reason = '', rule_name = EXCLUDED.rule_name, services = EXCLUDED.services`,
		ref.ID, r.ID, t.Key, r.Name, a.Services, now); err != nil {
		return IncidentRef{}, false, fmt.Errorf("postgres: link alert: %w", err)
	}
	var have []string
	if err := tx.QueryRow(ctx, "SELECT services FROM incidents WHERE id::text = $1", ref.ID).Scan(&have); err != nil {
		return IncidentRef{}, false, err
	}
	if merged := incident.MergeServices(have, a.Services); !slices.Equal(merged, have) {
		if _, err := tx.Exec(ctx, "UPDATE incidents SET services = $2 WHERE id::text = $1", ref.ID, merged); err != nil {
			return IncidentRef{}, false, fmt.Errorf("postgres: incident services: %w", err)
		}
	}
	if err := addAlertEvent(ctx, tx, ref.ID, incident.EvAlertFiring, now, alertPayload(r, t)); err != nil {
		return IncidentRef{}, false, err
	}
	if t.Fingerprint != 0 {
		if _, err := linkGroup(ctx, tx, ref.ID, t.Fingerprint, t.Template, "auto", nil, now); err != nil {
			return IncidentRef{}, false, err
		}
	}
	return ref, true, refreshAllResolved(ctx, tx, ref.ID, now)
}

func alertPayload(r alert.Rule, t alert.Transition) incident.AlertPayload {
	p := incident.AlertPayload{RuleID: r.ID, RuleName: r.Name, RuleType: string(r.Type), Key: t.Key, Value: t.Value, Threshold: t.Threshold, Template: t.Template}
	if t.Fingerprint != 0 {
		p.Fingerprint = fmt.Sprintf("%016x", t.Fingerprint)
	}
	return p
}

// closeLink closes the rule and key's open links: "resolved" when the state
// resolved (an alert_resolved event), else alert_closed with the reason.
func closeLink(ctx context.Context, tx pgx.Tx, r alert.Rule, key, reason string, t *alert.Transition, now time.Time) ([]IncidentRef, error) {
	rows, err := tx.Query(ctx, `UPDATE incident_alerts a SET closed_at = $3, close_reason = $4 FROM incidents i
		WHERE i.id = a.incident_id AND a.rule_key = $1 AND a.state_key = $2 AND a.closed_at IS NULL RETURNING i.id::text, i.number`, r.ID, key, now, reason)
	if err != nil {
		return nil, fmt.Errorf("postgres: close incident link: %w", err)
	}
	var refs []IncidentRef
	for rows.Next() {
		var ref IncidentRef
		if err := rows.Scan(&ref.ID, &ref.Number); err != nil {
			rows.Close()
			return nil, err
		}
		refs = append(refs, ref)
	}
	rows.Close()
	for _, ref := range refs {
		if t != nil {
			err = addAlertEvent(ctx, tx, ref.ID, incident.EvAlertResolved, now, alertPayload(r, *t))
		} else {
			_, err = addEvent(ctx, tx, ref.ID, incident.EvAlertClosed, nil, now, incident.AlertClosedPayload{RuleID: r.ID, RuleName: r.Name, Key: key, Reason: reason}, nil, "")
		}
		if err != nil {
			return nil, err
		}
		if err := refreshAllResolved(ctx, tx, ref.ID, now); err != nil {
			return nil, err
		}
	}
	return refs, nil
}

// closeRuleLinks closes every open link of a rule (disable, delete, or an
// edit that resets its states) with alert_closed and the reason.
func closeRuleLinks(ctx context.Context, tx pgx.Tx, r alert.Rule, reason string, now time.Time) error {
	rows, err := tx.Query(ctx, "SELECT DISTINCT state_key FROM incident_alerts WHERE rule_key = $1 AND closed_at IS NULL ORDER BY state_key", r.ID)
	if err != nil {
		return fmt.Errorf("postgres: rule links: %w", err)
	}
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, k)
	}
	rows.Close()
	if len(keys) > 0 {
		if err := lockIncidents(ctx, tx, r.ProjectID); err != nil {
			return err
		}
	}
	for _, k := range keys {
		if _, err := closeLink(ctx, tx, r, k, reason, nil, now); err != nil {
			return err
		}
	}
	return nil
}
