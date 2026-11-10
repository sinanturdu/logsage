package incident

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) time.Time { return now.Add(-d) }
func agoPtr(d time.Duration) *time.Time {
	t := ago(d)
	return &t
}

func TestCorrelate(t *testing.T) {
	payments := Alert{RuleID: "r1", Key: "rule", RuleName: "Error rate", Services: []string{"payments"}}
	wide := Alert{RuleID: "r9", Key: "rule", RuleName: "Silence"}
	open := func(id string, n int, services []string, active time.Duration) Candidate {
		return Candidate{ID: id, Number: n, Status: StatusOpen, Services: services, LastActivity: ago(active)}
	}
	cases := []struct {
		name      string
		alert     Alert
		open      []Candidate
		last      *Candidate
		openCount int
		want      Decision
	}{
		{"nothing open: create", payments, nil, nil, 0, Decision{Kind: Create, Rule: 5}},
		{"same rule and key, even outside the window", payments,
			[]Candidate{{ID: "i1", Number: 1, Status: StatusAcknowledged, Services: []string{"other"}, LastActivity: ago(5 * time.Hour), LinkedHere: true}}, nil, 1,
			Decision{Kind: Attach, IncidentID: "i1", Rule: 1}},
		{"service overlap inside the window", payments,
			[]Candidate{open("i1", 1, []string{"checkout", "payments"}, 10*time.Minute)}, nil, 1,
			Decision{Kind: Attach, IncidentID: "i1", Rule: 2}},
		{"window edge is inside", payments,
			[]Candidate{open("i1", 1, []string{"payments"}, 30*time.Minute)}, nil, 1,
			Decision{Kind: Attach, IncidentID: "i1", Rule: 2}},
		{"overlap but outside the window: create", payments,
			[]Candidate{open("i1", 1, []string{"payments"}, 31*time.Minute)}, nil, 1,
			Decision{Kind: Create, Rule: 5}},
		{"no overlap: create (no mega-incident)", payments,
			[]Candidate{open("i1", 1, []string{"checkout"}, time.Minute)}, nil, 1,
			Decision{Kind: Create, Rule: 5}},
		{"the most recently active of two matches", payments,
			[]Candidate{open("i1", 1, []string{"payments"}, 20*time.Minute), open("i2", 2, []string{"payments"}, 2*time.Minute)}, nil, 2,
			Decision{Kind: Attach, IncidentID: "i2", Rule: 2}},
		{"a project-wide incident does not take a service alert", payments,
			[]Candidate{open("i1", 1, nil, time.Minute)}, nil, 1,
			Decision{Kind: Create, Rule: 5}},
		{"project-wide alert joins a project-wide incident", wide,
			[]Candidate{open("i1", 1, nil, 5*time.Minute)}, nil, 1,
			Decision{Kind: Attach, IncidentID: "i1", Rule: 3}},
		{"project-wide alert never joins a service incident", wide,
			[]Candidate{open("i1", 1, []string{"payments"}, time.Minute)}, nil, 1,
			Decision{Kind: Create, Rule: 5}},
		{"project-wide outside the window: create", wide,
			[]Candidate{open("i1", 1, nil, time.Hour)}, nil, 1,
			Decision{Kind: Create, Rule: 5}},
		{"resolved recently: reopen", payments, nil,
			&Candidate{ID: "i7", Number: 7, Status: StatusResolved, ResolvedAt: agoPtr(59 * time.Minute)}, 0,
			Decision{Kind: Reopen, IncidentID: "i7", Rule: 4}},
		{"resolved long ago: a recurrence", payments, nil,
			&Candidate{ID: "i7", Number: 7, Status: StatusResolved, ResolvedAt: agoPtr(61 * time.Minute)}, 0,
			Decision{Kind: Create, RecurrenceOf: "i7", Rule: 5}},
		{"an open overlapping incident wins over a reopen", payments,
			[]Candidate{open("i8", 8, []string{"payments"}, time.Minute)},
			&Candidate{ID: "i7", Number: 7, Status: StatusResolved, ResolvedAt: agoPtr(time.Minute)}, 1,
			Decision{Kind: Attach, IncidentID: "i8", Rule: 2}},
		{"at the cap: skip, never merge into an unrelated incident", payments,
			[]Candidate{open("i1", 1, []string{"checkout"}, time.Minute)}, nil, 1000,
			Decision{Kind: Skip, Reason: "open incident limit reached", Rule: 5}},
		{"at the cap a reopen is skipped too", payments, nil,
			&Candidate{ID: "i7", Number: 7, Status: StatusResolved, ResolvedAt: agoPtr(time.Minute)}, 1000,
			Decision{Kind: Skip, Reason: "open incident limit reached", Rule: 5}},
		{"at the cap an existing link still attaches", payments,
			[]Candidate{{ID: "i1", Number: 1, Status: StatusOpen, LinkedHere: true, LastActivity: ago(time.Minute)}}, nil, 1000,
			Decision{Kind: Attach, IncidentID: "i1", Rule: 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Correlate(c.alert, c.open, c.last, c.openCount, now, DefaultConfig); got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestCollapseFlapping(t *testing.T) {
	if Collapse(time.Time{}, false, now) {
		t.Fatal("nothing before: a new event")
	}
	if !Collapse(ago(10*time.Minute), true, now) {
		t.Fatal("within 10 minutes: counted on the previous event")
	}
	if Collapse(ago(10*time.Minute+time.Second), true, now) {
		t.Fatal("after 10 minutes: a new event")
	}
}

func TestTransition(t *testing.T) {
	for _, c := range []struct {
		from   Status
		action string
		to     Status
		ok     bool
	}{
		{StatusOpen, "acknowledge", StatusAcknowledged, true},
		{StatusAcknowledged, "acknowledge", StatusAcknowledged, false},
		{StatusOpen, "resolve", StatusResolved, true},
		{StatusAcknowledged, "resolve", StatusResolved, true},
		{StatusResolved, "resolve", StatusResolved, false},
		{StatusResolved, "reopen", StatusOpen, true},
		{StatusOpen, "reopen", StatusOpen, false},
		{StatusOpen, "delete", StatusOpen, false},
	} {
		if to, ok := Transition(c.from, c.action); to != c.to || ok != c.ok {
			t.Errorf("%s %s = %s %v, want %s %v", c.from, c.action, to, ok, c.to, c.ok)
		}
	}
}

func TestTitleAndServices(t *testing.T) {
	if got := Title(" Error rate ", []string{"payments", "checkout"}); got != "Error rate in payments, checkout" {
		t.Fatal(got)
	}
	if got := Title(strings.Repeat("é", 300), nil); len([]rune(got)) != MaxTitle || !strings.HasSuffix(got, "…") {
		t.Fatalf("%d runes", len([]rune(got)))
	}
	if got := MergeServices([]string{"b"}, []string{"a", "b", ""}); strings.Join(got, ",") != "a,b" {
		t.Fatal(got)
	}
	many := make([]string, 30)
	for i := range many {
		many[i] = string(rune('a' + i%26)) + string(rune('a'+i/26))
	}
	if got := MergeServices(nil, many); len(got) != MaxServices {
		t.Fatal(len(got))
	}
}

func TestEventSchemasAreClosed(t *testing.T) {
	for _, typ := range EventTypes {
		if _, ok := payloadFor(typ); !ok {
			t.Errorf("%s has no schema", typ)
		}
	}
	b, err := EncodePayload(EvAlertFiring, AlertPayload{RuleID: "r1", RuleName: "x", RuleType: "new_group", Key: "fp:1", Fingerprint: "00000000000000ff", Template: "db timeout after <n> ms"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePayload(EvAlertFiring, b); err != nil {
		t.Fatal(err)
	}
	// A raw message or any field outside the schema is refused.
	if _, err := DecodePayload(EvAlertFiring, []byte(`{"rule_id":"r1","sample":"password=hunter2"}`)); !errors.Is(err, ErrSchema) {
		t.Fatalf("unknown field: %v", err)
	}
	if _, err := DecodePayload("deploy", []byte(`{}`)); !errors.Is(err, ErrSchema) {
		t.Fatalf("unknown type: %v", err)
	}
	if _, err := EncodePayload(EvStatusChanged, AlertPayload{}); !errors.Is(err, ErrSchema) {
		t.Fatalf("wrong struct: %v", err)
	}
	if _, err := EncodePayload("deploy", EmptyPayload{}); !errors.Is(err, ErrSchema) {
		t.Fatalf("unknown type: %v", err)
	}
}
