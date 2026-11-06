package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// defaultProjectID is written to every stream entry until projects exist (v0.2).
const defaultProjectID = "default"

// Stream entry field names.
const (
	fieldPayload    = "payload"
	fieldProjectID  = "project_id"
	fieldIngestedAt = "ingested_at"
)

// ErrOverloaded is returned by Publish when the stream already holds
// MaxStreamLen entries or more. Nothing was written; the caller should ask
// the client to retry later (HTTP 429).
var ErrOverloaded = errors.New("ingest: stream backlog at limit")

// DefaultLengthRefresh is how often the cached stream length is refreshed.
const DefaultLengthRefresh = 500 * time.Millisecond

// RedisOptions configures RedisPublisher.
type RedisOptions struct {
	Addr    string        // host:port
	Stream  string        // stream key, e.g. logsage:events
	Timeout time.Duration // per Publish/Ready call
	// MaxStreamLen is the soft backpressure limit: at or above this cached
	// XLEN, Publish returns ErrOverloaded without writing.
	MaxStreamLen int64
	// LengthRefresh is the interval of the background XLEN refresh
	// (RunLengthRefresher); DefaultLengthRefresh when zero.
	LengthRefresh time.Duration
}

func (o RedisOptions) validate() error {
	switch {
	case o.Addr == "":
		return errors.New("ingest: redis address is empty")
	case o.Stream == "":
		return errors.New("ingest: redis stream is empty")
	case o.MaxStreamLen <= 0:
		return errors.New("ingest: max stream length must be positive")
	case o.Timeout <= 0:
		return errors.New("ingest: redis timeout must be positive")
	case o.LengthRefresh < 0:
		return errors.New("ingest: length refresh interval must not be negative")
	}
	return nil
}

// streamClient is the part of Redis that RedisPublisher uses. It exists so
// the publisher logic can be unit-tested with a fake.
type streamClient interface {
	// XAddPipeline sends every entry in one pipeline (one round trip) and
	// returns the first error.
	XAddPipeline(ctx context.Context, entries []*redis.XAddArgs) error
	XLen(ctx context.Context, stream string) (int64, error)
	Ping(ctx context.Context) error
	Close() error
}

// RedisPublisher appends events to a Redis Stream, one entry per event. The
// stream is never trimmed here (the processor trims what every consumer group
// has finished, see ADR 0010); instead Publish refuses new events while the
// cached stream length is at or above MaxStreamLen.
type RedisPublisher struct {
	client streamClient
	opts   RedisOptions
	now    func() time.Time

	cachedLen atomic.Int64 // last XLEN seen by the refresher
	lenKnown  atomic.Bool  // false until the first successful refresh
}

// NewRedisPublisher returns a publisher backed by go-redis. It does not
// connect until the first call; use Ready to check connectivity.
func NewRedisPublisher(opts RedisOptions) (*RedisPublisher, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	c := redis.NewClient(&redis.Options{
		Addr:         opts.Addr,
		DialTimeout:  opts.Timeout,
		ReadTimeout:  opts.Timeout,
		WriteTimeout: opts.Timeout,
		// Honor the deadline of the context passed to each call.
		ContextTimeoutEnabled: true,
	})
	return newRedisPublisher(goRedisClient{c}, opts, time.Now)
}

func newRedisPublisher(c streamClient, opts RedisOptions, now func() time.Time) (*RedisPublisher, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	if opts.LengthRefresh == 0 {
		opts.LengthRefresh = DefaultLengthRefresh
	}
	return &RedisPublisher{client: c, opts: opts, now: now}, nil
}

// RefreshLength reads XLEN once and caches it. On error the previous value
// is kept (a Redis outage makes Publish fail with a real error anyway).
func (p *RedisPublisher) RefreshLength(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, p.opts.Timeout)
	defer cancel()
	n, err := p.client.XLen(ctx, p.opts.Stream)
	if err != nil {
		return fmt.Errorf("redis publisher: xlen: %w", err)
	}
	p.cachedLen.Store(n)
	p.lenKnown.Store(true)
	return nil
}

// RunLengthRefresher refreshes the cached stream length every LengthRefresh
// until ctx ends, so Publish never needs an extra Redis call per request.
func (p *RedisPublisher) RunLengthRefresher(ctx context.Context) {
	_ = p.RefreshLength(ctx)
	t := time.NewTicker(p.opts.LengthRefresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = p.RefreshLength(ctx)
		}
	}
}

// CachedStreamLength returns the last refreshed XLEN; ok is false before the
// first successful refresh.
func (p *RedisPublisher) CachedStreamLength() (n int64, ok bool) {
	return p.cachedLen.Load(), p.lenKnown.Load()
}

// Publish writes the whole batch in one pipeline. Each entry has the fields
// payload (event JSON), project_id and ingested_at (UTC, RFC3339).
//
// If the cached stream length is at or above MaxStreamLen, Publish returns
// ErrOverloaded and writes nothing. The limit is soft: the cache can be up to
// one refresh interval old and concurrent requests all pass the same check,
// so the stream can overshoot by roughly (ingest rate x refresh interval) plus
// the batches in flight.
//
// Any other failure, including the timeout and Redis refusing writes when
// out of memory, is returned so the caller can answer 503. Some entries of a
// failed batch may still have been written.
func (p *RedisPublisher) Publish(ctx context.Context, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	if p.cachedLen.Load() >= p.opts.MaxStreamLen {
		return ErrOverloaded
	}
	ctx, cancel := context.WithTimeout(ctx, p.opts.Timeout)
	defer cancel()

	ingestedAt := p.now().UTC().Format(time.RFC3339Nano)
	entries := make([]*redis.XAddArgs, len(events))
	for i, ev := range events {
		payload, err := json.Marshal(ev)
		if err != nil {
			return fmt.Errorf("redis publisher: encode event %d: %w", i, err)
		}
		entries[i] = &redis.XAddArgs{
			Stream: p.opts.Stream, // no MAXLEN: never trim unread entries here
			Values: []any{
				fieldPayload, string(payload),
				fieldProjectID, defaultProjectID,
				fieldIngestedAt, ingestedAt,
			},
		}
	}

	if err := p.client.XAddPipeline(ctx, entries); err != nil {
		return fmt.Errorf("redis publisher: xadd %d entries to %q: %w", len(entries), p.opts.Stream, err)
	}
	return nil
}

// Ready pings Redis within the configured timeout.
func (p *RedisPublisher) Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, p.opts.Timeout)
	defer cancel()
	if err := p.client.Ping(ctx); err != nil {
		return fmt.Errorf("redis publisher: ping: %w", err)
	}
	return nil
}

// Close releases the Redis connections.
func (p *RedisPublisher) Close() error {
	return p.client.Close()
}

// goRedisClient adapts *redis.Client to streamClient.
type goRedisClient struct {
	c *redis.Client
}

func (g goRedisClient) XAddPipeline(ctx context.Context, entries []*redis.XAddArgs) error {
	cmds, err := g.c.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for _, e := range entries {
			pipe.XAdd(ctx, e)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, cmd := range cmds {
		if err := cmd.Err(); err != nil {
			return err
		}
	}
	return nil
}

func (g goRedisClient) XLen(ctx context.Context, stream string) (int64, error) {
	return g.c.XLen(ctx, stream).Result()
}

func (g goRedisClient) Ping(ctx context.Context) error {
	return g.c.Ping(ctx).Err()
}

func (g goRedisClient) Close() error {
	return g.c.Close()
}
