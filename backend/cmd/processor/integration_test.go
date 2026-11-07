package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/sinanturdu/logsage/backend/internal/ingest"
	"github.com/sinanturdu/logsage/backend/internal/storage/clickhouse"
)

// TestIntegrationEndToEnd publishes events with the ingest Redis publisher,
// runs the processor binary's run function, and checks the rows in
// ClickHouse. Skipped unless REDIS_TEST_ADDR and CLICKHOUSE_TEST_URL are set.
func TestIntegrationEndToEnd(t *testing.T) {
	redisAddr := os.Getenv("REDIS_TEST_ADDR")
	chURL := os.Getenv("CLICKHOUSE_TEST_URL")
	if redisAddr == "" || chURL == "" {
		t.Skip("REDIS_TEST_ADDR and CLICKHOUSE_TEST_URL not set; skipping integration test")
	}
	chUser := os.Getenv("CLICKHOUSE_TEST_USER")
	if chUser == "" {
		chUser = "default"
	}
	chPassword := os.Getenv("CLICKHOUSE_TEST_PASSWORD")
	ctx := context.Background()

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	stream := "logsage:e2e:" + suffix
	db := "logsage_e2e_" + suffix

	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	ch, err := clickhouse.New(clickhouse.Options{URL: chURL, Database: db, User: chUser, Password: chPassword, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	// Close the client inside the cleanup: a deferred Close would run first
	// and make the DEL fail silently.
	t.Cleanup(func() {
		_ = rdb.Del(context.Background(), stream, stream+":dead").Err()
		_ = rdb.Close()
		_ = ch.Exec(context.Background(), "DROP DATABASE IF EXISTS "+db)
	})

	pub, err := ingest.NewRedisPublisher(ingest.RedisOptions{Addr: redisAddr, Stream: stream, MaxStreamLen: 1_000_000, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	ts := time.Date(2026, 11, 6, 10, 15, 30, 123_000_000, time.UTC)
	events := []ingest.Event{
		{Timestamp: ts, Level: "error", Service: "checkout", Message: "payment gateway timeout", TraceID: "abc", Attributes: map[string]string{"env": "it"}, ProjectID: "default"},
		{Timestamp: ts.Add(time.Second), Level: "info", Service: "checkout", Message: "retry ok", ProjectID: "default"},
		{Timestamp: ts.Add(2 * time.Second), Level: "warn", Service: "search", Message: "slow query", ProjectID: "default"},
	}
	if err := pub.Publish(ctx, events); err != nil {
		t.Fatalf("publish: %v", err)
	}

	httpAddr := freeAddr(t)
	env := map[string]string{
		"LOG_LEVEL":                "warn",
		"REDIS_ADDR":               redisAddr,
		"REDIS_STREAM":             stream,
		"CLICKHOUSE_URL":           chURL,
		"CLICKHOUSE_DB":            db,
		"CLICKHOUSE_USER":          chUser,
		"CLICKHOUSE_PASSWORD":      chPassword,
		"PROCESSOR_HTTP_ADDR":      httpAddr,
		"PROCESSOR_CONSUMER":       "e2e",
		"PROCESSOR_DEAD_STREAM":    stream + ":dead",
		"PROCESSOR_FLUSH_INTERVAL": "100ms",
		"PROCESSOR_READ_BLOCK":     "100ms",
		"PROCESSOR_STATS_INTERVAL": "1h",
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(runCtx, envFunc(env)) }()

	count := func() int {
		out, err := ch.Query(ctx, "SELECT count() FROM logs FINAL FORMAT TabSeparated")
		if err != nil {
			return -1 // database may not exist yet
		}
		n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
		return n
	}
	deadline := time.Now().Add(20 * time.Second)
	for count() != len(events) {
		if time.Now().After(deadline) {
			t.Fatalf("rows = %d, want %d", count(), len(events))
		}
		time.Sleep(100 * time.Millisecond)
	}

	out, err := ch.Query(ctx, "SELECT project_id, level, service, message, trace_id, attributes['env'], toString(timestamp), fingerprint, length(event_id) > 0 FROM logs FINAL ORDER BY timestamp FORMAT TabSeparated")
	if err != nil {
		t.Fatal(err)
	}
	want := "default\terror\tcheckout\tpayment gateway timeout\tabc\tit\t2026-11-06 10:15:30.123\t0\t1\n" +
		"default\tinfo\tcheckout\tretry ok\t\t\t2026-11-06 10:15:31.123\t0\t1\n" +
		"default\twarn\tsearch\tslow query\t\t\t2026-11-06 10:15:32.123\t0\t1\n"
	if string(out) != want {
		t.Errorf("rows =\n%s\nwant\n%s", out, want)
	}

	resp, err := http.Get("http://" + httpAddr + "/stats")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var stats map[string]any
	if err := json.Unmarshal(body, &stats); err != nil {
		t.Fatalf("stats %s: %v", body, err)
	}
	if stats["processed_total"] != 3.0 || stats["group_pending"] != 0.0 || stats["group_lag"] != 0.0 {
		t.Errorf("stats = %v", stats)
	}
	if resp, err := http.Get("http://" + httpAddr + "/readyz"); err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("/readyz = %v %v, want 200", resp, err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not stop")
	}
}
