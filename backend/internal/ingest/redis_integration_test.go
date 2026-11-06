package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisIntegration returns a raw client and options for a fresh stream key on
// the Redis at REDIS_TEST_ADDR, or skips the test when it is not set.
func redisIntegration(t *testing.T, maxStreamLen int64) (*redis.Client, RedisOptions) {
	t.Helper()
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR not set; skipping Redis integration test")
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = rdb.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("REDIS_TEST_ADDR=%s is set but Redis is unreachable: %v", addr, err)
	}

	stream := fmt.Sprintf("logsage:test:%s:%d", t.Name(), time.Now().UnixNano())
	t.Cleanup(func() { _ = rdb.Del(context.Background(), stream).Err() })
	return rdb, RedisOptions{Addr: addr, Stream: stream, MaxStreamLen: maxStreamLen, Timeout: 5 * time.Second, LengthRefresh: 20 * time.Millisecond}
}

func TestRedisIntegrationPublish(t *testing.T) {
	rdb, opts := redisIntegration(t, 1000)
	p, err := NewRedisPublisher(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx := context.Background()

	if err := p.Ready(ctx); err != nil {
		t.Fatalf("Ready: %v", err)
	}

	events := sampleEvents(3)
	if err := p.Publish(ctx, events); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	msgs, err := rdb.XRange(ctx, opts.Stream, "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != len(events) {
		t.Fatalf("stream has %d entries, want %d", len(msgs), len(events))
	}
	for i, m := range msgs {
		if len(m.Values) != 3 {
			t.Errorf("entry %d fields = %v, want payload, project_id, ingested_at", i, m.Values)
		}
		if m.Values["project_id"] != "default" {
			t.Errorf("entry %d project_id = %v", i, m.Values["project_id"])
		}
		ts, _ := m.Values["ingested_at"].(string)
		if parsed, err := time.Parse(time.RFC3339Nano, ts); err != nil || parsed.Location() != time.UTC {
			t.Errorf("entry %d ingested_at = %q, want UTC RFC3339", i, ts)
		}
		var got Event
		payload, _ := m.Values["payload"].(string)
		if err := json.Unmarshal([]byte(payload), &got); err != nil {
			t.Fatalf("entry %d payload: %v", i, err)
		}
		if got.Attributes["i"] != events[i].Attributes["i"] {
			t.Errorf("entry %d out of order: attributes %v, want %v", i, got.Attributes, events[i].Attributes)
		}
	}
}

// TestRedisIntegrationBackpressure: with a tiny INGEST_MAX_STREAM_LEN and
// no consumer reading, the publisher starts returning ErrOverloaded and the
// stream still holds every entry that was accepted (nothing is trimmed).
func TestRedisIntegrationBackpressure(t *testing.T) {
	rdb, opts := redisIntegration(t, 50)
	p, err := NewRedisPublisher(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.RunLengthRefresher(ctx)

	var accepted, overloaded int
	for i := 0; i < 200; i++ {
		err := p.Publish(ctx, sampleEvents(5))
		switch {
		case err == nil:
			accepted += 5
		case errors.Is(err, ErrOverloaded):
			overloaded++
		default:
			t.Fatalf("Publish: %v", err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if overloaded == 0 {
		t.Fatal("never overloaded with MaxStreamLen 50")
	}
	n, err := rdb.XLen(context.Background(), opts.Stream).Result()
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(accepted) {
		t.Fatalf("stream holds %d entries, accepted %d: accepted entries must all still be there", n, accepted)
	}
	t.Logf("accepted %d events, %d batches rejected, stream length %d (limit 50, soft)", accepted, overloaded, n)
}
