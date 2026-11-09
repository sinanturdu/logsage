package alert

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// RuleStore is what the evaluator needs from storage.
type RuleStore interface {
	DueRules(ctx context.Context, now time.Time, limit int) ([]Rule, error)
	RuleStates(ctx context.Context, ruleID string) (map[string]State, error)
	RuleChannels(ctx context.Context, ruleID string) ([]Channel, error)
	ApplyOutcome(ctx context.Context, r Rule, win Window, out Outcome, notes []NewNotification, next time.Time) error
	ResolvedGroups(ctx context.Context, projectID string) ([]ResolvedGroup, error)
	PruneAlerts(ctx context.Context, before time.Time) error
}

// Lock is the single-writer lock (a PostgreSQL advisory lock).
type Lock interface {
	Alive(ctx context.Context) bool
	Release(ctx context.Context)
}

// Locker takes the lock if nobody holds it; nil, nil when another
// instance has it.
type Locker func(ctx context.Context) (Lock, error)

// EvalOptions bound evaluation.
type EvalOptions struct {
	QueriesPerCycle int           // rules per cycle (200)
	Concurrency     int           // rules evaluated at a time (2)
	Lateness        time.Duration // windows end this long before now (60 s)
	Retention       time.Duration // events and finished deliveries kept (30 days)
	DashboardURL    string
}

// Stats are the evaluator's counters for /stats.
type Stats struct {
	HoldsLock        bool    `json:"holds_lock"`
	Cycles           int64   `json:"cycles"`
	RulesEvaluated   int64   `json:"rules_evaluated"`
	EvalErrors       int64   `json:"eval_errors"`
	StaleWrites      int64   `json:"stale_writes"`
	Notifications    int64   `json:"notifications_queued"`
	LastCycleSeconds float64 `json:"last_cycle_seconds"`
	LastCycleRules   int64   `json:"last_cycle_rules"`
}

// Evaluator runs due rules: it observes, steps the state machine and
// writes the outcome with its notifications, one rule per transaction.
type Evaluator struct {
	store RuleStore
	q     Querier
	clock Clock
	lock  Locker
	opts  EvalOptions
	log   *slog.Logger

	held      Lock
	lastPrune time.Time
	holds     atomic.Bool
	cycles    atomic.Int64
	evaluated atomic.Int64
	errs      atomic.Int64
	stale     atomic.Int64
	queued    atomic.Int64
	lastSecs  atomic.Int64 // milliseconds
	lastRules atomic.Int64
}

// NewEvaluator returns an evaluator.
func NewEvaluator(store RuleStore, q Querier, clock Clock, lock Locker, opts EvalOptions, log *slog.Logger) *Evaluator {
	if opts.QueriesPerCycle <= 0 {
		opts.QueriesPerCycle = 200
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 2
	}
	if opts.Retention <= 0 {
		opts.Retention = 30 * 24 * time.Hour
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Evaluator{store: store, q: q, clock: clock, lock: lock, opts: opts, log: log}
}

// Stats returns the current counters.
func (e *Evaluator) Stats() Stats {
	return Stats{
		HoldsLock: e.holds.Load(), Cycles: e.cycles.Load(), RulesEvaluated: e.evaluated.Load(), EvalErrors: e.errs.Load(),
		StaleWrites: e.stale.Load(), Notifications: e.queued.Load(), LastCycleSeconds: float64(e.lastSecs.Load()) / 1000,
		LastCycleRules: e.lastRules.Load(),
	}
}

// Evaluate runs one rule at now. A query error changes nothing but the
// rule's next evaluation time.
func (e *Evaluator) Evaluate(ctx context.Context, r Rule, now time.Time) error {
	win := WindowAt(r, now, e.opts.Lateness)
	next := now.Add(r.Interval)
	obs, err := Observe(ctx, r, e.q, e.store, win)
	if err != nil {
		e.errs.Add(1)
		e.log.Warn("alert rule evaluation failed", "rule", r.ID, "project", r.ProjectSlug, "err", err)
		return e.apply(ctx, r, win, Outcome{}, nil, next)
	}
	prev, err := e.store.RuleStates(ctx, r.ID)
	if err != nil {
		return err
	}
	out := Step(r, prev, obs, now)
	var notes []NewNotification
	if len(out.Notices) > 0 {
		channels, err := e.store.RuleChannels(ctx, r.ID)
		if err != nil {
			return err
		}
		for _, n := range out.Notices {
			ns, err := Notifications(r, win, n, channels, "", e.opts.DashboardURL, now)
			if err != nil {
				return err
			}
			notes = append(notes, ns...)
		}
	}
	if err := e.apply(ctx, r, win, out, notes, next); err != nil {
		return err
	}
	e.evaluated.Add(1)
	e.queued.Add(int64(len(notes)))
	return nil
}

func (e *Evaluator) apply(ctx context.Context, r Rule, win Window, out Outcome, notes []NewNotification, next time.Time) error {
	err := e.store.ApplyOutcome(ctx, r, win, out, notes, next)
	if errors.Is(err, ErrStale) {
		e.stale.Add(1) // edited, disabled or written by another instance: the next cycle starts over
		return nil
	}
	return err
}

// Cycle evaluates the due rules (at most QueriesPerCycle, Concurrency at a
// time). It returns how many it evaluated.
func (e *Evaluator) Cycle(ctx context.Context) (int, error) {
	start := e.clock.Now()
	rules, err := e.store.DueRules(ctx, start, e.opts.QueriesPerCycle)
	if err != nil {
		return 0, err
	}
	sem := make(chan struct{}, e.opts.Concurrency)
	var wg sync.WaitGroup
	for _, r := range rules {
		wg.Add(1)
		sem <- struct{}{}
		go func(r Rule) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := e.Evaluate(ctx, r, e.clock.Now()); err != nil && ctx.Err() == nil {
				e.errs.Add(1)
				e.log.Warn("alert rule write failed", "rule", r.ID, "err", err)
			}
		}(r)
	}
	wg.Wait()
	e.cycles.Add(1)
	e.lastRules.Store(int64(len(rules)))
	e.lastSecs.Store(e.clock.Now().Sub(start).Milliseconds())
	return len(rules), nil
}

// Tick takes or checks the lock and, when this instance holds it, runs a
// cycle and the hourly retention. A standby instance does nothing.
func (e *Evaluator) Tick(ctx context.Context) {
	if e.held != nil && !e.held.Alive(ctx) {
		e.held.Release(ctx)
		e.held = nil
		e.holds.Store(false)
		e.log.Warn("alert evaluator lost its lock; trying again")
	}
	if e.held == nil {
		l, err := e.lock(ctx)
		if err != nil {
			e.log.Warn("alert evaluator lock failed", "err", err)
			return
		}
		if l == nil {
			return // another instance evaluates
		}
		e.held = l
		e.holds.Store(true)
		e.log.Info("alert evaluator holds the lock")
	}
	if _, err := e.Cycle(ctx); err != nil && ctx.Err() == nil {
		e.log.Warn("alert cycle failed", "err", err)
	}
	if now := e.clock.Now(); now.Sub(e.lastPrune) >= time.Hour {
		e.lastPrune = now
		if err := e.store.PruneAlerts(ctx, now.Add(-e.opts.Retention)); err != nil && ctx.Err() == nil {
			e.log.Warn("alert retention failed", "err", err)
		}
	}
}

// Run ticks until ctx ends, then releases the lock.
func (e *Evaluator) Run(ctx context.Context, every <-chan time.Time) {
	defer func() {
		if e.held != nil {
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			e.held.Release(rctx)
			cancel()
			e.holds.Store(false)
		}
	}()
	for {
		e.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-every:
		}
	}
}
