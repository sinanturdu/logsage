package processor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/sinanturdu/logsage/backend/internal/storage/clickhouse"
	"github.com/sinanturdu/logsage/backend/migrations"
)

// integrationEnv returns Redis and ClickHouse clients bound to a fresh stream
// and database, or skips unless REDIS_TEST_ADDR and CLICKHOUSE_TEST_URL are
// set. CLICKHOUSE_TEST_USER/CLICKHOUSE_TEST_PASSWORD are optional.
func integrationEnv(t *testing.T) (redisAddr, stream string, ch *clickhouse.Client) {
	t.Helper()
	redisAddr = os.Getenv("REDIS_TEST_ADDR")
	chURL := os.Getenv("CLICKHOUSE_TEST_URL")
	if redisAddr == "" || chURL == "" {
		t.Skip("REDIS_TEST_ADDR and CLICKHOUSE_TEST_URL not set; skipping integration test")
	}
	user := os.Getenv("CLICKHOUSE_TEST_USER")
	if user == "" {
		user = "default"
	}
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	db := "logsage_it_" + suffix
	ch, err := clickhouse.New(clickhouse.Options{
		URL: chURL, Database: db, User: user, Password: os.Getenv("CLICKHOUSE_TEST_PASSWORD"), Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := ch.Migrate(ctx, migrations.ClickHouse(), clickhouse.MigrationData{Database: db, TTLDays: 30}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = ch.Exec(context.Background(), "DROP DATABASE IF EXISTS "+db) })

	stream = "logsage:it:" + suffix
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	t.Cleanup(func() {
		_ = rdb.Del(context.Background(), stream, stream+":dead").Err()
		_ = rdb.Close()
	})
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("redis at %s: %v", redisAddr, err)
	}
	return redisAddr, stream, ch
}

func newIntegrationStream(t *testing.T, addr, stream, consumer string) *RedisStream {
	t.Helper()
	rs, err := NewRedisStream(RedisStreamOptions{
		Addr: addr, Stream: stream, DeadStream: stream + ":dead", DeadMaxLen: 1000,
		Consumer: consumer, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rs.Close() })
	return rs
}

// testLogger routes processor logs to t.Log so failures show their cause.
func testLogger(t *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(b []byte) (int, error) {
	w.t.Helper()
	w.t.Log(strings.TrimRight(string(b), "\n"))
	return len(b), nil
}

func integrationOptions() Options {
	return Options{
		Table: "logs", BatchSize: 100, ReadCount: 100, ReadBlock: 50 * time.Millisecond,
		FlushInterval: 100 * time.Millisecond, ClaimIdle: 300 * time.Millisecond, ClaimInterval: 100 * time.Millisecond,
		RetryInitial: 10 * time.Millisecond, RetryMax: 50 * time.Millisecond, ShutdownTimeout: 5 * time.Second,
	}
}

func queryInt(t *testing.T, ch *clickhouse.Client, q string) int {
	t.Helper()
	out, err := ch.Query(context.Background(), q+" FORMAT TabSeparated")
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("%s: parse %q: %v", q, out, err)
	}
	return n
}

func addEntries(t *testing.T, rs *RedisStream, n int) []string {
	t.Helper()
	ids := make([]string, n)
	for i := range ids {
		v := validEntry("").Values
		id, err := rs.Client().XAdd(context.Background(), &redis.XAddArgs{
			Stream: rs.opts.Stream, Values: []any{"payload", v["payload"], "project_id", v["project_id"], "ingested_at", v["ingested_at"]},
		}).Result()
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
	}
	return ids
}

// noAckStream simulates a processor that crashes after inserting a batch but
// before acknowledging it.
type noAckStream struct{ *RedisStream }

func (noAckStream) Ack(context.Context, ...string) error {
	return errors.New("simulated crash before XACK")
}

func runFor(t *testing.T, p *Processor, until func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	deadline := time.Now().Add(20 * time.Second)
	for !until() {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("condition not reached within 20s")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop")
	}
}

// TestIntegrationCrashRecovery: consumer A inserts 5 rows but "crashes"
// before acking; consumer C reads 2 entries and crashes before inserting.
// A fresh consumer B must claim all 7, insert them (5 again), ack, and the
// deduplicated row count must be exactly 7.
func TestIntegrationCrashRecovery(t *testing.T) {
	addr, stream, ch := integrationEnv(t)
	ctx := context.Background()

	rsA := newIntegrationStream(t, addr, stream, "crashed-a")
	if err := rsA.EnsureGroup(ctx); err != nil {
		t.Fatal(err)
	}
	addEntries(t, rsA, 5)

	pA, err := New(noAckStream{rsA}, ch, integrationOptions(), testLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	runFor(t, pA, func() bool { return queryInt(t, ch, "SELECT count() FROM logs") >= 5 })
	if info, _ := rsA.Info(ctx); info.Pending != 5 {
		t.Fatalf("pending after crash A = %d, want 5", info.Pending)
	}

	// Consumer C reads two new entries and dies before inserting them.
	addEntries(t, rsA, 2)
	rsC := newIntegrationStream(t, addr, stream, "crashed-c")
	got, err := rsC.ReadNew(ctx, 10, 100*time.Millisecond)
	if err != nil || len(got) != 2 {
		t.Fatalf("crashed-c read %d entries, err %v; want 2", len(got), err)
	}

	time.Sleep(400 * time.Millisecond) // let the entries become idle > ClaimIdle

	rsB := newIntegrationStream(t, addr, stream, "recovery-b")
	pB, err := New(rsB, ch, integrationOptions(), testLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	runFor(t, pB, func() bool {
		info, err := rsB.Info(ctx)
		return err == nil && info.Pending == 0 && info.Lag == 0
	})

	raw := queryInt(t, ch, "SELECT count() FROM logs")
	if raw < 7 {
		t.Fatalf("raw rows = %d, want at least 7", raw)
	}
	if n := queryInt(t, ch, "SELECT count() FROM logs FINAL"); n != 7 {
		t.Errorf("rows with FINAL = %d, want 7", n)
	}
	if n := queryInt(t, ch, "SELECT uniqExact(event_id) FROM logs"); n != 7 {
		t.Errorf("distinct event_id = %d, want 7", n)
	}
	if err := ch.Exec(ctx, fmt.Sprintf("OPTIMIZE TABLE %s.logs FINAL", ch.Database())); err != nil {
		t.Fatal(err)
	}
	if n := queryInt(t, ch, "SELECT count() FROM logs"); n != 7 {
		t.Errorf("rows after merge = %d, want 7 (duplicates collapsed)", n)
	}
	t.Logf("raw rows before merge: %d (includes redelivered duplicates), after dedup: 7", raw)
}

// TestIntegrationDeadLetter: an unparseable entry lands in the dead-letter
// stream with its original fields and a reason, and is acked.
func TestIntegrationDeadLetter(t *testing.T) {
	addr, stream, ch := integrationEnv(t)
	ctx := context.Background()
	rs := newIntegrationStream(t, addr, stream, "dl")
	if err := rs.EnsureGroup(ctx); err != nil {
		t.Fatal(err)
	}
	badID, err := rs.Client().XAdd(ctx, &redis.XAddArgs{Stream: stream, Values: []any{"payload", "{broken", "project_id", "default", "ingested_at", "2026-11-06T10:00:00Z"}}).Result()
	if err != nil {
		t.Fatal(err)
	}
	addEntries(t, rs, 1)

	p, err := New(rs, ch, integrationOptions(), testLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	runFor(t, p, func() bool {
		info, err := rs.Info(ctx)
		return err == nil && info.Pending == 0 && info.Lag == 0 && queryInt(t, ch, "SELECT count() FROM logs") == 1
	})

	dead, err := rs.Client().XRange(ctx, stream+":dead", "-", "+").Result()
	if err != nil || len(dead) != 1 {
		t.Fatalf("dead-letter entries = %d, err %v; want 1", len(dead), err)
	}
	v := dead[0].Values
	if v["original_id"] != badID || v["payload"] != "{broken" || !strings.Contains(fmt.Sprint(v["reason"]), "payload") {
		t.Errorf("dead-letter entry = %v", v)
	}
	if p.Stats(ctx).DeadLettered != 1 {
		t.Errorf("dead_lettered_total = %d, want 1", p.Stats(ctx).DeadLettered)
	}
}
