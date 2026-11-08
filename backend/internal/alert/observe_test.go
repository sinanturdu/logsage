package alert

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/query"
)

type fakeQ struct {
	total, errs uint64
	newCount    uint64
	groups      []query.AlertGroup
	activity    []query.AlertGroup
	err         error
	windows     []query.AlertWindow
	scopes      []query.Scope
	fps         []uint64
	minEvents   uint64
}

func (f *fakeQ) AlertCounts(_ context.Context, s query.Scope, w query.AlertWindow) (uint64, uint64, error) {
	f.windows, f.scopes = append(f.windows, w), append(f.scopes, s)
	return f.total, f.errs, f.err
}
func (f *fakeQ) NewGroupCount(_ context.Context, s query.Scope, w query.AlertWindow) (uint64, error) {
	f.windows, f.scopes = append(f.windows, w), append(f.scopes, s)
	return f.newCount, f.err
}
func (f *fakeQ) NewGroups(_ context.Context, s query.Scope, w query.AlertWindow, min uint64) ([]query.AlertGroup, error) {
	f.windows, f.scopes, f.minEvents = append(f.windows, w), append(f.scopes, s), min
	return f.groups, f.err
}
func (f *fakeQ) GroupActivity(_ context.Context, s query.Scope, w query.AlertWindow, fps []uint64) ([]query.AlertGroup, error) {
	f.windows, f.scopes, f.fps = append(f.windows, w), append(f.scopes, s), fps
	return f.activity, f.err
}

type fakeResolved []ResolvedGroup

func (f fakeResolved) ResolvedGroups(context.Context, string) ([]ResolvedGroup, error) { return f, nil }

func pct(v float64) *float64 { return &v }

func observe(t *testing.T, r Rule, q *fakeQ, res ResolvedSource) []Observation {
	t.Helper()
	r.Normalize()
	if err := Validate(r); err != nil {
		t.Fatalf("rule: %v", err)
	}
	obs, err := Observe(context.Background(), r, q, res, WindowAt(r, t0, time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return obs
}

func base(tp Type, p Params) Rule {
	return Rule{ProjectID: "p1", Name: "r", Type: tp, Params: p, Interval: time.Minute, Window: 5 * time.Minute}
}

func TestErrorRate(t *testing.T) {
	r := base(ErrorRate, Params{ThresholdPercent: 5, MinLogs: 100})
	for _, c := range []struct {
		total, errs uint64
		fire, clear bool
		value       float64
	}{
		{1000, 50, true, false, 5},    // exactly the threshold fires
		{1000, 49, false, false, 4.9}, // inside the band (4.5 to 5)
		{1000, 44, false, true, 4.4},  // below the band
		{99, 99, false, true, 100},    // a small sample never fires
		{0, 0, false, true, 0},        // no logs: clear
		{3, 1, false, true, 33.33},    // rounding
	} {
		q := &fakeQ{total: c.total, errs: c.errs}
		o := observe(t, r, q, nil)[0]
		if o.Fire != c.fire || o.Clear != c.clear || o.Value != c.value || o.Threshold != 5 || o.Key != RuleKey {
			t.Errorf("%d/%d: %+v", c.errs, c.total, o)
		}
	}
	// The window ends a minute (the lateness) before now and spans the rule's window.
	q := &fakeQ{}
	observe(t, base(ErrorRate, Params{ThresholdPercent: 5, Services: []string{"api"}}), q, nil)
	w := q.windows[0]
	if !w.To.Equal(t0.Add(-time.Minute)) || !w.From.Equal(t0.Add(-6*time.Minute)) || w.Services[0] != "api" {
		t.Fatalf("window %+v", w)
	}
	if q.scopes[0].ProjectID != "p1" {
		t.Fatalf("scope %+v", q.scopes[0])
	}
}

func TestLogVolume(t *testing.T) {
	above := base(LogVolume, Params{Op: "above", Count: 100})
	below := base(LogVolume, Params{Op: "below", Count: 100})
	silence := base(LogVolume, Params{Op: "below", Count: 1})
	for _, c := range []struct {
		r           Rule
		n           uint64
		fire, clear bool
	}{
		{above, 101, true, false},
		{above, 100, false, false}, // inside the 10% band
		{above, 90, false, true},
		{below, 99, true, false},
		{below, 105, false, false},
		{below, 110, false, true},
		{silence, 0, true, false},
		{silence, 1, false, true},
	} {
		o := observe(t, c.r, &fakeQ{total: c.n}, nil)[0]
		if o.Fire != c.fire || o.Clear != c.clear || o.Value != float64(c.n) {
			t.Errorf("%s %d with %d: %+v", c.r.Params.Op, c.r.Params.Count, c.n, o)
		}
	}
	// A 0% band resolves as soon as the value is back.
	r := above
	r.Params.HysteresisPercent = pct(0)
	if o := observe(t, r, &fakeQ{total: 100}, nil)[0]; !o.Clear {
		t.Errorf("no band: %+v", o)
	}
}

func TestNewGroupsPerHour(t *testing.T) {
	r := base(NewGroupsPerHour, Params{MaxGroups: 50})
	r.Window = 0 // fixed at one hour
	q := &fakeQ{newCount: 51}
	o := observe(t, r, q, nil)[0]
	if !o.Fire || o.Threshold != 50 {
		t.Fatalf("%+v", o)
	}
	if w := q.windows[0]; w.To.Sub(w.From) != time.Hour {
		t.Fatalf("window %v", w.To.Sub(w.From))
	}
	if o := observe(t, r, &fakeQ{newCount: 45}, nil)[0]; o.Fire || !o.Clear {
		t.Fatalf("%+v", o)
	}
}

func TestNewGroup(t *testing.T) {
	q := &fakeQ{groups: []query.AlertGroup{{Fingerprint: 0xabc, Count: 7, Title: "boom <n>", Sample: "boom 42"}, {Fingerprint: 0xdef, Count: 3, Title: "x"}}}
	obs := observe(t, base(NewGroup, Params{MinEvents: 3}), q, nil)
	if len(obs) != 2 || obs[0].Key != "fp:0000000000000abc" || obs[0].Template != "boom <n>" || obs[0].Sample != "boom 42" || !obs[0].Fire || obs[0].Value != 7 {
		t.Fatalf("%+v", obs)
	}
	if q.minEvents != 3 {
		t.Fatalf("min events %d", q.minEvents)
	}
}

func TestRegression(t *testing.T) {
	resolvedAt := t0.Add(-30 * time.Minute)
	res := fakeResolved{{Fingerprint: 1, ResolvedAt: resolvedAt}, {Fingerprint: 2, ResolvedAt: resolvedAt}}
	q := &fakeQ{activity: []query.AlertGroup{
		{Fingerprint: 1, Count: 4, Title: "seen again", Last: resolvedAt.Add(time.Minute)},
		{Fingerprint: 2, Count: 2, Title: "only before", Last: resolvedAt},
	}}
	obs := observe(t, base(Regression, Params{}), q, res)
	if len(obs) != 1 || obs[0].Key != RegressionKey(1, resolvedAt) || !strings.HasPrefix(obs[0].Key, "fp:0000000000000001:") || obs[0].Template != "seen again" {
		t.Fatalf("%+v", obs)
	}
	if len(q.fps) != 2 {
		t.Fatalf("asked for %v", q.fps)
	}
	// No resolved groups: no query at all.
	q2 := &fakeQ{}
	if obs := observe(t, base(Regression, Params{}), q2, fakeResolved{}); obs != nil || len(q2.windows) != 0 {
		t.Fatalf("%+v %d", obs, len(q2.windows))
	}
}

func TestQueryErrorIsReturned(t *testing.T) {
	boom := errors.New("clickhouse down")
	r := base(ErrorRate, Params{ThresholdPercent: 5})
	r.Normalize()
	if _, err := Observe(context.Background(), r, &fakeQ{err: boom}, nil, WindowAt(r, t0, 0)); !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
}

func TestValidate(t *testing.T) {
	ok := []Rule{
		base(ErrorRate, Params{ThresholdPercent: 5}),
		base(LogVolume, Params{Op: "above", Count: 0}),
		base(LogVolume, Params{Op: "below", Count: 1, Levels: []string{"error"}}),
		base(NewGroup, Params{}),
		base(Regression, Params{Services: []string{"api"}}),
		{Name: "guard", Type: NewGroupsPerHour, Params: Params{MaxGroups: 100}, Interval: 5 * time.Minute},
	}
	for _, r := range ok {
		r.Normalize()
		if err := Validate(r); err != nil {
			t.Errorf("%s: %v", r.Type, err)
		}
	}
	mod := func(f func(*Rule)) Rule {
		r := base(ErrorRate, Params{ThresholdPercent: 5})
		f(&r)
		r.Normalize()
		return r
	}
	bad := map[string]Rule{
		"name: required":      mod(func(r *Rule) { r.Name = "  " }),
		"name: at most":       mod(func(r *Rule) { r.Name = strings.Repeat("x", 101) }),
		"note: at most":       mod(func(r *Rule) { r.Note = strings.Repeat("x", 501) }),
		"type:":               mod(func(r *Rule) { r.Type = "anomaly" }),
		"interval: must":      mod(func(r *Rule) { r.Interval = 29 * time.Second }),
		"interval: must ":     mod(func(r *Rule) { r.Interval = 2 * time.Hour }),
		"window: must":        mod(func(r *Rule) { r.Window = 59 * time.Second }),
		"window: must ":       mod(func(r *Rule) { r.Window = 25 * time.Hour }),
		"for: must":           mod(func(r *Rule) { r.For = 25 * time.Hour }),
		"repeat: must":        mod(func(r *Rule) { r.Repeat = time.Minute }),
		"channels: at most":   mod(func(r *Rule) { r.Channels = make([]string, 11) }),
		"threshold_percent:":  mod(func(r *Rule) { r.Params.ThresholdPercent = 0.01 }),
		"threshold_percent: ": mod(func(r *Rule) { r.Params.ThresholdPercent = 101 }),
		"min_logs:":           mod(func(r *Rule) { r.Params.MinLogs = 5 }),
		"services: at most":   mod(func(r *Rule) { r.Params.Services = make([]string, 21) }),
		"services: each":      mod(func(r *Rule) { r.Params.Services = []string{""} }),
		"levels: must":        mod(func(r *Rule) { r.Type = LogVolume; r.Params.Op = "above"; r.Params.Levels = []string{"loud"} }),
		"levels: not used":    mod(func(r *Rule) { r.Params.Levels = []string{"error"} }),
		"op: must":            mod(func(r *Rule) { r.Type = LogVolume; r.Params.Op = "equal" }),
		"count: must":         mod(func(r *Rule) { r.Type = LogVolume; r.Params.Op = "below"; r.Params.Count = 0 }),
		"min_events:":         mod(func(r *Rule) { r.Type = NewGroup; r.Params.MinEvents = 2_000_000 }),
		"max_groups:":         mod(func(r *Rule) { r.Type = NewGroupsPerHour; r.Params.MaxGroups = 0 }),
		"hysteresis_percent:": mod(func(r *Rule) { r.Params.HysteresisPercent = pct(60) }),
	}
	for want, r := range bad {
		err := Validate(r)
		if err == nil || !IsValidation(err) || !strings.HasPrefix(err.Error(), strings.TrimSpace(want)) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
}
