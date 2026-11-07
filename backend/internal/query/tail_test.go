package query

import (
	"context"
	"strings"
	"testing"
	"time"
)

func tailStatements(t *testing.T, scope Scope, f Filter) map[string]Statement {
	t.Helper()
	lo := TailKey{InsertedAt: time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC), EventID: "1-0"}
	hi := TailKey{InsertedAt: lo.InsertedAt.Add(time.Second), EventID: "9-0"}
	a, err := BuildTailAfter(scope, f, hi, 200)
	if err != nil {
		t.Fatal(err)
	}
	c, err := BuildTailCount(scope, f, lo, hi)
	if err != nil {
		t.Fatal(err)
	}
	r, err := BuildTailRange(scope, f, lo, hi, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]Statement{"after": a, "count": c, "range": r}
}

func TestTailStatementsScopeAndParams(t *testing.T) {
	now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	benign := TailWindow(Filter{Levels: []string{"error"}, Services: []string{"a", "b"}, Text: "x", TraceID: "y"}, now, 10*time.Minute)
	want := tailStatements(t, ScopeFor("p1"), benign)
	for _, v := range injection {
		f := benign
		f.Services = []string{v, "payments"}
		f.Text, f.TraceID = v, v
		for name, st := range tailStatements(t, ScopeFor("p1"), f) {
			if st.SQL != want[name].SQL {
				t.Fatalf("%s: SQL depends on %q:\n%s", name, v, st.SQL)
			}
			if !strings.Contains(st.SQL, projectScope) || st.Params["project_id"] != "p1" {
				t.Fatalf("%s: project scope missing", name)
			}
			if placeholderNames(st.SQL) != paramNames(st.Params) {
				t.Fatalf("%s: placeholders %s != params %s", name, placeholderNames(st.SQL), paramNames(st.Params))
			}
			if st.Params["from"] != "2026-11-07 09:50:00.000" || st.Params["to"] != "2026-11-07 11:00:00.000" {
				t.Fatalf("%s: window %s..%s", name, st.Params["from"], st.Params["to"])
			}
		}
	}
	for name, st := range want {
		if strings.Contains(st.SQL, "FINAL") {
			t.Errorf("%s: tail statements dedupe by event_id themselves, not with FINAL", name)
		}
	}
	if !strings.Contains(want["after"].SQL, "ORDER BY inserted_at, event_id") || want["after"].Params["after_id"] != "9-0" {
		t.Errorf("after statement:\n%s %v", want["after"].SQL, want["after"].Params)
	}
	if _, err := BuildTailAfter(Scope{}, benign, TailKey{}, 1); err == nil {
		t.Error("tail without scope accepted")
	}
	if _, err := BuildTailAfter(ScopeFor("p"), benign, TailKey{}, 0); err == nil {
		t.Error("zero limit accepted")
	}
}

func TestParseTail(t *testing.T) {
	svc, _ := NewService(&fakeStore{}, testOpts())
	f, err := svc.ParseTail("level=error,fatal&service=payments,checkout&q=timeout&trace_id=abc")
	if err != nil || len(f.Levels) != 2 || len(f.Services) != 2 || f.Text != "timeout" || f.TraceID != "abc" {
		t.Fatalf("ParseTail = %+v, %v", f, err)
	}
	if f, err := svc.ParseTail(""); err != nil || !f.From.IsZero() {
		t.Fatalf("empty tail query: %+v %v", f, err)
	}
	for q, want := range map[string]string{
		"from=2026-11-07T00:00:00Z":            "unknown parameter",
		"to=2026-11-07T00:00:00Z":              "unknown parameter",
		"limit=5":                              "unknown parameter",
		"level=loud":                           "level: unknown value",
		"service=a,,b":                         "service: empty value",
		"q=":                                   "q: must not be empty",
		"q=a&q=b":                              "q: given more than once",
		"trace_id=" + strings.Repeat("t", 257): "trace_id: longer than 256",
	} {
		if _, err := svc.ParseTail(q); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", q, err, want)
		}
	}
}

func TestTailRowsDecode(t *testing.T) {
	store := &cannedStore{bodies: []string{
		`{"event_id":"5-0","timestamp":"2026-11-07 09:59:58.120","level":"error","service":"payments","message":"m","trace_id":"t","attributes":{"k":"v"},"inserted_at":"2026-11-07 10:00:01.250"}` + "\n",
		`{"n":"3"}` + "\n",
	}}
	svc, _ := NewService(store, testOpts())
	f := TailWindow(Filter{}, time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC), time.Minute)
	rows, err := svc.TailAfter(context.Background(), ScopeFor("p"), f, TailKey{}, 200)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %v, %v", rows, err)
	}
	r := rows[0]
	if r.Event.Timestamp != "2026-11-07T09:59:58.120Z" || r.Key.EventID != "5-0" || !r.Key.InsertedAt.Equal(time.Date(2026, 11, 7, 10, 0, 1, 250e6, time.UTC)) {
		t.Errorf("row = %+v", r)
	}
	if store.settings[0]["readonly"] != "1" || store.settings[0]["max_result_rows"] != "200" {
		t.Errorf("settings = %v", store.settings[0])
	}
	n, err := svc.TailCount(context.Background(), ScopeFor("p"), f, TailKey{}, r.Key)
	if err != nil || n != 3 {
		t.Errorf("count = %d, %v", n, err)
	}
}

func TestTailKeyLess(t *testing.T) {
	t0 := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	a, b := TailKey{t0, "9-0"}, TailKey{t0.Add(time.Millisecond), "1-0"}
	if !a.Less(b) || b.Less(a) || a.Less(a) || !(TailKey{t0, "1-0"}).Less(a) {
		t.Error("Less does not order by inserted_at, then event_id")
	}
}
