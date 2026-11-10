package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/alert"
	"github.com/sinanturdu/logsage/backend/internal/incident"
)

// incidentRule creates a rule with open_incident.
func (f alertFixture) incidentRule(t *testing.T, name string, typ alert.Type, services ...string) alert.Rule {
	t.Helper()
	r := alert.Rule{ProjectID: f.project.ID, Name: name, Type: typ, Params: alert.Params{ThresholdPercent: 5, Services: services},
		Interval: time.Minute, Window: 5 * time.Minute, Enabled: true, Channels: []string{f.channel.ID}, OpenIncident: true, IncidentSeverity: "critical"}
	if typ == alert.NewGroup {
		r.Params.ThresholdPercent = 0
	}
	r.Normalize()
	if err := alert.Validate(r); err != nil {
		t.Fatal(err)
	}
	out, err := f.st.CreateRule(context.Background(), r, f.user, AuditEntry{UserID: f.user, Action: "alert_rule.create", Target: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// evaluate runs one evaluation of r at now with the observations, as the evaluator does.
func (f alertFixture) evaluate(t *testing.T, r alert.Rule, now time.Time, obs ...alert.Observation) alert.Outcome {
	t.Helper()
	ctx := context.Background()
	prev, err := f.st.RuleStates(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	cur, err := f.st.Rule(ctx, f.project.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	out := alert.Step(cur, prev, obs, now)
	out.EvaluatedAt = now
	var notes []alert.NewNotification
	win := alert.Window{From: now.Add(-5 * time.Minute), To: now}
	for _, n := range out.Notices {
		ns, err := alert.Notifications(cur, win, n, []alert.Channel{f.channel}, "", "", now)
		if err != nil {
			t.Fatal(err)
		}
		notes = append(notes, ns...)
	}
	if err := f.st.ApplyOutcome(ctx, cur, win, out, notes, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	return out
}

func fire(v float64) alert.Observation { return alert.Observation{Key: alert.RuleKey, Value: v, Threshold: 5, Fire: true} }
func clear() alert.Observation        { return alert.Observation{Key: alert.RuleKey, Value: 0, Threshold: 5, Clear: true} }
func group(fp uint64, tmpl string, services ...string) alert.Observation {
	return alert.Observation{Key: alert.GroupKey(fp), Value: 3, Threshold: 1, Fire: true, Fingerprint: fp, Template: tmpl, Services: services, Sample: "RAW secret=hunter2"}
}

func eventTypes(t *testing.T, f alertFixture, incidentID string) string {
	t.Helper()
	evs, err := f.st.IncidentEvents(context.Background(), incidentID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := len(evs) - 1; i >= 0; i-- {
		s := string(evs[i].Type)
		if evs[i].Count > 1 {
			s += "x" + string(rune('0'+evs[i].Count))
		}
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

func TestAlertsOpenAndGroupOneIncident(t *testing.T) {
	f, count := newAlertFixture(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)
	rate := f.incidentRule(t, "Error rate", alert.ErrorRate, "payments")
	groups := f.incidentRule(t, "New groups", alert.NewGroup)
	f.evaluate(t, rate, t0, fire(40))
	f.evaluate(t, groups, t0.Add(time.Minute), group(0xabc, "gateway timeout after <n> ms", "payments", "checkout"))
	if n := count("SELECT count(*) FROM incidents"); n != 1 {
		t.Fatalf("%d incidents, want 1 (service overlap)", n)
	}
	d, err := f.st.Incident(ctx, f.project.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	in := d.Incident
	if in.Source != "auto" || in.Severity != incident.Critical || in.Title != "Error rate in payments" || strings.Join(in.Services, ",") != "checkout,payments" {
		t.Fatalf("incident %+v", in)
	}
	if len(d.Alerts) != 2 || len(d.Groups) != 1 || d.Groups[0].Fingerprint != "0000000000000abc" || d.Groups[0].Source != "auto" {
		t.Fatalf("alerts %+v groups %+v", d.Alerts, d.Groups)
	}
	// The list rows carry the link counts.
	if list, err := f.st.ListIncidents(ctx, f.project.ID, incident.Filter{}, nil, 10); err != nil || len(list) != 1 || list[0].AlertCount != 2 || list[0].GroupCount != 1 {
		t.Fatalf("list counts %+v %v", list, err)
	}
	if got := eventTypes(t, f, in.ID); got != "opened,alert_firing,alert_firing,group_linked" {
		t.Fatalf("timeline %s", got)
	}
	// Never a raw message in the timeline.
	if count("SELECT count(*) FROM incident_events WHERE payload::text LIKE '%hunter2%'") != 0 {
		t.Fatal("a sample reached the timeline")
	}
	// Both resolve: links close and all_alerts_resolved is recorded once; no automatic resolve.
	f.evaluate(t, rate, t0.Add(2*time.Minute), clear())
	f.evaluate(t, rate, t0.Add(3*time.Minute), clear())
	f.evaluate(t, groups, t0.Add(4*time.Minute))
	f.evaluate(t, groups, t0.Add(5*time.Minute))
	d, _ = f.st.Incident(ctx, f.project.ID, 1)
	if !d.Incident.AllAlertsResolved || d.Incident.Status != incident.StatusOpen {
		t.Fatalf("after resolve %+v", d.Incident)
	}
	if got := eventTypes(t, f, in.ID); got != "opened,alert_firing,alert_firing,group_linked,alert_resolved,alert_resolved,all_alerts_resolved" {
		t.Fatalf("timeline %s", got)
	}
	// Every firing and resolved notification carries the incident inside its stored (signed) body.
	if n := count("SELECT count(*) FROM alert_notifications WHERE payload->'incident'->>'number' = '1' AND payload->'incident'->>'id' = '" + in.ID + "'"); n != 4 {
		t.Fatalf("%d notifications name the incident, want 4", n)
	}
	if n := count("SELECT count(*) FROM alert_notifications WHERE payload->>'version' <> '1'"); n != 0 {
		t.Fatal("the payload version must not change")
	}
}

func TestNoOverlapAndProjectWideRules(t *testing.T) {
	f, count := newAlertFixture(t)
	t0 := time.Now().UTC().Truncate(time.Second)
	f.evaluate(t, f.incidentRule(t, "Payments", alert.ErrorRate, "payments"), t0, fire(40))
	f.evaluate(t, f.incidentRule(t, "Search", alert.ErrorRate, "search"), t0, fire(40))
	wide1 := f.incidentRule(t, "Everything", alert.ErrorRate)
	wide2 := f.incidentRule(t, "Everything else", alert.ErrorRate)
	f.evaluate(t, wide1, t0, fire(40))
	f.evaluate(t, wide2, t0.Add(time.Minute), fire(40))
	if n := count("SELECT count(*) FROM incidents"); n != 3 {
		t.Fatalf("%d incidents, want 3: payments, search, one project-wide", n)
	}
	if n := count("SELECT count(*) FROM incident_alerts a JOIN incidents i ON i.id = a.incident_id WHERE i.services = '{}'"); n != 2 {
		t.Fatalf("the project-wide incident holds %d alerts", n)
	}
}

func TestFlappingCountsOnOneEvent(t *testing.T) {
	f, _ := newAlertFixture(t)
	t0 := time.Now().UTC().Truncate(time.Second)
	r := f.incidentRule(t, "Flappy", alert.ErrorRate, "api")
	// Three fire and resolve cycles inside ten minutes (clear_for is one interval).
	for i := 0; i < 3; i++ {
		at := t0.Add(time.Duration(i*3) * time.Minute)
		f.evaluate(t, r, at, fire(40))
		f.evaluate(t, r, at.Add(time.Minute), clear())
		f.evaluate(t, r, at.Add(2*time.Minute), clear())
	}
	d, err := f.st.Incident(context.Background(), f.project.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	got := eventTypes(t, f, d.Incident.ID)
	if !strings.HasPrefix(got, "opened,alert_firingx3,alert_resolvedx3") {
		t.Fatalf("timeline %s", got)
	}
}

func TestReopenOrRecurrence(t *testing.T) {
	f, count := newAlertFixture(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)
	r := f.incidentRule(t, "Payments", alert.ErrorRate, "payments")
	f.evaluate(t, r, t0, fire(40))
	f.evaluate(t, r, t0.Add(time.Minute), clear())
	f.evaluate(t, r, t0.Add(2*time.Minute), clear())
	in, _ := f.st.Incident(ctx, f.project.ID, 1)
	if _, err := f.st.ChangeStatus(ctx, f.project.ID, 1, in.Incident.Version, "resolve", "", f.user, AuditEntry{}); err != nil {
		t.Fatal(err)
	}
	// Fires again within the reopen window: the same incident reopens.
	f.evaluate(t, r, t0.Add(3*time.Minute), fire(40))
	d, _ := f.st.Incident(ctx, f.project.ID, 1)
	if d.Incident.Status != incident.StatusOpen || count("SELECT count(*) FROM incidents") != 1 || !strings.Contains(eventTypes(t, f, d.Incident.ID), "reopened") {
		t.Fatalf("reopen: %+v", d.Incident)
	}
	// Resolved long ago: a new incident, a recurrence of #1.
	f.evaluate(t, r, t0.Add(4*time.Minute), clear())
	f.evaluate(t, r, t0.Add(5*time.Minute), clear())
	d, _ = f.st.Incident(ctx, f.project.ID, 1)
	if _, err := f.st.ChangeStatus(ctx, f.project.ID, 1, d.Incident.Version, "resolve", "", f.user, AuditEntry{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.pool.Exec(ctx, "UPDATE incidents SET resolved_at = $1, last_activity_at = $1", t0.Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	f.evaluate(t, r, t0.Add(6*time.Minute), fire(40))
	two, err := f.st.Incident(ctx, f.project.ID, 2)
	if err != nil || two.Incident.RecurrenceOf == nil || *two.Incident.RecurrenceOf != 1 {
		t.Fatalf("recurrence: %+v %v", two.Incident, err)
	}
}

func TestDisabledOrDeletedRuleClosesItsLinks(t *testing.T) {
	f, _ := newAlertFixture(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)
	a := f.incidentRule(t, "A", alert.ErrorRate, "payments")
	b := f.incidentRule(t, "B", alert.ErrorRate, "payments")
	f.evaluate(t, a, t0, fire(40))
	f.evaluate(t, b, t0, fire(40))
	if _, err := f.st.SetRuleEnabled(ctx, f.project.ID, a.ID, false, closer(alert.ReasonRuleDisabled), AuditEntry{UserID: f.user, Action: "alert_rule.disable", Target: "shop"}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.DeleteRule(ctx, f.project.ID, b.ID, closer(alert.ReasonRuleDeleted), AuditEntry{UserID: f.user, Action: "alert_rule.delete", Target: "shop"}); err != nil {
		t.Fatal(err)
	}
	d, err := f.st.Incident(ctx, f.project.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	for _, la := range d.Alerts {
		if la.ClosedAt == nil {
			t.Fatalf("open link %+v", la)
		}
		reasons[la.RuleName] = la.CloseReason
	}
	if reasons["A"] != "rule_disabled" || reasons["B"] != "rule_deleted" || !d.Incident.AllAlertsResolved {
		t.Fatalf("%v %+v", reasons, d.Incident)
	}
	if got := eventTypes(t, f, d.Incident.ID); got != "opened,alert_firing,alert_firing,alert_closed,alert_closed,all_alerts_resolved" {
		t.Fatalf("timeline %s", got)
	}
}

func TestStaleSecondWriterAddsNoIncident(t *testing.T) {
	f, count := newAlertFixture(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)
	r := f.incidentRule(t, "A", alert.ErrorRate, "payments")
	cur, _ := f.st.Rule(ctx, f.project.ID, r.ID)
	out := alert.Step(cur, nil, []alert.Observation{fire(40)}, t0)
	out.EvaluatedAt = t0
	win := alert.Window{From: t0.Add(-time.Minute), To: t0}
	if err := f.st.ApplyOutcome(ctx, cur, win, out, nil, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// A second instance with the same, now stale, view writes nothing.
	if err := f.st.ApplyOutcome(ctx, cur, win, out, nil, t0.Add(time.Minute)); !errors.Is(err, alert.ErrStale) {
		t.Fatalf("second writer: %v", err)
	}
	if count("SELECT count(*) FROM incidents") != 1 || count("SELECT count(*) FROM incident_events") != 2 || count("SELECT count(*) FROM incident_alerts") != 1 {
		t.Fatal("the stale writer must not add incidents, events or links")
	}
}

func TestIncidentLinkAndNotificationsAreAtomic(t *testing.T) {
	f, count := newAlertFixture(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)
	r := f.incidentRule(t, "A", alert.ErrorRate, "payments")
	cur, _ := f.st.Rule(ctx, f.project.ID, r.ID)
	out := alert.Step(cur, nil, []alert.Observation{fire(40)}, t0)
	out.EvaluatedAt = t0
	// The notification write fails after the incident link (a channel that does not exist).
	bad := []alert.NewNotification{{ChannelID: "00000000-0000-0000-0000-000000000000", ChannelName: "gone", IdempotencyKey: "atomic", Event: alert.EventFiring, Payload: []byte(`{}`), Transition: 0}}
	if err := f.st.ApplyOutcome(ctx, cur, alert.Window{From: t0, To: t0}, out, bad, t0.Add(time.Minute)); err == nil {
		t.Fatal("the notification write must fail")
	}
	for _, q := range []string{"SELECT count(*) FROM incidents", "SELECT count(*) FROM incident_alerts", "SELECT count(*) FROM incident_events",
		"SELECT count(*) FROM alert_notifications", "SELECT count(*) FROM alert_states", "SELECT last FROM incident_counters UNION ALL SELECT 0 LIMIT 1"} {
		if n := count(q); n != 0 {
			t.Fatalf("%s = %d: a failure must leave neither an incident link nor a notification", q, n)
		}
	}
}

func TestProjectsNeverShareIncidents(t *testing.T) {
	f, count := newAlertFixture(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)
	other, err := f.st.CreateProjectWithOwner(ctx, "Other", "other", f.user)
	if err != nil {
		t.Fatal(err)
	}
	g := f
	g.project = other
	id, _ := NewID()
	g.channel, err = f.st.CreateChannel(ctx, alert.SealedChannel{Channel: alert.Channel{ID: id, Name: "ops", Kind: alert.KindWebhook, URLHost: "https://h"},
		ProjectID: other.ID, URL: []byte("u"), Secret: []byte("s")}, f.user, AuditEntry{UserID: f.user, Action: "channel.create", Target: "other"})
	if err != nil {
		t.Fatal(err)
	}
	f.evaluate(t, f.incidentRule(t, "Payments", alert.ErrorRate, "payments"), t0, fire(40))
	g.evaluate(t, g.incidentRule(t, "Payments", alert.ErrorRate, "payments"), t0, fire(40))
	if count("SELECT count(*) FROM incidents") != 2 || count("SELECT count(DISTINCT project_id) FROM incidents") != 2 {
		t.Fatal("each project gets its own incident")
	}
	if count("SELECT count(*) FROM incidents WHERE number = 1") != 2 {
		t.Fatal("numbers are per project")
	}
	if count("SELECT count(*) FROM incident_alerts a JOIN incidents i ON i.id = a.incident_id JOIN alert_rules r ON r.id = a.rule_id WHERE r.project_id <> i.project_id") != 0 {
		t.Fatal("a link crossed projects")
	}
}
