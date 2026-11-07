package query

import (
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/auth"
)

// injection holds hostile values for every free-text parameter.
var injection = []string{
	`' OR 1=1 --`,
	`'; DROP TABLE logs; --`,
	`x' UNION ALL SELECT name, '', '', '', '', '', map() FROM system.users --`,
	`/* comment */ OR project_id != 'default'`,
	`{project_id:String}`,
	`}) OR (1=1`,
	`\'`,
	"tab\tnew\nline\r\x00nul",
	"ünïcödé 漢字 🙂",
	strings.Repeat("A", 10_000),
}

var placeholder = regexp.MustCompile(`\{([a-z_]+):[^{}]+\}`)

func baseFilter() Filter {
	return Filter{
		From:  time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC),
		To:    time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC),
		Limit: 100,
	}
}

// shapes returns every combination of optional filters, filled with value.
func shapes(value string) []Filter {
	var out []Filter
	for mask := 0; mask < 32; mask++ {
		f := baseFilter()
		if mask&1 != 0 {
			f.Levels = []string{"error", "fatal"}
		}
		if mask&2 != 0 {
			f.Service = value
		}
		if mask&4 != 0 {
			f.Text = value
		}
		if mask&8 != 0 {
			f.TraceID = value
		}
		if mask&16 != 0 {
			f.After = &Cursor{Timestamp: f.From.Add(time.Hour), EventID: "1793993613811-0"}
		}
		out = append(out, f)
	}
	return out
}

func TestBuildAlwaysScopesProject(t *testing.T) {
	for _, value := range append([]string{"checkout"}, injection...) {
		for i, f := range shapes(value) {
			st, err := Build(ScopeFor(auth.DefaultProjectID), f)
			if err != nil {
				t.Fatalf("shape %d: %v", i, err)
			}
			if !strings.Contains(st.SQL, "\nFROM logs FINAL\nWHERE "+projectScope+"\n") {
				t.Fatalf("shape %d: project scope is not the first WHERE condition:\n%s", i, st.SQL)
			}
			// The scope fragment holds the two only mentions of project_id:
			// no other condition can widen or override it.
			if strings.Count(st.SQL, projectScope) != 1 || strings.Count(st.SQL, "project_id") != 3 {
				t.Fatalf("shape %d: project_id used outside the scope condition:\n%s", i, st.SQL)
			}
			if st.Params["project_id"] != auth.DefaultProjectID || st.Params["project_alias"] != "default" {
				t.Fatalf("shape %d: project params = %q / %q", i, st.Params["project_id"], st.Params["project_alias"])
			}
		}
	}
}

func TestBuildRejectsMissingProject(t *testing.T) {
	for _, sc := range []Scope{{}, {ProjectID: "x"}, {Alias: "x"}} {
		if _, err := Build(sc, baseFilter()); err == nil {
			t.Fatalf("Build with scope %+v succeeded", sc)
		}
	}
}

func TestBuildKeepsValuesOutOfSQL(t *testing.T) {
	benign := shapes("benign")
	for _, value := range injection {
		for i, f := range shapes(value) {
			st, err := Build(ScopeFor(auth.DefaultProjectID), f)
			if err != nil {
				t.Fatalf("shape %d: %v", i, err)
			}
			want, _ := Build(ScopeFor(auth.DefaultProjectID), benign[i])
			if st.SQL != want.SQL {
				t.Fatalf("shape %d: SQL depends on the value %q:\n%s", i, value, st.SQL)
			}
			// Every placeholder has a value and every value a placeholder.
			var names []string
			for _, m := range placeholder.FindAllStringSubmatch(st.SQL, -1) {
				names = append(names, m[1])
			}
			var keys []string
			for k := range st.Params {
				keys = append(keys, k)
			}
			sort.Strings(names)
			sort.Strings(keys)
			if strings.Join(names, ",") != strings.Join(keys, ",") {
				t.Fatalf("shape %d: placeholders %v != params %v", i, names, keys)
			}
			for _, name := range []string{"service", "q", "trace_id"} {
				if v, ok := st.Params[name]; ok && v != value {
					t.Fatalf("shape %d: param %s = %q, want the raw value", i, name, v)
				}
			}
		}
	}
}

func TestBuildFetchesOneExtraRow(t *testing.T) {
	f := baseFilter()
	f.Limit = 3
	st, err := Build(ScopeFor(auth.DefaultProjectID), f)
	if err != nil {
		t.Fatal(err)
	}
	if st.Params["limit"] != "4" || !strings.Contains(st.SQL, "LIMIT {limit:UInt32}") {
		t.Errorf("limit param = %q", st.Params["limit"])
	}
	if !strings.Contains(st.SQL, "ORDER BY timestamp DESC, event_id DESC") {
		t.Error("missing stable newest-first order")
	}
}

func TestBuildValidatesInput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Filter)
	}{
		{name: "level injection", mutate: func(f *Filter) { f.Levels = []string{"info') OR ('1'='1"} }},
		{name: "unknown level", mutate: func(f *Filter) { f.Levels = []string{"loud"} }},
		{name: "zero limit", mutate: func(f *Filter) { f.Limit = 0 }},
		{name: "empty range", mutate: func(f *Filter) { f.To = f.From }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := baseFilter()
			tt.mutate(&f)
			if _, err := Build(ScopeFor(auth.DefaultProjectID), f); err == nil {
				t.Fatal("Build() error = nil, want error")
			}
		})
	}
}

func TestBuildParamFormats(t *testing.T) {
	f := baseFilter()
	f.From = time.Date(2026, 11, 6, 12, 0, 0, 123_456_789, time.FixedZone("TRT", 3*3600))
	f.Levels = []string{"warn", "error"}
	f.After = &Cursor{Timestamp: time.Date(2026, 11, 6, 20, 0, 0, 5_000_000, time.UTC), EventID: "5-1"}
	st, err := Build(ScopeFor(auth.DefaultProjectID), f)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"from":      "2026-11-06 09:00:00.123",
		"to":        "2026-11-07 00:00:00.000",
		"levels":    "['warn','error']",
		"cursor_ts": "2026-11-06 20:00:00.005",
		"cursor_id": "5-1",
	}
	for k, v := range want {
		if st.Params[k] != v {
			t.Errorf("param %s = %q, want %q", k, st.Params[k], v)
		}
	}
}

func TestScopeFor(t *testing.T) {
	if sc := ScopeFor(auth.DefaultProjectID); sc.ProjectID != auth.DefaultProjectID || sc.Alias != "default" {
		t.Errorf("default project scope = %+v", sc)
	}
	other := "44444444-4444-4444-4444-444444444444"
	if sc := ScopeFor(other); sc.ProjectID != other || sc.Alias != other {
		t.Errorf("other project scope = %+v: must not match legacy default rows", sc)
	}
	// The SQL is identical for every project; only parameters differ.
	a, _ := Build(ScopeFor(auth.DefaultProjectID), baseFilter())
	b, _ := Build(ScopeFor(other), baseFilter())
	if a.SQL != b.SQL || b.Params["project_alias"] != other {
		t.Error("scope changed the SQL text or the alias leaked another project")
	}
}
