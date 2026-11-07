package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/api"
	"github.com/sinanturdu/logsage/backend/internal/auth"
	"github.com/sinanturdu/logsage/backend/internal/query"
	"github.com/sinanturdu/logsage/backend/internal/storage/clickhouse"
	"github.com/sinanturdu/logsage/backend/internal/storage/postgres"
	"github.com/sinanturdu/logsage/backend/migrations"
)

// itLog is one row of the shared test dataset.
type itLog struct {
	id, ts, level, service, message, trace string
}

const itTrace = "4bf92f3577b34da6a3ce929d0e0e4736"

// readDataset is inserted once per project, identically. 02:02 has no rows
// so a 1-minute histogram must zero-fill that bucket.
func readDataset() []itLog {
	rows := []itLog{
		{"1-0", "02:00:05", "info", "checkout", "cart converted to order", itTrace},
		{"2-0", "02:00:40", "error", "payments", "payment gateway timeout after 5000 ms", itTrace},
		{"3-0", "02:00:41", "error", "checkout", "charge failed: upstream payments returned 504", itTrace},
		{"4-0", "02:01:10", "warn", "payments", "retrying charge, attempt 3 of 3", itTrace},
		{"5-0", "02:01:50", "info", "search", "query served in 37 ms", "c21f7a9e04d84b3fa6e2d5b1907c3e48"},
		{"6-0", "02:03:30", "fatal", "payments", "gateway client pool exhausted", ""},
		{"7-0", "02:04:10", "debug", "auth", "session refreshed", ""},
	}
	for i := 0; i < 12; i++ {
		rows = append(rows, itLog{fmt.Sprintf("8-%d", i), fmt.Sprintf("02:04:%02d", 20+i), "info", "api", "GET /v1/products 200", ""})
	}
	return rows
}

type readEnv struct {
	h            *harness
	ch           *clickhouse.Client
	db           string
	alice, bob   string
	alphaID, bID string
}

func newReadEnv(t *testing.T) *readEnv {
	t.Helper()
	chURL := os.Getenv("CLICKHOUSE_TEST_URL")
	if chURL == "" {
		t.Skip("CLICKHOUSE_TEST_URL not set; skipping read endpoint integration test")
	}
	user := os.Getenv("CLICKHOUSE_TEST_USER")
	if user == "" {
		user = "default"
	}
	db := "logsage_rit_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	ch, err := clickhouse.New(clickhouse.Options{URL: chURL, Database: db, User: user, Password: os.Getenv("CLICKHOUSE_TEST_PASSWORD"), Timeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := ch.Migrate(ctx, migrations.ClickHouse(), clickhouse.MigrationData{Database: db, TTLDays: 3650}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = ch.Exec(context.Background(), "DROP DATABASE IF EXISTS "+db) })

	svc, err := query.NewService(ch, query.Options{MaxWindow: 24 * time.Hour, DefaultLimit: 100, MaxLimit: 1000, MaxTextLen: 256, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, func(o *api.Options) { o.Logs = svc }, nil)
	h.createUser("alice@example.com", alicePW)
	h.createUser("bob@example.com", bobPW)
	e := &readEnv{h: h, ch: ch, db: db, alice: h.login("alice@example.com", alicePW), bob: h.login("bob@example.com", bobPW)}
	r := h.do("POST", "/api/projects", `{"name":"Alpha","slug":"alpha"}`, e.alice, nil)
	h.wantStatus(r, 201, "create alpha")
	e.alphaID = r.json(t)["project"].(map[string]any)["id"].(string)
	r = h.do("POST", "/api/projects", `{"name":"Beta","slug":"beta"}`, e.bob, nil)
	h.wantStatus(r, 201, "create beta")
	e.bID = r.json(t)["project"].(map[string]any)["id"].(string)
	if _, err := h.st.AddMember(ctx, "default", "alice@example.com", postgres.RoleViewer); err != nil {
		t.Fatal(err)
	}

	var body strings.Builder
	add := func(project string, l itLog) {
		b, _ := json.Marshal(map[string]any{
			"project_id": project, "event_id": l.id, "timestamp": "2026-11-07 " + l.ts + ".000", "level": l.level,
			"service": l.service, "message": l.message, "fingerprint": 0, "trace_id": l.trace,
			"attributes": map[string]string{"env": "it"}, "ingested_at": "2026-11-07 03:00:00.000",
		})
		body.Write(b)
		body.WriteByte('\n')
	}
	for _, l := range readDataset() {
		add(e.alphaID, l)
		add(e.bID, l)
	}
	// Bob's project has one extra row with the same trace: it must never
	// show up for Alice.
	add(e.bID, itLog{"9-0", "02:00:42", "error", "payments", "beta-only marker", itTrace})
	// Default project: two legacy v0.1 rows and one row with the fixed id.
	add(query.LegacyDefaultProject, itLog{"d-1", "02:01:00", "info", "api", "legacy row one", ""})
	add(query.LegacyDefaultProject, itLog{"d-2", "02:01:30", "error", "api", "legacy row two", ""})
	add(auth.DefaultProjectID, itLog{"d-3", "02:02:30", "warn", "api", "default project row", ""})
	if err := ch.InsertJSONEachRow(ctx, "logs", []byte(body.String())); err != nil {
		t.Fatalf("insert: %v", err)
	}
	return e
}

const itRange = "from=2026-11-07T02:00:00Z&to=2026-11-07T02:05:00Z"

func (e *readEnv) get(t *testing.T, token, path string, want int) map[string]any {
	t.Helper()
	r := e.h.do("GET", path, "", token, nil)
	e.h.wantStatus(r, want, "GET "+path)
	return r.json(t)
}

// expected counts the dataset rows matching the filters (nil: any).
func expected(levels, services []string, q, trace string) (total int, byLevel, byService map[string]int) {
	byLevel, byService = map[string]int{}, map[string]int{}
	in := func(list []string, v string) bool {
		if list == nil {
			return true
		}
		for _, x := range list {
			if x == v {
				return true
			}
		}
		return false
	}
	for _, l := range readDataset() {
		if (q != "" && !strings.Contains(strings.ToLower(l.message), q)) || (trace != "" && l.trace != trace) {
			continue
		}
		if in(services, l.service) {
			byLevel[l.level]++ // level facet: every filter except level
		}
		if in(levels, l.level) {
			byService[l.service]++ // service facet: every filter except service
		}
		if in(levels, l.level) && in(services, l.service) {
			total++
		}
	}
	return total, byLevel, byService
}

func TestIntegrationReadEndpointsIsolation(t *testing.T) {
	e := newReadEnv(t)
	n := len(readDataset())
	for _, ep := range []string{"logs", "histogram", "facets"} {
		e.get(t, e.bob, "/api/projects/alpha/"+ep+"?"+itRange, 404)
		e.get(t, e.alice, "/api/projects/beta/"+ep+"?"+itRange, 404)
		e.get(t, e.bob, "/api/projects/default/"+ep+"?"+itRange, 404)
	}
	logs := e.get(t, e.alice, "/api/projects/alpha/logs?limit=1000&"+itRange, 200)
	if evs := logs["events"].([]any); len(evs) != n {
		t.Fatalf("alice sees %d logs, want %d", len(evs), n)
	}
	if strings.Contains(fmt.Sprint(logs), "beta-only") {
		t.Fatal("alice sees bob's rows")
	}
	if logs := e.get(t, e.bob, "/api/projects/beta/logs?limit=1000&"+itRange, 200); len(logs["events"].([]any)) != n+1 {
		t.Fatalf("bob sees %d logs, want %d", len(logs["events"].([]any)), n+1)
	}
	if f := e.get(t, e.alice, "/api/projects/alpha/facets?"+itRange, 200); f["total"].(float64) != float64(n) {
		t.Fatalf("alice facets total = %v", f["total"])
	}

	// Same-trace use case: logs filtered by the selected log's trace_id stay
	// inside the project even though bob's project shares the trace id.
	tr := e.get(t, e.alice, "/api/projects/alpha/logs?trace_id="+itTrace+"&"+itRange, 200)
	if evs := tr["events"].([]any); len(evs) != 4 {
		t.Fatalf("same trace: %d logs, want 4", len(evs))
	}
	if strings.Contains(fmt.Sprint(tr), "beta-only") {
		t.Fatal("same-trace query returned another project's log")
	}
	h := e.get(t, e.alice, "/api/projects/alpha/histogram?trace_id="+itTrace+"&"+itRange, 200)
	if sum := histogramSum(h); sum != 4 {
		t.Fatalf("same-trace histogram sum = %d, want 4", sum)
	}
}

func histogramSum(h map[string]any) int {
	sum := 0
	for _, b := range h["buckets"].([]any) {
		for _, l := range query.Levels {
			sum += int(b.(map[string]any)[l].(float64))
		}
	}
	return sum
}

func TestIntegrationHistogramMatchesLogs(t *testing.T) {
	e := newReadEnv(t)
	for _, tt := range []struct {
		params           string
		levels, services []string
		q, trace         string
	}{
		{params: ""},
		{params: "&level=error,fatal", levels: []string{"error", "fatal"}},
		{params: "&service=payments,checkout", services: []string{"payments", "checkout"}},
		{params: "&service=api", services: []string{"api"}},
		{params: "&q=TIMEOUT", q: "timeout"},
		{params: "&trace_id=" + itTrace, trace: itTrace},
		{params: "&level=error&service=payments,checkout&trace_id=" + itTrace, levels: []string{"error"}, services: []string{"payments", "checkout"}, trace: itTrace},
	} {
		want, _, _ := expected(tt.levels, tt.services, tt.q, tt.trace)
		logs := e.get(t, e.alice, "/api/projects/alpha/logs?limit=1000&"+itRange+tt.params, 200)
		hist := e.get(t, e.alice, "/api/projects/alpha/histogram?"+itRange+tt.params, 200)
		if got := len(logs["events"].([]any)); got != want {
			t.Errorf("%s: logs = %d, want %d", tt.params, got, want)
		}
		if got := histogramSum(hist); got != want {
			t.Errorf("%s: histogram sum = %d, want %d", tt.params, got, want)
		}
	}

	// Five 1-minute buckets (auto: 10s gives 30), 02:02 zero-filled.
	hist := e.get(t, e.alice, "/api/projects/alpha/histogram?bucket=1m&"+itRange, 200)
	buckets := hist["buckets"].([]any)
	if hist["bucket_seconds"].(float64) != 60 || len(buckets) != 5 {
		t.Fatalf("histogram = %v", hist)
	}
	b2 := buckets[2].(map[string]any)
	if b2["t"] != "2026-11-07T02:02:00Z" || histogramSum(map[string]any{"buckets": []any{b2}}) != 0 {
		t.Errorf("bucket 02:02 = %v, want zeros", b2)
	}
	if b0 := buckets[0].(map[string]any); b0["error"].(float64) != 2 || b0["info"].(float64) != 1 {
		t.Errorf("bucket 02:00 = %v", b0)
	}
	if auto := e.get(t, e.alice, "/api/projects/alpha/histogram?"+itRange, 200); auto["bucket_seconds"].(float64) != 10 || len(auto["buckets"].([]any)) != 30 {
		t.Errorf("auto bucket = %v (%d buckets)", auto["bucket_seconds"], len(auto["buckets"].([]any)))
	}
}

func TestIntegrationFacetExclusion(t *testing.T) {
	e := newReadEnv(t)
	for _, tt := range []struct {
		params           string
		levels, services []string
	}{
		{params: ""},
		{params: "&level=error", levels: []string{"error"}},
		{params: "&service=payments", services: []string{"payments"}},
		{params: "&level=error,warn&service=payments,checkout", levels: []string{"error", "warn"}, services: []string{"payments", "checkout"}},
	} {
		total, byLevel, byService := expected(tt.levels, tt.services, "", "")
		f := e.get(t, e.alice, "/api/projects/alpha/facets?"+itRange+tt.params, 200)
		if int(f["total"].(float64)) != total {
			t.Errorf("%s: total = %v, want %d", tt.params, f["total"], total)
		}
		levels := f["levels"].(map[string]any)
		for _, l := range query.Levels {
			if int(levels[l].(float64)) != byLevel[l] {
				t.Errorf("%s: level %s = %v, want %d", tt.params, l, levels[l], byLevel[l])
			}
		}
		got := map[string]int{}
		prev := -1
		for _, s := range f["services"].([]any) {
			m := s.(map[string]any)
			got[m["name"].(string)] = int(m["count"].(float64))
			if prev >= 0 && int(m["count"].(float64)) > prev {
				t.Errorf("%s: services not sorted by count: %v", tt.params, f["services"])
			}
			prev = int(m["count"].(float64))
		}
		if fmt.Sprint(got) != fmt.Sprint(byService) {
			t.Errorf("%s: services = %v, want %v", tt.params, got, byService)
		}
	}
}

func TestIntegrationDefaultProjectIncludesLegacyRows(t *testing.T) {
	e := newReadEnv(t)
	logs := e.get(t, e.alice, "/api/projects/default/logs?"+itRange, 200)
	if evs := logs["events"].([]any); len(evs) != 3 {
		t.Fatalf("default project: %d logs, want 2 legacy + 1", len(evs))
	}
	if h := e.get(t, e.alice, "/api/projects/default/histogram?"+itRange, 200); histogramSum(h) != 3 {
		t.Errorf("default histogram sum = %d", histogramSum(h))
	}
	f := e.get(t, e.alice, "/api/projects/default/facets?"+itRange+"&service=api", 200)
	if f["total"].(float64) != 3 || f["levels"].(map[string]any)["error"].(float64) != 1 {
		t.Errorf("default facets = %v", f)
	}
}

func TestIntegrationReadErrorsHideClickHouse(t *testing.T) {
	e := newReadEnv(t)
	// A bad parameter is answered from our validation, not by ClickHouse.
	r := e.h.do("GET", "/api/projects/alpha/logs?"+itRange+"&q="+url.QueryEscape(strings.Repeat("x", 300)), "", e.alice, nil)
	e.h.wantStatus(r, 400, "long q")
	// Break storage: every endpoint answers with a generic message.
	if err := e.ch.Exec(context.Background(), "DROP TABLE "+e.db+".logs"); err != nil {
		t.Fatal(err)
	}
	for _, ep := range []string{"logs", "histogram", "facets"} {
		r := e.h.do("GET", "/api/projects/alpha/"+ep+"?"+itRange, "", e.alice, nil)
		if r.status < 500 {
			t.Errorf("%s with the table gone: %d %s", ep, r.status, r.body)
		}
		for _, leak := range []string{"Exception", "Code:", "logs_rit", "UNKNOWN_TABLE", "SELECT", "doesn't exist"} {
			if strings.Contains(r.body, leak) {
				t.Errorf("%s: body leaks %q: %s", ep, leak, r.body)
			}
		}
	}
}

// TestIntegrationHostileServiceNames sends service lists whose names contain
// quotes, backslashes, brackets and control characters through ClickHouse:
// each must match exactly its own rows and nothing else.
func TestIntegrationHostileServiceNames(t *testing.T) {
	e := newReadEnv(t)
	names := []string{`pay'ments`, `back\slash`, `x\'`, `'] OR 1=1 --`, "tab\there", `''`, `[`, `\\`}
	var body strings.Builder
	for i, n := range names {
		b, _ := json.Marshal(map[string]any{
			"project_id": e.alphaID, "event_id": fmt.Sprintf("h-%d", i), "timestamp": "2026-11-07 02:10:00.000",
			"level": "info", "service": n, "message": "hostile", "fingerprint": 0, "trace_id": "",
			"attributes": map[string]string{}, "ingested_at": "2026-11-07 03:00:00.000",
		})
		body.Write(b)
		body.WriteByte('\n')
	}
	if err := e.ch.InsertJSONEachRow(context.Background(), "logs", []byte(body.String())); err != nil {
		t.Fatal(err)
	}
	rng := "from=2026-11-07T02:09:00Z&to=2026-11-07T02:11:00Z"
	for i := 0; i+1 < len(names); i++ {
		pair := names[i] + "," + names[i+1]
		logs := e.get(t, e.alice, "/api/projects/alpha/logs?"+rng+"&service="+url.QueryEscape(pair), 200)
		got := map[string]bool{}
		for _, ev := range logs["events"].([]any) {
			got[ev.(map[string]any)["service"].(string)] = true
		}
		if len(got) != 2 || !got[names[i]] || !got[names[i+1]] {
			t.Errorf("service=%q matched %v", pair, got)
		}
		f := e.get(t, e.alice, "/api/projects/alpha/facets?"+rng+"&service="+url.QueryEscape(pair), 200)
		if f["total"].(float64) != 2 {
			t.Errorf("facets service=%q total = %v", pair, f["total"])
		}
	}
	// One name alone keeps the v0.1 single-value path.
	if logs := e.get(t, e.alice, "/api/projects/alpha/logs?"+rng+"&service="+url.QueryEscape(names[0]), 200); len(logs["events"].([]any)) != 1 {
		t.Errorf("single hostile service: %v", logs["events"])
	}
}
