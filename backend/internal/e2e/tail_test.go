package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/api"
	"github.com/sinanturdu/logsage/backend/internal/keys"
	"github.com/sinanturdu/logsage/backend/internal/password"
	"github.com/sinanturdu/logsage/backend/internal/query"
	"github.com/sinanturdu/logsage/backend/internal/storage/clickhouse"
)

// chProxy sits between the tail and ClickHouse; down() makes ClickHouse
// unreachable for the tail only (the processor keeps writing directly).
type chProxy struct {
	ln     net.Listener
	target string
	mu     sync.Mutex
	isDown bool
	conns  map[net.Conn]bool
}

func newCHProxy(t *testing.T, target string) *chProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &chProxy{ln: ln, target: target, conns: map[net.Conn]bool{}}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go p.serve(c)
		}
	}()
	t.Cleanup(func() { ln.Close(); p.down() })
	return p
}

func (p *chProxy) serve(c net.Conn) {
	p.mu.Lock()
	if p.isDown {
		p.mu.Unlock()
		c.Close()
		return
	}
	p.conns[c] = true
	p.mu.Unlock()
	up, err := net.Dial("tcp", p.target)
	if err != nil {
		c.Close()
		return
	}
	p.mu.Lock()
	p.conns[up] = true
	p.mu.Unlock()
	go func() { _, _ = io.Copy(up, c); up.Close() }()
	_, _ = io.Copy(c, up)
	c.Close()
}

func (p *chProxy) down() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.isDown = true
	for c := range p.conns {
		c.Close()
	}
	p.conns = map[net.Conn]bool{}
}

func (p *chProxy) up() { p.mu.Lock(); p.isDown = false; p.mu.Unlock() }

type tailEnv struct {
	*env
	api     *httptest.Server
	srv     *api.Server
	proxy   *chProxy
	keys    map[string]string // project -> ingest key
	ids     map[string]string // project slug -> project id
	cookies map[string]string // user -> session token
}

const tailPW = "a long e2e passphrase"

func newTailEnv(t *testing.T) *tailEnv {
	t.Helper()
	e := newEnv(t)
	ctx := context.Background()
	chURL, _ := url.Parse(os.Getenv("CLICKHOUSE_TEST_URL"))
	proxy := newCHProxy(t, chURL.Host)
	user := os.Getenv("CLICKHOUSE_TEST_USER")
	if user == "" {
		user = "default"
	}
	viaProxy, err := clickhouse.New(clickhouse.Options{URL: "http://" + proxy.ln.Addr().String(), Database: e.ch.Database(),
		User: user, Password: os.Getenv("CLICKHOUSE_TEST_PASSWORD"), Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := query.NewService(viaProxy, query.Options{MaxWindow: time.Hour, DefaultLimit: 100, MaxLimit: 1000, MaxTextLen: 256, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	hasher, _ := password.NewHasher(password.Params{MemoryKiB: password.MinMemoryKiB, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}, 4)
	srv, err := api.New(e.store, hasher, api.NewLimiter(100, time.Second, time.Minute, 1000), api.Options{
		IdleTTL: time.Hour, AbsoluteTTL: time.Hour, Logs: svc, Tail: svc,
		TailOptions: api.TailOptions{PollInterval: 200 * time.Millisecond, Heartbeat: time.Second, AuthRecheck: 500 * time.Millisecond,
			Overlap: 5 * time.Second, RetryMax: time.Second, WriteTimeout: 5 * time.Second, MaxConnUser: 4, MaxConnProject: 8, MaxConnTotal: 16},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv.Register(mux)
	hs := httptest.NewServer(mux)
	t.Cleanup(func() { srv.CloseStreams(); hs.Close() })

	te := &tailEnv{env: e, api: hs, srv: srv, proxy: proxy, keys: map[string]string{}, ids: map[string]string{}, cookies: map[string]string{}}
	for _, p := range []struct{ user, slug string }{{"alice", "alpha"}, {"bob", "beta"}} {
		phc, _ := hasher.Hash(ctx, tailPW)
		u, err := e.store.CreateUser(ctx, p.user+"@example.com", phc)
		if err != nil {
			t.Fatal(err)
		}
		proj, err := e.store.CreateProjectWithOwner(ctx, p.slug, p.slug, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		te.ids[p.slug] = proj.ID
		te.keys[p.slug], _ = e.newKey(p.slug, keys.KindIngest)
		te.cookies[p.user] = te.login(p.user)
	}
	return te
}

func (te *tailEnv) login(user string) string {
	te.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, te.api.URL+"/api/auth/login",
		strings.NewReader(`{"email":"`+user+`@example.com","password":"`+tailPW+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", te.api.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		te.t.Fatal(err)
	}
	resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == "logsage_session" {
			return c.Value
		}
	}
	te.t.Fatalf("login %s: %d", user, resp.StatusCode)
	return ""
}

type tailEvent struct{ name, id, data string }

type tailConn struct {
	events chan tailEvent
	cancel context.CancelFunc
}

func (te *tailEnv) open(user, slug, query, lastID string) (*tailConn, int) {
	te.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, te.api.URL+"/api/projects/"+slug+"/tail?"+query, nil)
	if user != "" {
		req.AddCookie(&http.Cookie{Name: "logsage_session", Value: te.cookies[user]})
	}
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		te.t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		cancel()
		return nil, resp.StatusCode
	}
	c := &tailConn{events: make(chan tailEvent, 10_000), cancel: cancel}
	go func() {
		defer close(c.events)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		var ev tailEvent
		for sc.Scan() {
			switch l := sc.Text(); {
			case l == "":
				if ev.name != "" {
					c.events <- ev
				}
				ev = tailEvent{}
			case strings.HasPrefix(l, "event: "):
				ev.name = l[7:]
			case strings.HasPrefix(l, "id: "):
				ev.id = l[4:]
			case strings.HasPrefix(l, "data: "):
				ev.data = l[6:]
			}
		}
	}()
	te.t.Cleanup(cancel)
	return c, 200
}

// collect gathers log events until want arrive or the timeout passes; it
// also returns the status states seen and the id of the last log event.
func collect(t *testing.T, c *tailConn, want int, timeout time.Duration) (logs []query.Event, states []string, lastID string) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if want >= 0 && len(logs) >= want {
			return
		}
		select {
		case ev, ok := <-c.events:
			if !ok {
				return
			}
			switch ev.name {
			case "log":
				var e query.Event
				if err := json.Unmarshal([]byte(ev.data), &e); err != nil {
					t.Fatal(err)
				}
				logs = append(logs, e)
				lastID = ev.id
			case "status":
				var st struct {
					State, Reason string
					Heartbeat     bool
				}
				_ = json.Unmarshal([]byte(ev.data), &st)
				if !st.Heartbeat {
					states = append(states, st.State+":"+st.Reason)
				}
			case "reset":
				states = append(states, "reset")
			}
		case <-deadline:
			return
		}
	}
}

// e2eBound is the generous upper bound of the waits below; they return as
// soon as their condition holds, so it never decides a result.
const e2eBound = 30 * time.Second

// waitState reads events until a status "state:reason" arrives and returns
// the log events seen before it. It fails after e2eBound.
func waitState(t *testing.T, c *tailConn, want string) []query.Event {
	t.Helper()
	var logs []query.Event
	deadline := time.After(e2eBound)
	for {
		select {
		case ev, ok := <-c.events:
			if !ok {
				t.Fatalf("stream ended before status %s", want)
			}
			switch ev.name {
			case "log":
				var e query.Event
				_ = json.Unmarshal([]byte(ev.data), &e)
				logs = append(logs, e)
			case "status":
				var st struct{ State, Reason string }
				_ = json.Unmarshal([]byte(ev.data), &st)
				if st.State+":"+st.Reason == want {
					return logs
				}
			}
		case <-deadline:
			t.Fatalf("no status %s within %s", want, e2eBound)
		}
	}
}

func messages(logs []query.Event) []string {
	var out []string
	for _, e := range logs {
		out = append(out, e.Message)
	}
	sort.Strings(out)
	return out
}

func nowEvents(prefix string, n int, mutate func(i int, e *ev)) []ev {
	var out []ev
	for i := 0; i < n; i++ {
		e := ev{Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Level: "info", Service: "checkout", Message: fmt.Sprintf("%s %02d", prefix, i)}
		if mutate != nil {
			mutate(i, &e)
		}
		out = append(out, e)
	}
	return out
}

func waitLive(t *testing.T, c *tailConn) {
	t.Helper()
	_, states, _ := collect(t, c, -1, 0)
	deadline := time.After(e2eBound)
	for !strings.Contains(strings.Join(states, ","), "live:") {
		select {
		case ev := <-c.events:
			if ev.name == "status" && strings.Contains(ev.data, `"live"`) {
				return
			}
		case <-deadline:
			t.Fatal("stream did not become live")
		}
	}
}

func TestTailIngestToStreamWithIsolation(t *testing.T) {
	te := newTailEnv(t)
	a, _ := te.open("alice", "alpha", "", "")
	b, _ := te.open("bob", "beta", "", "")
	waitLive(t, a)
	waitLive(t, b)

	start := time.Now()
	same := nowEvents("same data", 10, nil)
	if post(t, te.ingest, te.keys["alpha"], same) != 202 || post(t, te.ingest, te.keys["beta"], same) != 202 ||
		post(t, te.ingest, te.keys["beta"], nowEvents("beta only", 3, nil)) != 202 {
		t.Fatal("ingest failed")
	}
	got, _, _ := collect(t, a, 10, e2eBound)
	if len(got) != 10 {
		t.Fatalf("alice got %d events, want 10", len(got))
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("delivery took %v", d)
	}
	extra, _, _ := collect(t, a, -1, 2*time.Second)
	if len(extra) != 0 {
		t.Fatalf("alice received beta's events: %v", messages(extra))
	}
	gotB, _, _ := collect(t, b, 13, e2eBound)
	if len(gotB) != 13 {
		t.Fatalf("bob got %d events, want 13", len(gotB))
	}
	// Non-members, no cookie, bad parameters.
	if _, st := te.open("bob", "alpha", "", ""); st != 404 {
		t.Errorf("non-member: %d", st)
	}
	if _, st := te.open("", "alpha", "", ""); st != 401 {
		t.Errorf("no cookie: %d", st)
	}
	if _, st := te.open("alice", "alpha", "from=2026-11-07T00:00:00Z", ""); st != 400 {
		t.Errorf("bad parameter: %d", st)
	}
}

func TestTailFiltersEndToEnd(t *testing.T) {
	te := newTailEnv(t)
	c, _ := te.open("alice", "alpha", "level=error,fatal&service=payments,checkout&q=TIMEOUT&trace_id=tr-1", "")
	waitLive(t, c)
	evs := nowEvents("mix", 16, func(i int, e *ev) {
		e.Level = []string{"error", "info", "fatal", "warn"}[i%4]
		e.Service = []string{"payments", "checkout", "search", "payments"}[i/4%4]
		e.Message = fmt.Sprintf("%s payment timeout %02d", []string{"x", "y"}[i%2], i)
		if i%3 == 0 {
			e.TraceID = "tr-1"
		}
	})
	want := 0
	for _, e := range evs {
		if (e.Level == "error" || e.Level == "fatal") && (e.Service == "payments" || e.Service == "checkout") && e.TraceID == "tr-1" {
			want++
		}
	}
	if post(t, te.ingest, te.keys["alpha"], evs) != 202 {
		t.Fatal("ingest failed")
	}
	got, _, _ := collect(t, c, want, e2eBound)
	more, _, _ := collect(t, c, -1, 2*time.Second)
	if len(got) != want || len(more) != 0 || want == 0 {
		t.Fatalf("got %d+%d events, want %d", len(got), len(more), want)
	}
}

// insertRow writes a row straight into ClickHouse, as a processor batch would.
func (te *tailEnv) insertRow(t *testing.T, project, id string, ts, ingested, inserted time.Time, msg string) {
	t.Helper()
	const f = "2006-01-02 15:04:05.000"
	b, _ := json.Marshal(map[string]any{"project_id": project, "event_id": id, "timestamp": ts.UTC().Format(f), "level": "warn",
		"service": "payments", "message": msg, "fingerprint": 0, "trace_id": "", "attributes": map[string]string{},
		"ingested_at": ingested.UTC().Format(f), "inserted_at": inserted.UTC().Format(f)})
	if err := te.ch.InsertJSONEachRow(context.Background(), "logs", append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}

func TestTailLateVisibleRowAndRedelivery(t *testing.T) {
	te := newTailEnv(t)
	alpha := te.ids["alpha"]
	c, _ := te.open("alice", "alpha", "", "")
	waitLive(t, c)
	now := time.Now()

	te.insertRow(t, alpha, "9000000000002-0", now, now, now, "newer row")
	got, _, _ := collect(t, c, 1, e2eBound)
	if len(got) != 1 || got[0].Message != "newer row" {
		t.Fatalf("got %v", messages(got))
	}
	// Older event timestamp, older ingest time and an inserted_at before the
	// row already streamed: it became visible late (a slow or retried batch).
	te.insertRow(t, alpha, "9000000000001-0", now.Add(-time.Minute), now.Add(-30*time.Second), now.Add(-2*time.Second), "late row")
	got, _, _ = collect(t, c, 1, e2eBound)
	if len(got) != 1 || got[0].Message != "late row" {
		t.Fatalf("late-visible row: got %v", messages(got))
	}
	// A redelivered event id (processor retry after a lost ack) is not sent twice.
	te.insertRow(t, alpha, "9000000000002-0", now, now, time.Now(), "newer row")
	te.insertRow(t, alpha, "9000000000003-0", now, now, time.Now(), "after redelivery")
	got, _, _ = collect(t, c, 1, e2eBound)
	more, _, _ := collect(t, c, -1, 2*time.Second)
	if len(got) != 1 || got[0].Message != "after redelivery" || len(more) != 0 {
		t.Fatalf("after redelivery got %v %v", messages(got), messages(more))
	}
}

func TestTailReconnectWithLastEventID(t *testing.T) {
	te := newTailEnv(t)
	c, _ := te.open("alice", "alpha", "", "")
	waitLive(t, c)
	if post(t, te.ingest, te.keys["alpha"], nowEvents("first", 5, nil)) != 202 {
		t.Fatal("ingest failed")
	}
	first, _, lastID := collect(t, c, 5, e2eBound)
	if len(first) != 5 || lastID == "" {
		t.Fatalf("first part: %d events", len(first))
	}
	c.cancel()
	time.Sleep(300 * time.Millisecond)
	if post(t, te.ingest, te.keys["alpha"], nowEvents("while away", 7, nil)) != 202 {
		t.Fatal("ingest failed")
	}
	te.waitRows(12)
	r, _ := te.open("alice", "alpha", "", lastID)
	second, _, _ := collect(t, r, 7, e2eBound)
	more, _, _ := collect(t, r, -1, 2*time.Second)
	all := append(append([]query.Event{}, first...), append(second, more...)...)
	seen := map[string]int{}
	for _, e := range all {
		seen[e.EventID]++
	}
	if len(all) != 12 || len(seen) != 12 {
		t.Fatalf("got %d events (%d distinct), want 12 without gaps or duplicates: %v", len(all), len(seen), messages(all))
	}
}

func TestTailClosesOnRevokedSessionAndDisabledUser(t *testing.T) {
	te := newTailEnv(t)
	c, _ := te.open("alice", "alpha", "", "")
	waitLive(t, c)
	req, _ := http.NewRequest(http.MethodPost, te.api.URL+"/api/auth/logout", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", te.api.URL)
	req.AddCookie(&http.Cookie{Name: "logsage_session", Value: te.cookies["alice"]})
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 204 {
		t.Fatalf("logout: %v %v", resp.StatusCode, err)
	}
	waitState(t, c, "closed:session_ended")

	b, _ := te.open("bob", "beta", "", "")
	waitLive(t, b)
	if _, err := te.store.DisableUser(context.Background(), "bob@example.com"); err != nil {
		t.Fatal(err)
	}
	waitState(t, b, "closed:session_ended")
}

func TestTailDegradedThenLiveWithoutGaps(t *testing.T) {
	te := newTailEnv(t)
	c, _ := te.open("alice", "alpha", "", "")
	waitLive(t, c)
	te.proxy.down()
	if post(t, te.ingest, te.keys["alpha"], nowEvents("during outage", 6, nil)) != 202 {
		t.Fatal("ingest failed")
	}
	if got := waitState(t, c, "degraded:storage_unavailable"); len(got) != 0 {
		t.Fatalf("log events while ClickHouse is unreachable: %v", messages(got))
	}
	te.proxy.up()
	got, states2, _ := collect(t, c, 6, e2eBound)
	if !strings.Contains(strings.Join(states2, ","), "live:") || len(got) != 6 {
		t.Fatalf("after recovery: states %v, %d events", states2, len(got))
	}
}
