package processor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFlushBySize(t *testing.T) {
	h := newHarness(t, testOptions()) // BatchSize 3, FlushInterval 1h
	h.stream.push(entries("1-0", "2-0", "3-0", "4-0", "5-0", "6-0", "7-0")...)
	stop := h.start(t)

	waitFor(t, "two full batches", func() bool { return len(h.store.batchSizes()) == 2 })
	if got := h.store.batchSizes(); !reflect.DeepEqual(got, []int{3, 3}) {
		t.Fatalf("batches = %v, want [3 3] before shutdown", got)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if got := h.store.batchSizes(); !reflect.DeepEqual(got, []int{3, 3, 1}) {
		t.Errorf("batches = %v, want [3 3 1] after shutdown flush", got)
	}
	if got := len(h.stream.ackedIDs()); got != 7 {
		t.Errorf("acked = %d, want 7", got)
	}
}

func TestFlushByTime(t *testing.T) {
	opts := testOptions()
	opts.BatchSize = 100
	opts.FlushInterval = 50 * time.Millisecond
	h := newHarness(t, opts)
	h.stream.push(entries("1-0", "2-0")...)

	start := time.Now()
	h.start(t)
	waitFor(t, "time-based flush", func() bool { return h.store.rowCount() == 2 })
	if d := time.Since(start); d < 40*time.Millisecond {
		t.Errorf("flushed after %v, want about the 50ms interval", d)
	}
	waitFor(t, "ack", func() bool { return len(h.stream.ackedIDs()) == 2 })
}

func TestAckOnlyAfterSuccessfulInsert(t *testing.T) {
	h := newHarness(t, testOptions())
	h.store.failFirst = 2
	h.stream.push(entries("1-0", "2-0", "3-0")...)
	h.start(t)

	waitFor(t, "ack", func() bool { return len(h.stream.ackedIDs()) == 3 })
	want := []string{"group", "insert-fail", "insert-fail", "insert:1-0,2-0,3-0", "ack:1-0,2-0,3-0"}
	if got := h.rec.list(); !reflect.DeepEqual(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
	if s := h.p.Stats(context.Background()); s.FailedBatches != 2 || s.Processed != 3 {
		t.Errorf("stats = %+v, want 2 failed, 3 processed", s)
	}
}

func TestBackoff(t *testing.T) {
	opts := testOptions()
	opts.RetryInitial = 100 * time.Millisecond
	opts.RetryMax = time.Second

	tests := []struct {
		attempt int
		jitter  float64
		want    time.Duration
	}{
		{attempt: 0, jitter: 0, want: 50 * time.Millisecond},
		{attempt: 0, jitter: 0.5, want: 75 * time.Millisecond},
		{attempt: 1, jitter: 0, want: 100 * time.Millisecond},
		{attempt: 3, jitter: 0, want: 400 * time.Millisecond},
		{attempt: 3, jitter: 0.999, want: 799_600_000},
		{attempt: 4, jitter: 0, want: 500 * time.Millisecond}, // 1.6s capped to 1s
		{attempt: 4, jitter: 0.999, want: 999_500_000},
		{attempt: 200, jitter: 0, want: 500 * time.Millisecond}, // no overflow
	}
	for _, tt := range tests {
		h := newHarness(t, opts)
		h.p.jitter = func() float64 { return tt.jitter }
		if got := h.p.backoff(tt.attempt); got != tt.want {
			t.Errorf("backoff(%d, jitter %.3f) = %v, want %v", tt.attempt, tt.jitter, got, tt.want)
		}
	}
}

func TestRetrySleepsWithGrowingBackoff(t *testing.T) {
	opts := testOptions()
	opts.RetryInitial = 10 * time.Millisecond
	opts.RetryMax = 40 * time.Millisecond
	h := newHarness(t, opts)
	h.store.failFirst = 4
	h.p.jitter = func() float64 { return 0 }

	var mu sync.Mutex
	var sleeps []time.Duration
	h.p.sleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		sleeps = append(sleeps, d)
		mu.Unlock()
		return ctx.Err()
	}
	h.stream.push(entries("1-0", "2-0", "3-0")...)
	h.start(t)

	waitFor(t, "insert after retries", func() bool { return h.store.rowCount() == 3 })
	mu.Lock()
	defer mu.Unlock()
	want := []time.Duration{5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond, 20 * time.Millisecond}
	if !reflect.DeepEqual(sleeps[:4], want) {
		t.Errorf("sleeps = %v, want prefix %v (exponential, capped at 40ms, half jitter floor)", sleeps, want)
	}
}

func TestBackpressureStopsReadingWhileFlushFails(t *testing.T) {
	h := newHarness(t, testOptions())
	h.store.failAlways.Store(true)
	h.stream.push(entries("1-0", "2-0", "3-0", "4-0", "5-0", "6-0")...)
	h.start(t)

	waitFor(t, "repeated insert failures", func() bool { return h.store.calls.Load() >= 5 })
	reads := h.stream.readNewCall.Load()
	time.Sleep(50 * time.Millisecond)
	if got := h.stream.readNewCall.Load(); got != reads {
		t.Fatalf("ReadNew called %d more times while the batch was failing, want 0", got-reads)
	}
	if acked := h.stream.ackedIDs(); len(acked) != 0 {
		t.Fatalf("acked %v while inserts fail, want none", acked)
	}

	h.store.failAlways.Store(false)
	waitFor(t, "recovery", func() bool { return len(h.stream.ackedIDs()) == 6 })
	if got := h.stream.readNewCall.Load(); got <= reads {
		t.Error("reading did not resume after the store recovered")
	}
	if h.store.rowCount() != 6 {
		t.Errorf("rows = %d, want 6 (nothing dropped)", h.store.rowCount())
	}
}

func TestDeadLetter(t *testing.T) {
	h := newHarness(t, testOptions())
	bad := Entry{ID: "1-0", Values: map[string]any{"payload": "{not json", "project_id": "default", "ingested_at": "2026-11-06T10:15:31Z"}}
	h.stream.push(bad)
	h.stream.push(entries("2-0", "3-0", "4-0")...)
	h.start(t)

	waitFor(t, "good batch", func() bool { return h.store.rowCount() == 3 })
	h.stream.mu.Lock()
	dead := append([]string(nil), h.stream.dead...)
	h.stream.mu.Unlock()
	if len(dead) != 1 || !strings.HasPrefix(dead[0], "1-0|payload is not a valid event JSON") {
		t.Fatalf("dead = %v", dead)
	}
	acked := h.stream.ackedIDs()
	if len(acked) == 0 || acked[0] != "1-0" {
		t.Errorf("acked = %v, want dead-lettered 1-0 acked first", acked)
	}
	for _, id := range h.store.batch(0) {
		if id.EventID == "1-0" {
			t.Error("dead-lettered entry was inserted")
		}
	}
	if s := h.p.Stats(context.Background()); s.DeadLettered != 1 {
		t.Errorf("dead_lettered_total = %d, want 1", s.DeadLettered)
	}
}

func TestDeadLetterFailureKeepsEntryPending(t *testing.T) {
	h := newHarness(t, testOptions())
	h.stream.deadErr = errors.New("redis down")
	h.stream.push(Entry{ID: "1-0", Values: map[string]any{}})
	stop := h.start(t)

	waitFor(t, "entry read", func() bool { return h.stream.readNewCall.Load() >= 2 })
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if acked := h.stream.ackedIDs(); len(acked) != 0 {
		t.Errorf("acked = %v, want none when dead-lettering failed", acked)
	}
	if s := h.p.Stats(context.Background()); s.DeadLettered != 0 {
		t.Errorf("dead_lettered_total = %d, want 0", s.DeadLettered)
	}
}

func TestGracefulShutdownFlushesAndAcks(t *testing.T) {
	opts := testOptions()
	opts.BatchSize = 100
	h := newHarness(t, opts)
	h.stream.push(entries("1-0", "2-0")...)
	stop := h.start(t)

	waitFor(t, "entries read", func() bool { return h.stream.readNewCall.Load() >= 2 })
	if h.store.rowCount() != 0 {
		t.Fatal("flushed before shutdown; FlushInterval is 1h")
	}
	if err := stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h.store.rowCount() != 2 || len(h.stream.ackedIDs()) != 2 {
		t.Errorf("rows = %d acked = %v, want 2 and 2", h.store.rowCount(), h.stream.ackedIDs())
	}
}

func TestShutdownWithFailingStoreLeavesEntriesPending(t *testing.T) {
	opts := testOptions()
	opts.BatchSize = 100
	opts.ShutdownTimeout = 50 * time.Millisecond
	h := newHarness(t, opts)
	h.store.failAlways.Store(true)
	h.stream.push(entries("1-0", "2-0")...)
	stop := h.start(t)

	waitFor(t, "entries read", func() bool { return h.stream.readNewCall.Load() >= 2 })
	start := time.Now()
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("shutdown took %v, want it bounded by ShutdownTimeout", d)
	}
	if acked := h.stream.ackedIDs(); len(acked) != 0 {
		t.Errorf("acked = %v, want none", acked)
	}
}

func TestClaimIdleEntries(t *testing.T) {
	opts := testOptions()
	opts.ClaimIdle = 250 * time.Millisecond
	h := newHarness(t, opts)
	h.stream.claims = [][]Entry{entries("1-0", "2-0"), entries("3-0")}
	h.start(t)

	waitFor(t, "claimed entries flushed", func() bool { return len(h.stream.ackedIDs()) == 3 })
	h.stream.mu.Lock()
	calls := append([]claimCall(nil), h.stream.claimCalls...)
	h.stream.mu.Unlock()
	if len(calls) < 2 {
		t.Fatalf("claim calls = %v, want the scan to continue across pages", calls)
	}
	if calls[0].minIdle != 250*time.Millisecond || calls[0].start != "0-0" || calls[1].start != "cursor-1" {
		t.Errorf("claim calls = %+v", calls)
	}
}

func TestOwnPendingProcessedFirst(t *testing.T) {
	opts := testOptions()
	opts.BatchSize = 2
	h := newHarness(t, opts)
	h.stream.pending = entries("1-0", "2-0")
	h.stream.push(entries("5-0", "6-0")...)
	h.start(t)

	waitFor(t, "both batches", func() bool { return len(h.store.batchSizes()) == 2 })
	if got := h.store.batch(0); got[0].EventID != "1-0" || got[1].EventID != "2-0" {
		t.Errorf("first batch = %v, want own pending entries 1-0, 2-0", got)
	}
}

func TestEntryClaimedTwiceInOneBatchIsInsertedOnce(t *testing.T) {
	opts := testOptions()
	opts.BatchSize = 100
	h := newHarness(t, opts)
	h.stream.claims = [][]Entry{entries("1-0")}
	h.stream.push(entries("1-0", "2-0")...)
	stop := h.start(t)
	waitFor(t, "reads", func() bool { return h.stream.readNewCall.Load() >= 2 })
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if h.store.rowCount() != 2 {
		t.Errorf("rows = %d, want 2", h.store.rowCount())
	}
}

func TestEnsureGroupIsRetried(t *testing.T) {
	h := newHarness(t, testOptions())
	h.stream.groupFails = 3
	h.stream.push(entries("1-0", "2-0", "3-0")...)
	h.start(t)
	waitFor(t, "processing after group creation", func() bool { return h.store.rowCount() == 3 })
}

func TestReadErrorsAreRetried(t *testing.T) {
	h := newHarness(t, testOptions())
	h.stream.readErr = errors.New("connection reset")
	h.start(t)
	waitFor(t, "read retries", func() bool { return h.stream.readNewCall.Load() >= 3 })
	h.stream.mu.Lock()
	h.stream.readErr = nil
	h.stream.mu.Unlock()
	h.stream.push(entries("1-0", "2-0", "3-0")...)
	waitFor(t, "recovery", func() bool { return h.store.rowCount() == 3 })
}

func TestAckFailureAfterInsertDoesNotDropOrBlock(t *testing.T) {
	h := newHarness(t, testOptions())
	h.stream.ackErr = errors.New("redis down")
	h.stream.push(entries("1-0", "2-0", "3-0", "4-0", "5-0", "6-0")...)
	h.start(t)
	// Each flush retries the ack a few times, then moves on: nothing blocks.
	waitFor(t, "both batches processed", func() bool { return h.p.Stats(context.Background()).Processed == 6 })
	if h.store.rowCount() != 6 {
		t.Errorf("rows = %d, want 6", h.store.rowCount())
	}
}

func TestStatsHandler(t *testing.T) {
	tests := []struct {
		name     string
		info     StreamInfo
		infoErr  error
		wantJSON map[string]any
	}{
		{
			name: "stream available",
			info: StreamInfo{Length: 10, Pending: 2, Lag: 3},
			wantJSON: map[string]any{
				"stream_length": 10.0, "group_pending": 2.0, "group_lag": 3.0, "oldest_unprocessed_age_s": 0.0,
				"processed_total": 0.0, "failed_batches_total": 0.0, "dead_lettered_total": 0.0, "trimmed_total": 0.0,
			},
		},
		{
			name:    "stream unavailable",
			infoErr: errors.New("dial tcp: refused"),
			wantJSON: map[string]any{
				"stream_length": nil, "group_pending": nil, "group_lag": nil, "oldest_unprocessed_age_s": nil, "stream_error": "stream unavailable",
				"processed_total": 0.0, "failed_batches_total": 0.0, "dead_lettered_total": 0.0, "trimmed_total": 0.0,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, testOptions())
			h.stream.info, h.stream.infoErr = tt.info, tt.infoErr
			mux := http.NewServeMux()
			h.p.RegisterStats(mux)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stats", nil))
			if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("status = %d content-type = %q", rec.Code, rec.Header().Get("Content-Type"))
			}
			var got map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.wantJSON) {
				t.Errorf("body = %v, want %v", got, tt.wantJSON)
			}
		})
	}
}

func TestNewValidatesOptions(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Options)
	}{
		{name: "no table", mutate: func(o *Options) { o.Table = "" }},
		{name: "zero batch", mutate: func(o *Options) { o.BatchSize = 0 }},
		{name: "zero block", mutate: func(o *Options) { o.ReadBlock = 0 }},
		{name: "zero flush", mutate: func(o *Options) { o.FlushInterval = 0 }},
		{name: "retry initial above max", mutate: func(o *Options) { o.RetryInitial = time.Hour }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := testOptions()
			tt.mutate(&o)
			if _, err := New(&fakeStream{rec: &recorder{}}, &fakeStore{rec: &recorder{}}, o, nil); err == nil {
				t.Fatal("New() error = nil, want error")
			}
		})
	}
}
