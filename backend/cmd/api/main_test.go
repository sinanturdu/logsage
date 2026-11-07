package main

import (
	"bufio"
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/password"
	"github.com/sinanturdu/logsage/backend/internal/storage/postgres/pgtest"
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
	chUser, chPass := os.Getenv("CLICKHOUSE_TEST_USER"), os.Getenv("CLICKHOUSE_TEST_PASSWORD")
	if chUser == "" || chPass == "" {
		// cmd/api requires every CLICKHOUSE_* setting (ADR 0014), so this
		// test needs a ClickHouse user with a password, not the
		// passwordless default user.
		t.Fatal("set CLICKHOUSE_TEST_USER and CLICKHOUSE_TEST_PASSWORD: cmd/api refuses an empty ClickHouse user or password")
	}
	env := map[string]string{"DATABASE_URL": url, "API_HTTP_ADDR": "127.0.0.1:0", "COOKIE_SECURE": "false",
		"ARGON2_MEMORY_KIB": "19456", "ARGON2_ITERATIONS": "1", "ARGON2_PARALLELISM": "1",
		"CLICKHOUSE_URL": chURL, "CLICKHOUSE_DB": "default", "CLICKHOUSE_USER": chUser, "CLICKHOUSE_PASSWORD": chPass}
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

// TestRunClosesTailsOnShutdown: a live tail does not hold up a graceful
// shutdown; it gets status closed (reason shutdown) and run returns promptly.
func TestRunClosesTailsOnShutdown(t *testing.T) {
	chURL, chUser, chPass := os.Getenv("CLICKHOUSE_TEST_URL"), os.Getenv("CLICKHOUSE_TEST_USER"), os.Getenv("CLICKHOUSE_TEST_PASSWORD")
	if chURL == "" {
		t.Skip("CLICKHOUSE_TEST_URL not set")
	}
	if chUser == "" || chPass == "" {
		t.Fatal("set CLICKHOUSE_TEST_USER and CLICKHOUSE_TEST_PASSWORD: cmd/api refuses an empty ClickHouse user or password")
	}
	_, st, dbURL := pgtest.New(t)
	hasher, err := password.NewHasher(password.Params{MemoryKiB: password.MinMemoryKiB, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	phc, _ := hasher.Hash(ctx, "a long test passphrase")
	u, err := st.CreateUser(ctx, "tail@example.com", phc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateProjectWithOwner(ctx, "Shop", "shop", u.ID); err != nil {
		t.Fatal(err)
	}

	env := map[string]string{"DATABASE_URL": dbURL, "API_HTTP_ADDR": "127.0.0.1:0", "COOKIE_SECURE": "false",
		"ARGON2_MEMORY_KIB": "19456", "ARGON2_ITERATIONS": "1", "ARGON2_PARALLELISM": "1",
		"CLICKHOUSE_URL": chURL, "CLICKHOUSE_DB": "default", "CLICKHOUSE_USER": chUser, "CLICKHOUSE_PASSWORD": chPass}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- run(runCtx, envFunc(env), ready) }()
	addr := <-ready

	lreq, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/auth/login",
		strings.NewReader(`{"email":"tail@example.com","password":"a long test passphrase"}`))
	lreq.Header.Set("Content-Type", "application/json")
	lreq.Header.Set("Origin", "http://"+addr)
	login, err := http.DefaultClient.Do(lreq)
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	var cookie *http.Cookie
	for _, c := range login.Cookies() {
		if c.Name == "logsage_session" {
			cookie = c
		}
	}
	if login.StatusCode != 200 || cookie == nil {
		t.Fatalf("login = %d", login.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/api/projects/shop/tail", nil)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("tail = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	lines := make(chan string, 100)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	waitLine := func(want string) {
		t.Helper()
		deadline := time.After(20 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("stream ended before %q", want)
				}
				if strings.Contains(l, want) {
					return
				}
			case <-deadline:
				t.Fatalf("no %q within 10s", want)
			}
		}
	}
	waitLine(`"state":`) // the stream is open

	start := time.Now()
	cancel()
	waitLine(`"reason":"shutdown"`)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() = %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("run did not return with an open tail")
	}
	// Without the shutdown hook, Shutdown would wait its full timeout for the
	// stream and then force-close it; the boundary is that timeout, not a guess.
	if d := time.Since(start); d >= httpShutdownTimeout {
		t.Errorf("shutdown with an open tail took %v, the full shutdown timeout", d)
	}
}
