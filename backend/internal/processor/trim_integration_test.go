package processor

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/sinanturdu/logsage/backend/internal/ingest"
)

// redisOnly returns a Redis address and a fresh stream name, or skips unless
// REDIS_TEST_ADDR is set.
func redisOnly(t *testing.T) (string, string) {
	t.Helper()
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR not set; skipping Redis integration test")
	}
	stream := "logsage:trim-it:" + strconv.FormatInt(time.Now().UnixNano(), 36)
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() {
		_ = rdb.Del(context.Background(), stream, stream+":dead").Err()
		_ = rdb.Close()
	})
	return addr, stream
}

func streamIDs(t *testing.T, c *redis.Client, stream string) []string {
	t.Helper()
	msgs, err := c.XRange(context.Background(), stream, "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	return ids
}

// TestIntegrationTrimmerKeepsUnacknowledged: a fast group and a slow second
// group with a backlog. The trimmer removes only what both acknowledged;
// pending and undelivered entries always survive.
func TestIntegrationTrimmerKeepsUnacknowledged(t *testing.T) {
	addr, stream := redisOnly(t)
	ctx := context.Background()
	fast := newIntegrationStream(t, addr, stream, "fast-1")
	if err := fast.EnsureGroup(ctx); err != nil {
		t.Fatal(err)
	}
	slowOpts := fast.opts
	slowOpts.Group, slowOpts.Consumer = "alerting", "slow-1"
	slow, err := NewRedisStream(slowOpts)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	if err := slow.EnsureGroup(ctx); err != nil {
		t.Fatal(err)
	}
	c := fast.Client()
	ids := addEntries(t, fast, 100)

	h := newHarness(t, testOptions())
	trim := func() int64 {
		t.Helper()
		n, err := h.p.TrimOnce(ctx, fast)
		if err != nil {
			t.Fatalf("TrimOnce: %v", err)
		}
		return n
	}
	ack := func(rs *RedisStream, es []Entry) {
		t.Helper()
		var keys []string
		for _, e := range es {
			keys = append(keys, e.ID)
		}
		if err := rs.Ack(ctx, keys...); err != nil {
			t.Fatal(err)
		}
	}

	// Nothing read yet by either group: no safe boundary, nothing removed.
	if _, err := h.p.TrimOnce(ctx, fast); err == nil {
		t.Fatal("trimmed although no group had read anything")
	}

	// Fast group: reads 60, acks the first 40 (20 pending, 40 undelivered).
	got, err := fast.ReadNew(ctx, 60, 100*time.Millisecond)
	if err != nil || len(got) != 60 {
		t.Fatalf("fast read %d, %v", len(got), err)
	}
	ack(fast, got[:40])
	// Slow group: reads and acks only 30.
	sgot, err := slow.ReadNew(ctx, 30, 100*time.Millisecond)
	if err != nil || len(sgot) != 30 {
		t.Fatalf("slow read %d, %v", len(sgot), err)
	}
	ack(slow, sgot)

	if n := trim(); n != 30 {
		t.Fatalf("removed %d, want 30 (only what both groups acknowledged)", n)
	}
	left := streamIDs(t, c, stream)
	if len(left) != 70 || left[0] != ids[30] {
		t.Fatalf("stream has %d entries starting at %v, want 70 starting at %s", len(left), left[:1], ids[30])
	}

	// Slow group catches up completely: now the fast group's pending entries
	// (ids[40:60]) and undelivered entries (ids[60:]) must still be kept.
	rest, err := slow.ReadNew(ctx, 100, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ack(slow, rest)
	if n := trim(); n != 10 {
		t.Fatalf("removed %d, want 10 (ids[30:40], acknowledged by both)", n)
	}
	left = streamIDs(t, c, stream)
	if len(left) != 60 || left[0] != ids[40] {
		t.Fatalf("stream has %d entries starting at %v, want 60 starting at the fast group's lowest pending %s", len(left), left[:1], ids[40])
	}

	// Oldest unprocessed for the fast group is its lowest pending entry.
	info, err := fast.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.Pending != 20 || info.OldestUnprocessedAge <= 0 {
		t.Errorf("info = %+v, want 20 pending and a positive oldest age", info)
	}

	// Fast group finishes: everything acknowledged by both, all trimmed.
	ack(fast, got[40:])
	more, err := fast.ReadNew(ctx, 100, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ack(fast, more)
	if n := trim(); n != 60 {
		t.Fatalf("removed %d, want the remaining 60", n)
	}
	if left := streamIDs(t, c, stream); len(left) != 0 {
		t.Fatalf("%d entries left, want 0", len(left))
	}
	if info, err := fast.Info(ctx); err != nil || info.OldestUnprocessedAge != 0 {
		t.Errorf("caught up: info = %+v, %v; want age 0", info, err)
	}
	if h.p.Stats(ctx).Trimmed != 100 {
		t.Errorf("trimmed_total = %d, want 100", h.p.Stats(ctx).Trimmed)
	}
}

// TestIntegrationBurstNoLoss: a burst through the real publisher with a small
// backpressure limit while the processor and trimmer run. Every accepted
// event must end up in ClickHouse exactly once (FINAL).
func TestIntegrationBurstNoLoss(t *testing.T) {
	addr, stream, ch := integrationEnv(t)
	ctx := context.Background()

	pub, err := ingest.NewRedisPublisher(ingest.RedisOptions{
		Addr: addr, Stream: stream, Timeout: 5 * time.Second, MaxStreamLen: 2000, LengthRefresh: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	refreshCtx, stopRefresh := context.WithCancel(ctx)
	defer stopRefresh()
	go pub.RunLengthRefresher(refreshCtx)

	rs := newIntegrationStream(t, addr, stream, "burst-1")
	p, err := New(rs, ch, integrationOptions(), testLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	go p.RunTrimmer(runCtx, rs, 50*time.Millisecond)

	var accepted, rejected atomic.Int64
	var wg sync.WaitGroup
	sendUntil := time.Now().Add(2 * time.Second)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(sendUntil) {
				batch := make([]ingest.Event, 100)
				for i := range batch {
					batch[i] = ingest.Event{Timestamp: time.Now().UTC(), Level: "info", Service: "burst", Message: "m", ProjectID: "default"}
				}
				err := pub.Publish(ctx, batch)
				switch {
				case err == nil:
					accepted.Add(100)
				case errors.Is(err, ingest.ErrOverloaded):
					rejected.Add(1)
					time.Sleep(5 * time.Millisecond)
				default:
					t.Errorf("Publish: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	deadline := time.Now().Add(60 * time.Second)
	for {
		info, err := rs.Info(ctx)
		if err == nil && info.Pending == 0 && info.Lag == 0 && queryInt(t, ch, "SELECT count() FROM logs FINAL") >= int(accepted.Load()) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not drained: info %+v err %v", info, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	stored := queryInt(t, ch, "SELECT count() FROM logs FINAL")
	if int64(stored) != accepted.Load() {
		t.Fatalf("accepted %d, stored %d, lost %d", accepted.Load(), stored, accepted.Load()-int64(stored))
	}
	if rejected.Load() == 0 {
		t.Log("note: backpressure never triggered in this run")
	}
	t.Logf("accepted %d, stored %d, lost 0; %d batches got 429-equivalent; trimmed %d", accepted.Load(), stored, rejected.Load(), p.Stats(ctx).Trimmed)
}
