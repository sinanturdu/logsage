package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// RedisOptions configures RedisPublisher.
type RedisOptions struct {
	Addr    string        // host:port
	Stream  string        // stream key, e.g. logsage:events
	MaxLen  int64         // approximate cap on stream length (XADD MAXLEN ~)
	Timeout time.Duration // per Publish/Ready call
}

func (o RedisOptions) validate() error {
	switch {
	case o.Addr == "":
		return errors.New("ingest: redis address is empty")
	case o.Stream == "":
		return errors.New("ingest: redis stream is empty")
	case o.MaxLen <= 0:
		return errors.New("ingest: redis stream max length must be positive")
	case o.Timeout <= 0:
		return errors.New("ingest: redis timeout must be positive")
	}
	return nil
}

// streamClient is the part of Redis that RedisPublisher uses. It exists so
// the publisher logic can be unit-tested with a fake.
type streamClient interface {
	// XAddPipeline sends every entry in one pipeline (one round trip) and
	// returns the first error.
	XAddPipeline(ctx context.Context, entries []*redis.XAddArgs) error
	Ping(ctx context.Context) error
	Close() error
}

// RedisPublisher appends events to a Redis Stream, one entry per event.
type RedisPublisher struct {
	client streamClient
	opts   RedisOptions
	now    func() time.Time
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
	return &RedisPublisher{client: c, opts: opts, now: now}, nil
}

// Publish writes the whole batch in one pipeline. Each entry has the fields
// payload (event JSON), project_id and ingested_at (UTC, RFC3339). Any
// failure, including the timeout, is returned so the caller can answer 503.
// Some entries of a failed batch may still have been written.
func (p *RedisPublisher) Publish(ctx context.Context, events []Event) error {
	if len(events) == 0 {
		return nil
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
			Stream: p.opts.Stream,
			MaxLen: p.opts.MaxLen,
			Approx: true,
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

func (g goRedisClient) Ping(ctx context.Context) error {
	return g.c.Ping(ctx).Err()
}

func (g goRedisClient) Close() error {
	return g.c.Close()
}
