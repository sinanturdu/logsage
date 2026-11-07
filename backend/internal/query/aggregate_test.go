package query

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

func placeholderNames(sql string) string {
	seen := map[string]bool{}
	var names []string
	for _, m := range placeholder.FindAllStringSubmatch(sql, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			names = append(names, m[1])
		}
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func paramNames(p map[string]string) string {
	var keys []string
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// aggregates builds the three aggregate statements for f.
func aggregates(t *testing.T, scope Scope, f Filter) map[string]Statement {
	t.Helper()
	h, err := BuildHistogram(scope, f, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	l, err := BuildLevelFacets(scope, f)
	if err != nil {
		t.Fatal(err)
	}
	s, err := BuildServiceFacets(scope, f, ServiceFacetLimit)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]Statement{"histogram": h, "levels": l, "services": s}
}

func TestAggregatesAlwaysScopeAndKeepValuesOut(t *testing.T) {
	benign := baseFilter()
	benign.Levels = []string{"error"}
	benign.Services = []string{"a", "b"}
	benign.Text, benign.TraceID = "x", "y"
	want := aggregates(t, ScopeFor("p1"), benign)
	for _, v := range injection {
		f := benign
		f.Services = []string{v, v + ",2"}
		f.Text, f.TraceID = v, v
		for name, st := range aggregates(t, ScopeFor("p1"), f) {
			if st.SQL != want[name].SQL {
				t.Fatalf("%s: SQL depends on %q:\n%s", name, v, st.SQL)
			}
			if !strings.Contains(st.SQL, projectScope) || st.Params["project_id"] != "p1" {
				t.Fatalf("%s: project scope missing", name)
			}
			if !strings.Contains(st.SQL, "FROM logs FINAL") {
				t.Fatalf("%s: not reading FINAL", name)
			}
			if placeholderNames(st.SQL) != paramNames(st.Params) {
				t.Fatalf("%s: placeholders %s != params %s", name, placeholderNames(st.SQL), paramNames(st.Params))
			}
			for _, p := range []string{"q", "trace_id"} {
				if st.Params[p] != v {
					t.Fatalf("%s: %s = %q", name, p, st.Params[p])
				}
			}
			if s, ok := st.Params["services"]; ok {
				if got := decodeArrayParam(t, s); len(got) != 2 || got[0] != v || got[1] != v+",2" {
					t.Fatalf("%s: services decoded as %q", name, got)
				}
			}
		}
	}
}

func TestFacetsExcludeTheirOwnFilter(t *testing.T) {
	f := baseFilter()
	f.Levels = []string{"error"}
	f.Services = []string{"payments", "checkout"}
	st := aggregates(t, ScopeFor("p1"), f)
	if strings.Contains(st["levels"].SQL, "level IN") || !strings.Contains(st["levels"].SQL, "service IN") {
		t.Errorf("level facets must drop only the level filter:\n%s", st["levels"].SQL)
	}
	if strings.Contains(st["services"].SQL, "service IN") || !strings.Contains(st["services"].SQL, "level IN") {
		t.Errorf("service facets must drop only the service filter:\n%s", st["services"].SQL)
	}
	if !strings.Contains(st["histogram"].SQL, "level IN") || !strings.Contains(st["histogram"].SQL, "service IN") {
		t.Errorf("histogram must apply every filter:\n%s", st["histogram"].SQL)
	}
}

func TestAggregatesValidate(t *testing.T) {
	f := baseFilter()
	if _, err := BuildHistogram(Scope{}, f, time.Minute); err == nil {
		t.Error("histogram without scope")
	}
	if _, err := BuildLevelFacets(Scope{ProjectID: "p"}, f); err == nil {
		t.Error("facets without alias")
	}
	if _, err := BuildHistogram(ScopeFor("p"), f, 1500*time.Millisecond); err == nil {
		t.Error("fractional bucket accepted")
	}
	if _, err := BuildServiceFacets(ScopeFor("p"), f, 0); err == nil {
		t.Error("zero facet limit accepted")
	}
	f.Levels = []string{"loud"}
	if _, err := BuildLevelFacets(ScopeFor("p"), f); err != nil {
		t.Error("level facets must ignore the level filter, even an invalid one built by hand")
	}
	if _, err := BuildHistogram(ScopeFor("p"), f, time.Minute); err == nil {
		t.Error("invalid level accepted")
	}
}

func TestParseHistogramBuckets(t *testing.T) {
	svc, _ := NewService(&fakeStore{}, testOpts())
	base := "from=2026-11-06T00:00:00Z&to="
	for _, tt := range []struct {
		to, bucket string
		want       time.Duration
		wantErr    string
	}{
		{to: "2026-11-06T00:15:00Z", want: 10 * time.Second}, // 90 buckets
		{to: "2026-11-06T01:00:00Z", want: 30 * time.Second}, // 120
		{to: "2026-11-06T06:00:00Z", want: 5 * time.Minute},  // 72; 1m would be 360
		{to: "2026-11-07T00:00:00Z", want: 15 * time.Minute}, // 96
		{to: "2026-11-13T00:00:00Z", want: time.Hour},        // 168
		{to: "2026-11-06T01:00:00Z", bucket: "1m", want: time.Minute},
		{to: "2026-11-06T01:23:20Z", bucket: "10s", want: 10 * time.Second}, // exactly 500
		{to: "2026-11-06T01:23:21Z", bucket: "10s", wantErr: "bucket: 10s gives 501 buckets for this range, max 500"},
		{to: "2026-11-06T01:00:00Z", bucket: "2m", wantErr: "bucket: must be one of 10s, 30s, 1m, 5m, 15m, 1h, 6h, 1d"},
		{to: "2026-11-06T01:00:00Z", bucket: "", wantErr: "bucket: must be one of"},
	} {
		q := base + tt.to
		if tt.bucket != "" || tt.wantErr != "" {
			q += "&bucket=" + tt.bucket
		}
		_, b, err := svc.ParseHistogram(q)
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("%s: err = %v, want %q", q, err, tt.wantErr)
			}
			continue
		}
		if err != nil || b != tt.want {
			t.Errorf("%s: bucket %s, %v; want %s", q, b, err, tt.want)
		}
	}
	for _, q := range []string{base + "2026-11-06T01:00:00Z&limit=5", base + "2026-11-06T01:00:00Z&cursor=x"} {
		if _, _, err := svc.ParseHistogram(q); err == nil || !strings.Contains(err.Error(), "unknown parameter") {
			t.Errorf("%s: err = %v", q, err)
		}
	}
	for _, q := range []string{base + "2026-11-06T01:00:00Z&bucket=1m", base + "2026-11-06T01:00:00Z&limit=1"} {
		if _, err := svc.ParseFacets(q); err == nil || !strings.Contains(err.Error(), "unknown parameter") {
			t.Errorf("facets %s: err = %v", q, err)
		}
	}
}

// cannedStore answers every query with the next canned body.
type cannedStore struct {
	bodies   []string
	sqls     []string
	settings []map[string]string
}

func (c *cannedStore) QueryWithParams(_ context.Context, sql string, _, settings map[string]string) ([]byte, error) {
	c.sqls = append(c.sqls, sql)
	c.settings = append(c.settings, settings)
	b := c.bodies[0]
	c.bodies = c.bodies[1:]
	return []byte(b), nil
}
func (c *cannedStore) Ready(context.Context) error { return nil }

func TestHistogramZeroFills(t *testing.T) {
	from := time.Date(2026, 11, 6, 10, 0, 20, 0, time.UTC)
	to := from.Add(5 * time.Minute)
	t1 := time.Date(2026, 11, 6, 10, 2, 0, 0, time.UTC).Unix()
	store := &cannedStore{bodies: []string{fmt.Sprintf(`{"t":"%d","debug":"0","info":"7","warn":"1","error":"2","fatal":"0"}`+"\n", t1)}}
	svc, _ := NewService(store, testOpts())
	h, err := svc.Histogram(context.Background(), ScopeFor("p"), Filter{From: from, To: to}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// 10:00 (holding from) .. 10:05 (holding the last instant before to).
	if h.BucketSeconds != 60 || len(h.Buckets) != 6 || h.Buckets[0].T != "2026-11-06T10:00:00Z" || h.Buckets[5].T != "2026-11-06T10:05:00Z" {
		t.Fatalf("buckets = %+v", h)
	}
	for i, b := range h.Buckets {
		sum := b.Debug + b.Info + b.Warn + b.Error + b.Fatal
		if (i == 2) != (sum == 10) || (i != 2 && sum != 0) {
			t.Errorf("bucket %d = %+v", i, b)
		}
	}
	if store.settings[0]["readonly"] != "1" || store.settings[0]["max_result_rows"] != "501" {
		t.Errorf("settings = %v", store.settings[0])
	}
}

func TestFacetsTotalsAndOther(t *testing.T) {
	var services strings.Builder
	for i := 0; i < ServiceFacetLimit; i++ {
		fmt.Fprintf(&services, `{"service":"s%02d","n":"10","groups":"53","all_n":"530"}`+"\n", i)
	}
	store := &cannedStore{bodies: []string{
		`{"level":"info","n":"300"}` + "\n" + `{"level":"error","n":"200"}` + "\n" + `{"level":"warn","n":"30"}` + "\n",
		services.String(),
	}}
	svc, _ := NewService(store, testOpts())
	f := baseFilter()
	f.Levels = []string{"error", "warn"}
	got, err := svc.Facets(context.Background(), ScopeFor("p"), f)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 230 || got.Levels["info"] != 300 || got.Levels["fatal"] != 0 || len(got.Levels) != 5 {
		t.Errorf("levels/total = %v %d", got.Levels, got.Total)
	}
	last := got.Services[len(got.Services)-1]
	if len(got.Services) != ServiceFacetLimit+1 || !last.Other || last.Name != "other" || last.Count != 30 {
		t.Errorf("services: %d entries, last %+v", len(got.Services), last)
	}

	// Without truncation there is no "other".
	store.bodies = []string{"", `{"service":"a","n":"3","groups":"1","all_n":"3"}` + "\n"}
	got, _ = svc.Facets(context.Background(), ScopeFor("p"), baseFilter())
	if len(got.Services) != 1 || got.Total != 0 || got.Services[0].Other {
		t.Errorf("services = %+v", got.Services)
	}
}
