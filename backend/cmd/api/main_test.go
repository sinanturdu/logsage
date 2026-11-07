package main

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

func envFunc(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestRunRejectsBadConfig(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"no database":        {},
		"no clickhouse":      {"DATABASE_URL": "postgres://u:p@127.0.0.1:1/db"},
		"no clickhouse pass": {"DATABASE_URL": "postgres://u:p@127.0.0.1:1/db", "CLICKHOUSE_URL": "http://127.0.0.1:1", "CLICKHOUSE_DB": "logsage", "CLICKHOUSE_USER": "u"},
		"bad ttl":            {"DATABASE_URL": "postgres://u:p@127.0.0.1:1/db", "SESSION_IDLE_TTL": "nope"},
		"wildcard origin":    {"DATABASE_URL": "postgres://u:p@127.0.0.1:1/db", "ALLOWED_ORIGINS": "*"},
		"weak argon2":        {"DATABASE_URL": "postgres://u:p@127.0.0.1:1/db", "ARGON2_MEMORY_KIB": "1024"},
		"unreachable db":     {"DATABASE_URL": "postgres://u:p@127.0.0.1:1/db?connect_timeout=1", "POSTGRES_CONNECT_TIMEOUT": "1s"},
		"idle over absolute": {"DATABASE_URL": "postgres://u:p@127.0.0.1:1/db", "SESSION_IDLE_TTL": "48h", "SESSION_ABSOLUTE_TTL": "1h"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(context.Background(), envFunc(env), nil); err == nil {
				t.Fatal("run() = nil, want error")
			}
		})
	}
}

// TestRunServesAndShutsDown needs PostgreSQL; cancel stops run within the
// shutdown budget without OS signals.
func TestRunServesAndShutsDown(t *testing.T) {
	url, chURL := os.Getenv("POSTGRES_TEST_URL"), os.Getenv("CLICKHOUSE_TEST_URL")
	if url == "" || chURL == "" {
		t.Skip("POSTGRES_TEST_URL or CLICKHOUSE_TEST_URL not set")
	}
	chUser := os.Getenv("CLICKHOUSE_TEST_USER")
	if chUser == "" {
		chUser = "default"
	}
	env := map[string]string{"DATABASE_URL": url, "API_HTTP_ADDR": "127.0.0.1:0", "COOKIE_SECURE": "false",
		"ARGON2_MEMORY_KIB": "19456", "ARGON2_ITERATIONS": "1", "ARGON2_PARALLELISM": "1",
		"CLICKHOUSE_URL": chURL, "CLICKHOUSE_DB": "default", "CLICKHOUSE_USER": chUser, "CLICKHOUSE_PASSWORD": os.Getenv("CLICKHOUSE_TEST_PASSWORD")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- run(ctx, envFunc(env), ready) }()
	var addr string
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatalf("run exited early: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("server did not start")
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	for path, want := range map[string]int{"/healthz": 200, "/readyz": 200, "/api/auth/me": 401, "/api/nope": 404} {
		resp, err := client.Get("http://" + addr + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("GET %s = %d, want %d", path, resp.StatusCode, want)
		}
		if path != "/healthz" && path != "/readyz" && resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("GET %s: missing Cache-Control no-store", path)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return after cancel")
	}
}
