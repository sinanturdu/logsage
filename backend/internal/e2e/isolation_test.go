package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/sinanturdu/logsage/backend/internal/auth"
	"github.com/sinanturdu/logsage/backend/internal/config"
	"github.com/sinanturdu/logsage/backend/internal/ingest"
	"github.com/sinanturdu/logsage/backend/internal/keys"
	"github.com/sinanturdu/logsage/backend/internal/processor"
	"github.com/sinanturdu/logsage/backend/internal/query"
	"github.com/sinanturdu/logsage/backend/internal/storage/clickhouse"
	"github.com/sinanturdu/logsage/backend/internal/storage/postgres"
	"github.com/sinanturdu/logsage/backend/migrations"
)

const (
	legacyIngest = "legacy-ingest-key-e2e"
	legacyQuery  = "legacy-query-key-e2e"
	cacheTTL     = 300 * time.Millisecond
)

// env is one isolated deployment: its own Postgres database, ClickHouse
// database and Redis stream, with ingest, processor and query running.
type env struct {
	t      *testing.T
	store  *postgres.Store
	ch     *clickhouse.Client
	rdb    *redis.Client
	stream string
	ingest *httptest.Server
	query  *httptest.Server
	// handlers with ALLOW_LEGACY_STATIC_KEYS=false over the same data
	ingestNoLegacy, queryNoLegacy *httptest.Server
}

func randomSuffix() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pgURL, redisAddr, chURL := os.Getenv("POSTGRES_TEST_URL"), os.Getenv("REDIS_TEST_ADDR"), os.Getenv("CLICKHOUSE_TEST_URL")
	if pgURL == "" || redisAddr == "" || chURL == "" {
		t.Skip("POSTGRES_TEST_URL, REDIS_TEST_ADDR and CLICKHOUSE_TEST_URL are needed")
	}
	ctx := context.Background()
	suffix := randomSuffix()

	// PostgreSQL: a fresh database with migrations.
	adminCfg := config.PostgresConfig{URL: pgURL, MaxConns: 2, ConnectTimeout: 10 * time.Second, QueryTimeout: 30 * time.Second}
	adminPool, err := postgres.Open(ctx, adminCfg)
	if err != nil {
		t.Fatal(err)
	}
	pgName := "logsage_e2e_" + suffix
	if _, err := adminPool.Exec(ctx, "CREATE DATABASE "+pgName); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(pgURL)
	u.Path = "/" + pgName
	pool, err := postgres.Open(ctx, config.PostgresConfig{URL: u.String(), MaxConns: 10, ConnectTimeout: 10 * time.Second, QueryTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := postgres.Migrate(ctx, pool, migrations.Postgres()); err != nil {
		t.Fatal(err)
	}
	store := postgres.NewStore(pool, 10*time.Second)

	// ClickHouse: a fresh database with the logs table.
	chUser := os.Getenv("CLICKHOUSE_TEST_USER")
	if chUser == "" {
		chUser = "default"
	}
	chDB := "logsage_e2e_" + suffix
	ch, err := clickhouse.New(clickhouse.Options{URL: chURL, Database: chDB, User: chUser, Password: os.Getenv("CLICKHOUSE_TEST_PASSWORD"), Timeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Migrate(ctx, migrations.ClickHouse(), clickhouse.MigrationData{Database: chDB, TTLDays: 30}); err != nil {
		t.Fatal(err)
	}

	// Redis stream, ingest publisher and processor.
	stream := "logsage:e2e:" + suffix
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	pub, err := ingest.NewRedisPublisher(ingest.RedisOptions{Addr: redisAddr, Stream: stream, Timeout: 5 * time.Second, MaxStreamLen: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	rs, err := processor.NewRedisStream(processor.RedisStreamOptions{Addr: redisAddr, Stream: stream, DeadStream: stream + ":dead", DeadMaxLen: 1000, Consumer: "e2e", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	proc, err := processor.New(rs, ch, processor.Options{
		Table: "logs", BatchSize: 500, ReadCount: 500, ReadBlock: 50 * time.Millisecond, FlushInterval: 100 * time.Millisecond,
		ClaimIdle: 30 * time.Second, ClaimInterval: time.Second, RetryInitial: 10 * time.Millisecond, RetryMax: 100 * time.Millisecond, ShutdownTimeout: 5 * time.Second,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = proc.Run(runCtx); close(done) }()

	newAuth := func(legacy bool) auth.Authenticator {
		a, err := auth.New(auth.Options{
			Verifier: keys.NewVerifier(store, nil), AllowLegacy: legacy, LegacyIngestKey: legacyIngest, LegacyQueryKey: legacyQuery,
			CacheTTL: cacheTTL, NegativeTTL: 100 * time.Millisecond, CacheSize: 1000,
		})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	serve := func(legacy bool) (*httptest.Server, *httptest.Server) {
		ih, err := ingest.NewHandler(pub, newAuth(legacy), ingest.DefaultLimits(), nil)
		if err != nil {
			t.Fatal(err)
		}
		qh, err := query.NewHandler(ch, newAuth(legacy), query.Options{MaxWindow: 168 * time.Hour, DefaultLimit: 100, MaxLimit: 1000, MaxTextLen: 256, Timeout: 10 * time.Second}, nil)
		if err != nil {
			t.Fatal(err)
		}
		im, qm := http.NewServeMux(), http.NewServeMux()
		ih.Register(im)
		qh.Register(qm)
		return httptest.NewServer(im), httptest.NewServer(qm)
	}
	e := &env{t: t, store: store, ch: ch, rdb: rdb, stream: stream}
	e.ingest, e.query = serve(true)
	e.ingestNoLegacy, e.queryNoLegacy = serve(false)

	t.Cleanup(func() {
		e.ingest.Close()
		e.query.Close()
		e.ingestNoLegacy.Close()
		e.queryNoLegacy.Close()
		stop()
		<-done
		_ = rs.Close()
		_ = pub.Close()
		_ = rdb.Del(context.Background(), stream, stream+":dead").Err()
		_ = rdb.Close()
		_ = ch.Exec(context.Background(), "DROP DATABASE IF EXISTS "+chDB)
		pool.Close()
		_, _ = adminPool.Exec(context.Background(), "DROP DATABASE IF EXISTS "+pgName+" WITH (FORCE)")
		adminPool.Close()
	})
	return e
}

// newKey creates a key with the store and returns its plaintext and prefix.
func (e *env) newKey(project string, kind keys.Kind) (string, string) {
	e.t.Helper()
	g, err := keys.Generate(kind)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.store.CreateKey(context.Background(), project, kind, "e2e", g.Prefix, g.Hash[:], nil); err != nil {
		e.t.Fatal(err)
	}
	return g.Plaintext, g.Prefix
}

type ev struct {
	Timestamp  string            `json:"timestamp"`
	Level      string            `json:"level"`
	Service    string            `json:"service"`
	Message    string            `json:"message"`
	TraceID    string            `json:"trace_id,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

var base = time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)

// events builds the same set for every origin: identical services, levels,
// messages and trace ids; only the "origin" attribute differs.
func events(origin string) []ev {
	var out []ev
	levels := []string{"info", "warn", "error", "info", "fatal", "debug"}
	for i := 0; i < 12; i++ {
		out = append(out, ev{
			Timestamp:  base.Add(time.Duration(i) * time.Second).Format(time.RFC3339),
			Level:      levels[i%len(levels)],
			Service:    []string{"checkout", "search"}[i%2],
			Message:    fmt.Sprintf("shared message %d payment timeout", i%3),
			TraceID:    []string{"trace-shared", ""}[i%2],
			Attributes: map[string]string{"origin": origin},
		})
	}
	return out
}

func post(t *testing.T, srv *httptest.Server, key string, evs []ev) int {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"events": evs})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+ingest.Path, bytes.NewReader(body))
	req.Header.Set("X-API-Key", key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

type result struct {
	status  int
	origins []string // origin attribute of every event, in order
	ids     []string
	next    *string
}

func get(t *testing.T, srv *httptest.Server, key string, params url.Values) result {
	t.Helper()
	if params.Get("from") == "" {
		params.Set("from", base.Add(-time.Hour).Format(time.RFC3339))
		params.Set("to", base.Add(time.Hour).Format(time.RFC3339))
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+query.Path+"?"+params.Encode(), nil)
	req.Header.Set("X-API-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	r := result{status: resp.StatusCode}
	if resp.StatusCode != 200 {
		return r
	}
	var body query.Response
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	for _, e := range body.Events {
		r.origins = append(r.origins, e.Attributes["origin"])
		r.ids = append(r.ids, e.EventID)
	}
	r.next = body.NextCursor
	return r
}

func (e *env) waitRows(n int) {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		out, err := e.ch.Query(context.Background(), "SELECT count() FROM logs FINAL FORMAT TabSeparated")
		if err == nil {
			if got, _ := strconv.Atoi(strings.TrimSpace(string(out))); got >= n {
				return
			}
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("ClickHouse did not reach %d rows: %s %v", n, out, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func onlyOrigin(t *testing.T, what string, r result, origin string, want int) {
	t.Helper()
	if r.status != 200 {
		t.Fatalf("%s: status %d", what, r.status)
	}
	for _, o := range r.origins {
		if o != origin {
			t.Fatalf("%s: got an event from %q, want only %q (%v)", what, o, origin, r.origins)
		}
	}
	if want >= 0 && len(r.origins) != want {
		t.Fatalf("%s: %d events, want %d", what, len(r.origins), want)
	}
}

func TestProjectIsolation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, p := range []string{"alpha", "beta"} {
		if _, err := e.store.CreateProject(ctx, strings.ToUpper(p), p); err != nil {
			t.Fatal(err)
		}
	}
	aIng, _ := e.newKey("alpha", keys.KindIngest)
	aQry, _ := e.newKey("alpha", keys.KindQuery)
	bIng, _ := e.newKey("beta", keys.KindIngest)
	bQry, _ := e.newKey("beta", keys.KindQuery)

	if s := post(t, e.ingest, aIng, events("A")); s != 202 {
		t.Fatalf("ingest A: %d", s)
	}
	if s := post(t, e.ingest, bIng, events("B")); s != 202 {
		t.Fatalf("ingest B: %d", s)
	}
	if s := post(t, e.ingest, legacyIngest, events("LEGACY")[:3]); s != 202 {
		t.Fatalf("ingest legacy: %d", s)
	}
	e.waitRows(27)

	filters := map[string]url.Values{
		"all":      {},
		"level":    {"level": {"error,fatal"}},
		"service":  {"service": {"checkout"}},
		"q":        {"q": {"PAYMENT timeout"}},
		"trace_id": {"trace_id": {"trace-shared"}},
		"combined": {"service": {"search"}, "level": {"warn,info,debug,fatal,error"}, "q": {"shared"}},
	}
	for name, f := range filters {
		ra := get(t, e.query, aQry, cloneValues(f))
		rb := get(t, e.query, bQry, cloneValues(f))
		onlyOrigin(t, "A "+name, ra, "A", -1)
		onlyOrigin(t, "B "+name, rb, "B", -1)
		if len(ra.origins) == 0 || len(ra.origins) != len(rb.origins) {
			t.Fatalf("%s: A saw %d, B saw %d; identical data should give equal non-zero counts", name, len(ra.origins), len(rb.origins))
		}
	}
	onlyOrigin(t, "A all", get(t, e.query, aQry, url.Values{}), "A", 12)

	// Full pagination stays inside the project, every row exactly once.
	for key, origin := range map[string]string{aQry: "A", bQry: "B"} {
		seen := map[string]bool{}
		params := url.Values{"limit": {"5"}}
		for page := 0; ; page++ {
			r := get(t, e.query, key, cloneValues(params))
			onlyOrigin(t, origin+" page "+strconv.Itoa(page), r, origin, -1)
			for _, id := range r.ids {
				if seen[id] {
					t.Fatalf("%s: event %s repeated", origin, id)
				}
				seen[id] = true
			}
			if r.next == nil {
				break
			}
			params.Set("cursor", *r.next)
		}
		if len(seen) != 12 {
			t.Fatalf("%s: paginated %d events, want 12", origin, len(seen))
		}
	}

	// A cursor produced for A, used with B's key, returns only B's data.
	first := get(t, e.query, aQry, url.Values{"limit": {"4"}})
	if first.next == nil {
		t.Fatal("expected a next cursor")
	}
	cross := get(t, e.query, bQry, url.Values{"limit": {"100"}, "cursor": {*first.next}})
	onlyOrigin(t, "A's cursor with B's key", cross, "B", -1)

	// Keys of the wrong kind, and keys sent to the other API.
	if s := get(t, e.query, bIng, url.Values{}).status; s != 401 {
		t.Errorf("B's ingest key on query: %d, want 401", s)
	}
	if s := post(t, e.ingest, aQry, events("A")[:1]); s != 401 {
		t.Errorf("A's query key on ingest: %d, want 401", s)
	}
	// The URL cannot pick a project.
	if s := get(t, e.query, aQry, url.Values{"project_id": {"beta"}}).status; s != 400 {
		t.Errorf("project_id parameter: %d, want 400", s)
	}

	// Legacy static keys see only the default project, and only with the flag.
	onlyOrigin(t, "legacy query key", get(t, e.query, legacyQuery, url.Values{}), "LEGACY", 3)
	if s := get(t, e.queryNoLegacy, legacyQuery, url.Values{}).status; s != 401 {
		t.Errorf("legacy query key with the flag off: %d, want 401", s)
	}
	if s := post(t, e.ingestNoLegacy, legacyIngest, events("LEGACY")[:1]); s != 401 {
		t.Errorf("legacy ingest key with the flag off: %d, want 401", s)
	}
	if s := get(t, e.query, legacyIngest, url.Values{}).status; s != 401 {
		t.Errorf("legacy ingest key on query: %d, want 401", s)
	}
}

func TestRevokedAndDisabledAfterTTL(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, p := range []string{"gamma", "delta"} {
		if _, err := e.store.CreateProject(ctx, p, p); err != nil {
			t.Fatal(err)
		}
	}
	rev, revPrefix := e.newKey("gamma", keys.KindIngest)
	dis, _ := e.newKey("delta", keys.KindQuery)
	if s := post(t, e.ingest, rev, events("G")[:1]); s != 202 {
		t.Fatalf("before revoke: %d", s)
	}
	if s := get(t, e.query, dis, url.Values{}).status; s != 200 {
		t.Fatalf("before disable: %d", s)
	}
	if _, err := e.store.RevokeKey(ctx, revPrefix); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.DisableProject(ctx, "delta"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(cacheTTL + 100*time.Millisecond)
	if s := post(t, e.ingest, rev, events("G")[:1]); s != 401 {
		t.Errorf("revoked key after TTL: %d, want 401", s)
	}
	if s := get(t, e.query, dis, url.Values{}).status; s != 401 {
		t.Errorf("disabled project's key after TTL: %d, want 401", s)
	}
}

// TestDeadLetterKeepsProject: entries with a bad payload keep their
// project_id in the dead-letter stream, and entries with a forged
// project_id never reach ClickHouse.
func TestDeadLetterKeepsProject(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p, err := e.store.CreateProject(ctx, "Eps", "epsilon")
	if err != nil {
		t.Fatal(err)
	}
	add := func(project, payload string) {
		t.Helper()
		if err := e.rdb.XAdd(ctx, &redis.XAddArgs{Stream: e.stream, Values: []any{
			"payload", payload, "project_id", project, "ingested_at", base.Format(time.RFC3339Nano),
		}}).Err(); err != nil {
			t.Fatal(err)
		}
	}
	add(p.ID, "{not json")
	add("evil'project", `{"timestamp":"2026-11-07T10:00:00Z","level":"info","service":"s","message":"forged"}`)
	add(p.ID, `{"timestamp":"2026-11-07T10:00:00Z","level":"info","service":"s","message":"good"}`)
	e.waitRows(1)

	deadline := time.Now().Add(10 * time.Second)
	var dead []redis.XMessage
	for time.Now().Before(deadline) {
		dead, _ = e.rdb.XRange(ctx, e.stream+":dead", "-", "+").Result()
		if len(dead) >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(dead) != 2 {
		t.Fatalf("dead-letter entries = %d, want 2", len(dead))
	}
	var projects []string
	for _, d := range dead {
		projects = append(projects, fmt.Sprint(d.Values["project_id"]))
	}
	sort.Strings(projects)
	if projects[0] != p.ID && projects[1] != p.ID {
		t.Errorf("dead-lettered project ids = %v, want %s kept", projects, p.ID)
	}
	out, _ := e.ch.Query(ctx, "SELECT count() FROM logs WHERE project_id = 'evil''project' FORMAT TabSeparated")
	if strings.TrimSpace(string(out)) != "0" {
		t.Errorf("forged project id stored: %s", out)
	}
}

func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}
