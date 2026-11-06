package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
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
		{name: "missing query key", env: map[string]string{}},
		{name: "query key equals ingest key", env: map[string]string{"QUERY_API_KEY": "same", "INGEST_API_KEY": "same"}},
		{name: "bad window", env: map[string]string{"QUERY_API_KEY": "k", "QUERY_MAX_WINDOW": "a week"}},
		{name: "bad clickhouse url", env: map[string]string{"QUERY_API_KEY": "k", "CLICKHOUSE_URL": "localhost"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := run(ctx, envFunc(tt.env)); err == nil {
				t.Fatal("run() = nil, want error")
			}
		})
	}
}

// TestRunServesAndShutsDown runs the binary with ClickHouse unreachable:
// /healthz is 200, /readyz 503, /v1/logs 401 without a key and 503 with
// one, and cancel stops run within the shutdown budget (no OS signals).
func TestRunServesAndShutsDown(t *testing.T) {
	addr := freeAddr(t)
	env := map[string]string{
		"LOG_LEVEL":          "error",
		"QUERY_HTTP_ADDR":    addr,
		"QUERY_API_KEY":      "read-key",
		"INGEST_API_KEY":     "write-key",
		"CLICKHOUSE_URL":     "http://" + freeAddr(t),
		"CLICKHOUSE_TIMEOUT": "1s",
		"QUERY_TIMEOUT":      "1s",
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, envFunc(env)) }()

	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	base := "http://" + addr
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := client.Get(base + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server not up: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	get := func(path, key string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, base+path, nil)
		if key != "" {
			req.Header.Set("X-API-Key", key)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	logs := "/v1/logs?from=2026-11-06T00:00:00Z&to=2026-11-07T00:00:00Z"
	checks := []struct {
		path, key  string
		wantStatus int
	}{
		{"/readyz", "", http.StatusServiceUnavailable},
		{logs, "", http.StatusUnauthorized},
		{logs, "write-key", http.StatusUnauthorized},
		{logs, "read-key", http.StatusServiceUnavailable},
	}
	for _, c := range checks {
		status, body := get(c.path, c.key)
		if status != c.wantStatus {
			t.Errorf("GET %s key=%q: %d %s, want %d", c.path, c.key, status, body, c.wantStatus)
		}
		if strings.Contains(body, "127.0.0.1") {
			t.Errorf("GET %s leaks the ClickHouse address: %s", c.path, body)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return within 10s after cancel")
	}
}
