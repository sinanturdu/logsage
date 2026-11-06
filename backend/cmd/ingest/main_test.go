package main

import (
	"context"
	"net"
	"net/http"
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
