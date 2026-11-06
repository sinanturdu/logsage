package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// fakeStream records pipelines and can fail or block until the context ends.
type fakeStream struct {
	mu        sync.Mutex
	calls     [][]*redis.XAddArgs
	err       error
	block     bool
	pingErr   error
	closed    bool
	xlen      int64
	xlenErr   error
	xlenCalls int
}

func (f *fakeStream) XLen(_ context.Context, _ string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.xlenCalls++
	return f.xlen, f.xlenErr
}

func (f *fakeStream) setXLen(n int64, err error) {
	f.mu.Lock()
	f.xlen, f.xlenErr = n, err
	f.mu.Unlock()
}

func (f *fakeStream) pipelines() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeStream) XAddPipeline(ctx context.Context, entries []*redis.XAddArgs) error {
	f.mu.Lock()
	f.calls = append(f.calls, entries)
	f.mu.Unlock()
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.err
}

func (f *fakeStream) Ping(ctx context.Context) error {
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.pingErr
}

func (f *fakeStream) Close() error { f.closed = true; return nil }

var fixedNow = time.Date(2026, 11, 6, 15, 4, 5, 123_000_000, time.FixedZone("TRT", 3*3600))

func testRedisOpts() RedisOptions {
	return RedisOptions{Addr: "fake:6379", Stream: "test:events", MaxStreamLen: 1000, Timeout: 200 * time.Millisecond}
}

func newFakePublisher(t *testing.T, f *fakeStream, opts RedisOptions) *RedisPublisher {
	t.Helper()
	p, err := newRedisPublisher(f, opts, func() time.Time { return fixedNow })
	if err != nil {
		t.Fatalf("newRedisPublisher: %v", err)
	}
	return p
}

func sampleEvents(n int) []Event {
	evs := make([]Event, n)
	for i := range evs {
		evs[i] = Event{
			Timestamp:  time.Date(2026, 11, 6, 10, 15, 30, 0, time.UTC),
			Level:      "error",
			Service:    "checkout",
			Message:    "payment gateway timeout",
			TraceID:    "abc123",
			Attributes: map[string]string{"env": "prod", "i": string(rune('a' + i))},
		}
	}
	return evs
}

func TestRedisPublisherPublishPipeline(t *testing.T) {
	f := &fakeStream{}
	p := newFakePublisher(t, f, testRedisOpts())
	events := sampleEvents(3)

	if err := p.Publish(context.Background(), events); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if len(f.calls) != 1 {
		t.Fatalf("pipelines = %d, want 1 for the whole batch", len(f.calls))
	}
	entries := f.calls[0]
	if len(entries) != len(events) {
		t.Fatalf("entries = %d, want %d", len(entries), len(events))
	}
	for i, e := range entries {
		// No trimming at publish time: MAXLEN would drop unread entries.
		if e.Stream != "test:events" || e.MaxLen != 0 || e.MinID != "" || e.Approx {
			t.Errorf("entry %d: stream=%q maxlen=%d minid=%q approx=%v, want test:events without trimming", i, e.Stream, e.MaxLen, e.MinID, e.Approx)
		}
		if e.ID != "" {
			t.Errorf("entry %d: ID = %q, want server-assigned", i, e.ID)
		}
		vals, ok := e.Values.([]any)
		if !ok || len(vals) != 6 {
			t.Fatalf("entry %d: Values = %#v, want 3 field/value pairs", i, e.Values)
		}
		fields := map[string]string{}
		for j := 0; j < len(vals); j += 2 {
			fields[vals[j].(string)] = vals[j+1].(string)
		}
		if fields["project_id"] != "default" {
			t.Errorf("entry %d: project_id = %q, want default", i, fields["project_id"])
		}
		if fields["ingested_at"] != "2026-11-06T12:04:05.123Z" {
			t.Errorf("entry %d: ingested_at = %q, want UTC RFC3339", i, fields["ingested_at"])
		}
		var got Event
		if err := json.Unmarshal([]byte(fields["payload"]), &got); err != nil {
			t.Fatalf("entry %d: payload not JSON: %v", i, err)
		}
		if !got.Timestamp.Equal(events[i].Timestamp) || got.Message != events[i].Message || got.Attributes["i"] != events[i].Attributes["i"] {
			t.Errorf("entry %d: payload = %+v, want %+v", i, got, events[i])
		}
		if !strings.Contains(fields["payload"], `"trace_id":"abc123"`) || !strings.Contains(fields["payload"], `"timestamp":"2026-11-06T10:15:30Z"`) {
			t.Errorf("entry %d: payload field names/format: %s", i, fields["payload"])
		}
	}
}

func TestRedisPublisherPublishErrors(t *testing.T) {
	errDown := errors.New("connection refused")

	tests := []struct {
		name     string
		fake     *fakeStream
		ctx      func() (context.Context, context.CancelFunc)
		wantIs   error
		maxDelay time.Duration
	}{
		{
			name:   "client error is returned",
			fake:   &fakeStream{err: errDown},
			wantIs: errDown,
		},
		{
			name:     "timeout bounds a hanging redis",
			fake:     &fakeStream{block: true},
			wantIs:   context.DeadlineExceeded,
			maxDelay: time.Second,
		},
		{
			name: "canceled request context",
			fake: &fakeStream{block: true},
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			},
			wantIs: context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newFakePublisher(t, tt.fake, testRedisOpts())
			ctx, cancel := context.Background(), context.CancelFunc(func() {})
			if tt.ctx != nil {
				ctx, cancel = tt.ctx()
			}
			defer cancel()

			start := time.Now()
			err := p.Publish(ctx, sampleEvents(2))
			if !errors.Is(err, tt.wantIs) {
				t.Fatalf("Publish() error = %v, want errors.Is %v", err, tt.wantIs)
			}
			if tt.maxDelay > 0 && time.Since(start) > tt.maxDelay {
				t.Errorf("Publish took %v, want under %v", time.Since(start), tt.maxDelay)
			}
			if strings.Contains(err.Error(), "payment gateway timeout") {
				t.Errorf("error leaks event contents: %v", err)
			}
		})
	}
}

func TestRedisPublisherEmptyBatch(t *testing.T) {
	f := &fakeStream{}
	p := newFakePublisher(t, f, testRedisOpts())
	if err := p.Publish(context.Background(), nil); err != nil {
		t.Fatalf("Publish(nil) = %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("pipelines = %d, want 0", len(f.calls))
	}
}

func TestRedisPublisherReady(t *testing.T) {
	tests := []struct {
		name    string
		fake    *fakeStream
		wantErr bool
	}{
		{name: "ping ok", fake: &fakeStream{}},
		{name: "ping fails", fake: &fakeStream{pingErr: errors.New("down")}, wantErr: true},
		{name: "ping hangs", fake: &fakeStream{block: true}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newFakePublisher(t, tt.fake, testRedisOpts())
			err := p.Ready(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("Ready() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRedisPublisherClose(t *testing.T) {
	f := &fakeStream{}
	p := newFakePublisher(t, f, testRedisOpts())
	if err := p.Close(); err != nil || !f.closed {
		t.Fatalf("Close() = %v, closed = %v", err, f.closed)
	}
}

func TestRedisOptionsValidate(t *testing.T) {
	ok := testRedisOpts()
	tests := []struct {
		name   string
		mutate func(*RedisOptions)
	}{
		{name: "empty addr", mutate: func(o *RedisOptions) { o.Addr = "" }},
		{name: "empty stream", mutate: func(o *RedisOptions) { o.Stream = "" }},
		{name: "zero max stream len", mutate: func(o *RedisOptions) { o.MaxStreamLen = 0 }},
		{name: "negative refresh", mutate: func(o *RedisOptions) { o.LengthRefresh = -time.Second }},
		{name: "zero timeout", mutate: func(o *RedisOptions) { o.Timeout = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := ok
			tt.mutate(&o)
			if _, err := NewRedisPublisher(o); err == nil {
				t.Fatal("NewRedisPublisher() error = nil, want error")
			}
		})
	}
}

// TestRedisPublisherUnreachable uses the real go-redis client against a
// closed port: both calls must fail within the timeout.
func TestRedisPublisherUnreachable(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	opts := testRedisOpts()
	opts.Addr = addr
	opts.Timeout = 300 * time.Millisecond
	p, err := NewRedisPublisher(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	start := time.Now()
	if err := p.Publish(context.Background(), sampleEvents(1)); err == nil {
		t.Error("Publish() error = nil, want error for unreachable redis")
	}
	if err := p.Ready(context.Background()); err == nil {
		t.Error("Ready() error = nil, want error for unreachable redis")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("calls took %v, want them bounded by the timeout", d)
	}
}

func TestRedisPublisherBackpressure(t *testing.T) {
	tests := []struct {
		name      string
		xlen      int64
		refresh   bool // run RefreshLength before publishing
		wantErr   error
		wantWrite bool
	}{
		{name: "no refresh yet publishes", refresh: false, wantWrite: true},
		{name: "well below the limit", xlen: 10, refresh: true, wantWrite: true},
		{name: "one below the limit", xlen: 999, refresh: true, wantWrite: true},
		{name: "at the limit", xlen: 1000, refresh: true, wantErr: ErrOverloaded},
		{name: "above the limit", xlen: 50_000, refresh: true, wantErr: ErrOverloaded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeStream{xlen: tt.xlen}
			p := newFakePublisher(t, f, testRedisOpts()) // MaxStreamLen 1000
			if tt.refresh {
				if err := p.RefreshLength(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			calls := f.xlenCalls
			err := p.Publish(context.Background(), sampleEvents(3))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Publish() error = %v, want %v", err, tt.wantErr)
			}
			if got := f.pipelines() > 0; got != tt.wantWrite {
				t.Errorf("wrote to Redis = %v, want %v", got, tt.wantWrite)
			}
			if f.xlenCalls != calls {
				t.Error("Publish called XLEN; the length must come from the cache")
			}
		})
	}
}

func TestRedisPublisherRefreshKeepsLastValueOnError(t *testing.T) {
	f := &fakeStream{xlen: 2000}
	p := newFakePublisher(t, f, testRedisOpts())
	if n, ok := p.CachedStreamLength(); ok || n != 0 {
		t.Fatalf("before refresh: %d %v, want unknown", n, ok)
	}
	if err := p.RefreshLength(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.setXLen(0, errors.New("redis down"))
	if err := p.RefreshLength(context.Background()); err == nil {
		t.Fatal("RefreshLength() = nil, want error")
	}
	if n, ok := p.CachedStreamLength(); !ok || n != 2000 {
		t.Errorf("after failed refresh: %d %v, want last value 2000", n, ok)
	}
	if err := p.Publish(context.Background(), sampleEvents(1)); !errors.Is(err, ErrOverloaded) {
		t.Errorf("Publish() = %v, want ErrOverloaded from the last known length", err)
	}
}

func TestRedisPublisherLengthRefresher(t *testing.T) {
	f := &fakeStream{xlen: 5}
	opts := testRedisOpts()
	opts.LengthRefresh = 10 * time.Millisecond
	p := newFakePublisher(t, f, opts)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.RunLengthRefresher(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	waitLen := func(want int64) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for {
			if n, ok := p.CachedStreamLength(); ok && n == want {
				return
			}
			if time.Now().After(deadline) {
				n, ok := p.CachedStreamLength()
				t.Fatalf("cached length = %d (known %v), want %d", n, ok, want)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	waitLen(5)
	f.setXLen(1500, nil)
	waitLen(1500)
	if err := p.Publish(context.Background(), sampleEvents(1)); !errors.Is(err, ErrOverloaded) {
		t.Errorf("Publish() = %v, want ErrOverloaded", err)
	}
	f.setXLen(10, nil)
	waitLen(10)
	if err := p.Publish(context.Background(), sampleEvents(1)); err != nil {
		t.Errorf("Publish() after the backlog drained = %v", err)
	}
}
