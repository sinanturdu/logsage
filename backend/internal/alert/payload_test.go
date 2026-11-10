package alert

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNotificationsPayload(t *testing.T) {
	r := Rule{ID: "r1", ProjectSlug: "demo", Name: "Checkout errors", Type: NewGroup}
	win := Window{From: t0.Add(-5 * time.Minute), To: t0}
	n := Notice{Key: GroupKey(0x6fde77d96a33b9a3), Event: EventFiring, Value: 12, Threshold: 1, FiredAt: t0, Fingerprint: 0x6fde77d96a33b9a3,
		Template: "payment gateway timeout after <n> ms", Sample: "token=SECRET " + strings.Repeat("é", 700), Transition: 0}
	chs := []Channel{{ID: "c1", Name: "ops"}, {ID: "c2", Name: "audit", IncludeSample: true}}
	notes, err := Notifications(r, win, n, chs, "", "https://logsage.example.com/", t0)
	if err != nil || len(notes) != 2 {
		t.Fatal(err)
	}
	if notes[0].IdempotencyKey == notes[1].IdempotencyKey || len(notes[0].IdempotencyKey) != 64 {
		t.Fatal("keys must differ per channel")
	}
	again, _ := Notifications(r, win, n, chs, "", "https://logsage.example.com/", t0.Add(time.Hour))
	if again[0].IdempotencyKey != notes[0].IdempotencyKey {
		t.Fatal("the key must not depend on when the payload was built")
	}
	var p0, p1 Payload
	_ = json.Unmarshal(notes[0].Payload, &p0)
	_ = json.Unmarshal(notes[1].Payload, &p1)
	if p0.Version != 1 || p0.Event != EventFiring || p0.State != "firing" || p0.Project != "demo" || p0.Rule.Type != "new_group" || p0.Unit != "events" {
		t.Fatalf("payload %+v", p0)
	}
	if p0.Group == nil || p0.Group.Fingerprint != "6fde77d96a33b9a3" || p0.Group.Template != n.Template || p0.Window.Seconds != 300 {
		t.Fatalf("group %+v", p0.Group)
	}
	if p0.URL != "https://logsage.example.com/projects/demo/groups/6fde77d96a33b9a3?from=2026-11-09T09%3A55%3A00Z&to=2026-11-09T10%3A00%3A00Z" {
		t.Fatalf("url %s", p0.URL)
	}
	if p0.Sample != nil || strings.Contains(string(notes[0].Payload), "SECRET") {
		t.Fatal("a raw message reached a channel that did not opt in")
	}
	if p1.Sample == nil || len(*p1.Sample) > MaxSample || !strings.HasPrefix(*p1.Sample, "token=SECRET") {
		t.Fatalf("sample %v", p1.Sample)
	}
	if p0.Reason != "" || strings.Contains(string(notes[0].Payload), `"reason"`) {
		t.Fatal("reason only when a rule ended the alert")
	}
}

func TestFinalNotificationsCarryTheReason(t *testing.T) {
	fired := t0.Add(-time.Hour)
	states := []State{
		{DedupKey: RuleKey, State: StateFiring, FiredAt: &fired, LastValue: 9, NotifyCount: 2},
		{DedupKey: "x", State: StatePending},
	}
	notices := CloseNotices(states, t0)
	if len(notices) != 1 || notices[0].Event != EventResolved || !notices[0].FiredAt.Equal(fired) || notices[0].Transition != 0 {
		t.Fatalf("%+v", notices)
	}
	build := FinalNotifications(ReasonRuleDeleted, "", func() time.Time { return t0 })
	notes, err := build(Rule{ID: "r", Type: ErrorRate, ProjectSlug: "demo"}, []Channel{{ID: "c"}}, notices)
	if err != nil || len(notes) != 1 {
		t.Fatal(err)
	}
	var p Payload
	_ = json.Unmarshal(notes[0].Payload, &p)
	if p.Reason != "rule_deleted" || p.State != "resolved" || p.ResolvedAt == nil || p.Unit != "percent" || p.Group != nil {
		t.Fatalf("%+v", p)
	}
}

func TestDashboardLink(t *testing.T) {
	r := Rule{ProjectSlug: "my shop", Type: ErrorRate}
	win := Window{From: t0, To: t0.Add(time.Minute)}
	if got := DashboardLink("", r, win, 0); got != "" {
		t.Fatal("no base, no link")
	}
	if got := DashboardLink("https://x.test", r, win, 0); got != "https://x.test/projects/my%20shop/logs?from=2026-11-09T10%3A00%3A00Z&level=error%2Cfatal&to=2026-11-09T10%3A01%3A00Z" {
		t.Fatalf("%s", got)
	}
}

func TestIncidentFieldIsOptionalAndStable(t *testing.T) {
	r := Rule{ID: "r1", ProjectSlug: "demo", Name: "x", Type: ErrorRate}
	ns, err := Notifications(r, Window{From: t0, To: t0}, Notice{Key: RuleKey, Event: EventFiring, FiredAt: t0}, []Channel{{ID: "c1", Name: "ops"}}, "", "", t0)
	if err != nil || len(ns) != 1 || ns[0].Key != RuleKey {
		t.Fatal(ns, err)
	}
	if strings.Contains(string(ns[0].Payload), "incident") {
		t.Fatalf("no incident field without an incident: %s", ns[0].Payload)
	}
	with, err := WithIncident(ns[0].Payload, PayloadIncident{ID: "i1", Number: 7, URL: IncidentLink("https://logsage.example.com/", "demo", 7)})
	if err != nil {
		t.Fatal(err)
	}
	var p Payload
	if err := json.Unmarshal(with, &p); err != nil || p.Version != 1 || p.Incident == nil || p.Incident.Number != 7 ||
		p.Incident.URL != "https://logsage.example.com/projects/demo/incidents/7" || p.IdempotencyKey != ns[0].IdempotencyKey {
		t.Fatalf("%s %v", with, err)
	}
	// Everything else is unchanged: the same body without the incident.
	p.Incident = nil
	back, _ := json.Marshal(p)
	if string(back) != string(ns[0].Payload) {
		t.Fatalf("payload changed:\n%s\n%s", back, ns[0].Payload)
	}
	if IncidentLink("", "demo", 7) != "" {
		t.Fatal("no link without a dashboard URL")
	}
}
