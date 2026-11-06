package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/ingest"
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestPercentile(t *testing.T) {
	hundred := make([]time.Duration, 100)
	for i := range hundred {
		hundred[i] = ms(i + 1) // 1..100 ms
	}
	tests := []struct {
		name   string
		sorted []time.Duration
		p      float64
		want   time.Duration
	}{
		{"empty", nil, 50, 0},
		{"single", []time.Duration{ms(7)}, 99, ms(7)},
		{"p50 of 1..100", hundred, 50, ms(50)},
		{"p95 of 1..100", hundred, 95, ms(95)},
		{"p99 of 1..100", hundred, 99, ms(99)},
		{"p100 is max", hundred, 100, ms(100)},
		{"p0 is min", hundred, 0, ms(1)},
		{"p50 of 4 values (nearest rank)", []time.Duration{ms(1), ms(2), ms(3), ms(4)}, 50, ms(2)},
		{"p99 of 10 values is the max", []time.Duration{ms(1), ms(2), ms(3), ms(4), ms(5), ms(6), ms(7), ms(8), ms(9), ms(10)}, 99, ms(10)},
		{"p90 of 10 values", []time.Duration{ms(1), ms(2), ms(3), ms(4), ms(5), ms(6), ms(7), ms(8), ms(9), ms(10)}, 90, ms(9)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := percentile(tt.sorted, tt.p); got != tt.want {
				t.Errorf("percentile(p%.0f) = %v, want %v", tt.p, got, tt.want)
			}
		})
	}
}

func TestSummarizeSortsAndRounds(t *testing.T) {
	s := summarize([]time.Duration{ms(30), 1500 * time.Microsecond, ms(10), ms(20)})
	if s.Count != 4 || s.P50 != 10 || s.Max != 30 || s.P99 != 30 {
		t.Errorf("summary = %+v", s)
	}
	if summarize(nil).Count != 0 {
		t.Error("empty summary should be zero")
	}
}

func TestCheckTarget(t *testing.T) {
	tests := []struct {
		url         string
		allowRemote bool
		wantErr     bool
	}{
		{"http://127.0.0.1:8080/v1/ingest", false, false},
		{"http://localhost:8080", false, false},
		{"http://[::1]:8080", false, false},
		{"http://127.0.0.5:8080", false, false},
		{"https://logs.example.com/v1/ingest", false, true},
		{"http://10.0.0.5:8080", false, true},
		{"http://localhost.example.com", false, true},
		{"https://logs.example.com/v1/ingest", true, false},
		{"ftp://127.0.0.1", false, true},
		{"not a url", true, true},
	}
	for _, tt := range tests {
		if err := checkTarget(tt.url, tt.allowRemote); (err != nil) != tt.wantErr {
			t.Errorf("checkTarget(%q, %v) error = %v, wantErr %v", tt.url, tt.allowRemote, err, tt.wantErr)
		}
	}
}

func TestRunRequiresKeyFromEnvAndLocalTarget(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{"no command", nil, nil, "usage"},
		{"unknown command", []string{"blast"}, nil, "unknown command"},
		{"ingest without key", []string{"ingest"}, nil, "LOADGEN_INGEST_KEY is not set"},
		{"query without key", []string{"query"}, nil, "LOADGEN_QUERY_KEY is not set"},
		{"demo without key", []string{"demo"}, nil, "LOADGEN_INGEST_KEY is not set"},
		{"remote ingest refused", []string{"ingest", "--url", "https://prod.example.com/v1/ingest"}, map[string]string{"LOADGEN_INGEST_KEY": "k"}, "refusing non-local target"},
		{"remote stats refused", []string{"ingest", "--wait-drain", "http://10.1.2.3:8081/stats"}, map[string]string{"LOADGEN_INGEST_KEY": "k"}, "refusing non-local target"},
		{"no key flag exists", []string{"ingest", "--key", "k"}, nil, "flag provided but not defined"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := run(ctx, tt.args, func(k string) string { return tt.env[k] }, &out)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("run(%v) error = %v, want %q", tt.args, err, tt.want)
			}
		})
	}
}

// TestRunIngestAgainstRealHandler drives the real ingest handler (memory
// publisher) and checks counts, statuses and the max-events cap.
func TestRunIngestAgainstRealHandler(t *testing.T) {
	pub := ingest.NewMemoryPublisher(0)
	h, err := ingest.NewHandler(pub, "k", ingest.DefaultLimits(), nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res, err := runIngest(context.Background(), ingestOptions{
		URL: srv.URL + ingest.Path, Key: "k", Concurrency: 4, BatchSize: 37, MaxEvents: 1000, Seed: 3, Spread: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.EventsAccepted != 1000 || len(pub.Events()) != 1000 {
		t.Fatalf("accepted %d, published %d; want exactly 1000", res.EventsAccepted, len(pub.Events()))
	}
	if res.Status["202"] != res.Requests || res.Errors != 0 {
		t.Errorf("status %v errors %d requests %d", res.Status, res.Errors, res.Requests)
	}
	if res.Latency.Count != int(res.Requests) || res.Latency.P99 <= 0 {
		t.Errorf("latency = %+v", res.Latency)
	}

	bad, err := runIngest(context.Background(), ingestOptions{URL: srv.URL + ingest.Path, Key: "wrong", Concurrency: 1, BatchSize: 10, MaxEvents: 20})
	if err != nil {
		t.Fatal(err)
	}
	if bad.Status["401"] != 2 || bad.EventsAccepted != 0 {
		t.Errorf("wrong key: status %v accepted %d", bad.Status, bad.EventsAccepted)
	}
}

func TestLimiterCapsRate(t *testing.T) {
	l := newLimiter(1000) // 1000 events/s
	start := time.Now()
	for i := 0; i < 5; i++ {
		if err := l.wait(context.Background(), 50); err != nil {
			t.Fatal(err)
		}
	}
	// 250 events at 1000/s: the 5th batch may start at 200ms.
	if d := time.Since(start); d < 180*time.Millisecond {
		t.Errorf("5 batches of 50 at 1000 ev/s took %v, want >= ~200ms", d)
	}
	if newLimiter(0) != nil {
		t.Error("rate 0 should mean unlimited")
	}
}
