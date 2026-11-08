package query

import (
	"strings"
	"testing"
	"time"
)

func TestAlertBuildersAlwaysScopeAndParameterize(t *testing.T) {
	scope := Scope{ProjectID: "p1", Alias: "p1"}
	from := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	hostile := "x' OR 1=1 --"
	w := AlertWindow{From: from, To: from.Add(5 * time.Minute), Services: []string{hostile}, Levels: []string{hostile}}
	builders := map[string]func() (Statement, error){
		"counts":    func() (Statement, error) { return BuildAlertCounts(scope, w) },
		"new count": func() (Statement, error) { return BuildNewGroupCount(scope, w) },
		"new":       func() (Statement, error) { return BuildNewGroups(scope, w, 3) },
		"activity":  func() (Statement, error) { return BuildGroupActivity(scope, w, []uint64{1, 2}) },
	}
	for name, build := range builders {
		st, err := build()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(st.SQL, projectScope) || st.Params["project_id"] != "p1" {
			t.Errorf("%s: no project scope:\n%s", name, st.SQL)
		}
		if strings.Contains(st.SQL, hostile) || strings.Contains(st.SQL, "OR 1=1") {
			t.Errorf("%s: a value reached the SQL text", name)
		}
		if st.Params["from"] != "2026-11-09 10:00:00.000" || st.Params["to"] != "2026-11-09 10:05:00.000" {
			t.Errorf("%s: window params %v", name, st.Params)
		}
	}
	// new_groups_per_hour ignores services and levels; the group queries scope group_seen too.
	if st, _ := BuildNewGroupCount(scope, w); strings.Contains(st.SQL, "service IN") || st.Params["services"] != "" {
		t.Errorf("guard query filters by service: %s", st.SQL)
	}
	if st, _ := BuildNewGroups(scope, w, 0); strings.Count(st.SQL, projectScope) != 2 || st.Params["min_events"] != "1" {
		t.Errorf("new groups: %s %v", st.SQL, st.Params)
	}
	if st, _ := BuildGroupActivity(scope, w, []uint64{7, 1 << 63}); st.Params["fps"] != "[7,9223372036854775808]" {
		t.Errorf("fps param %q", st.Params["fps"])
	}
}

func TestAlertBuildersRefuse(t *testing.T) {
	from := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	ok := AlertWindow{From: from, To: from.Add(time.Minute)}
	if _, err := BuildAlertCounts(Scope{}, ok); err == nil {
		t.Error("no scope must fail")
	}
	if _, err := BuildAlertCounts(Scope{ProjectID: "p", Alias: "p"}, AlertWindow{From: from, To: from}); err == nil {
		t.Error("an empty window must fail")
	}
	if _, err := BuildGroupActivity(Scope{ProjectID: "p", Alias: "p"}, ok, nil); err == nil {
		t.Error("no fingerprints must fail")
	}
}
