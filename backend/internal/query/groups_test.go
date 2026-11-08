package query

import (
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/auth"
)

func groupQuery(text string) GroupQuery {
	q := GroupQuery{Filter: Filter{From: day.Add(10 * time.Hour), To: day.Add(11 * time.Hour), Levels: []string{"error"}, Services: []string{"api", text}, Text: text}, Sort: SortCount}
	q.Limit = 50
	return q
}

func checkPlaceholders(t *testing.T, st Statement) {
	t.Helper()
	var names, keys []string
	seen := map[string]bool{}
	for _, m := range placeholder.FindAllStringSubmatch(st.SQL, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			names = append(names, m[1])
		}
	}
	for k := range st.Params {
		keys = append(keys, k)
	}
	sort.Strings(names)
	sort.Strings(keys)
	if strings.Join(names, ",") != strings.Join(keys, ",") {
		t.Fatalf("placeholders %v != params %v\n%s", names, keys, st.SQL)
	}
}

func TestBuildGroupsScopesBothSides(t *testing.T) {
	st, err := BuildGroups(ScopeFor("11111111-1111-4111-8111-111111111111"), groupQuery("timeout"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(st.SQL, projectScope); n != 2 {
		t.Fatalf("project scope appears %d times, want 2 (logs and group_seen):\n%s", n, st.SQL)
	}
	for _, want := range []string{"FROM logs FINAL", "fingerprint != 0", "positionCaseInsensitiveUTF8(template, {q:String})", "FROM group_seen", "ORDER BY g.c DESC, g.fingerprint", "LIMIT {limit:UInt32} OFFSET {offset:UInt32}"} {
		if !strings.Contains(st.SQL, want) {
			t.Errorf("missing %q in\n%s", want, st.SQL)
		}
	}
	if strings.Contains(st.SQL, "message, {q") || strings.Contains(st.SQL, "count() FROM group_seen") {
		t.Errorf("q must match the template, and counts must not come from the view:\n%s", st.SQL)
	}
	if st.Params["limit"] != "51" || st.Params["offset"] != "0" {
		t.Errorf("limit/offset params = %q/%q", st.Params["limit"], st.Params["offset"])
	}
	checkPlaceholders(t, st)
	if _, err := BuildGroups(Scope{}, groupQuery("x")); err == nil {
		t.Fatal("missing scope must be rejected")
	}
}

func TestBuildGroupsKeepsValuesOutOfSQL(t *testing.T) {
	want, _ := BuildGroups(ScopeFor(auth.DefaultProjectID), groupQuery("benign"))
	for _, v := range injection {
		q := groupQuery(v)
		st, err := BuildGroups(ScopeFor(auth.DefaultProjectID), q)
		if err != nil {
			t.Fatal(err)
		}
		if st.SQL != want.SQL {
			t.Fatalf("SQL depends on %q:\n%s", v, st.SQL)
		}
		if st.Params["q"] != v {
			t.Fatalf("q param = %q", st.Params["q"])
		}
		checkPlaceholders(t, st)
		sp, err := BuildSparklines(ScopeFor(auth.DefaultProjectID), q, []uint64{1, 18446744073709551615})
		if err != nil {
			t.Fatal(err)
		}
		wantSp, _ := BuildSparklines(ScopeFor(auth.DefaultProjectID), groupQuery("benign"), []uint64{1, 18446744073709551615})
		if sp.Params["include"] != "[1,18446744073709551615]" || sp.SQL != wantSp.SQL {
			t.Fatalf("sparkline params %v", sp.Params)
		}
		checkPlaceholders(t, sp)
	}
}

func TestBuildGroupsSortsAndFilters(t *testing.T) {
	scope := ScopeFor(auth.DefaultProjectID)
	for sortBy, want := range map[string]string{SortCount: "g.c DESC", SortLastSeen: "last_seen DESC", SortFirstSeen: "first_seen DESC"} {
		q := groupQuery("x")
		q.Sort = sortBy
		st, err := BuildGroups(scope, q)
		if err != nil || !strings.Contains(st.SQL, "ORDER BY "+want+", g.fingerprint") {
			t.Errorf("sort %s: %v\n%s", sortBy, err, st.SQL)
		}
	}
	q := groupQuery("x")
	q.Sort = "count; DROP TABLE logs"
	if _, err := BuildGroups(scope, q); err == nil {
		t.Error("an unknown sort must be rejected")
	}
	q = groupQuery("x")
	q.Include, q.Exclude = []uint64{7, 8}, []uint64{9}
	st, _ := BuildGroups(scope, q)
	if st.Params["include"] != "[7,8]" || st.Params["exclude"] != "[9]" || !strings.Contains(st.SQL, "fingerprint IN {include:Array(UInt64)}") || !strings.Contains(st.SQL, "fingerprint NOT IN {exclude:Array(UInt64)}") {
		t.Errorf("include/exclude: %v\n%s", st.Params, st.SQL)
	}
	for _, bad := range []GroupQuery{{Filter: Filter{Limit: 0}}, {Filter: Filter{Limit: MaxGroupLimit + 1}}, {Filter: Filter{Limit: 1}, Offset: -1}, {Filter: Filter{Limit: 1}, Offset: MaxGroupOffset + 1}} {
		bad.From, bad.To = day, day.Add(time.Hour)
		if _, err := BuildGroups(scope, bad); err == nil {
			t.Errorf("bad limit/offset accepted: %+v", bad)
		}
	}
}

func TestFingerprintFilter(t *testing.T) {
	f := Filter{From: day, To: day.Add(time.Hour), Limit: 10, Fingerprint: 0xfedcba9876543210}
	st, err := Build(ScopeFor(auth.DefaultProjectID), f)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.SQL, "fingerprint = {fingerprint:UInt64}") || st.Params["fingerprint"] != "18364758544493064720" {
		t.Fatalf("fingerprint filter: %v\n%s", st.Params, st.SQL)
	}
	if !strings.Contains(st.SQL, "SELECT event_id, timestamp, level, service, message, trace_id, attributes, fingerprint") {
		t.Fatalf("the fingerprint column must be selected:\n%s", st.SQL)
	}
	st, _ = BuildGroupCounts(ScopeFor(auth.DefaultProjectID), f, 42)
	if st.Params["fingerprint"] != "42" || !strings.Contains(st.SQL, projectScope) {
		t.Fatalf("group counts: %v", st.Params)
	}
	if _, err := BuildGroupCounts(ScopeFor(auth.DefaultProjectID), f, 0); err == nil {
		t.Fatal("fingerprint 0 is not a group")
	}
	if _, err := BuildGroupSeen(Scope{}, 1); err == nil {
		t.Fatal("missing scope must be rejected")
	}
}

func TestParseFingerprintParam(t *testing.T) {
	svc, _ := NewService(&fakeStore{}, testOpts())
	base := "from=2026-11-06T10:00:00Z&to=2026-11-06T11:00:00Z&fingerprint="
	f, err := svc.ParseSearch(base + "FEDCBA9876543210")
	if err != nil || f.Fingerprint != 0xfedcba9876543210 {
		t.Fatalf("valid fingerprint: %v %x", err, f.Fingerprint)
	}
	for _, bad := range []string{"", "0000000000000000", "123", "zzzzzzzzzzzzzzzz", url.QueryEscape("' OR 1=1 --xxxxx"), "fedcba98765432100"} {
		_, err := svc.ParseSearch(base + bad)
		if err == nil || err.Error() != "fingerprint: must be 16 hexadecimal characters" {
			t.Errorf("fingerprint %q: %v", bad, err)
		}
		if _, err := svc.ParseFacets(base + bad); err == nil {
			t.Errorf("facets accepted %q", bad)
		}
		if _, _, err := svc.ParseHistogram(base + bad); err == nil {
			t.Errorf("histogram accepted %q", bad)
		}
		if _, err := svc.ParseTail("fingerprint=" + bad); err == nil {
			t.Errorf("tail accepted %q", bad)
		}
	}
}

func TestParseGroups(t *testing.T) {
	svc, _ := NewService(&fakeStore{}, testOpts())
	base := "from=2026-11-06T10:00:00Z&to=2026-11-06T11:00:00Z"
	q, err := svc.ParseGroups(base + "&q=timeout&status=resolved&sort=last_seen&limit=10&level=error,fatal&service=api")
	if err != nil || q.Text != "timeout" || q.Status != StatusResolved || q.Sort != SortLastSeen || q.Limit != 10 || len(q.Levels) != 2 {
		t.Fatalf("parse: %v %+v", err, q)
	}
	if q, _ := svc.ParseGroups(base); q.Sort != SortCount || q.Limit != DefaultGroupLimit || q.Status != "" {
		t.Fatalf("defaults: %+v", q)
	}
	for bad, msg := range map[string]string{
		"":                                     "from",
		base + "&sort=random":                  "sort:",
		base + "&status=deleted":               "status:",
		base + "&limit=0":                      "limit:",
		base + "&limit=101":                    "limit:",
		base + "&fingerprint=0000000000000001": "unknown parameter",
		base + "&cursor=xyz":                   "cursor:",
		base + "&sort=count&sort=count":        "more than once",
	} {
		if _, err := svc.ParseGroups(bad); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%q: %v, want %q", bad, err, msg)
		}
	}
}

func TestGroupCursor(t *testing.T) {
	svc, _ := NewService(&fakeStore{}, testOpts())
	base := "from=2026-11-06T10:00:00Z&to=2026-11-06T11:00:00Z&q=timeout"
	q, _ := svc.ParseGroups(base)
	c := encodeGroupCursor(50, q.hash)
	q2, err := svc.ParseGroups(base + "&cursor=" + c)
	if err != nil || q2.Offset != 50 {
		t.Fatalf("roundtrip: %v %d", err, q2.Offset)
	}
	// Same cursor, different filters: refused.
	if _, err := svc.ParseGroups("from=2026-11-06T10:00:00Z&to=2026-11-06T11:00:00Z&q=other&cursor=" + c); err == nil {
		t.Fatal("cursor replayed against another query")
	}
	// Parameter order does not matter.
	if _, err := svc.ParseGroups("q=timeout&to=2026-11-06T11:00:00Z&from=2026-11-06T10:00:00Z&cursor=" + c); err != nil {
		t.Fatalf("reordered query: %v", err)
	}
	for _, bad := range []string{encodeGroupCursor(MaxGroupOffset+1, q.hash), encodeGroupCursor(-1, q.hash), "Zzzz", strings.Repeat("a", 100)} {
		if _, err := svc.ParseGroups(base + "&cursor=" + bad); err == nil {
			t.Errorf("bad cursor %q accepted", bad)
		}
	}
}

func TestSparkBucket(t *testing.T) {
	for w, want := range map[time.Duration]time.Duration{
		24 * time.Hour:                    time.Hour,
		time.Hour:                         150 * time.Second,
		15 * time.Minute:                  37500 * time.Millisecond,
		7*24*time.Hour + time.Millisecond: 7*time.Hour + time.Millisecond,
		time.Millisecond:                  time.Millisecond,
	} {
		if got := sparkBucket(Filter{From: day, To: day.Add(w)}); got != want {
			t.Errorf("window %v: bucket %v, want %v", w, got, want)
		}
		if got := sparkBucket(Filter{From: day, To: day.Add(w)}); time.Duration(SparkBuckets)*got < w {
			t.Errorf("window %v: %d buckets of %v do not cover it", w, SparkBuckets, got)
		}
	}
}
