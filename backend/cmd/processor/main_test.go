package main

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func envFunc(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestRunRejectsBadConfig(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "missing redis addr", env: map[string]string{}},
		{name: "bad clickhouse url", env: map[string]string{"REDIS_ADDR": "localhost:6379", "CLICKHOUSE_URL": "localhost"}},
		{name: "bad batch size", env: map[string]string{"REDIS_ADDR": "localhost:6379", "PROCESSOR_BATCH_SIZE": "-5"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel() // a regression returns promptly instead of running
			if err := run(ctx, envFunc(tt.env)); err == nil {
				t.Fatal("run() = nil, want config error")
			}
		})
	}
}

// TestRunWithDependenciesDown starts the processor while Redis and ClickHouse
// are unreachable: it must serve /healthz (200) and /readyz (503), and stop
// cleanly on context cancel without OS signals.
func TestRunWithDependenciesDown(t *testing.T) {
	httpAddr := freeAddr(t)
	env := map[string]string{
		"LOG_LEVEL":                "error",
		"REDIS_ADDR":               freeAddr(t),
		"REDIS_TIMEOUT":            "200ms",
		"CLICKHOUSE_URL":           "http://" + freeAddr(t),
		"CLICKHOUSE_TIMEOUT":       "200ms",
		"PROCESSOR_HTTP_ADDR":      httpAddr,
		"PROCESSOR_RETRY_INITIAL":  "50ms",
		"PROCESSOR_RETRY_MAX":      "200ms",
		"PROCESSOR_CONSUMER":       "test-consumer",
		"PROCESSOR_STATS_INTERVAL": "1h",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, envFunc(env)) }()

	client := &http.Client{Timeout: 3 * time.Second}
	base := "http://" + httpAddr
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := client.Get(base + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("/healthz = %d, want 200", resp.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("http server not up: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	resp, err := client.Get(base + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("/readyz = %d, want 503 while dependencies are down", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() = %v, want nil after cancel", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return within 5s after cancel")
	}
}
