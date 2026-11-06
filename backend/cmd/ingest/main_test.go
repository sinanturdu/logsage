package main

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// freeAddr returns a loopback address with a port that was free a moment ago.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return addr
}

func TestRunGracefulShutdown(t *testing.T) {
	addr := freeAddr(t)
	t.Setenv("HTTP_ADDR", addr)
	t.Setenv("LOG_LEVEL", "error")
	t.Setenv("INGEST_API_KEY", "test-key")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- run(ctx) }()

	client := &http.Client{
		Timeout:   500 * time.Millisecond,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	url := "http://" + addr + "/healthz"

	// Wait until the server is serving.
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("healthz status = %d, want %d", resp.StatusCode, http.StatusOK)
			}
			break
		}
		select {
		case err := <-done:
			t.Fatalf("run returned before shutdown: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("server not ready: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not return within 2s after cancel")
	}

	resp, err := client.Get(url)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("request after shutdown succeeded with status %d, want error", resp.StatusCode)
	}
}

func TestRunRequiresAPIKey(t *testing.T) {
	t.Setenv("HTTP_ADDR", freeAddr(t))
	t.Setenv("INGEST_API_KEY", "")

	// Cancel up front so a regression returns promptly instead of serving forever.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx); err == nil {
		t.Fatal("run() = nil, want error when INGEST_API_KEY is unset")
	}
}

func TestRunRejectsBadPublisherConfig(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "unknown publisher", env: map[string]string{"PUBLISHER": "kafka"}},
		{name: "redis without addr", env: map[string]string{"PUBLISHER": "redis", "REDIS_ADDR": ""}},
		{name: "bad redis timeout", env: map[string]string{"PUBLISHER": "redis", "REDIS_ADDR": "localhost:6379", "REDIS_TIMEOUT": "soon"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HTTP_ADDR", freeAddr(t))
			t.Setenv("INGEST_API_KEY", "test-key")
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := run(ctx); err == nil {
				t.Fatal("run() = nil, want config error")
			}
		})
	}
}

// TestRunRedisPublisherUnreachable starts with PUBLISHER=redis pointing at a
// closed port: the server must still start, /readyz must report 503, and
// ingest must answer 503.
func TestRunRedisPublisherUnreachable(t *testing.T) {
	addr := freeAddr(t)
	t.Setenv("HTTP_ADDR", addr)
	t.Setenv("LOG_LEVEL", "error")
	t.Setenv("INGEST_API_KEY", "test-key")
	t.Setenv("PUBLISHER", "redis")
	t.Setenv("REDIS_ADDR", freeAddr(t))
	t.Setenv("REDIS_TIMEOUT", "300ms")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()

	client := &http.Client{Timeout: 3 * time.Second}
	base := "http://" + addr

	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := client.Get(base + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server not ready: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	resp, err := client.Get(base + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("/readyz status = %d, want 503", resp.StatusCode)
	}

	body := `{"events":[{"timestamp":"2026-11-06T10:15:30Z","level":"info","service":"s","message":"m"}]}`
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/ingest", strings.NewReader(body))
	req.Header.Set("X-API-Key", "test-key")
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("/v1/ingest status = %d, want 503", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after cancel")
	}
}
