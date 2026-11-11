package processor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recorder keeps an ordered log of store and stream side effects so tests
// can assert ordering (insert before ack).
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(s string) {
	r.mu.Lock()
	r.events = append(r.events, s)
	r.mu.Unlock()
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

type claimCall struct {
	minIdle time.Duration
	start   string
	count   int64
}

// fakeStream is an in-memory Stream.
type fakeStream struct {
	rec *recorder

	mu          sync.Mutex
	pending     []Entry   // returned by ReadPending
	queue       []Entry   // returned by ReadNew, up to count per call
	claims      [][]Entry // one page per Claim call
	claimCalls  []claimCall
	acked       []string
	dead        []string // "id|reason"
	deadErr     error
	ackErr      error
	readErr     error
	groupFails  int
	info        StreamInfo
	infoErr     error
	readNewCall atomic.Int64
	onReadNew   func(call int64) // optional, runs at the start of each ReadNew (1-based call number)
}

func (f *fakeStream) EnsureGroup(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.groupFails > 0 {
		f.groupFails--
		return errors.New("redis down")
	}
	f.rec.add("group")
	return nil
}

func (f *fakeStream) ReadPending(_ context.Context, after string, count int64) ([]Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Entry
	for _, e := range f.pending {
		if (after == "0" || e.ID > after) && int64(len(out)) < count {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeStream) ReadNew(ctx context.Context, count int64, block time.Duration) ([]Entry, error) {
	call := f.readNewCall.Add(1)
	if f.onReadNew != nil {
		f.onReadNew(call)
	}
	if err := ctx.Err(); err != nil {
		return nil, err // like the Redis client: a cancelled call reads nothing
	}
	f.mu.Lock()
	if f.readErr != nil {
		err := f.readErr
		f.mu.Unlock()
		return nil, err
	}
	n := int(count)
	if n > len(f.queue) {
		n = len(f.queue)
	}
	out := append([]Entry(nil), f.queue[:n]...)
	f.queue = f.queue[n:]
	f.mu.Unlock()
	if len(out) > 0 {
		return out, nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(block):
		return nil, nil
	}
}

func (f *fakeStream) push(es ...Entry) {
	f.mu.Lock()
	f.queue = append(f.queue, es...)
	f.mu.Unlock()
}

func (f *fakeStream) Claim(_ context.Context, minIdle time.Duration, start string, count int64) ([]Entry, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimCalls = append(f.claimCalls, claimCall{minIdle, start, count})
	if len(f.claims) == 0 {
		return nil, "0-0", nil
	}
	page := f.claims[0]
	f.claims = f.claims[1:]
	next := "0-0"
	if len(f.claims) > 0 {
		next = "cursor-" + fmt.Sprint(len(f.claims))
	}
	return page, next, nil
}

func (f *fakeStream) Ack(_ context.Context, ids ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ackErr != nil {
		f.rec.add("ack-fail")
		return f.ackErr
	}
	f.acked = append(f.acked, ids...)
	f.rec.add("ack:" + strings.Join(ids, ","))
	return nil
}

func (f *fakeStream) DeadLetter(_ context.Context, e Entry, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deadErr != nil {
		return f.deadErr
	}
	f.dead = append(f.dead, e.ID+"|"+reason)
	f.rec.add("dead:" + e.ID)
	return nil
}

func (f *fakeStream) Info(context.Context) (StreamInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.info, f.infoErr
}

func (f *fakeStream) Ready(context.Context) error { return nil }

func (f *fakeStream) ackedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.acked...)
}

// queued is how many pushed entries ReadNew has not handed out yet.
func (f *fakeStream) queued() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.queue)
}

// fakeStore is an in-memory Store that can fail inserts.
type fakeStore struct {
	rec *recorder

	mu         sync.Mutex
	failFirst  int         // fail this many inserts, then succeed
	failAlways atomic.Bool // fail every insert while set
	batches    [][]jsonRow
	calls      atomic.Int64
	// attemptInsertedAt is the inserted_at of every insert attempt,
	// failed ones included.
	attemptInsertedAt []string
}

func (s *fakeStore) attempts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.attemptInsertedAt...)
}

func (s *fakeStore) InsertJSONEachRow(_ context.Context, table string, body []byte) error {
	s.calls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if table != "logs" {
		return fmt.Errorf("unexpected table %q", table)
	}
	var first jsonRow
	_ = json.Unmarshal(bytes.SplitN(body, []byte("\n"), 2)[0], &first)
	s.attemptInsertedAt = append(s.attemptInsertedAt, first.InsertedAt)
	if s.failAlways.Load() || s.failFirst > 0 {
		if s.failFirst > 0 {
			s.failFirst--
		}
		s.rec.add("insert-fail")
		return errors.New("clickhouse unreachable")
	}
	var rows []jsonRow
	sc := bufio.NewScanner(bytes.NewReader(body))
	for sc.Scan() {
		var r jsonRow
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return err
		}
		rows = append(rows, r)
	}
	s.batches = append(s.batches, rows)
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.EventID
	}
	s.rec.add("insert:" + strings.Join(ids, ","))
	return nil
}

func (s *fakeStore) Ready(context.Context) error { return nil }

func (s *fakeStore) batchSizes() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]int, len(s.batches))
	for i, b := range s.batches {
		out[i] = len(b)
	}
	return out
}

func (s *fakeStore) batch(i int) []jsonRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]jsonRow(nil), s.batches[i]...)
}

func (s *fakeStore) rowCount() int {
	n := 0
	for _, b := range s.batchSizes() {
		n += b
	}
	return n
}

// validEntry returns an entry shaped like the ingest API writes it.
func validEntry(id string) Entry {
	return Entry{ID: id, Values: map[string]any{
		"payload":     `{"timestamp":"2026-11-06T10:15:30.123Z","level":"error","service":"checkout","message":"timeout","trace_id":"t1","attributes":{"env":"dev"}}`,
		"project_id":  "default",
		"ingested_at": "2026-11-06T10:15:31.5Z",
	}}
}

func entries(ids ...string) []Entry {
	out := make([]Entry, len(ids))
	for i, id := range ids {
		out[i] = validEntry(id)
	}
	return out
}

func testOptions() Options {
	return Options{
		Table:           "logs",
		BatchSize:       3,
		ReadCount:       10,
		ReadBlock:       5 * time.Millisecond,
		FlushInterval:   time.Hour, // only size or shutdown flushes unless a test lowers it
		ClaimIdle:       30 * time.Second,
		ClaimInterval:   time.Hour, // one claim scan at start
		RetryInitial:    time.Millisecond,
		RetryMax:        4 * time.Millisecond,
		ShutdownTimeout: time.Second,
	}
}

type harness struct {
	rec    *recorder
	stream *fakeStream
	store  *fakeStore
	p      *Processor
}

func newHarness(t *testing.T, opts Options) *harness {
	t.Helper()
	rec := &recorder{}
	h := &harness{rec: rec, stream: &fakeStream{rec: rec}, store: &fakeStore{rec: rec}}
	p, err := New(h.stream, h.store, opts, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.p = p
	return h
}

// start runs the processor in the background and returns a stop function
// that cancels it and waits for Run to return.
func (h *harness) start(t *testing.T) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.p.Run(ctx) }()
	var once sync.Once
	var err error
	stop = func() error {
		once.Do(func() {
			cancel()
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				err = errors.New("Run did not return within 5s after cancel")
			}
		})
		return err
	}
	t.Cleanup(func() { _ = stop() })
	return stop
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
