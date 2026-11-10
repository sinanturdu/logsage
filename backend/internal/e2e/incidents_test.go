package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/alert"
	"github.com/sinanturdu/logsage/backend/internal/fingerprint"
	inc "github.com/sinanturdu/logsage/backend/internal/incident"
	"github.com/sinanturdu/logsage/backend/internal/storage/postgres"
)

// The demo incident through ingest, the processor, ClickHouse, the
// evaluator and the outbox opens one incident per project with both alerts
// and the error group linked, and the delivered payloads name it (ADR 0026).
func TestIncidentsEndToEnd(t *testing.T) {
	T := time.Now().UTC().Truncate(time.Second)
	a := newAlertEnv(t, T)
	ctx := context.Background()
	ids := map[string]string{}
	for _, slug := range []string{"shop", "mirror"} { // identical data and rules: isolation
		p, ch, key := a.project(slug)
		ids[slug] = p.ID
		r := a.rule(p, ch, "Payment errors", alert.ErrorRate, alert.Params{ThresholdPercent: 20, MinLogs: 10, Services: []string{"payments"}}, 5*time.Minute)
		g := a.rule(p, ch, "New groups", alert.NewGroup, alert.Params{MinEvents: 3}, 5*time.Minute)
		for _, rl := range []alert.Rule{r, g} {
			rl.OpenIncident, rl.IncidentSeverity = true, "critical"
			if _, err := a.store.UpdateRule(ctx, rl, postgres.AuditEntry{Action: "alert_rule.update"}); err != nil {
				t.Fatal(err)
			}
		}
		if code := post(t, a.ingest, key, incident(T, 40)); code != http.StatusAccepted {
			t.Fatalf("ingest %s: %d", slug, code)
		}
	}
	a.waitRows(80)
	a.step(T.Add(30 * time.Second))
	// A second evaluator instance stays on standby: no duplicate incident or event.
	second := alert.NewEvaluator(a.store, a.svc, a.clock, a.locker, alert.EvalOptions{}, nil)
	second.Tick(ctx)
	if second.Stats().Cycles != 0 {
		t.Fatalf("second instance ran: %+v", second.Stats())
	}

	payFP, _ := fingerprint.Compute("error", "payments", "payment gateway timeout after 5000 ms (psp-eu-2)", nil)
	numbers := map[string]string{}
	for slug, pid := range ids {
		list, err := a.store.ListIncidents(ctx, pid, inc.Filter{}, nil, 10)
		if err != nil || len(list) != 1 {
			t.Fatalf("%s: %d incidents, %v", slug, len(list), err)
		}
		d, err := a.store.Incident(ctx, pid, list[0].Number)
		if err != nil {
			t.Fatal(err)
		}
		if d.Incident.Number != 1 || d.Incident.Severity != inc.Critical || d.Incident.Source != "auto" || len(d.Alerts) != 2 ||
			len(d.Groups) != 1 || d.Groups[0].Fingerprint != fingerprint.Hex(payFP) {
			t.Fatalf("%s: %+v alerts %+v groups %+v", slug, d.Incident, d.Alerts, d.Groups)
		}
		numbers[slug] = d.Incident.ID
	}
	if numbers["shop"] == numbers["mirror"] {
		t.Fatal("projects share an incident")
	}
	got := a.sink.deliveries()
	if len(got) != 4 {
		t.Fatalf("%d deliveries", len(got))
	}
	for _, d := range got {
		slug := d.path[len("/hook/"):]
		if !d.verified || d.payload.Incident == nil || d.payload.Incident.Number != 1 || d.payload.Incident.ID != numbers[slug] {
			t.Fatalf("delivery without its own project's incident: %+v", d)
		}
	}
	// Evaluating again while firing adds no incident and no event.
	before := map[string]int{}
	for slug, pid := range ids {
		evs, _ := a.store.IncidentEvents(ctx, numbers[slug], 0, 100)
		before[slug] = len(evs)
		_ = pid
	}
	a.step(T.Add(90 * time.Second))
	for slug, pid := range ids {
		list, _ := a.store.ListIncidents(ctx, pid, inc.Filter{}, nil, 10)
		evs, _ := a.store.IncidentEvents(ctx, numbers[slug], 0, 100)
		if len(list) != 1 || len(evs) != before[slug] {
			t.Fatalf("%s: %d incidents, %d events (was %d)", slug, len(list), len(evs), before[slug])
		}
	}
}
