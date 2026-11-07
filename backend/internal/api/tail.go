package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/query"
	"github.com/sinanturdu/logsage/backend/internal/storage/postgres"
)

// TailReader is what the live tail needs from log storage (*query.Service).
type TailReader interface {
	ParseTail(rawQuery string) (query.Filter, error)
	TailAfter(ctx context.Context, scope query.Scope, f query.Filter, after query.TailKey, limit int) ([]query.TailRow, error)
	TailCount(ctx context.Context, scope query.Scope, f query.Filter, lower, upper query.TailKey) (uint64, error)
	TailRange(ctx context.Context, scope query.Scope, f query.Filter, lower, upper query.TailKey, limit int) ([]query.TailRow, error)
}

// Clock is the time source of the live tail; tests replace it. Timer fires
// once after d; stop releases it (a stream stops its timers when it ends).
type Clock interface {
	Now() time.Time
	Timer(d time.Duration) (c <-chan time.Time, stop func())
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) Timer(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTimer(d)
	return t.C, func() { t.Stop() }
}

// timerSlot is one re-armable timer of a stream.
type timerSlot struct {
	clock Clock
	c     <-chan time.Time
	stop  func()
}

func (s *timerSlot) arm(d time.Duration) {
	if s.stop != nil {
		s.stop()
	}
	s.c, s.stop = s.clock.Timer(d)
}

// TailOptions configure the live tail (ADR 0015). Zero values take the
// defaults of DefaultTailOptions.
type TailOptions struct {
	PollInterval   time.Duration // TAIL_POLL_INTERVAL
	Batch          int           // TAIL_BATCH rows per poll; a full batch polls again at once
	Heartbeat      time.Duration // TAIL_HEARTBEAT
	MaxDuration    time.Duration // TAIL_MAX_DURATION, then the stream closes
	AuthRecheck    time.Duration // TAIL_AUTH_RECHECK
	MaxReplay      time.Duration // TAIL_MAX_REPLAY: older cursors get a reset
	MaxLateness    time.Duration // TAIL_MAX_LATENESS: event timestamps older than this are not streamed
	Overlap        time.Duration // TAIL_OVERLAP: how late a row may become visible and still be delivered
	WriteTimeout   time.Duration // TAIL_WRITE_TIMEOUT: a slower client is dropped
	RetryMax       time.Duration // TAIL_RETRY_MAX: backoff cap while ClickHouse fails
	MaxConnUser    int           // TAIL_MAX_CONN_USER
	MaxConnProject int           // TAIL_MAX_CONN_PROJECT
	MaxConnTotal   int           // TAIL_MAX_CONN_TOTAL
	MaxSeen        int           // TAIL_MAX_SEEN: delivered ids remembered per stream
	Clock          Clock         // default: the real clock
}

// DefaultTailOptions are the documented defaults.
func DefaultTailOptions() TailOptions {
	return TailOptions{
		PollInterval: time.Second, Batch: 200, Heartbeat: 15 * time.Second, MaxDuration: time.Hour,
		AuthRecheck: 30 * time.Second, MaxReplay: 5 * time.Minute, MaxLateness: 10 * time.Minute,
		Overlap: 30 * time.Second, WriteTimeout: 10 * time.Second, RetryMax: 30 * time.Second,
		MaxConnUser: 2, MaxConnProject: 10, MaxConnTotal: 50, MaxSeen: 50_000,
	}
}

func (o TailOptions) withDefaults() TailOptions {
	d := DefaultTailOptions()
	setD := func(v *time.Duration, def time.Duration) {
		if *v <= 0 {
			*v = def
		}
	}
	setI := func(v *int, def int) {
		if *v <= 0 {
			*v = def
		}
	}
	setD(&o.PollInterval, d.PollInterval)
	setD(&o.Heartbeat, d.Heartbeat)
	setD(&o.MaxDuration, d.MaxDuration)
	setD(&o.AuthRecheck, d.AuthRecheck)
	setD(&o.MaxReplay, d.MaxReplay)
	setD(&o.MaxLateness, d.MaxLateness)
	setD(&o.Overlap, d.Overlap)
	setD(&o.WriteTimeout, d.WriteTimeout)
	setD(&o.RetryMax, d.RetryMax)
	setI(&o.Batch, d.Batch)
	setI(&o.MaxConnUser, d.MaxConnUser)
	setI(&o.MaxConnProject, d.MaxConnProject)
	setI(&o.MaxConnTotal, d.MaxConnTotal)
	setI(&o.MaxSeen, d.MaxSeen)
	if o.Clock == nil {
		o.Clock = realClock{}
	}
	return o
}

// ---- connection limits ----

type tailGate struct {
	mu                          sync.Mutex
	maxUser, maxProject, maxAll int
	users, projects             map[string]int
	all                         int
}

func newTailGate(o TailOptions) *tailGate {
	return &tailGate{maxUser: o.MaxConnUser, maxProject: o.MaxConnProject, maxAll: o.MaxConnTotal,
		users: map[string]int{}, projects: map[string]int{}}
}

func (g *tailGate) acquire(user, project string) (func(), bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.users[user] >= g.maxUser || g.projects[project] >= g.maxProject || g.all >= g.maxAll {
		return nil, false
	}
	g.users[user]++
	g.projects[project]++
	g.all++
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			if g.users[user]--; g.users[user] <= 0 {
				delete(g.users, user)
			}
			if g.projects[project]--; g.projects[project] <= 0 {
				delete(g.projects, project)
			}
			g.all--
		})
	}, true
}

func (g *tailGate) open() int { g.mu.Lock(); defer g.mu.Unlock(); return g.all }

// ---- per-stream state ----

// seenRow is a delivered event: every version (key) observed and the send
// sequence number.
type seenRow struct {
	keys []query.TailKey
	ts   time.Time // event timestamp, for the lateness window
	seq  uint64
}

// tailState is what a stream knows about its position. It is kept for
// TAIL_MAX_REPLAY after a disconnect so a reconnect with Last-Event-ID
// continues with no gap and no duplicate.
type tailState struct {
	last  query.TailKey
	seq   uint64
	seen  map[string]*seenRow
	saved time.Time
}

// resumeStore keeps the state of recently closed streams, per instance.
type resumeStore struct {
	mu     sync.Mutex
	max    int
	ttl    time.Duration
	states map[string]*tailState
}

func (r *resumeStore) put(token string, st *tailState, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, v := range r.states { // drop expired states first
		if now.Sub(v.saved) > r.ttl {
			delete(r.states, k)
		}
	}
	if len(r.states) >= r.max {
		var oldest string
		for k, v := range r.states {
			if oldest == "" || v.saved.Before(r.states[oldest].saved) {
				oldest = k
			}
		}
		delete(r.states, oldest)
	}
	st.saved = now
	r.states[token] = st
}

func (r *resumeStore) take(token string, now time.Time) *tailState {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.states[token]
	delete(r.states, token)
	if st == nil || now.Sub(st.saved) > r.ttl {
		return nil
	}
	return st
}

// ---- SSE framing ----

// sseWriter writes events with a deadline per write: a client that does not
// read is dropped instead of buffered, and the server's WriteTimeout does
// not end healthy long streams.
type sseWriter struct {
	w       http.ResponseWriter
	rc      *http.ResponseController
	timeout time.Duration
}

func (e *sseWriter) raw(b []byte) error {
	_ = e.rc.SetWriteDeadline(time.Now().Add(e.timeout))
	if _, err := e.w.Write(b); err != nil {
		return err
	}
	return e.rc.Flush()
}

// frame renders one SSE event; data is one JSON line (json.Marshal escapes
// newlines), so it cannot inject fields.
func frame(event, id string, data any) []byte {
	b, _ := json.Marshal(data)
	var buf bytes.Buffer
	if id != "" {
		fmt.Fprintf(&buf, "id: %s\n", id)
	}
	fmt.Fprintf(&buf, "event: %s\ndata: %s\n\n", event, b)
	return buf.Bytes()
}

func (e *sseWriter) send(event, id string, data any) error { return e.raw(frame(event, id, data)) }

type statusData struct {
	State     string `json:"state"`            // live, degraded, closed
	Reason    string `json:"reason,omitempty"` // for closed and degraded
	Heartbeat bool   `json:"heartbeat,omitempty"`
}

type resetData struct {
	Reason string `json:"reason"`
}

// ---- handler ----

// errClosed ends a stream after its closing status was sent.
var errClosed = errors.New("tail closed")

// handleTail serves GET /api/projects/{slug}/tail (ADR 0015).
func (s *Server) handleTail(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	if s.tail == nil {
		writeError(w, http.StatusNotFound)
		return
	}
	m, ok := s.member(w, r, sess, actView)
	if !ok {
		return
	}
	f, err := s.tail.ParseTail(r.URL.RawQuery)
	if err != nil {
		writeInvalid(w, err.Error())
		return
	}
	var resume *tailCursor
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		c, err := decodeTailCursor(v)
		if err != nil {
			writeInvalid(w, errBadCursor.Error())
			return
		}
		resume = &c
	}
	release, ok := s.tailGate.acquire(sess.UserID, m.ID)
	if !ok {
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusTooManyRequests)
		return
	}
	defer release()

	cookie, _ := r.Cookie(cookieName(s.opts.CookieSecure))
	tokenHash, _ := hashToken(cookie.Value)
	t := &tailStream{s: s, o: s.tailOpts, scope: query.ScopeFor(m.ID), filter: f, user: sess.UserID,
		slug: m.Slug, tokenHash: tokenHash, ctx: r.Context()}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	t.out = &sseWriter{w: w, rc: rc, timeout: s.tailOpts.WriteTimeout}
	t.run(resume)
}

type tailStream struct {
	s         *Server
	o         TailOptions
	scope     query.Scope
	filter    query.Filter
	user      string
	slug      string
	tokenHash []byte
	ctx       context.Context
	out       *sseWriter

	token    string
	st       *tailState
	degraded bool
	failures int
}

func (t *tailStream) now() time.Time { return t.o.Clock.Now() }

func (t *tailStream) status(state, reason string, heartbeat bool) error {
	return t.out.send("status", "", statusData{State: state, Reason: reason, Heartbeat: heartbeat})
}

func (t *tailStream) close(reason string) error {
	_ = t.status("closed", reason, false)
	return errClosed
}

// run positions the stream, then polls until the client leaves, the stream
// is closed, or the server shuts down. The state is saved for resuming.
func (t *tailStream) run(resume *tailCursor) {
	if err := t.out.raw([]byte("retry: 5000\n\n")); err != nil {
		return
	}
	if err := t.position(resume); err != nil {
		return
	}
	defer func() { t.s.resume.put(t.token, t.st, t.now()) }()

	end, beat, recheck, poll := &timerSlot{clock: t.o.Clock}, &timerSlot{clock: t.o.Clock}, &timerSlot{clock: t.o.Clock}, &timerSlot{clock: t.o.Clock}
	defer func() {
		for _, s := range []*timerSlot{end, beat, recheck, poll} {
			s.stop()
		}
	}()
	end.arm(t.o.MaxDuration)
	beat.arm(t.o.Heartbeat)
	recheck.arm(t.o.AuthRecheck)
	poll.arm(0)
	for {
		select {
		case <-t.ctx.Done():
			return
		case <-t.s.shutdown:
			_ = t.close("shutdown")
			return
		case <-end.c:
			_ = t.close("max_duration")
			return
		case <-beat.c:
			state := "live"
			if t.degraded {
				state = "degraded"
			}
			if t.status(state, "", true) != nil {
				return
			}
			beat.arm(t.o.Heartbeat)
		case <-recheck.c:
			if reason := t.s.tailAuthorized(t.ctx, t.tokenHash, t.user, t.slug); reason != "" {
				_ = t.close(reason)
				return
			}
			recheck.arm(t.o.AuthRecheck)
		case <-poll.c:
			next, err := t.tick()
			if errors.Is(err, errClosed) || errors.Is(err, errWrite) {
				return
			}
			poll.arm(next)
		}
	}
}

var (
	errWrite    = errors.New("tail write failed")
	errOverload = errors.New("tail window holds more rows than TAIL_MAX_SEEN")
)

// position sets the stream's start: a fresh stream starts now; a resumed one
// restores its state, or continues strictly after the cursor when the state
// is gone; a cursor older than TAIL_MAX_REPLAY gets a reset first.
func (t *tailStream) position(resume *tailCursor) error {
	now := t.now()
	if resume != nil && now.Sub(resume.Last.InsertedAt) <= t.o.MaxReplay {
		t.token = resume.Stream
		if st := t.s.resume.take(resume.Stream, now); st != nil {
			t.st = st
			t.rewind(*resume)
		} else {
			t.st = &tailState{last: resume.Last, seq: resume.Seq, seen: map[string]*seenRow{}}
			t.prefill() // everything up to the cursor counts as delivered
		}
		return t.status(t.liveState(), "", false)
	}
	if resume != nil {
		if err := t.out.send("reset", "", resetData{Reason: "cursor_too_old"}); err != nil {
			return err
		}
	}
	tok, err := newStreamToken()
	if err != nil {
		return err
	}
	t.token = tok
	t.st = &tailState{last: query.TailKey{InsertedAt: now.UTC()}, seen: map[string]*seenRow{}}
	t.prefill() // rows already visible at the start are not streamed
	return t.status(t.liveState(), "", false)
}

func (t *tailStream) liveState() string {
	if t.degraded {
		return "degraded"
	}
	return "live"
}

// rewind undoes what was sent after the client's last received event: those
// rows were lost with the connection and are sent again.
func (t *tailStream) rewind(c tailCursor) {
	for id, sr := range t.st.seen {
		if sr.seq > c.Seq {
			delete(t.st.seen, id)
		}
	}
	t.st.last, t.st.seq = c.Last, c.Seq
}

// prefill marks the rows in the overlap window below the position as
// delivered. A ClickHouse error leaves the set empty; the stream then starts
// degraded and may send some of those rows, never miss any.
func (t *tailStream) prefill() {
	f := query.TailWindow(t.filter, t.now(), t.o.MaxLateness)
	lower := query.TailKey{InsertedAt: t.st.last.InsertedAt.Add(-t.o.Overlap)}
	rows, err := t.s.tail.TailRange(t.ctx, t.scope, f, lower, t.st.last, t.o.MaxSeen)
	if err != nil {
		t.fail(err)
		return
	}
	for _, row := range rows {
		t.observe(row, 0)
	}
}

// observe records a row as delivered (or a further version of one).
func (t *tailStream) observe(row query.TailRow, seq uint64) {
	ts, _ := time.Parse(time.RFC3339Nano, row.Event.Timestamp)
	sr := t.st.seen[row.Event.EventID]
	if sr == nil {
		t.st.seen[row.Event.EventID] = &seenRow{keys: []query.TailKey{row.Key}, ts: ts, seq: seq}
		return
	}
	for _, k := range sr.keys {
		if k == row.Key {
			return
		}
	}
	sr.keys = append(sr.keys, row.Key)
}

// tick runs one poll. It returns the delay to the next poll: zero after a
// full batch (drain), the backoff while ClickHouse fails.
func (t *tailStream) tick() (time.Duration, error) {
	release, ok := t.s.gate.acquire(t.user) // counts toward the per-user query cap
	if !ok {
		return t.o.PollInterval, nil // at the cap: skip this tick, never 429 a stream
	}
	defer release()

	f := query.TailWindow(t.filter, t.now(), t.o.MaxLateness)
	full, err := t.pollNew(f)
	if err == nil {
		err = t.pollLate(f)
	}
	switch {
	case errors.Is(err, errClosed), errors.Is(err, errWrite):
		return 0, err
	case errors.Is(err, errOverload):
		// The client cannot be told exactly what it missed: it refetches
		// via /logs and the stream starts over from now.
		if t.out.send("reset", "", resetData{Reason: "overloaded"}) != nil {
			return 0, errWrite
		}
		t.st = &tailState{last: query.TailKey{InsertedAt: t.now().UTC()}, seen: map[string]*seenRow{}}
		t.prefill()
		return t.o.PollInterval, nil
	case err != nil:
		t.fail(err)
		if t.degraded && t.failures == 1 {
			if t.status("degraded", "storage_unavailable", false) != nil {
				return 0, errWrite
			}
		}
		return t.backoff(), nil
	}
	t.prune(f)
	if full {
		return 0, nil
	}
	return t.o.PollInterval, nil
}

// recovered reports live again after storage answered, before any row of
// that poll is sent.
func (t *tailStream) recovered() error {
	if !t.degraded {
		return nil
	}
	t.degraded, t.failures = false, 0
	if t.status("live", "", false) != nil {
		return errWrite
	}
	return nil
}

func (t *tailStream) fail(err error) {
	status, _ := query.Classify(err)
	t.s.log.Warn("live tail poll failed", "status", status, "err", err)
	t.degraded = true
	t.failures++
}

func (t *tailStream) backoff() time.Duration {
	d := t.o.PollInterval
	for i := 1; i < t.failures && d < t.o.RetryMax; i++ {
		d *= 2
	}
	return min(d, t.o.RetryMax)
}

// pollNew sends rows after the position, in order.
func (t *tailStream) pollNew(f query.Filter) (bool, error) {
	rows, err := t.s.tail.TailAfter(t.ctx, t.scope, f, t.st.last, t.o.Batch)
	if err != nil {
		return false, err
	}
	if err := t.recovered(); err != nil {
		return false, err
	}
	for _, row := range rows {
		if err := t.deliver(row); err != nil {
			return false, err
		}
		if t.st.last.Less(row.Key) {
			t.st.last = row.Key
		}
	}
	return len(rows) >= t.o.Batch, nil
}

// pollLate finds rows that became visible inside the overlap window below
// the position after it moved past them: ClickHouse counts the distinct ids
// in the window, and only if that differs from what was delivered there are
// the window's rows fetched and the missing ones sent.
func (t *tailStream) pollLate(f query.Filter) error {
	lower := query.TailKey{InsertedAt: t.st.last.InsertedAt.Add(-t.o.Overlap)}
	n, err := t.s.tail.TailCount(t.ctx, t.scope, f, lower, t.st.last)
	if err != nil {
		return err
	}
	if n == t.seenIn(lower, t.st.last, f.From) {
		return nil
	}
	rows, err := t.s.tail.TailRange(t.ctx, t.scope, f, lower, t.st.last, t.o.MaxSeen+1)
	if err != nil {
		return err
	}
	if len(rows) > t.o.MaxSeen {
		return errOverload
	}
	for _, row := range rows {
		if err := t.deliver(row); err != nil {
			return err
		}
	}
	return nil
}

// seenIn counts delivered ids with an observed version in (lower, upper]
// and an event timestamp inside the window, the same set ClickHouse counts.
func (t *tailStream) seenIn(lower, upper query.TailKey, from time.Time) uint64 {
	var n uint64
	for _, sr := range t.st.seen {
		if sr.ts.Before(from) {
			continue
		}
		for _, k := range sr.keys {
			if lower.Less(k) && !upper.Less(k) {
				n++
				break
			}
		}
	}
	return n
}

// deliver sends a row unless its event id was already delivered.
func (t *tailStream) deliver(row query.TailRow) error {
	if _, dup := t.st.seen[row.Event.EventID]; dup {
		t.observe(row, 0)
		return nil
	}
	if len(t.st.seen) >= t.o.MaxSeen {
		return errOverload // more rows in the window than a stream can track
	}
	t.st.seq++
	t.observe(row, t.st.seq)
	last := t.st.last
	if last.Less(row.Key) {
		last = row.Key
	}
	id := encodeTailCursor(tailCursor{Stream: t.token, Last: last, Seq: t.st.seq})
	if err := t.out.send("log", id, row.Event); err != nil {
		return errWrite
	}
	return nil
}

// prune forgets delivered ids that left both the overlap window and the
// lateness window: they can no longer come back in a poll.
func (t *tailStream) prune(f query.Filter) {
	lower := query.TailKey{InsertedAt: t.st.last.InsertedAt.Add(-t.o.Overlap)}
	for id, sr := range t.st.seen {
		keep := false
		for _, k := range sr.keys {
			if lower.Less(k) {
				keep = true
				break
			}
		}
		if !keep || sr.ts.Before(f.From) {
			delete(t.st.seen, id)
		}
	}
}

// tailAuthorized re-checks a stream's session and membership. It returns a
// close reason, or "" while the stream may continue. PostgreSQL errors do
// not close streams; the next check decides.
func (s *Server) tailAuthorized(ctx context.Context, tokenHash []byte, user, slug string) string {
	sess, err := s.store.LookupSession(ctx, tokenHash)
	switch {
	case errors.Is(err, postgres.ErrNotFound):
		return "session_ended"
	case err != nil:
		s.log.Warn("live tail auth re-check failed", "err", err)
		return ""
	case !sess.Valid(s.opts.Now()) || sess.UserID != user:
		return "session_ended"
	}
	m, err := s.store.MembershipFor(ctx, user, slug)
	switch {
	case errors.Is(err, postgres.ErrNotFound):
		return "access_revoked"
	case err != nil:
		s.log.Warn("live tail auth re-check failed", "err", err)
		return ""
	case !can(m.Role, actView):
		return "access_revoked"
	}
	return ""
}

// CloseStreams ends every live tail with status closed (reason shutdown).
// cmd/api registers it with http.Server.RegisterOnShutdown, so a graceful
// shutdown does not wait for streams to end on their own.
func (s *Server) CloseStreams() { s.shutdownOnce.Do(func() { close(s.shutdown) }) }

// OpenStreams is the number of live tails (for tests and stats).
func (s *Server) OpenStreams() int { return s.tailGate.open() }
