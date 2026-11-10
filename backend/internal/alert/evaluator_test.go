package alert

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/query"
	"github.com/sinanturdu/logsage/backend/internal/storage/clickhouse"
)

// memStore is an in-memory RuleStore with ApplyOutcome's checks.
type memStore struct {
	mu       sync.Mutex
	rules    map[string]Rule
	states   map[string]map[string]State
	events   []Transition
	notes    []NewNotification
	channels []Channel
	pruned   int
}

func newMem(rules ...Rule) *memStore {
	m := &memStore{rules: map[string]Rule{}, states: map[string]map[string]State{}, channels: []Channel{{ID: "c1", Name: "ops"}}}
	for _, r := range rules {
		m.rules[r.ID] = r
	}
	return m
}

func (m *memStore) DueRules(_ context.Context, now time.Time, limit int) ([]Rule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Rule
	for _, r := range m.rules {
		if r.Enabled && !r.NextEvalAt.After(now) && len(out) < limit {
			out = append(out, r)
		}
	}
	return out, nil
}
func (m *memStore) RuleStates(_ context.Context, id string) (map[string]State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]State{}
	for k, v := range m.states[id] {
		out[k] = v
	}
	return out, nil
}
func (m *memStore) RuleChannels(context.Context, string) ([]Channel, error) { return m.channels, nil }
func (m *memStore) ResolvedGroups(context.Context, string) ([]ResolvedGroup, error) {
	return nil, nil
}
func (m *memStore) PruneAlerts(context.Context, time.Time) error { m.pruned++; return nil }
func (m *memStore) ApplyOutcome(_ context.Context, r Rule, _ Window, out Outcome, notes []NewNotification, next time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.rules[r.ID]
	if !ok || cur.Version != r.Version || !cur.Enabled {
		return ErrStale
	}
	if m.states[r.ID] == nil {
		m.states[r.ID] = map[string]State{}
	}
	for _, s := range out.States {
		if m.states[r.ID][s.DedupKey].Version != s.Version {
			return ErrStale
		}
	}
	for _, s := range out.States {
		s.Version++
		m.states[r.ID][s.DedupKey] = s
	}
	cur.NextEvalAt = next
	if !out.EvaluatedAt.IsZero() {
		t := out.EvaluatedAt
		cur.Summary.LastEvaluatedAt = &t
	}
	cur.Summary.LastEvalError = out.EvalError
	m.rules[r.ID] = cur
	m.events = append(m.events, out.Transitions...)
	m.notes = append(m.notes, notes...)
	return nil
}

type memLock struct {
	mu   *sync.Mutex
	held *bool
}

func (l memLock) Alive(context.Context) bool { return true }
func (l memLock) Release(context.Context)    { l.mu.Lock(); *l.held = false; l.mu.Unlock() }

// sharedLock behaves like pg_try_advisory_lock across instances.
func sharedLock() Locker {
	var mu sync.Mutex
	held := false
	return func(context.Context) (Lock, error) {
		mu.Lock()
		defer mu.Unlock()
		if held {
			return nil, nil
		}
		held = true
		return memLock{&mu, &held}, nil
	}
}

func errorRule(id string) Rule {
	r := Rule{ID: id, ProjectID: "p1", ProjectSlug: "demo", Name: "errors", Type: ErrorRate, Params: Params{ThresholdPercent: 5},
		Interval: time.Minute, Window: 5 * time.Minute, Enabled: true, Version: 1, NextEvalAt: t0}
	r.Normalize()
	return r
}

func TestEvaluatorFiresOnceAndSchedules(t *testing.T) {
	store := newMem(errorRule("r1"))
	q := &fakeQ{total: 1000, errs: 100}
	clock := &fakeClock{now: t0}
	e := NewEvaluator(store, q, clock, sharedLock(), EvalOptions{Lateness: time.Minute}, nil)
	e.Tick(context.Background())
	if len(store.notes) != 1 || store.notes[0].Event != EventFiring || store.rules["r1"].NextEvalAt != t0.Add(time.Minute) {
		t.Fatalf("notes %d, next %v", len(store.notes), store.rules["r1"].NextEvalAt)
	}
	// Not due yet: nothing happens.
	e.Tick(context.Background())
	if e.Stats().RulesEvaluated != 1 {
		t.Fatal("evaluated before it was due")
	}
	clock.add(time.Minute)
	e.Tick(context.Background())
	if len(store.notes) != 1 || e.Stats().RulesEvaluated != 2 {
		t.Fatalf("still firing must not notify again: %d", len(store.notes))
	}
	// The value drops past the band: clear for clear_for (one interval), then resolved.
	q.errs = 10
	clock.add(time.Minute)
	e.Tick(context.Background())
	if len(store.notes) != 1 {
		t.Fatal("resolved before clear_for")
	}
	clock.add(time.Minute)
	e.Tick(context.Background())
	if len(store.notes) != 2 || store.notes[1].Event != EventResolved {
		t.Fatalf("resolve: %+v", store.notes)
	}
	if !e.Stats().HoldsLock || store.pruned != 1 {
		t.Fatalf("stats %+v pruned %d", e.Stats(), store.pruned)
	}
}

func TestQueryErrorOnlyAdvancesTheSchedule(t *testing.T) {
	store := newMem(errorRule("r1"))
	clock := &fakeClock{now: t0}
	e := NewEvaluator(store, &fakeQ{err: errors.New("clickhouse down")}, clock, sharedLock(), EvalOptions{}, nil)
	e.Tick(context.Background())
	if len(store.events) != 0 || len(store.notes) != 0 || e.Stats().EvalErrors != 1 || store.rules["r1"].NextEvalAt != t0.Add(time.Minute) {
		t.Fatalf("events %d notes %d stats %+v", len(store.events), len(store.notes), e.Stats())
	}
	// The heartbeat: no successful evaluation yet, and the failure's category (never its text).
	if s := store.rules["r1"].Summary; s.LastEvaluatedAt != nil || s.LastEvalError != EvalStoreUnavailable {
		t.Fatalf("heartbeat %+v", s)
	}
}

func TestHeartbeatRecordsTheLastSuccessfulEvaluation(t *testing.T) {
	store := newMem(errorRule("r1"))
	q := &fakeQ{total: 1000, errs: 1}
	clock := &fakeClock{now: t0}
	e := NewEvaluator(store, q, clock, sharedLock(), EvalOptions{}, nil)
	e.Tick(context.Background())
	if s := store.rules["r1"].Summary; s.LastEvaluatedAt == nil || !s.LastEvaluatedAt.Equal(t0) || s.LastEvalError != "" {
		t.Fatalf("heartbeat %+v", s)
	}
	// A failed attempt keeps the last success and records the category.
	q.err = context.DeadlineExceeded
	clock.add(time.Minute)
	e.Tick(context.Background())
	if s := store.rules["r1"].Summary; !s.LastEvaluatedAt.Equal(t0) || s.LastEvalError != EvalQueryTimeout {
		t.Fatalf("after a timeout %+v", s)
	}
	q.err = nil
	clock.add(time.Minute)
	e.Tick(context.Background())
	if s := store.rules["r1"].Summary; !s.LastEvaluatedAt.Equal(t0.Add(2*time.Minute)) || s.LastEvalError != "" {
		t.Fatalf("after recovery %+v", s)
	}
}

func TestEvalErrorCategories(t *testing.T) {
	for err, want := range map[error]string{
		context.DeadlineExceeded:                    EvalQueryTimeout,
		&clickhouse.Error{Status: 500, Code: "159"}: EvalQueryTimeout,
		&clickhouse.Error{Status: 400, Code: "62"}:  EvalQueryRejected,
		errors.New("dial tcp: connection refused"):  EvalStoreUnavailable,
		query.ErrGroupNotFound:                      EvalUnknown,
	} {
		if got := EvalErrorCategory(err); got != want {
			t.Errorf("EvalErrorCategory(%v) = %s, want %s", err, got, want)
		}
	}
}

// A second instance stays on standby; when both evaluate the same rule the
// optimistic check lets only one write.
func TestSecondInstanceNeverDoubleFires(t *testing.T) {
	store := newMem(errorRule("r1"))
	q := &fakeQ{total: 1000, errs: 100}
	lock := sharedLock()
	clock := &fakeClock{now: t0}
	a := NewEvaluator(store, q, clock, lock, EvalOptions{}, nil)
	b := NewEvaluator(store, q, clock, lock, EvalOptions{}, nil)
	a.Tick(context.Background())
	b.Tick(context.Background())
	if b.Stats().HoldsLock || b.Stats().Cycles != 0 || len(store.notes) != 1 {
		t.Fatalf("standby ran: %+v, notes %d", b.Stats(), len(store.notes))
	}
	// Even without the lock (a split brain), the version check stops a double write.
	r := store.rules["r1"]
	r.NextEvalAt = t0
	store.rules["r1"] = r
	stale := map[string]State{} // what a second writer read before the first wrote
	out := Step(r, stale, []Observation{{Key: RuleKey, Fire: true}}, t0)
	if err := store.ApplyOutcome(context.Background(), r, Window{}, out, nil, t0); !errors.Is(err, ErrStale) {
		t.Fatalf("double write: %v", err)
	}
	if len(store.notes) != 1 {
		t.Fatalf("notes %d", len(store.notes))
	}
	// When the holder stops, the standby takes over.
	ctx, cancel := context.WithCancel(context.Background())
	tick := make(chan time.Time)
	done := make(chan struct{})
	go func() { a.Run(ctx, tick); close(done) }()
	cancel()
	<-done
	b.Tick(context.Background())
	if !b.Stats().HoldsLock {
		t.Fatal("standby did not take over")
	}
}

func TestEditedRuleIsStale(t *testing.T) {
	store := newMem(errorRule("r1"))
	clock := &fakeClock{now: t0}
	e := NewEvaluator(store, &fakeQ{total: 1000, errs: 100}, clock, sharedLock(), EvalOptions{}, nil)
	r := store.rules["r1"]
	edited := r
	edited.Version = 2
	store.rules["r1"] = edited
	if err := e.Evaluate(context.Background(), r, t0); err != nil {
		t.Fatal(err)
	}
	if e.Stats().StaleWrites != 1 || len(store.notes) != 0 {
		t.Fatalf("%+v", e.Stats())
	}
}

func TestCycleBudget(t *testing.T) {
	var rules []Rule
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		rules = append(rules, errorRule(id))
	}
	store := newMem(rules...)
	clock := &fakeClock{now: t0}
	e := NewEvaluator(store, &fakeQ{total: 10}, clock, sharedLock(), EvalOptions{QueriesPerCycle: 2, Concurrency: 1}, nil)
	n, _ := e.Cycle(context.Background())
	if n != 2 {
		t.Fatalf("cycle evaluated %d, budget 2", n)
	}
	n, _ = e.Cycle(context.Background())
	m, _ := e.Cycle(context.Background())
	if n != 2 || m != 1 {
		t.Fatalf("the rest waits for later cycles: %d %d", n, m)
	}
}
