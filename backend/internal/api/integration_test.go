package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sinanturdu/logsage/backend/internal/api"
	"github.com/sinanturdu/logsage/backend/internal/auth"
	"github.com/sinanturdu/logsage/backend/internal/ingest"
	"github.com/sinanturdu/logsage/backend/internal/keys"
	"github.com/sinanturdu/logsage/backend/internal/password"
	"github.com/sinanturdu/logsage/backend/internal/query"
	"github.com/sinanturdu/logsage/backend/internal/storage/postgres"
	"github.com/sinanturdu/logsage/backend/internal/storage/postgres/pgtest"
)

const (
	alicePW = "alice's long passphrase"
	bobPW   = "bob's very long passphrase"
)

// syncBuf collects log output from concurrent handlers.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

type harness struct {
	t      *testing.T
	pool   *pgxpool.Pool
	st     *postgres.Store
	hasher *password.Hasher
	srv    *httptest.Server
	origin string
	logs   *syncBuf
	clock  atomic.Int64 // unix nanos
}

func (h *harness) now() time.Time          { return time.Unix(0, h.clock.Load()) }
func (h *harness) advance(d time.Duration) { h.clock.Add(int64(d)) }

func newHarness(t *testing.T, mutate func(*api.Options), lim *api.Limiter) *harness {
	t.Helper()
	pool, st, _ := pgtest.New(t)
	hasher, err := password.NewHasher(password.Params{MemoryKiB: password.MinMemoryKiB, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}, 4)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, pool: pool, st: st, hasher: hasher, logs: &syncBuf{}}
	h.clock.Store(time.Now().UnixNano())
	opts := api.Options{IdleTTL: time.Hour, AbsoluteTTL: 3 * time.Hour, CookieSecure: false, TouchInterval: time.Minute, Now: h.now}
	if mutate != nil {
		mutate(&opts)
	}
	if lim == nil {
		lim = api.NewLimiter(100, time.Second, time.Minute, 1000)
	}
	log := slog.New(slog.NewJSONHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s, err := api.New(st, hasher, lim, opts, log)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Register(mux)
	h.srv = httptest.NewServer(mux)
	t.Cleanup(h.srv.Close)
	h.origin = h.srv.URL
	return h
}

func (h *harness) createUser(email, pw string) postgres.User {
	h.t.Helper()
	phc, err := h.hasher.Hash(context.Background(), pw)
	if err != nil {
		h.t.Fatal(err)
	}
	u, err := h.st.CreateUser(context.Background(), email, phc)
	if err != nil {
		h.t.Fatal(err)
	}
	return u
}

type resp struct {
	status int
	body   string
	header http.Header
	cookie *http.Cookie // session cookie set by the response, if any
}

func (r resp) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(r.body), &m); err != nil {
		t.Fatalf("body %q: %v", r.body, err)
	}
	return m
}

// do sends a request as a same-origin browser would, unless hdr overrides.
func (h *harness) do(method, path, body, token string, hdr map[string]string) resp {
	h.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, rd)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", h.origin)
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	if token != "" {
		req.AddCookie(&http.Cookie{Name: "logsage_session", Value: token})
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, body: string(b), header: res.Header}
	for _, c := range res.Cookies() {
		if c.Name == "logsage_session" {
			out.cookie = c
		}
	}
	return out
}

func (h *harness) login(email, pw string) string {
	h.t.Helper()
	r := h.do("POST", "/api/auth/login", `{"email":"`+email+`","password":"`+pw+`"}`, "", nil)
	if r.status != http.StatusOK || r.cookie == nil || r.cookie.Value == "" {
		h.t.Fatalf("login %s = %d %s", email, r.status, r.body)
	}
	return r.cookie.Value
}

func (h *harness) wantStatus(r resp, want int, what string) {
	h.t.Helper()
	if r.status != want {
		h.t.Fatalf("%s = %d %s, want %d", what, r.status, r.body, want)
	}
}

func TestIntegrationLoginMeAndCookie(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.createUser("alice@example.com", alicePW)
	r := h.do("POST", "/api/auth/login", `{"email":"ALICE@example.com","password":"`+alicePW+`"}`, "", nil)
	h.wantStatus(r, 200, "login")
	c := r.cookie
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Secure || c.MaxAge != 3*3600 {
		t.Fatalf("cookie = %+v", c)
	}
	if strings.Contains(r.body, alicePW) || strings.Contains(r.body, "argon2") || strings.Contains(r.body, c.Value) {
		t.Errorf("login body leaks a secret: %s", r.body)
	}
	me := h.do("GET", "/api/auth/me", "", c.Value, nil)
	h.wantStatus(me, 200, "me")
	if u := me.json(t)["user"].(map[string]any); u["email"] != "alice@example.com" {
		t.Errorf("me = %s", me.body)
	}
	if me.header.Get("Cache-Control") != "no-store" || me.header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers = %v", me.header)
	}
	unauth := h.do("GET", "/api/auth/me", "", "", nil)
	h.wantStatus(unauth, 401, "me without cookie")
	if strings.TrimSpace(unauth.body) != `{"error":"unauthorized"}` {
		t.Errorf("401 body = %q", unauth.body)
	}
	h.wantStatus(h.do("GET", "/api/auth/me", "", "garbage", nil), 401, "me with garbage cookie")
	h.wantStatus(h.do("POST", "/api/auth/login", `{"email":"a@b.c","password":"x","admin":true}`, "", nil), 400, "unknown field")
	h.wantStatus(h.do("POST", "/api/auth/login", `{"email":"`+strings.Repeat("a", 20000)+`"}`, "", nil), 413, "huge body")
}

func TestIntegrationWrongPasswordAndUnknownEmailLookAlike(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.createUser("alice@example.com", alicePW)
	wrong := h.do("POST", "/api/auth/login", `{"email":"alice@example.com","password":"not the password"}`, "", nil)
	unknown := h.do("POST", "/api/auth/login", `{"email":"nobody@example.com","password":"not the password"}`, "", nil)
	if wrong.status != 401 || unknown.status != 401 || wrong.body != unknown.body || wrong.cookie != nil || unknown.cookie != nil {
		t.Fatalf("wrong = %d %q, unknown = %d %q", wrong.status, wrong.body, unknown.status, unknown.body)
	}
}

func TestIntegrationSessionFixationAndLogout(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.createUser("alice@example.com", alicePW)
	a := h.login("alice@example.com", alicePW)
	b := h.login("alice@example.com", alicePW)
	if a == b {
		t.Fatal("two logins returned the same token")
	}
	// Logging in while holding a session replaces it.
	r := h.do("POST", "/api/auth/login", `{"email":"alice@example.com","password":"`+alicePW+`"}`, a, nil)
	h.wantStatus(r, 200, "re-login")
	c := r.cookie.Value
	if c == a {
		t.Fatal("re-login kept the presented token")
	}
	h.wantStatus(h.do("GET", "/api/auth/me", "", a, nil), 401, "old session after re-login")

	// logout revokes one session.
	lo := h.do("POST", "/api/auth/logout", "{}", b, nil)
	h.wantStatus(lo, 204, "logout")
	if lo.cookie == nil || lo.cookie.MaxAge >= 0 {
		t.Errorf("logout cookie = %+v, want cleared", lo.cookie)
	}
	h.wantStatus(h.do("GET", "/api/auth/me", "", b, nil), 401, "me after logout")
	h.wantStatus(h.do("GET", "/api/auth/me", "", c, nil), 200, "other session after logout")

	// logout-all revokes every session.
	d := h.login("alice@example.com", alicePW)
	h.wantStatus(h.do("POST", "/api/auth/logout-all", "{}", c, nil), 204, "logout-all")
	h.wantStatus(h.do("GET", "/api/auth/me", "", c, nil), 401, "me after logout-all")
	h.wantStatus(h.do("GET", "/api/auth/me", "", d, nil), 401, "other session after logout-all")
}

func TestIntegrationIdleAndAbsoluteExpiry(t *testing.T) {
	h := newHarness(t, nil, nil) // idle 1h, absolute 3h, touch every minute
	h.createUser("alice@example.com", alicePW)
	idle := h.login("alice@example.com", alicePW)
	h.advance(59 * time.Minute)
	h.wantStatus(h.do("GET", "/api/auth/me", "", idle, nil), 200, "me within idle TTL")
	h.advance(59 * time.Minute) // activity above slid the idle expiry
	h.wantStatus(h.do("GET", "/api/auth/me", "", idle, nil), 200, "me within slid idle TTL")
	h.advance(61 * time.Minute)
	h.wantStatus(h.do("GET", "/api/auth/me", "", idle, nil), 401, "me after idle TTL")

	abs := h.login("alice@example.com", alicePW)
	for i := 0; i < 5; i++ { // active every 30 minutes: 2h30m
		h.advance(30 * time.Minute)
		h.wantStatus(h.do("GET", "/api/auth/me", "", abs, nil), 200, "active session")
	}
	h.advance(31 * time.Minute) // 3h01m since login
	h.wantStatus(h.do("GET", "/api/auth/me", "", abs, nil), 401, "me after absolute TTL")
}

func TestIntegrationDisabledUser(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.createUser("alice@example.com", alicePW)
	tok := h.login("alice@example.com", alicePW)
	if _, err := h.st.DisableUser(context.Background(), "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	h.wantStatus(h.do("GET", "/api/auth/me", "", tok, nil), 401, "me of disabled user")
	r := h.do("POST", "/api/auth/login", `{"email":"alice@example.com","password":"`+alicePW+`"}`, "", nil)
	h.wantStatus(r, 401, "login of disabled user")
}

func TestIntegrationChangePassword(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.createUser("alice@example.com", alicePW)
	a := h.login("alice@example.com", alicePW)
	b := h.login("alice@example.com", alicePW)
	h.wantStatus(h.do("POST", "/api/auth/change-password", `{"current_password":"wrong password!!","new_password":"a brand new passphrase"}`, a, nil), 403, "wrong current")
	h.wantStatus(h.do("POST", "/api/auth/change-password", `{"current_password":"`+alicePW+`","new_password":"short"}`, a, nil), 400, "weak new")
	h.wantStatus(h.do("POST", "/api/auth/change-password", `{"current_password":"`+alicePW+`","new_password":"alice@example.com"}`, a, nil), 400, "email as password")
	h.wantStatus(h.do("POST", "/api/auth/change-password", `{"current_password":"`+alicePW+`","new_password":"a brand new passphrase"}`, a, nil), 204, "change")
	h.wantStatus(h.do("GET", "/api/auth/me", "", a, nil), 200, "current session after change")
	h.wantStatus(h.do("GET", "/api/auth/me", "", b, nil), 401, "other session after change")
	h.wantStatus(h.do("POST", "/api/auth/login", `{"email":"alice@example.com","password":"`+alicePW+`"}`, "", nil), 401, "old password")
	h.login("alice@example.com", "a brand new passphrase")
}

func TestIntegrationRehashOnLogin(t *testing.T) {
	h := newHarness(t, nil, nil)
	weak, _ := password.NewHasher(password.Params{MemoryKiB: password.MinMemoryKiB, Iterations: 2, Parallelism: 1, SaltLen: 16, KeyLen: 32}, 1)
	phc, _ := weak.Hash(context.Background(), alicePW)
	u, _ := h.st.CreateUser(context.Background(), "alice@example.com", phc)
	h.login("alice@example.com", alicePW)
	got, _ := h.st.UserByID(context.Background(), u.ID)
	if got.PasswordHash == phc || !strings.Contains(got.PasswordHash, "t=1") {
		t.Errorf("hash not upgraded to current params: %s", got.PasswordHash[:30])
	}
	h.login("alice@example.com", alicePW)
}

// stubQuery records the scope of the last query and returns no rows.
type stubQuery struct {
	mu     sync.Mutex
	params map[string]string
}

func (s *stubQuery) QueryWithParams(_ context.Context, _ string, params, _ map[string]string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.params = params
	return nil, nil
}
func (s *stubQuery) Ready(context.Context) error { return nil }

func TestIntegrationProjectsKeysAndIsolation(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.createUser("alice@example.com", alicePW)
	h.createUser("bob@example.com", bobPW)
	h.createUser("carol@example.com", alicePW)
	alice := h.login("alice@example.com", alicePW)
	bob := h.login("bob@example.com", bobPW)

	r := h.do("POST", "/api/projects", `{"name":"Alpha","slug":"alpha"}`, alice, nil)
	h.wantStatus(r, 201, "create project")
	alphaID := r.json(t)["project"].(map[string]any)["id"].(string)
	h.wantStatus(h.do("POST", "/api/projects", `{"name":"Again","slug":"alpha"}`, bob, nil), 409, "duplicate slug")
	h.wantStatus(h.do("POST", "/api/projects", `{"name":"Bad","slug":"No Spaces"}`, alice, nil), 400, "bad slug")
	h.wantStatus(h.do("POST", "/api/projects", `{"name":"Beta","slug":"beta"}`, bob, nil), 201, "bob's project")

	lst := h.do("GET", "/api/projects", "", alice, nil)
	if ps := lst.json(t)["projects"].([]any); len(ps) != 1 || ps[0].(map[string]any)["role"] != "owner" {
		t.Fatalf("alice projects = %s", lst.body)
	}

	ki := h.do("POST", "/api/projects/alpha/keys", `{"kind":"ingest","name":"ci","expires_in":"720h"}`, alice, nil)
	h.wantStatus(ki, 201, "create ingest key")
	ingestKey := ki.json(t)["key"].(string)
	ingestPrefix := ki.json(t)["info"].(map[string]any)["prefix"].(string)
	kq := h.do("POST", "/api/projects/alpha/keys", `{"kind":"query","name":"grafana"}`, alice, nil)
	h.wantStatus(kq, 201, "create query key")
	queryKey := kq.json(t)["key"].(string)
	h.wantStatus(h.do("POST", "/api/projects/alpha/keys", `{"kind":"admin","name":"x"}`, alice, nil), 400, "bad kind")
	h.wantStatus(h.do("POST", "/api/projects/alpha/keys", `{"kind":"query","name":"x","expires_in":"-1h"}`, alice, nil), 400, "bad expiry")

	kl := h.do("GET", "/api/projects/alpha/keys", "", alice, nil)
	h.wantStatus(kl, 200, "list keys")
	if strings.Contains(kl.body, ingestKey) || strings.Contains(kl.body, queryKey) || strings.Contains(kl.body, "hash") {
		t.Errorf("key list leaks secrets: %s", kl.body)
	}

	// Bob is not a member: every project endpoint is 404, never 403.
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/projects/alpha", ""},
		{"GET", "/api/projects/alpha/keys", ""},
		{"POST", "/api/projects/alpha/keys", `{"kind":"ingest","name":"evil"}`},
		{"POST", "/api/projects/alpha/keys/" + ingestPrefix + "/revoke", "{}"},
		{"POST", "/api/projects/alpha/disable", "{}"},
		{"GET", "/api/projects/nonexistent", ""},
	} {
		h.wantStatus(h.do(c.method, c.path, c.body, bob, nil), 404, "bob "+c.method+" "+c.path)
	}
	// Bob cannot revoke alpha's key through his own project either.
	h.wantStatus(h.do("POST", "/api/projects/beta/keys/"+ingestPrefix+"/revoke", "{}", bob, nil), 404, "revoke via other project")

	// Viewer: read-only.
	if _, err := h.st.AddMember(context.Background(), "alpha", "bob@example.com", postgres.RoleViewer); err != nil {
		t.Fatal(err)
	}
	h.wantStatus(h.do("GET", "/api/projects/alpha", "", bob, nil), 200, "viewer get")
	h.wantStatus(h.do("GET", "/api/projects/alpha/keys", "", bob, nil), 200, "viewer list keys")
	h.wantStatus(h.do("POST", "/api/projects/alpha/keys", `{"kind":"ingest","name":"v"}`, bob, nil), 403, "viewer create key")
	h.wantStatus(h.do("POST", "/api/projects/alpha/keys/"+ingestPrefix+"/revoke", "{}", bob, nil), 403, "viewer revoke")
	h.wantStatus(h.do("POST", "/api/projects/alpha/disable", "{}", bob, nil), 403, "viewer disable")
	// Admin: keys yes, disable no.
	_, _ = h.st.AddMember(context.Background(), "alpha", "carol@example.com", postgres.RoleAdmin)
	carol := h.login("carol@example.com", alicePW)
	h.wantStatus(h.do("POST", "/api/projects/alpha/keys", `{"kind":"ingest","name":"by-admin"}`, carol, nil), 201, "admin create key")
	h.wantStatus(h.do("POST", "/api/projects/alpha/disable", "{}", carol, nil), 403, "admin disable")

	// Keys created through the API work on ingest and query, scoped to alpha.
	verifier := keys.NewVerifier(h.st, nil)
	authn, err := auth.New(auth.Options{Verifier: verifier, CacheTTL: time.Nanosecond, NegativeTTL: time.Nanosecond, CacheSize: 10, VerifyTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	pub := ingest.NewMemoryPublisher(100)
	ih, err := ingest.NewHandler(pub, authn, ingest.Limits{MaxBodyBytes: 1 << 20, MaxEvents: 10, MaxMessageBytes: 1000,
		MaxAttributes: 10, MaxAttrKeyChars: 50, MaxAttrValueChars: 100, MaxTraceIDChars: 64}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ingestReq := func(key string) int {
		body := `{"events":[{"timestamp":"2026-11-06T10:15:30Z","level":"info","service":"api","message":"hello"}]}`
		req := httptest.NewRequest("POST", ingest.Path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Key", key)
		w := httptest.NewRecorder()
		ih.ServeHTTP(w, req)
		return w.Code
	}
	if code := ingestReq(ingestKey); code != 202 {
		t.Fatalf("ingest with API-created key = %d", code)
	}
	if evs := pub.Events(); len(evs) != 1 || evs[0].ProjectID != alphaID {
		t.Fatalf("ingested project = %+v, want %s", evs, alphaID)
	}
	if code := ingestReq(queryKey); code != 401 {
		t.Errorf("ingest with query key = %d, want 401", code)
	}
	sq := &stubQuery{}
	qh, err := query.NewHandler(sq, authn, query.Options{MaxWindow: 168 * time.Hour, DefaultLimit: 10, MaxLimit: 100, MaxTextLen: 64, Timeout: 10 * time.Second},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	qreq := httptest.NewRequest("GET", query.Path+"?from=2026-11-06T00:00:00Z&to=2026-11-07T00:00:00Z", nil)
	qreq.Header.Set("X-API-Key", queryKey)
	qw := httptest.NewRecorder()
	qh.ServeHTTP(qw, qreq)
	if qw.Code != 200 || sq.params["project_id"] != alphaID {
		t.Fatalf("query with API-created key = %d %s, scope %q", qw.Code, qw.Body.String(), sq.params["project_id"])
	}

	// Revoke through the API; the key stops working (cache TTL is 1ns here).
	h.wantStatus(h.do("POST", "/api/projects/alpha/keys/"+ingestPrefix+"/revoke", "{}", alice, nil), 200, "revoke")
	time.Sleep(2 * time.Millisecond)
	if code := ingestReq(ingestKey); code != 401 {
		t.Errorf("ingest with revoked key = %d, want 401", code)
	}

	// Owner disables; creating keys then conflicts.
	h.wantStatus(h.do("POST", "/api/projects/alpha/disable", "{}", alice, nil), 200, "owner disable")
	h.wantStatus(h.do("POST", "/api/projects/alpha/keys", `{"kind":"ingest","name":"late"}`, alice, nil), 409, "key on disabled project")

	// Secrets never reach the database or the logs.
	h.assertNoSecrets([]string{alicePW, bobPW, alice, bob, carol, ingestKey, queryKey})
}

// assertNoSecrets dumps every table as JSON text and the captured logs and
// checks that none of the given plaintext values appear.
func (h *harness) assertNoSecrets(secrets []string) {
	h.t.Helper()
	ctx := context.Background()
	var dump strings.Builder
	for _, table := range []string{"users", "sessions", "project_members", "projects", "api_keys", "audit_log", "schema_migrations"} {
		rows, err := h.pool.Query(ctx, "SELECT row_to_json(t)::text FROM "+table+" t")
		if err != nil {
			h.t.Fatal(err)
		}
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			dump.WriteString(s)
			dump.WriteByte('\n')
		}
		rows.Close()
	}
	all := dump.String() + h.logs.String()
	for _, s := range secrets {
		if s != "" && strings.Contains(all, s) {
			h.t.Errorf("a secret (%d chars) appears in the database or logs", len(s))
		}
	}
}

func TestIntegrationCSRFAndCORS(t *testing.T) {
	h := newHarness(t, func(o *api.Options) { o.AllowedOrigins = []string{"https://app.example.com"} }, nil)
	h.createUser("alice@example.com", alicePW)
	body := `{"email":"alice@example.com","password":"` + alicePW + `"}`
	h.wantStatus(h.do("POST", "/api/auth/login", body, "", map[string]string{"Origin": "https://evil.example"}), 403, "foreign origin")
	h.wantStatus(h.do("POST", "/api/auth/login", body, "", map[string]string{"Origin": ""}), 403, "no origin or referer")
	h.wantStatus(h.do("POST", "/api/auth/login", body, "", map[string]string{"Content-Type": "text/plain"}), 403, "text/plain")
	h.wantStatus(h.do("POST", "/api/auth/login", body, "", map[string]string{"Origin": "", "Referer": h.origin + "/login"}), 200, "same-host referer")
	r := h.do("POST", "/api/auth/login", body, "", map[string]string{"Origin": "https://app.example.com"})
	h.wantStatus(r, 200, "listed origin")
	if r.header.Get("Access-Control-Allow-Origin") != "https://app.example.com" || r.header.Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("CORS headers = %v", r.header)
	}
	tok := r.cookie.Value
	// A cross-origin state change with a valid cookie is still refused.
	h.wantStatus(h.do("POST", "/api/auth/logout-all", "{}", tok, map[string]string{"Origin": "https://evil.example"}), 403, "cross-origin logout-all")
	h.wantStatus(h.do("GET", "/api/auth/me", "", tok, nil), 200, "session survives refused request")
	g := h.do("GET", "/api/auth/me", "", tok, map[string]string{"Origin": "https://evil.example"})
	if g.header.Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS granted to a foreign origin")
	}
}

func TestIntegrationRegistration(t *testing.T) {
	off := newHarness(t, nil, nil)
	h := off
	h.wantStatus(h.do("POST", "/api/auth/register", `{"email":"new@example.com","password":"a long enough password"}`, "", nil), 404, "registration disabled by default")

	h = newHarness(t, func(o *api.Options) { o.RegistrationEnabled = true }, nil)
	h.wantStatus(h.do("POST", "/api/auth/register", `{"email":"new@example.com","password":"short"}`, "", nil), 400, "weak password")
	h.wantStatus(h.do("POST", "/api/auth/register", `{"email":"bad","password":"a long enough password"}`, "", nil), 400, "bad email")
	h.wantStatus(h.do("POST", "/api/auth/register", `{"email":"new@example.com","password":"a long enough password"}`, "", nil), 201, "register")
	h.wantStatus(h.do("POST", "/api/auth/register", `{"email":"NEW@example.com","password":"a long enough password"}`, "", nil), 409, "duplicate")
	h.login("new@example.com", "a long enough password")
}

func TestIntegrationLoginThrottle(t *testing.T) {
	lim := api.NewLimiter(2, time.Minute, 10*time.Minute, 1000)
	h := newHarness(t, nil, lim)
	h.createUser("alice@example.com", alicePW)
	bad := `{"email":"alice@example.com","password":"definitely wrong"}`
	for i := 0; i < 3; i++ {
		h.wantStatus(h.do("POST", "/api/auth/login", bad, "", nil), 401, "bad login")
	}
	r := h.do("POST", "/api/auth/login", `{"email":"alice@example.com","password":"`+alicePW+`"}`, "", nil)
	h.wantStatus(r, 429, "throttled even with the right password")
	if ra := r.header.Get("Retry-After"); ra != "60" {
		t.Errorf("Retry-After = %q, want 60", ra)
	}
	// Another email from the same IP is throttled too (per-IP key).
	h.wantStatus(h.do("POST", "/api/auth/login", `{"email":"other@example.com","password":"whatever pw!!"}`, "", nil), 429, "same IP other email")
	h.advance(61 * time.Second)
	h.login("alice@example.com", alicePW)
}

func TestIntegrationAuditRows(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.createUser("alice@example.com", alicePW)
	h.do("POST", "/api/auth/login", `{"email":"alice@example.com","password":"wrong password!!"}`, "", nil)
	h.do("POST", "/api/auth/login", `{"email":"ghost@example.com","password":"wrong password!!"}`, "", nil)
	tok := h.login("alice@example.com", alicePW)
	h.do("POST", "/api/projects", `{"name":"Alpha","slug":"alpha"}`, tok, nil)
	k := h.do("POST", "/api/projects/alpha/keys", `{"kind":"ingest","name":"ci"}`, tok, nil)
	prefix := k.json(t)["info"].(map[string]any)["prefix"].(string)
	h.do("POST", "/api/projects/alpha/keys/"+prefix+"/revoke", "{}", tok, nil)
	h.do("POST", "/api/auth/change-password", `{"current_password":"`+alicePW+`","new_password":"a brand new passphrase"}`, tok, nil)
	h.do("POST", "/api/auth/logout", "{}", tok, nil)

	want := map[string]int{"login.failure": 2, "login.success": 1, "project.create": 1, "key.create": 1, "key.revoke": 1, "password.change": 1, "logout": 1}
	rows, err := h.pool.Query(context.Background(), "SELECT action, count(*) FROM audit_log GROUP BY action")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for rows.Next() {
		var a string
		var n int
		_ = rows.Scan(&a, &n)
		got[a] = n
	}
	rows.Close()
	for a, n := range want {
		if got[a] != n {
			t.Errorf("audit %s = %d, want %d (all: %v)", a, got[a], n, got)
		}
	}
	var leaked int
	_ = h.pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_log WHERE target LIKE '%ghost%' OR detail LIKE '%!!%'").Scan(&leaked)
	if leaked != 0 {
		t.Error("audit log stores typed emails or passwords")
	}
	h.assertNoSecrets([]string{alicePW, "wrong password!!", "a brand new passphrase", tok, "ghost@example.com"})
}
