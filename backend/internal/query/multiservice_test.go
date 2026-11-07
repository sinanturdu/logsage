package query

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/auth"
)

// decodeArrayParam parses an Array(String) parameter value the way
// ClickHouse does for quoted string elements, so the tests can check that
// every value survives exactly and cannot end its element early.
func decodeArrayParam(t *testing.T, v string) []string {
	t.Helper()
	if !strings.HasPrefix(v, "[") || !strings.HasSuffix(v, "]") {
		t.Fatalf("not an array: %q", v)
	}
	body := v[1 : len(v)-1]
	var out []string
	for i := 0; i < len(body); {
		if body[i] != '\'' {
			t.Fatalf("element does not start with a quote at %d in %q", i, v)
		}
		var b strings.Builder
		i++
		for ; ; i++ {
			if i >= len(body) {
				t.Fatalf("unterminated element in %q", v)
			}
			c := body[i]
			if c == '\\' {
				i++
				b.WriteByte(body[i])
				continue
			}
			if c == '\'' {
				break
			}
			b.WriteByte(c)
		}
		out = append(out, b.String())
		i++ // closing quote
		if i < len(body) {
			if body[i] != ',' {
				t.Fatalf("expected comma at %d in %q", i, v)
			}
			i++
		}
	}
	return out
}

func TestBuildMultipleServices(t *testing.T) {
	f := baseFilter()
	f.Services = []string{"payments", "checkout"}
	st, err := Build(ScopeFor("p1"), f)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.SQL, "service IN {services:Array(String)}") || strings.Contains(st.SQL, "{service:String}") {
		t.Fatalf("SQL:\n%s", st.SQL)
	}
	if _, ok := st.Params["service"]; ok {
		t.Fatal("single-service param set for a list")
	}
	if got := st.Params["services"]; got != "['payments','checkout']" {
		t.Fatalf("services = %q", got)
	}
	if !strings.Contains(st.SQL, projectScope) {
		t.Fatal("project scope missing")
	}

	// One service keeps the v0.1 statement.
	f.Services = []string{"payments"}
	st, _ = Build(ScopeFor("p1"), f)
	if !strings.Contains(st.SQL, "service = {service:String}") || st.Params["service"] != "payments" {
		t.Fatalf("single service: %s %v", st.SQL, st.Params)
	}
}

func TestBuildServicesKeepValuesOutOfSQL(t *testing.T) {
	benign := baseFilter()
	benign.Services = []string{"a", "b", "c"}
	want, _ := Build(ScopeFor(auth.DefaultProjectID), benign)
	hostile := append([]string{}, injection...)
	hostile = append(hostile, `a','b`, `x\`, `\\'`, `'`, `]`, `[`, `', 'other']) OR 1=1 --`, "comma,inside")
	for _, v := range hostile {
		f := baseFilter()
		f.Services = []string{v, "payments", v + "2"}
		st, err := Build(ScopeFor(auth.DefaultProjectID), f)
		if err != nil {
			t.Fatal(err)
		}
		if st.SQL != want.SQL {
			t.Fatalf("SQL depends on %q:\n%s", v, st.SQL)
		}
		got := decodeArrayParam(t, st.Params["services"])
		if len(got) != 3 || got[0] != v || got[1] != "payments" || got[2] != v+"2" {
			t.Fatalf("value %q decoded as %q", v, got)
		}
	}
}

func TestBuildRejectsTooManyServices(t *testing.T) {
	f := baseFilter()
	for i := 0; i <= MaxServices; i++ {
		f.Services = append(f.Services, fmt.Sprintf("s%d", i))
	}
	if _, err := Build(ScopeFor("p1"), f); err == nil {
		t.Fatal("Build accepted more than MaxServices services")
	}
}

func TestHandlerServiceList(t *testing.T) {
	store := &fakeStore{rows: []fakeRow{
		{project: "default", id: "1-0", ts: day.Add(time.Hour), level: "error", service: "payments", message: "m"},
		{project: "default", id: "2-0", ts: day.Add(2 * time.Hour), level: "error", service: "checkout", message: "m"},
		{project: "default", id: "3-0", ts: day.Add(3 * time.Hour), level: "info", service: "search", message: "m"},
	}}
	h, _ := newTestHandler(t, store)

	rec := do(h, http.MethodGet, okRange+"&service=payments,checkout,payments", testKey)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var resp Response
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(resp.Events))
	}
	if store.params["services"] != "['payments','checkout']" {
		t.Fatalf("services param = %q (duplicates must be removed)", store.params["services"])
	}

	// URL-encoded quotes and brackets are values, not syntax.
	rec = do(h, http.MethodGet, okRange+"&service=%27%5D%20OR%201%3D1%20--,payments", testKey)
	if rec.Code != 200 || len(decodeArrayParam(t, store.params["services"])) != 2 {
		t.Fatalf("hostile list: %d %q", rec.Code, store.params["services"])
	}

	long := strings.Repeat("s", 257)
	many := strings.TrimSuffix(strings.Repeat("x,", MaxServices+1), ",")
	for _, tt := range []struct{ query, wantErr string }{
		{"&service=a,,b", "service: empty value in the comma-separated list"},
		{"&service=a,", "service: empty value in the comma-separated list"},
		{"&service=,", "service: empty value in the comma-separated list"},
		{"&service=" + many, "service: at most 20 comma-separated values"},
		{"&service=a," + long, "service: longer than 256 characters"},
		{"&service=a,%FF", "service: must be valid UTF-8"},
	} {
		calls := store.calls
		rec := do(h, http.MethodGet, okRange+tt.query, testKey)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), tt.wantErr) {
			t.Errorf("%s: %d %s", tt.query, rec.Code, rec.Body)
		}
		if store.calls != calls {
			t.Errorf("%s: store was called", tt.query)
		}
	}

	// Twenty values are allowed.
	twenty := strings.TrimSuffix(strings.Repeat("x,", MaxServices), ",")
	if rec := do(h, http.MethodGet, okRange+"&service="+twenty, testKey); rec.Code != 200 {
		t.Fatalf("20 values: %d %s", rec.Code, rec.Body)
	}
}
