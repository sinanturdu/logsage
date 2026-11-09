package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/auth"
	"github.com/sinanturdu/logsage/backend/internal/password"
	"github.com/sinanturdu/logsage/backend/internal/query"
	"github.com/sinanturdu/logsage/backend/internal/storage/clickhouse"
	"github.com/sinanturdu/logsage/backend/internal/storage/postgres"
)

// readStore fakes the PostgreSQL calls the read endpoints make. Other Store
// methods are not used here (the embedded nil interface would panic).
type readStore struct {
	Store
	sessions map[string]string            // token -> user id
	roles    map[string]map[string]string // user -> slug -> role
	ids      map[string]string            // slug -> project id

	mu     sync.Mutex
	groups map[string]postgres.GroupState // project id + "/" + hex fingerprint -> state
	audits []postgres.AuditEntry
}

func (s *readStore) LookupSession(_ context.Context, hash []byte) (postgres.Session, error) {
	for tok, user := range s.sessions {
		if h, _ := hashToken(tok); string(h) == string(hash) {
			now := time.Now()
			return postgres.Session{ID: "s-" + user, UserID: user, LastSeenAt: now, IdleExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(time.Hour)}, nil
		}
	}
	return postgres.Session{}, postgres.ErrNotFound
}

func (s *readStore) TouchSession(context.Context, string, time.Time, time.Time) error { return nil }

func (s *readStore) MembershipFor(_ context.Context, user, slug string) (postgres.Membership, error) {
	role, ok := s.roles[user][slug]
	if !ok {
		return postgres.Membership{}, postgres.ErrNotFound
	}
	return postgres.Membership{Project: postgres.Project{ID: s.ids[slug], Slug: slug}, Role: postgres.Role(role)}, nil
}

// chStore fakes ClickHouse: it records parameters, can block, and can fail.
type chStore struct {
	mu      sync.Mutex
	params  []map[string]string
	block   chan struct{}
	started chan struct{}
	err     error
	// respond, when set, answers a statement with JSONEachRow lines.
	respond func(sql string, params map[string]string) []byte
}

func (c *chStore) QueryWithParams(ctx context.Context, sql string, params, _ map[string]string) ([]byte, error) {
	c.mu.Lock()
	c.params = append(c.params, params)
	started, block, err, respond := c.started, c.block, c.err, c.respond
	c.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err == nil && respond != nil {
		return respond(sql, params), nil
	}
	return nil, err
}
func (c *chStore) Ready(context.Context) error { return nil }

func (c *chStore) calls() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.params) }

// set changes the fake under its lock (the server reads it concurrently).
func (c *chStore) set(fn func(c *chStore)) { c.mu.Lock(); defer c.mu.Unlock(); fn(c) }

func (c *chStore) recorded() []map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]string(nil), c.params...)
}

type readFixture struct {
	srv    *httptest.Server
	st     *readStore
	ch     *chStore
	tokens map[string]string // test name -> session token
}

const fromTo = "from=2026-11-07T02:00:00Z&to=2026-11-07T02:15:00Z"

func newReadFixture(t *testing.T, maxConcurrent int, more ...func(*Options)) *readFixture {
	t.Helper()
	st := &readStore{
		sessions: map[string]string{},
		roles: map[string]map[string]string{
			"u-owner":  {"shop": "owner", "default": "owner"},
			"u-admin":  {"shop": "admin"},
			"u-viewer": {"shop": "viewer"},
			"u-out":    {"other": "owner"},
		},
		ids: map[string]string{"shop": "11111111-1111-1111-1111-111111111111", "other": "22222222-2222-2222-2222-222222222222", "default": auth.DefaultProjectID},
	}
	tokens := map[string]string{"tok-unknown": "not-a-valid-token"}
	for name, user := range map[string]string{"tok-owner": "u-owner", "tok-admin": "u-admin", "tok-viewer": "u-viewer", "tok-outsider": "u-out"} {
		tok, _, err := newToken()
		if err != nil {
			t.Fatal(err)
		}
		tokens[name] = tok
		st.sessions[tok] = user
	}
	ch := &chStore{}
	svc, err := query.NewService(ch, query.Options{MaxWindow: 24 * time.Hour, DefaultLimit: 100, MaxLimit: 1000, MaxTextLen: 256, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	hasher, _ := password.NewHasher(password.Params{MemoryKiB: password.MinMemoryKiB, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}, 1)
	opts := Options{IdleTTL: time.Hour, AbsoluteTTL: time.Hour, Logs: svc, MaxConcurrentQueries: maxConcurrent}
	for _, f := range more {
		f(&opts)
	}
	s, err := New(st, hasher, NewLimiter(5, time.Second, time.Minute, 100), opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &readFixture{srv: srv, st: st, ch: ch, tokens: tokens}
}

func (f *readFixture) get(t *testing.T, path, token string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, f.srv.URL+path, nil)
	if token != "" {
		req.AddCookie(&http.Cookie{Name: cookieName(false), Value: f.tokens[token]})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

var readEndpoints = []string{"logs", "histogram", "facets"}

func TestReadEndpointsRoleMatrix(t *testing.T) {
	f := newReadFixture(t, 4)
	for _, ep := range readEndpoints {
		for _, tt := range []struct {
			token, slug string
			want        int
		}{
			{"tok-owner", "shop", 200}, {"tok-admin", "shop", 200}, {"tok-viewer", "shop", 200},
			{"tok-outsider", "shop", 404}, // non-member
			{"tok-owner", "nope", 404},    // unknown project
			{"tok-owner", "NOT_A_SLUG", 404},
			{"", "shop", 401}, {"tok-unknown", "shop", 401},
		} {
			calls := f.ch.calls()
			status, body, _ := f.get(t, "/api/projects/"+tt.slug+"/"+ep+"?"+fromTo, tt.token)
			if status != tt.want {
				t.Errorf("%s %s as %s: %d %s, want %d", ep, tt.slug, tt.token, status, body, tt.want)
			}
			if tt.want != 200 && f.ch.calls() != calls {
				t.Errorf("%s %s as %s: ClickHouse was queried", ep, tt.slug, tt.token)
			}
		}
	}
	// Non-members get 404 even with bad parameters: membership comes first.
	if status, _, _ := f.get(t, "/api/projects/shop/logs?bogus=1", "tok-outsider"); status != 404 {
		t.Errorf("non-member with bad params: %d", status)
	}
}

func TestReadEndpointsScopeToMembership(t *testing.T) {
	f := newReadFixture(t, 4)
	for _, ep := range readEndpoints {
		f.ch.set(func(c *chStore) { c.params = nil })
		if status, body, _ := f.get(t, "/api/projects/shop/"+ep+"?"+fromTo+"&project_id=x", "tok-owner"); status != 400 || !strings.Contains(body, "unknown parameter") {
			t.Errorf("%s: project_id param accepted: %d %s", ep, status, body)
		}
		if status, _, _ := f.get(t, "/api/projects/shop/"+ep+"?"+fromTo, "tok-owner"); status != 200 {
			t.Fatalf("%s: %d", ep, status)
		}
		for _, p := range f.ch.recorded() {
			if p["project_id"] != "11111111-1111-1111-1111-111111111111" || p["project_alias"] != p["project_id"] {
				t.Errorf("%s: scope params %v", ep, p)
			}
		}
		f.ch.set(func(c *chStore) { c.params = nil })
		f.get(t, "/api/projects/default/"+ep+"?"+fromTo, "tok-owner")
		for _, p := range f.ch.recorded() {
			if p["project_id"] != auth.DefaultProjectID || p["project_alias"] != "default" {
				t.Errorf("%s: default scope params %v", ep, p)
			}
		}
	}
}

func TestReadEndpointsValidation(t *testing.T) {
	f := newReadFixture(t, 4)
	cases := []struct{ ep, query, want string }{
		{"logs", "to=2026-11-07T02:15:00Z", "from: required"},
		{"histogram", "from=2026-11-07T02:00:00Z", "to: required"},
		{"facets", "from=2026-11-07T02:00:00Z&to=2026-11-09T02:00:00Z", "max 24h0m0s"},
		{"logs", fromTo + "&limit=0", "limit: must be an integer"},
		{"logs", fromTo + "&cursor=zz", "cursor"},
		{"logs", fromTo + "&q=" + strings.Repeat("a", 257), "q: longer than 256"},
		{"histogram", fromTo + "&level=loud", "level: unknown value"},
		{"histogram", fromTo + "&bucket=2m", "bucket: must be one of"},
		{"histogram", fromTo + "&limit=5", "unknown parameter"},
		{"facets", fromTo + "&cursor=x", "unknown parameter"},
		{"facets", fromTo + "&service=a&service=b", "service: given more than once"},
		{"facets", fromTo + "&service=a,,b", "service: empty value"},
		{"logs", fromTo + "&service=" + strings.TrimSuffix(strings.Repeat("s,", 21), ","), "service: at most 20"},
	}
	for _, c := range cases {
		calls := f.ch.calls()
		status, body, _ := f.get(t, "/api/projects/shop/"+c.ep+"?"+c.query, "tok-viewer")
		if status != 400 || !strings.Contains(body, c.want) {
			t.Errorf("%s?%s: %d %s, want 400 %q", c.ep, c.query, status, body, c.want)
		}
		if f.ch.calls() != calls {
			t.Errorf("%s?%s: ClickHouse was queried", c.ep, c.query)
		}
	}
}

func TestReadEndpointsHideClickHouseErrors(t *testing.T) {
	f := newReadFixture(t, 4)
	f.ch.set(func(c *chStore) {
		c.err = &clickhouse.Error{Status: 500, Code: "62", Message: "Code: 62. DB::Exception: Syntax error near 'SELECT secret_column FROM logs'"}
	})
	for _, ep := range readEndpoints {
		status, body, _ := f.get(t, "/api/projects/shop/"+ep+"?"+fromTo, "tok-owner")
		if status < 500 || strings.Contains(body, "Syntax") || strings.Contains(body, "secret_column") || strings.Contains(body, "DB::") {
			t.Errorf("%s: %d %s", ep, status, body)
		}
	}
	f.ch.set(func(c *chStore) {
		c.err = &clickhouse.Error{Status: 500, Code: "159", Message: "Timeout exceeded: elapsed 10.1 seconds"}
	})
	if status, body, _ := f.get(t, "/api/projects/shop/facets?"+fromTo, "tok-owner"); status != 504 || strings.Contains(body, "elapsed") {
		t.Errorf("timeout: %d %s", status, body)
	}
}

func TestReadEndpointsConcurrencyCap(t *testing.T) {
	f := newReadFixture(t, 2)
	block, started := make(chan struct{}), make(chan struct{}, 10)
	f.ch.set(func(c *chStore) { c.block, c.started = block, started })
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, _, _ := f.get(t, "/api/projects/shop/logs?"+fromTo, "tok-owner")
			results <- status
		}()
	}
	<-started
	<-started

	status, body, hdr := f.get(t, "/api/projects/shop/histogram?"+fromTo, "tok-owner")
	if status != 429 || hdr.Get("Retry-After") != "1" || !strings.Contains(body, "too many requests") {
		t.Errorf("third query: %d %q Retry-After=%q", status, body, hdr.Get("Retry-After"))
	}
	// Invalid parameters are rejected before the cap is consulted.
	if status, _, _ := f.get(t, "/api/projects/shop/logs?bogus=1", "tok-owner"); status != 400 {
		t.Errorf("invalid query at the cap: %d", status)
	}
	// The cap is per user: another member still gets through.
	done := make(chan int, 1)
	go func() { s, _, _ := f.get(t, "/api/projects/shop/logs?"+fromTo, "tok-viewer"); done <- s }()
	<-started

	close(block)
	wg.Wait()
	close(results)
	for s := range results {
		if s != 200 {
			t.Errorf("blocked query finished with %d", s)
		}
	}
	if s := <-done; s != 200 {
		t.Errorf("other user: %d", s)
	}
	f.ch.set(func(c *chStore) { c.block = nil })
	if status, _, _ := f.get(t, "/api/projects/shop/facets?"+fromTo, "tok-owner"); status != 200 {
		t.Errorf("after release: %d", status)
	}
}

func TestQueryGate(t *testing.T) {
	g := newQueryGate(0)
	var rel []func()
	for i := 0; i < DefaultMaxConcurrentQueries; i++ {
		r, ok := g.acquire("u")
		if !ok {
			t.Fatalf("slot %d refused", i)
		}
		rel = append(rel, r)
	}
	if _, ok := g.acquire("u"); ok {
		t.Fatal("cap not enforced")
	}
	rel[0]()
	if _, ok := g.acquire("u"); !ok {
		t.Fatal("slot not released")
	}
	for _, r := range rel[1:] {
		r()
	}
}
