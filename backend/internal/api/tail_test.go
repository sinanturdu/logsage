package api

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/password"
	"github.com/sinanturdu/logsage/backend/internal/query"
	"github.com/sinanturdu/logsage/backend/internal/storage/postgres"
)

// ---- fake clock ----

type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWaiter
}

type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, fakeWaiter{at: c.now.Add(d), ch: ch})
	return ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	keep := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
		} else {
			keep = append(keep, w)
		}
	}
	c.waiters = keep
}

// ---- fake log storage ----

type memRow struct {
	project string
	row     query.TailRow
	visible bool
}

// memTail evaluates tail calls on in-memory rows. ParseTail is the real
// query service, so validation is the one the endpoint uses.
type memTail struct {
	*query.Service
	mu    sync.Mutex
	rows  []*memRow
	err   error
	calls int
}

func (m *memTail) add(project, id string, ins time.Time, level, msg string, visible bool) *memRow {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := &memRow{project: project, visible: visible, row: query.TailRow{
		Event: query.Event{EventID: id, Timestamp: ins.UTC().Format("2006-01-02T15:04:05.000Z"), Level: level, Service: "svc", Message: msg, Attributes: map[string]string{}},
		Key:   query.TailKey{InsertedAt: ins.UTC(), EventID: id},
	}}
	m.rows = append(m.rows, r)
	return r
}

func (m *memTail) show(r *memRow) { m.mu.Lock(); r.visible = true; m.mu.Unlock() }

func (m *memTail) setErr(err error) { m.mu.Lock(); m.err = err; m.mu.Unlock() }

func (m *memTail) match(scope query.Scope, f query.Filter, r *memRow) bool {
	if !r.visible || (r.project != scope.ProjectID && r.project != scope.Alias) {
		return false
	}
	ts, _ := time.Parse(time.RFC3339Nano, r.row.Event.Timestamp)
	if ts.Before(f.From) || !ts.Before(f.To) {
		return false
	}
	if len(f.Levels) > 0 && !contains(f.Levels, r.row.Event.Level) {
		return false
	}
	return f.Text == "" || strings.Contains(r.row.Event.Message, f.Text)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (m *memTail) sorted(scope query.Scope, f query.Filter, keep func(query.TailKey) bool) ([]query.TailRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	var out []query.TailRow
	for _, r := range m.rows {
		if m.match(scope, f, r) && keep(r.row.Key) {
			out = append(out, r.row)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key.Less(out[j].Key) })
	return out, nil
}

func (m *memTail) TailAfter(_ context.Context, scope query.Scope, f query.Filter, after query.TailKey, limit int) ([]query.TailRow, error) {
	rows, err := m.sorted(scope, f, func(k query.TailKey) bool { return after.Less(k) })
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, err
}

func (m *memTail) TailRange(_ context.Context, scope query.Scope, f query.Filter, lo, hi query.TailKey, limit int) ([]query.TailRow, error) {
	rows, err := m.sorted(scope, f, func(k query.TailKey) bool { return lo.Less(k) && !hi.Less(k) })
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, err
}

func (m *memTail) TailCount(ctx context.Context, scope query.Scope, f query.Filter, lo, hi query.TailKey) (uint64, error) {
	rows, err := m.TailRange(ctx, scope, f, lo, hi, 1<<30)
	ids := map[string]bool{}
	for _, r := range rows {
		ids[r.Event.EventID] = true
	}
	return uint64(len(ids)), err
}

// ---- fake PostgreSQL with revocation ----

type tailPG struct {
	*readStore
	mu       sync.Mutex
	revoked  map[string]bool // user
	disabled map[string]bool
	removed  map[string]bool // user: membership removed
}

func (p *tailPG) LookupSession(ctx context.Context, hash []byte) (postgres.Session, error) {
	s, err := p.readStore.LookupSession(ctx, hash)
	if err != nil {
		return s, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.revoked[s.UserID] {
		return postgres.Session{}, postgres.ErrNotFound
	}
	s.UserDisabled = p.disabled[s.UserID]
	return s, nil
}

func (p *tailPG) MembershipFor(ctx context.Context, user, slug string) (postgres.Membership, error) {
	p.mu.Lock()
	gone := p.removed[user]
	p.mu.Unlock()
	if gone {
		return postgres.Membership{}, postgres.ErrNotFound
	}
	return p.readStore.MembershipFor(ctx, user, slug)
}

// ---- fixture ----

var t0 = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

const (
	shopID  = "11111111-1111-1111-1111-111111111111"
	otherID = "22222222-2222-2222-2222-222222222222"
)

type tailFixture struct {
	srv    *httptest.Server
	s      *Server
	clock  *fakeClock
	mem    *memTail
	pg     *tailPG
	tokens map[string]string
}

func newTailFixture(t *testing.T, mutate func(*TailOptions)) *tailFixture {
	t.Helper()
	pg := &tailPG{readStore: &readStore{
		sessions: map[string]string{},
		roles: map[string]map[string]string{
			"u-owner": {"shop": "owner"}, "u-viewer": {"shop": "viewer"}, "u-other": {"other": "owner"}, "u-third": {"shop": "viewer"},
		},
		ids: map[string]string{"shop": shopID, "other": otherID},
	}, revoked: map[string]bool{}, disabled: map[string]bool{}, removed: map[string]bool{}}
	tokens := map[string]string{}
	for name, user := range map[string]string{"owner": "u-owner", "viewer": "u-viewer", "other": "u-other", "third": "u-third"} {
		tok, _, err := newToken()
		if err != nil {
			t.Fatal(err)
		}
		tokens[name] = tok
		pg.sessions[tok] = user
	}
	svc, _ := query.NewService(&chStore{}, query.Options{MaxWindow: time.Hour, DefaultLimit: 10, MaxLimit: 100, MaxTextLen: 64, Timeout: time.Second})
	mem := &memTail{Service: svc}
	clock := &fakeClock{now: t0}
	opts := TailOptions{PollInterval: time.Second, Batch: 3, Heartbeat: 15 * time.Second, MaxDuration: time.Hour,
		AuthRecheck: 30 * time.Second, MaxReplay: 5 * time.Minute, MaxLateness: 10 * time.Minute, Overlap: 30 * time.Second,
		WriteTimeout: time.Second, RetryMax: 8 * time.Second, MaxConnUser: 2, MaxConnProject: 3, MaxConnTotal: 4, MaxSeen: 1000, Clock: clock}
	if mutate != nil {
		mutate(&opts)
	}
	hasher, _ := password.NewHasher(password.Params{MemoryKiB: password.MinMemoryKiB, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}, 1)
	s, err := New(pg, hasher, NewLimiter(5, time.Second, time.Minute, 100), Options{
		IdleTTL: time.Hour, AbsoluteTTL: time.Hour, Tail: mem, TailOptions: opts, Now: clock.Now,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(func() { s.CloseStreams(); srv.Close() })
	return &tailFixture{srv: srv, s: s, clock: clock, mem: mem, pg: pg, tokens: tokens}
}

type sseEvent struct {
	name, id, data string
}

type stream struct {
	events chan sseEvent
	resp   *http.Response
	cancel context.CancelFunc
}

func (f *tailFixture) open(t *testing.T, who, query, lastID string) (*stream, int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, f.srv.URL+"/api/projects/shop/tail?"+query, nil)
	if who != "" {
		req.AddCookie(&http.Cookie{Name: cookieName(false), Value: f.tokens[who]})
	}
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		cancel()
		return nil, resp.StatusCode
	}
	s := &stream{events: make(chan sseEvent, 1000), resp: resp, cancel: cancel}
	go func() {
		defer close(s.events)
		sc := bufio.NewScanner(resp.Body)
		var ev sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if ev.name != "" {
					s.events <- ev
				}
				ev = sseEvent{}
			case strings.HasPrefix(line, "event: "):
				ev.name = line[7:]
			case strings.HasPrefix(line, "id: "):
				ev.id = line[4:]
			case strings.HasPrefix(line, "data: "):
				ev.data = line[6:]
			}
		}
	}()
	t.Cleanup(func() { cancel(); resp.Body.Close() })
	return s, 200
}

// next returns the next event that is not a heartbeat, advancing the fake
// clock by step while waiting.
func (f *tailFixture) next(t *testing.T, s *stream, step time.Duration) sseEvent {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case ev, ok := <-s.events:
			if !ok {
				t.Fatal("stream ended")
			}
			if ev.name == "status" && strings.Contains(ev.data, `"heartbeat":true`) {
				continue
			}
			return ev
		case <-time.After(10 * time.Millisecond):
			if step > 0 {
				f.clock.Advance(step)
			}
		}
	}
	t.Fatal("no event within 3s")
	return sseEvent{}
}

// quiet asserts no log event arrives while the clock moves on.
func (f *tailFixture) quiet(t *testing.T, s *stream, ticks int) {
	t.Helper()
	for i := 0; i < ticks; i++ {
		f.clock.Advance(time.Second)
		select {
		case ev := <-s.events:
			if ev.name == "log" {
				t.Fatalf("unexpected log event %s", ev.data)
			}
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func logID(t *testing.T, ev sseEvent) string {
	t.Helper()
	if ev.name != "log" {
		t.Fatalf("event %s %s, want log", ev.name, ev.data)
	}
	var e query.Event
	if err := json.Unmarshal([]byte(ev.data), &e); err != nil {
		t.Fatal(err)
	}
	return e.EventID
}

func expectStatus(t *testing.T, ev sseEvent, state, reason string) {
	t.Helper()
	var st statusData
	_ = json.Unmarshal([]byte(ev.data), &st)
	if ev.name != "status" || st.State != state || st.Reason != reason {
		t.Fatalf("event %s %s, want status %s %s", ev.name, ev.data, state, reason)
	}
}

// ---- tests ----

func TestTailCursorStrict(t *testing.T) {
	tok, _ := newStreamToken()
	c := tailCursor{Stream: tok, Last: query.TailKey{InsertedAt: t0.Add(1500 * time.Millisecond), EventID: "1793999215713-4"}, Seq: 42}
	s := encodeTailCursor(c)
	got, err := decodeTailCursor(s)
	if err != nil || got != c {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	if _, err := decodeTailCursor(encodeTailCursor(tailCursor{Stream: tok, Last: query.TailKey{InsertedAt: t0}})); err != nil {
		t.Errorf("cursor before the first in-order row rejected: %v", err)
	}
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	for name, bad := range map[string]string{
		"empty":         "",
		"padding":       s + "=",
		"not base64":    "!!!",
		"truncated":     s[:len(s)-3],
		"unknown field": b64(`{"v":1,"s":"` + tok + `","t":1,"e":"1-0","n":1,"x":1}`),
		"version":       b64(`{"v":2,"s":"` + tok + `","t":1,"e":"1-0","n":1}`),
		"bad token":     b64(`{"v":1,"s":"short","t":1,"e":"1-0","n":1}`),
		"bad event id":  b64(`{"v":1,"s":"` + tok + `","t":1,"e":"1-0' OR 1=1","n":1}`),
		"zero time":     b64(`{"v":1,"s":"` + tok + `","t":0,"e":"1-0","n":1}`),
		"reordered":     b64(`{"s":"` + tok + `","v":1,"t":1,"e":"1-0","n":1}`),
		"trailing":      b64(`{"v":1,"s":"` + tok + `","t":1,"e":"1-0","n":1} {}`),
		"too long":      strings.Repeat("A", 300),
	} {
		if _, err := decodeTailCursor(bad); err == nil {
			t.Errorf("%s: accepted %q", name, bad)
		}
	}
}

func TestSSEFrame(t *testing.T) {
	got := string(frame("log", "abc", query.Event{EventID: "1-0", Message: "line one\nline two\r\nid: fake\n\nevent: x", Attributes: map[string]string{}}))
	if !strings.HasPrefix(got, "id: abc\nevent: log\ndata: {") || !strings.HasSuffix(got, "}\n\n") || strings.Count(got, "\n") != 4 {
		t.Fatalf("frame = %q", got)
	}
	if got := string(frame("status", "", statusData{State: "live"})); got != "event: status\ndata: {\"state\":\"live\"}\n\n" {
		t.Fatalf("status frame = %q", got)
	}
}

func TestTailRequestChecks(t *testing.T) {
	f := newTailFixture(t, nil)
	for _, tt := range []struct {
		who, query, lastID string
		want               int
	}{
		{"", "", "", 401},
		{"other", "", "", 404},
		{"owner", "from=2026-11-07T00:00:00Z", "", 400},
		{"owner", "level=loud", "", 400},
		{"owner", "service=a,,b", "", 400},
		{"owner", "q=" + strings.Repeat("q", 65), "", 400},
		{"owner", "", "not-a-cursor", 400},
	} {
		if _, status := f.open(t, tt.who, tt.query, tt.lastID); status != tt.want {
			t.Errorf("%+v: status %d, want %d", tt, status, tt.want)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, f.srv.URL+"/api/projects/nope/tail", nil)
	req.AddCookie(&http.Cookie{Name: cookieName(false), Value: f.tokens["owner"]})
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 404 {
		t.Errorf("unknown project: %v %v", resp.StatusCode, err)
	}
	if f.mem.calls != 0 {
		t.Errorf("storage queried %d times for rejected requests", f.mem.calls)
	}
}

func TestTailHeadersAndStart(t *testing.T) {
	f := newTailFixture(t, nil)
	f.mem.add(shopID, "1-0", t0.Add(-5*time.Second), "info", "before start", true)
	s, _ := f.open(t, "viewer", "", "")
	for k, v := range map[string]string{"Content-Type": "text/event-stream", "Cache-Control": "no-store", "X-Accel-Buffering": "no"} {
		if got := s.resp.Header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	expectStatus(t, f.next(t, s, 0), "live", "")
	f.quiet(t, s, 3) // rows visible before the start are not streamed

	f.mem.add(shopID, "2-0", t0.Add(2*time.Second), "error", "after start", true)
	if id := logID(t, f.next(t, s, time.Second)); id != "2-0" {
		t.Fatalf("got %s", id)
	}
}

func TestTailFiltersAndScope(t *testing.T) {
	f := newTailFixture(t, nil)
	s, _ := f.open(t, "owner", "level=error&q=boom", "")
	expectStatus(t, f.next(t, s, 0), "live", "")
	f.mem.add(otherID, "1-0", t0.Add(time.Second), "error", "boom other project", true)
	f.mem.add(shopID, "2-0", t0.Add(time.Second), "info", "boom wrong level", true)
	f.mem.add(shopID, "3-0", t0.Add(time.Second), "error", "no match", true)
	f.mem.add(shopID, "4-0", t0.Add(time.Second), "error", "boom", true)
	if id := logID(t, f.next(t, s, time.Second)); id != "4-0" {
		t.Fatalf("got %s", id)
	}
	f.quiet(t, s, 3)
}

func TestTailLateVisibleRowAndDedupe(t *testing.T) {
	f := newTailFixture(t, nil)
	s, _ := f.open(t, "owner", "", "")
	expectStatus(t, f.next(t, s, 0), "live", "")

	late := f.mem.add(shopID, "5-0", t0.Add(time.Second), "info", "late", false) // inserted first, visible later
	f.mem.add(shopID, "6-0", t0.Add(2*time.Second), "info", "newer", true)
	if id := logID(t, f.next(t, s, time.Second)); id != "6-0" {
		t.Fatalf("got %s", id)
	}
	f.quiet(t, s, 2)
	f.mem.show(late)
	if id := logID(t, f.next(t, s, time.Second)); id != "5-0" {
		t.Fatalf("late row: got %s", id)
	}
	// A redelivered version (same event id, later inserted_at) is not sent again.
	f.mem.add(shopID, "6-0", t0.Add(4*time.Second), "info", "newer", true)
	f.mem.add(shopID, "5-0", t0.Add(4*time.Second), "info", "late", true)
	f.quiet(t, s, 3)
	f.mem.add(shopID, "7-0", t0.Add(5*time.Second), "info", "next", true)
	if id := logID(t, f.next(t, s, time.Second)); id != "7-0" {
		t.Fatalf("got %s", id)
	}
}

func TestTailDrainsFullBatches(t *testing.T) {
	f := newTailFixture(t, nil) // Batch 3
	s, _ := f.open(t, "owner", "", "")
	expectStatus(t, f.next(t, s, 0), "live", "")
	for i := 0; i < 7; i++ {
		f.mem.add(shopID, fmt.Sprintf("%d-0", 10+i), t0.Add(time.Second), "info", "m", true)
	}
	f.clock.Advance(time.Second)
	for i := 0; i < 7; i++ { // all seven without further clock ticks
		if id := logID(t, f.next(t, s, 0)); id != fmt.Sprintf("%d-0", 10+i) {
			t.Fatalf("event %d = %s", i, id)
		}
	}
}

func TestTailResumeWithoutGapsOrDuplicates(t *testing.T) {
	f := newTailFixture(t, nil)
	s, _ := f.open(t, "owner", "", "")
	expectStatus(t, f.next(t, s, 0), "live", "")
	f.mem.add(shopID, "1-0", t0.Add(time.Second), "info", "a", true)
	f.mem.add(shopID, "2-0", t0.Add(time.Second), "info", "b", true)
	first := f.next(t, s, time.Second)
	_ = logID(t, first)
	_ = logID(t, f.next(t, s, 0)) // "2-0" was sent, but the client "lost" it
	s.cancel()
	waitStreams(t, f.s, 0)

	late := f.mem.add(shopID, "3-0", t0.Add(500*time.Millisecond), "info", "late during the outage", false)
	f.mem.show(late)
	f.mem.add(shopID, "4-0", t0.Add(3*time.Second), "info", "after", true)
	r, _ := f.open(t, "owner", "", first.id) // resume after "1-0"
	expectStatus(t, f.next(t, r, 0), "live", "")
	var got []string
	for len(got) < 3 {
		got = append(got, logID(t, f.next(t, r, time.Second)))
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "2-0,3-0,4-0" {
		t.Fatalf("after resume got %v, want 2-0, 3-0 and 4-0 once each", got)
	}
	f.quiet(t, r, 3)
}

func TestTailResumeAfterStateLossAndReset(t *testing.T) {
	f := newTailFixture(t, nil)
	tok, _ := newStreamToken()
	// Unknown stream (other instance, restart): continue strictly after the cursor.
	f.mem.add(shopID, "1-0", t0.Add(-2*time.Second), "info", "before cursor", true)
	f.mem.add(shopID, "2-0", t0.Add(time.Second), "info", "after cursor", true)
	cur := encodeTailCursor(tailCursor{Stream: tok, Last: query.TailKey{InsertedAt: t0, EventID: "1-0"}, Seq: 7})
	s, _ := f.open(t, "owner", "", cur)
	expectStatus(t, f.next(t, s, 0), "live", "")
	if id := logID(t, f.next(t, s, time.Second)); id != "2-0" {
		t.Fatalf("got %s", id)
	}
	s.cancel()

	f.clock.Advance(5 * time.Second) // the rows above are now in the past
	old := encodeTailCursor(tailCursor{Stream: tok, Last: query.TailKey{InsertedAt: t0.Add(-6 * time.Minute), EventID: "1-0"}, Seq: 1})
	r, _ := f.open(t, "viewer", "", old)
	if ev := f.next(t, r, 0); ev.name != "reset" || !strings.Contains(ev.data, "cursor_too_old") {
		t.Fatalf("got %s %s, want reset", ev.name, ev.data)
	}
	expectStatus(t, f.next(t, r, 0), "live", "")
	f.quiet(t, r, 2) // starts fresh from now
}

func TestTailHeartbeatAndAuthRecheck(t *testing.T) {
	f := newTailFixture(t, nil)
	s, _ := f.open(t, "viewer", "", "")
	expectStatus(t, f.next(t, s, 0), "live", "")
	f.clock.Advance(15 * time.Second)
	select {
	case ev := <-s.events:
		if ev.name != "status" || ev.data != `{"state":"live","heartbeat":true}` {
			t.Fatalf("got %s %s, want heartbeat", ev.name, ev.data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no heartbeat")
	}

	f.pg.mu.Lock()
	f.pg.revoked["u-viewer"] = true
	f.pg.mu.Unlock()
	expectStatus(t, f.next(t, s, 5*time.Second), "closed", "session_ended")

	d, _ := f.open(t, "owner", "", "")
	expectStatus(t, f.next(t, d, 0), "live", "")
	f.pg.mu.Lock()
	f.pg.disabled["u-owner"] = true
	f.pg.mu.Unlock()
	expectStatus(t, f.next(t, d, 5*time.Second), "closed", "session_ended")

	m, _ := f.open(t, "third", "", "")
	expectStatus(t, f.next(t, m, 0), "live", "")
	f.pg.mu.Lock()
	f.pg.removed["u-third"] = true
	f.pg.mu.Unlock()
	expectStatus(t, f.next(t, m, 5*time.Second), "closed", "access_revoked")
}

func TestTailConnectionLimits(t *testing.T) {
	f := newTailFixture(t, nil) // 2 per user, 3 per project, 4 in total
	open := func(who string) int { _, st := f.open(t, who, "", ""); return st }
	if open("owner") != 200 || open("owner") != 200 {
		t.Fatal("first two streams refused")
	}
	if st := open("owner"); st != 429 {
		t.Errorf("third stream of a user: %d", st)
	}
	if open("viewer") != 200 {
		t.Fatal("other user refused")
	}
	if st := open("third"); st != 429 {
		t.Errorf("fourth stream of a project: %d", st)
	}
	req, _ := http.NewRequest(http.MethodGet, f.srv.URL+"/api/projects/other/tail", nil)
	req.AddCookie(&http.Cookie{Name: cookieName(false), Value: f.tokens["other"]})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 { // fourth in total, other project
		t.Fatalf("other project: %d", resp.StatusCode)
	}
	req2, _ := http.NewRequest(http.MethodGet, f.srv.URL+"/api/projects/other/tail", nil)
	req2.AddCookie(&http.Cookie{Name: cookieName(false), Value: f.tokens["other"]})
	resp2, _ := http.DefaultClient.Do(req2)
	resp2.Body.Close()
	if resp2.StatusCode != 429 || resp2.Header.Get("Retry-After") == "" {
		t.Errorf("fifth stream in total: %d Retry-After=%q", resp2.StatusCode, resp2.Header.Get("Retry-After"))
	}
}

func TestTailDegradedThenLive(t *testing.T) {
	f := newTailFixture(t, nil)
	s, _ := f.open(t, "owner", "", "")
	expectStatus(t, f.next(t, s, 0), "live", "")
	f.mem.setErr(errors.New("clickhouse: HTTP 500 (code 999): DB::Exception secret details"))
	f.mem.add(shopID, "1-0", t0.Add(time.Second), "info", "while down", true)
	ev := f.next(t, s, time.Second)
	expectStatus(t, ev, "degraded", "storage_unavailable")
	if strings.Contains(ev.data, "DB::") || strings.Contains(ev.data, "secret") {
		t.Fatalf("status leaks storage error: %s", ev.data)
	}
	f.quiet(t, s, 3)
	f.mem.setErr(nil)
	expectStatus(t, f.next(t, s, 2*time.Second), "live", "")
	if id := logID(t, f.next(t, s, time.Second)); id != "1-0" {
		t.Fatalf("got %s after recovery", id)
	}
}

func TestTailMaxDurationAndShutdown(t *testing.T) {
	f := newTailFixture(t, func(o *TailOptions) { o.MaxDuration = 10 * time.Second })
	s, _ := f.open(t, "owner", "", "")
	expectStatus(t, f.next(t, s, 0), "live", "")
	expectStatus(t, f.next(t, s, 2*time.Second), "closed", "max_duration")

	r, _ := f.open(t, "viewer", "", "")
	expectStatus(t, f.next(t, r, 0), "live", "")
	f.s.CloseStreams()
	expectStatus(t, f.next(t, r, 0), "closed", "shutdown")
	waitStreams(t, f.s, 0)
}

func TestTailDropsSlowClient(t *testing.T) {
	f := newTailFixture(t, func(o *TailOptions) { o.WriteTimeout = 200 * time.Millisecond; o.Batch = 200 })
	big := strings.Repeat("x", 64<<10)
	for i := 0; i < 400; i++ {
		f.mem.add(shopID, fmt.Sprintf("%d-0", 100+i), t0.Add(time.Second), "info", big, true)
	}
	// A client that connects and never reads.
	req, _ := http.NewRequest(http.MethodGet, f.srv.URL+"/api/projects/shop/tail", nil)
	req.AddCookie(&http.Cookie{Name: cookieName(false), Value: f.tokens["owner"]})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// The rows are visible before the start; make them new.
	f.mem.mu.Lock()
	for _, r := range f.mem.rows {
		r.row.Key.InsertedAt = t0.Add(2 * time.Second)
	}
	f.mem.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for f.s.OpenStreams() != 0 && time.Now().Before(deadline) {
		f.clock.Advance(time.Second)
		time.Sleep(20 * time.Millisecond)
	}
	if n := f.s.OpenStreams(); n != 0 {
		t.Fatalf("slow client still connected (%d streams)", n)
	}
}

func waitStreams(t *testing.T, s *Server, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for s.OpenStreams() != want && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := s.OpenStreams(); n != want {
		t.Fatalf("open streams = %d, want %d", n, want)
	}
}
