package processor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// GroupName is the consumer group used by all processor instances.
const GroupName = "logsage-processor"

// RedisStreamOptions configures RedisStream.
type RedisStreamOptions struct {
	Addr       string
	Stream     string        // source stream, e.g. logsage:events
	DeadStream string        // dead-letter stream, e.g. logsage:events:dead
	DeadMaxLen int64         // approximate cap on the dead-letter stream
	Group      string        // consumer group; GroupName if empty
	Consumer   string        // unique per process
	Timeout    time.Duration // per non-blocking operation
}

// RedisStream implements Stream with go-redis.
type RedisStream struct {
	c    *redis.Client
	opts RedisStreamOptions
	now  func() time.Time
}

// NewRedisStream returns a RedisStream. It does not connect.
func NewRedisStream(opts RedisStreamOptions) (*RedisStream, error) {
	if opts.Group == "" {
		opts.Group = GroupName
	}
	switch {
	case opts.Addr == "":
		return nil, errors.New("processor: redis address is empty")
	case opts.Stream == "" || opts.DeadStream == "":
		return nil, errors.New("processor: stream names must not be empty")
	case opts.Stream == opts.DeadStream:
		return nil, errors.New("processor: dead-letter stream must differ from the source stream")
	case opts.Consumer == "":
		return nil, errors.New("processor: consumer name is empty")
	case opts.Timeout <= 0 || opts.DeadMaxLen <= 0:
		return nil, errors.New("processor: timeout and dead-letter max length must be positive")
	}
	c := redis.NewClient(&redis.Options{
		Addr:                  opts.Addr,
		DialTimeout:           opts.Timeout,
		ReadTimeout:           opts.Timeout,
		WriteTimeout:          opts.Timeout,
		ContextTimeoutEnabled: true,
	})
	return &RedisStream{c: c, opts: opts, now: time.Now}, nil
}

// Client exposes the underlying client (used by integration tests).
func (r *RedisStream) Client() *redis.Client { return r.c }

// Close releases connections.
func (r *RedisStream) Close() error { return r.c.Close() }

func (r *RedisStream) withTimeout(ctx context.Context, extra time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, r.opts.Timeout+extra)
}

// EnsureGroup runs XGROUP CREATE <stream> <group> 0 MKSTREAM, ignoring
// BUSYGROUP (group already exists).
func (r *RedisStream) EnsureGroup(ctx context.Context) error {
	ctx, cancel := r.withTimeout(ctx, 0)
	defer cancel()
	err := r.c.XGroupCreateMkStream(ctx, r.opts.Stream, r.opts.Group, "0").Err()
	if err != nil && !strings.HasPrefix(err.Error(), "BUSYGROUP") {
		return fmt.Errorf("xgroup create: %w", err)
	}
	return nil
}

// ReadPending reads this consumer's pending entries after ID after.
func (r *RedisStream) ReadPending(ctx context.Context, after string, count int64) ([]Entry, error) {
	ctx, cancel := r.withTimeout(ctx, 0)
	defer cancel()
	return r.read(ctx, after, count, -1)
}

// ReadNew reads never-delivered entries, blocking up to block.
func (r *RedisStream) ReadNew(ctx context.Context, count int64, block time.Duration) ([]Entry, error) {
	ctx, cancel := r.withTimeout(ctx, block)
	defer cancel()
	return r.read(ctx, ">", count, block)
}

func (r *RedisStream) read(ctx context.Context, id string, count int64, block time.Duration) ([]Entry, error) {
	res, err := r.c.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    r.opts.Group,
		Consumer: r.opts.Consumer,
		Streams:  []string{r.opts.Stream, id},
		Count:    count,
		Block:    block, // negative: no BLOCK argument
	}).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("xreadgroup: %w", err)
	}
	var out []Entry
	for _, s := range res {
		for _, m := range s.Messages {
			out = append(out, Entry{ID: m.ID, Values: m.Values})
		}
	}
	return out, nil
}

// Claim runs XAUTOCLAIM for entries idle longer than minIdle.
func (r *RedisStream) Claim(ctx context.Context, minIdle time.Duration, start string, count int64) ([]Entry, string, error) {
	ctx, cancel := r.withTimeout(ctx, 0)
	defer cancel()
	msgs, next, err := r.c.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream:   r.opts.Stream,
		Group:    r.opts.Group,
		Consumer: r.opts.Consumer,
		MinIdle:  minIdle,
		Start:    start,
		Count:    count,
	}).Result()
	if err != nil {
		return nil, "", fmt.Errorf("xautoclaim: %w", err)
	}
	out := make([]Entry, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, Entry{ID: m.ID, Values: m.Values})
	}
	return out, next, nil
}

// Ack runs XACK.
func (r *RedisStream) Ack(ctx context.Context, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	ctx, cancel := r.withTimeout(ctx, 0)
	defer cancel()
	if err := r.c.XAck(ctx, r.opts.Stream, r.opts.Group, ids...).Err(); err != nil {
		return fmt.Errorf("xack: %w", err)
	}
	return nil
}

// DeadLetter appends the original fields plus original_id, reason and
// dead_lettered_at to the dead-letter stream.
func (r *RedisStream) DeadLetter(ctx context.Context, e Entry, reason string) error {
	ctx, cancel := r.withTimeout(ctx, 0)
	defer cancel()
	values := make([]any, 0, 2*len(e.Values)+6)
	for k, v := range e.Values {
		switch k {
		case "original_id", "reason", "dead_lettered_at":
			k = "orig_" + k // never let the original overwrite our metadata
		}
		values = append(values, k, v)
	}
	values = append(values,
		"original_id", e.ID,
		"reason", reason,
		"dead_lettered_at", r.now().UTC().Format(time.RFC3339Nano),
	)
	err := r.c.XAdd(ctx, &redis.XAddArgs{
		Stream: r.opts.DeadStream,
		MaxLen: r.opts.DeadMaxLen,
		Approx: true,
		Values: values,
	}).Err()
	if err != nil {
		return fmt.Errorf("xadd dead-letter: %w", err)
	}
	return nil
}

// Info returns XLEN plus the group's pending count and lag.
func (r *RedisStream) Info(ctx context.Context) (StreamInfo, error) {
	ctx, cancel := r.withTimeout(ctx, 0)
	defer cancel()
	length, err := r.c.XLen(ctx, r.opts.Stream).Result()
	if err != nil {
		return StreamInfo{}, fmt.Errorf("xlen: %w", err)
	}
	groups, err := r.c.XInfoGroups(ctx, r.opts.Stream).Result()
	if err != nil {
		return StreamInfo{}, fmt.Errorf("xinfo groups: %w", err)
	}
	for _, g := range groups {
		if g.Name == r.opts.Group {
			return StreamInfo{Length: length, Pending: g.Pending, Lag: g.Lag}, nil
		}
	}
	return StreamInfo{Length: length, Pending: 0, Lag: length}, nil
}

// Ready pings Redis.
func (r *RedisStream) Ready(ctx context.Context) error {
	ctx, cancel := r.withTimeout(ctx, 0)
	defer cancel()
	return r.c.Ping(ctx).Err()
}
