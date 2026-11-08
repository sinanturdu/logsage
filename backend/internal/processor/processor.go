package processor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/fingerprint"
)

// Stream is the queue side of the processor: a Redis Stream consumer group
// in production, a fake in tests.
type Stream interface {
	// EnsureGroup creates the consumer group (and stream) if missing.
	EnsureGroup(ctx context.Context) error
	// ReadPending returns this consumer's own pending entries after ID after.
	ReadPending(ctx context.Context, after string, count int64) ([]Entry, error)
	// ReadNew returns never-delivered entries, waiting up to block.
	ReadNew(ctx context.Context, count int64, block time.Duration) ([]Entry, error)
	// Claim takes over entries idle longer than minIdle, scanning from start.
	// It returns the cursor for the next call ("0-0" when the scan is done).
	Claim(ctx context.Context, minIdle time.Duration, start string, count int64) ([]Entry, string, error)
	// Ack acknowledges entries so they leave the pending list.
	Ack(ctx context.Context, ids ...string) error
	// DeadLetter copies e and reason to the dead-letter stream.
	DeadLetter(ctx context.Context, e Entry, reason string) error
	// Info reports stream length, group pending count and lag.
	Info(ctx context.Context) (StreamInfo, error)
	Ready(ctx context.Context) error
}

// Store is the storage side: ClickHouse in production.
type Store interface {
	InsertJSONEachRow(ctx context.Context, table string, body []byte) error
	Ready(ctx context.Context) error
}

// StreamInfo is a point-in-time view of the stream and consumer group.
type StreamInfo struct {
	Length  int64 // XLEN
	Pending int64 // delivered but not acknowledged
	Lag     int64 // entries not yet delivered to the group; -1 if unknown
	// OldestUnprocessedAge is the age of the oldest entry this group has not
	// acknowledged (pending or undelivered), from its ID timestamp; 0 when
	// the group is caught up.
	OldestUnprocessedAge time.Duration
}

// Options tunes the processor loop.
type Options struct {
	Table           string // ClickHouse table, e.g. logs
	BatchSize       int
	ReadCount       int
	ReadBlock       time.Duration
	FlushInterval   time.Duration
	ClaimIdle       time.Duration
	ClaimInterval   time.Duration
	RetryInitial    time.Duration
	RetryMax        time.Duration
	ShutdownTimeout time.Duration
	// LagWarnAge: LogStats warns when the oldest unprocessed entry is older.
	// Zero disables the warning.
	LagWarnAge time.Duration
}

func (o Options) validate() error {
	switch {
	case o.Table == "":
		return errors.New("processor: table is empty")
	case o.BatchSize <= 0 || o.ReadCount <= 0:
		return errors.New("processor: batch size and read count must be positive")
	case o.ReadBlock < time.Millisecond:
		return errors.New("processor: read block must be at least 1ms")
	case o.FlushInterval <= 0 || o.ClaimIdle <= 0 || o.ClaimInterval <= 0 || o.ShutdownTimeout <= 0:
		return errors.New("processor: intervals must be positive")
	case o.RetryInitial <= 0 || o.RetryMax < o.RetryInitial:
		return errors.New("processor: retry initial must be positive and not above retry max")
	}
	return nil
}

// ackAttempts bounds retries of XACK after a successful insert. If acking
// keeps failing the entries are redelivered later, which is harmless because
// rows are deduplicated by event_id.
const ackAttempts = 5

// Processor moves entries from a Stream into a Store in batches. Entries are
// acknowledged only after the batch was inserted. Run is single-threaded;
// Stats may be called concurrently.
type Processor struct {
	// fp memoizes message templates (ADR 0022); add runs on one goroutine.
	fp     *fingerprint.Cache
	stream Stream
	store  Store
	opts   Options
	log    *slog.Logger

	sleep  func(ctx context.Context, d time.Duration) error
	jitter func() float64 // in [0, 1)
	now    func() time.Time

	processed     atomic.Int64
	failedBatches atomic.Int64
	deadLettered  atomic.Int64
	trimmed       atomic.Int64

	claimCursor string
	lastClaim   time.Time
}

// New returns a Processor. log may be nil.
func New(stream Stream, store Store, opts Options, log *slog.Logger) (*Processor, error) {
	if stream == nil || store == nil {
		return nil, errors.New("processor: stream and store are required")
	}
	if err := opts.validate(); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Processor{
		fp:          fingerprint.NewCache(fingerprint.DefaultCacheEntries),
		stream:      stream,
		store:       store,
		opts:        opts,
		log:         log,
		sleep:       sleepCtx,
		jitter:      rand.Float64,
		now:         time.Now,
		claimCursor: "0-0",
	}, nil
}

// batch holds entries read but not yet inserted.
type batch struct {
	ids      []string
	rows     []Row
	seen     map[string]struct{}
	deadline time.Time // flush when reached; zero while empty
}

func newBatch() *batch { return &batch{seen: map[string]struct{}{}} }

func (b *batch) len() int { return len(b.rows) }

func (b *batch) reset() {
	b.ids, b.rows, b.seen, b.deadline = nil, nil, map[string]struct{}{}, time.Time{}
}

// Run processes entries until ctx is canceled, then flushes and acks the
// current batch within ShutdownTimeout and returns nil. Entries of a batch
// that could not be flushed stay pending in Redis and are claimed later.
func (p *Processor) Run(ctx context.Context) error {
	if err := p.retry(ctx, "create consumer group", p.stream.EnsureGroup); err != nil {
		return nil // canceled before start
	}

	b := newBatch()
	p.drainOwnPending(ctx, b)

	readFailures := 0
	for ctx.Err() == nil {
		now := p.now()
		if b.len() >= p.opts.BatchSize || (b.len() > 0 && !now.Before(b.deadline)) {
			p.flush(ctx, b)
			continue
		}

		if now.Sub(p.lastClaim) >= p.opts.ClaimInterval {
			p.claim(ctx, b)
			if b.len() >= p.opts.BatchSize {
				continue
			}
		}

		block := p.opts.ReadBlock
		if b.len() > 0 {
			if until := b.deadline.Sub(p.now()); until < block {
				block = until
			}
		}
		if block < time.Millisecond {
			block = time.Millisecond // BLOCK 0 would wait forever
		}
		entries, err := p.stream.ReadNew(ctx, int64(p.room(b)), block)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			p.log.Warn("read from stream failed", "attempt", readFailures+1, "err", err)
			_ = p.sleep(ctx, p.backoff(readFailures))
			readFailures++
			continue
		}
		readFailures = 0
		p.add(ctx, b, entries)
	}

	if b.len() > 0 {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.opts.ShutdownTimeout)
		defer cancel()
		p.log.Info("shutdown: flushing current batch", "rows", b.len())
		p.flush(sctx, b)
		if b.len() > 0 {
			p.log.Warn("shutdown: batch not flushed; entries stay pending and will be claimed", "rows", b.len())
		}
	}
	return nil
}

// drainOwnPending processes entries this consumer received earlier but never
// acknowledged (for example before a crash with a fixed consumer name).
func (p *Processor) drainOwnPending(ctx context.Context, b *batch) {
	cursor := "0"
	failures := 0
	for ctx.Err() == nil {
		if b.len() >= p.opts.BatchSize {
			p.flush(ctx, b)
			continue
		}
		entries, err := p.stream.ReadPending(ctx, cursor, int64(p.room(b)))
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			p.log.Warn("read own pending entries failed", "attempt", failures+1, "err", err)
			_ = p.sleep(ctx, p.backoff(failures))
			failures++
			continue
		}
		if len(entries) == 0 {
			return
		}
		failures = 0
		cursor = entries[len(entries)-1].ID
		p.log.Info("recovering own pending entries", "count", len(entries))
		p.add(ctx, b, entries)
	}
}

// claim takes over one page of entries idle longer than ClaimIdle.
func (p *Processor) claim(ctx context.Context, b *batch) {
	entries, next, err := p.stream.Claim(ctx, p.opts.ClaimIdle, p.claimCursor, int64(p.room(b)))
	if err != nil {
		if ctx.Err() == nil {
			p.log.Warn("claim idle entries failed", "err", err)
		}
		p.lastClaim = p.now()
		return
	}
	if len(entries) > 0 {
		p.log.Info("claimed idle entries", "count", len(entries))
	}
	p.add(ctx, b, entries)
	p.claimCursor = next
	if next == "" || next == "0-0" {
		p.claimCursor = "0-0"
		p.lastClaim = p.now() // scan complete; wait for the next interval
	}
}

// room is how many more entries fit in the batch, capped by ReadCount.
func (p *Processor) room(b *batch) int {
	n := p.opts.BatchSize - b.len()
	if n > p.opts.ReadCount {
		n = p.opts.ReadCount
	}
	if n < 1 {
		n = 1
	}
	return n
}

// add parses entries into the batch. Unusable entries are dead-lettered.
func (p *Processor) add(ctx context.Context, b *batch, entries []Entry) {
	for _, e := range entries {
		if _, dup := b.seen[e.ID]; dup {
			continue // claimed again while still in this batch
		}
		row, reason := parseEntry(e, p.fp)
		if reason != "" {
			p.deadLetter(ctx, e, reason)
			continue
		}
		if b.len() == 0 {
			b.deadline = p.now().Add(p.opts.FlushInterval)
		}
		b.seen[e.ID] = struct{}{}
		b.ids = append(b.ids, e.ID)
		b.rows = append(b.rows, row)
	}
}

// deadLetter copies e to the dead-letter stream and acks it. If either step
// fails the entry stays pending and is retried after it is claimed again.
func (p *Processor) deadLetter(ctx context.Context, e Entry, reason string) {
	if err := p.stream.DeadLetter(ctx, e, reason); err != nil {
		p.log.Warn("dead-letter failed; entry stays pending", "id", e.ID, "reason", reason, "err", err)
		return
	}
	if err := p.stream.Ack(ctx, e.ID); err != nil {
		p.log.Warn("ack of dead-lettered entry failed; it may be dead-lettered again", "id", e.ID, "err", err)
	}
	p.deadLettered.Add(1)
	p.log.Warn("entry dead-lettered", "id", e.ID, "reason", reason)
}

// flush inserts the batch, retrying with backoff until it succeeds or ctx
// ends; nothing new is read meanwhile (backpressure). Entries are acked only
// after a successful insert. On ctx end the batch is kept unacked.
//
// Each attempt is encoded again with inserted_at = the attempt's start time,
// so a batch that becomes visible only after retries is not stamped with the
// time of its first, failed attempt (the live tail relies on it, ADR 0015).
func (p *Processor) flush(ctx context.Context, b *batch) {
	for attempt := 0; ; attempt++ {
		body, err := encodeRows(b.rows, p.now())
		if err != nil {
			// Unreachable for rows produced by parseEntry; keep entries pending.
			p.log.Error("encode batch failed; entries stay pending", "rows", b.len(), "err", err)
			b.reset()
			return
		}
		err = p.store.InsertJSONEachRow(ctx, p.opts.Table, body)
		if err == nil {
			break
		}
		p.failedBatches.Add(1)
		if ctx.Err() != nil {
			return
		}
		wait := p.backoff(attempt)
		p.log.Warn("insert batch failed; retrying, not reading new entries", "rows", b.len(), "attempt", attempt+1, "retry_in", wait.String(), "err", err)
		if p.sleep(ctx, wait) != nil {
			return
		}
	}

	if err := p.ackWithRetry(ctx, b.ids); err != nil {
		p.log.Error("ack after insert failed; entries will be redelivered and deduplicated by event_id", "rows", b.len(), "err", err)
	}
	p.processed.Add(int64(b.len()))
	p.log.Debug("batch flushed", "rows", b.len())
	b.reset()
}

func (p *Processor) ackWithRetry(ctx context.Context, ids []string) error {
	var err error
	for attempt := 0; attempt < ackAttempts; attempt++ {
		if err = p.stream.Ack(ctx, ids...); err == nil {
			return nil
		}
		if p.sleep(ctx, p.backoff(attempt)) != nil {
			break
		}
	}
	return err
}

// retry calls fn until it succeeds or ctx ends.
func (p *Processor) retry(ctx context.Context, what string, fn func(context.Context) error) error {
	for attempt := 0; ; attempt++ {
		err := fn(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		p.log.Warn(what+" failed; retrying", "attempt", attempt+1, "err", err)
		if err := p.sleep(ctx, p.backoff(attempt)); err != nil {
			return err
		}
	}
}

// backoff returns the wait before retry number attempt (0-based):
// exponential from RetryInitial, capped at RetryMax, with equal jitter
// (a uniform value in [d/2, d)).
func (p *Processor) backoff(attempt int) time.Duration {
	d := p.opts.RetryInitial
	for i := 0; i < attempt && d < p.opts.RetryMax; i++ {
		d *= 2
	}
	if d > p.opts.RetryMax {
		d = p.opts.RetryMax
	}
	half := d / 2
	return half + time.Duration(p.jitter()*float64(d-half))
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Stats is the processor state reported on /stats and in the periodic log.
type Stats struct {
	StreamLength *int64 `json:"stream_length"`
	GroupPending *int64 `json:"group_pending"`
	GroupLag     *int64 `json:"group_lag"`
	// OldestUnprocessedAgeS is the age in seconds of the oldest entry the
	// group has not acknowledged; 0 when caught up, null if Redis is down.
	OldestUnprocessedAgeS *float64 `json:"oldest_unprocessed_age_s"`
	StreamError           string   `json:"stream_error,omitempty"`
	Processed             int64    `json:"processed_total"`
	FailedBatches         int64    `json:"failed_batches_total"`
	DeadLettered          int64    `json:"dead_lettered_total"`
	Trimmed               int64    `json:"trimmed_total"`
}

// Stats returns counters and, when Redis answers, stream figures.
func (p *Processor) Stats(ctx context.Context) Stats {
	s := Stats{
		Processed:     p.processed.Load(),
		FailedBatches: p.failedBatches.Load(),
		DeadLettered:  p.deadLettered.Load(),
		Trimmed:       p.trimmed.Load(),
	}
	info, err := p.stream.Info(ctx)
	if err != nil {
		s.StreamError = "stream unavailable"
		p.log.Debug("stream info failed", "err", err)
		return s
	}
	age := math.Round(info.OldestUnprocessedAge.Seconds()*10) / 10
	s.StreamLength, s.GroupPending, s.GroupLag, s.OldestUnprocessedAgeS = &info.Length, &info.Pending, &info.Lag, &age
	return s
}

// LogStats logs Stats at info level every interval until ctx ends.
func (p *Processor) LogStats(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.logStatsOnce(ctx)
		}
	}
}

// logStatsOnce logs the stats at info level and warns when the oldest
// unprocessed entry is older than LagWarnAge.
func (p *Processor) logStatsOnce(ctx context.Context) {
	s := p.Stats(ctx)
	attrs := []any{"processed_total", s.Processed, "failed_batches_total", s.FailedBatches,
		"dead_lettered_total", s.DeadLettered, "trimmed_total", s.Trimmed}
	if s.StreamError != "" {
		p.log.Info("processor stats", append(attrs, "stream_error", s.StreamError)...)
		return
	}
	attrs = append(attrs, "stream_length", *s.StreamLength, "group_pending", *s.GroupPending,
		"group_lag", *s.GroupLag, "oldest_unprocessed_age_s", *s.OldestUnprocessedAgeS)
	p.log.Info("processor stats", attrs...)
	if w := p.opts.LagWarnAge; w > 0 && *s.OldestUnprocessedAgeS > w.Seconds() {
		p.log.Warn("processor is falling behind: oldest unprocessed entry is older than the threshold",
			"oldest_unprocessed_age_s", *s.OldestUnprocessedAgeS, "threshold_s", w.Seconds(),
			"group_pending", *s.GroupPending, "group_lag", *s.GroupLag)
	}
}

// String describes the options without secrets, for startup logs.
func (o Options) String() string {
	return fmt.Sprintf("batch=%d read=%d block=%s flush=%s claim_idle=%s claim_every=%s retry=%s..%s",
		o.BatchSize, o.ReadCount, o.ReadBlock, o.FlushInterval, o.ClaimIdle, o.ClaimInterval, o.RetryInitial, o.RetryMax)
}
