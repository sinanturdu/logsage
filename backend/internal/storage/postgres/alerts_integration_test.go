package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/alert"
)

type alertFixture struct {
	st      *Store
	project Project
	user    string
	channel alert.Channel
}

func newAlertFixture(t *testing.T) (alertFixture, func(string) int) {
	pool, st := migrated(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "owner@example.com", fakePHC)
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateProjectWithOwner(ctx, "Shop", "shop", u.ID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := NewID()
	ch, err := st.CreateChannel(ctx, alert.SealedChannel{Channel: alert.Channel{ID: id, Name: "ops", Kind: alert.KindWebhook, URLHost: "https://hooks.example.com"},
		ProjectID: p.ID, URL: []byte("sealed-url"), Secret: []byte("sealed-secret")}, u.ID, AuditEntry{UserID: u.ID, Action: "channel.create", Target: "shop/ops"})
	if err != nil {
		t.Fatal(err)
	}
	count := func(q string) int {
		var n int
		if err := pool.QueryRow(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	return alertFixture{st: st, project: p, user: u.ID, channel: ch}, count
}

func (f alertFixture) rule(t *testing.T, name string) alert.Rule {
	t.Helper()
	r := alert.Rule{ProjectID: f.project.ID, Name: name, Type: alert.ErrorRate, Params: alert.Params{ThresholdPercent: 5},
		Interval: time.Minute, Window: 5 * time.Minute, Enabled: true, Channels: []string{f.channel.ID}}
	r.Normalize()
	if err := alert.Validate(r); err != nil {
		t.Fatal(err)
	}
	out, err := f.st.CreateRule(context.Background(), r, f.user, AuditEntry{UserID: f.user, Action: "alert_rule.create", Target: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAlertRulesAndChannels(t *testing.T) {
	f, count := newAlertFixture(t)
	ctx := context.Background()
	r := f.rule(t, "errors")
	if r.ID == "" || r.ProjectSlug != "shop" || len(r.Channels) != 1 || r.Params.MinLogs != 50 || r.Version != 1 || r.ClearFor != time.Minute {
		t.Fatalf("created %+v", r)
	}
	if _, err := f.st.CreateRule(ctx, r, f.user, AuditEntry{Action: "x"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	bad := r
	bad.Name, bad.Channels = "other", []string{"00000000-0000-0000-0000-000000000000"}
	if _, err := f.st.CreateRule(ctx, bad, f.user, AuditEntry{Action: "x"}); !errors.Is(err, ErrBadChannel) {
		t.Fatalf("foreign channel: %v", err)
	}
	// Channel listing never has the sealed fields; the sealed read does.
	chs, _ := f.st.ListChannels(ctx, f.project.ID)
	if len(chs) != 1 || chs[0].URLHost != "https://hooks.example.com" {
		t.Fatalf("channels %+v", chs)
	}
	sealed, err := f.st.SealedChannel(ctx, f.project.ID, f.channel.ID)
	if err != nil || string(sealed.URL) != "sealed-url" || string(sealed.Secret) != "sealed-secret" {
		t.Fatalf("sealed %+v %v", sealed, err)
	}
	if _, err := f.st.SealedChannel(ctx, "00000000-0000-0000-0000-000000000001", f.channel.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other project reads the channel: %v", err)
	}
	// Limits: 50 rules and 10 channels per project.
	for i := 1; i < alert.MaxRulesPerProject; i++ {
		f.rule(t, fmt.Sprintf("r%02d", i))
	}
	extra := r
	extra.Name = "one too many"
	if _, err := f.st.CreateRule(ctx, extra, f.user, AuditEntry{Action: "x"}); !errors.Is(err, ErrLimit) {
		t.Fatalf("rule limit: %v", err)
	}
	for i := 1; i < alert.MaxChannelsPerProject; i++ {
		id, _ := NewID()
		if _, err := f.st.CreateChannel(ctx, alert.SealedChannel{Channel: alert.Channel{ID: id, Name: fmt.Sprintf("c%d", i), Kind: alert.KindSlack, URLHost: "https://h"},
			ProjectID: f.project.ID, URL: []byte("u")}, f.user, AuditEntry{Action: "channel.create"}); err != nil {
			t.Fatal(err)
		}
	}
	id, _ := NewID()
	if _, err := f.st.CreateChannel(ctx, alert.SealedChannel{Channel: alert.Channel{ID: id, Name: "c11", Kind: alert.KindSlack, URLHost: "https://h"},
		ProjectID: f.project.ID, URL: []byte("u")}, f.user, AuditEntry{Action: "channel.create"}); !errors.Is(err, ErrLimit) {
		t.Fatalf("channel limit: %v", err)
	}
	// One audit row per successful create: 50 rules and 10 channels.
	if n := count("SELECT count(*) FROM audit_log WHERE action IN ('alert_rule.create', 'channel.create')"); n != alert.MaxRulesPerProject+alert.MaxChannelsPerProject {
		t.Fatalf("audit rows %d", n)
	}
}

func TestApplyOutcomeAndEdits(t *testing.T) {
	f, count := newAlertFixture(t)
	ctx := context.Background()
	r := f.rule(t, "errors")
	now := time.Now().UTC().Truncate(time.Millisecond)
	win := alert.Window{From: now.Add(-5 * time.Minute), To: now}
	fire := alert.Step(r, nil, []alert.Observation{{Key: alert.RuleKey, Value: 12, Threshold: 5, Fire: true}}, now)
	notes := []alert.NewNotification{{ChannelID: f.channel.ID, ChannelName: "ops", IdempotencyKey: "k1", Event: alert.EventFiring, Payload: []byte(`{"version":1}`), Transition: 0}}
	if err := f.st.ApplyOutcome(ctx, r, win, fire, notes, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// The same notification key again is ignored (idempotent insert).
	if err := f.st.ApplyOutcome(ctx, r, win, alert.Outcome{}, notes, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n := count("SELECT count(*) FROM alert_notifications"); n != 1 {
		t.Fatalf("%d notifications", n)
	}
	states, _ := f.st.RuleStates(ctx, r.ID)
	if s := states[alert.RuleKey]; s.State != alert.StateFiring || s.Version != 1 || s.FiredAt == nil {
		t.Fatalf("state %+v", s)
	}
	// A writer with an old state version is refused and writes nothing.
	stale := alert.Outcome{States: []alert.State{{DedupKey: alert.RuleKey, State: alert.StateResolved, Since: now, Version: 7}},
		Transitions: []alert.Transition{{Key: alert.RuleKey, From: alert.StateFiring, To: alert.StateResolved}}}
	if err := f.st.ApplyOutcome(ctx, r, win, stale, nil, now); !errors.Is(err, alert.ErrStale) {
		t.Fatalf("stale state: %v", err)
	}
	// A second writer that also thinks the state is new is refused too.
	if err := f.st.ApplyOutcome(ctx, r, win, fire, nil, now); !errors.Is(err, alert.ErrStale) {
		t.Fatalf("double insert: %v", err)
	}
	if n := count("SELECT count(*) FROM alert_events"); n != 1 {
		t.Fatalf("%d events after refused writes", n)
	}
	// Editing what the rule measures closes the state without a notification.
	edited := r
	edited.Params.ThresholdPercent = 10
	r2, err := f.st.UpdateRule(ctx, edited, AuditEntry{Action: "alert_rule.update"})
	if err != nil || r2.Version != 2 {
		t.Fatalf("update %+v %v", r2, err)
	}
	if states, _ := f.st.RuleStates(ctx, r.ID); len(states) != 0 {
		t.Fatalf("states kept after a change: %v", states)
	}
	if n := count("SELECT count(*) FROM alert_events WHERE reason = 'rule changed' AND to_state = 'ok'"); n != 1 {
		t.Fatalf("%d change events", n)
	}
	// The evaluator still holding version 1 is refused.
	if err := f.st.ApplyOutcome(ctx, r, win, alert.Outcome{}, nil, now); !errors.Is(err, alert.ErrStale) {
		t.Fatalf("old version: %v", err)
	}
	// Renaming keeps the version.
	renamed := r2
	renamed.Name = "renamed"
	if r3, _ := f.st.UpdateRule(ctx, renamed, AuditEntry{Action: "alert_rule.update"}); r3.Version != 2 || r3.Name != "renamed" {
		t.Fatalf("rename %+v", r3)
	}
	// Disable while firing: closed, pending deliveries cancelled.
	if err := f.st.ApplyOutcome(ctx, r2, win, alert.Step(r2, nil, []alert.Observation{{Key: alert.RuleKey, Fire: true}}, now),
		[]alert.NewNotification{{ChannelID: f.channel.ID, ChannelName: "ops", IdempotencyKey: "k2", Event: alert.EventFiring, Payload: []byte(`{}`), Transition: 0}}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.SetRuleEnabled(ctx, f.project.ID, r.ID, false, AuditEntry{Action: "alert_rule.disable"}); err != nil {
		t.Fatal(err)
	}
	if n := count("SELECT count(*) FROM alert_notifications WHERE idempotency_key = 'k2' AND status = 'cancelled'"); n != 1 {
		t.Fatal("pending delivery not cancelled on disable")
	}
	due, _ := f.st.DueRules(ctx, now.Add(time.Hour), 10)
	if len(due) != 0 {
		t.Fatalf("a disabled rule is due: %v", due)
	}
	// Delete keeps the events.
	if err := f.st.DeleteRule(ctx, f.project.ID, r.ID, AuditEntry{Action: "alert_rule.delete"}); err != nil {
		t.Fatal(err)
	}
	if n := count("SELECT count(*) FROM alert_events WHERE rule_id IS NULL"); n < 3 {
		t.Fatalf("events lost on delete: %d", n)
	}
}

func TestOutboxClaimsAreExclusive(t *testing.T) {
	f, count := newAlertFixture(t)
	ctx := context.Background()
	r := f.rule(t, "errors")
	now := time.Now().UTC()
	var notes []alert.NewNotification
	for i := 0; i < 40; i++ {
		notes = append(notes, alert.NewNotification{ChannelID: f.channel.ID, ChannelName: "ops", IdempotencyKey: fmt.Sprintf("k%d", i), Event: alert.EventFiring, Payload: []byte(`{}`), Transition: -1})
	}
	if err := f.st.ApplyOutcome(ctx, r, alert.Window{From: now.Add(-time.Minute), To: now}, alert.Outcome{}, notes, now); err != nil {
		t.Fatal(err)
	}
	// Two workers claim concurrently: every row is taken exactly once.
	var mu sync.Mutex
	seen := map[int64]string{}
	var wg sync.WaitGroup
	for _, w := range []string{"w1", "w2"} {
		wg.Add(1)
		go func(w string) {
			defer wg.Done()
			for {
				got, err := f.st.ClaimNotifications(ctx, w, 3, now.Add(time.Second), time.Minute)
				if err != nil {
					t.Error(err)
					return
				}
				if len(got) == 0 {
					return
				}
				mu.Lock()
				for _, c := range got {
					if prev, dup := seen[c.ID]; dup {
						t.Errorf("row %d claimed by %s and %s", c.ID, prev, w)
					}
					seen[c.ID] = w
					if string(c.Channel.URL) != "sealed-url" || c.Channel.Kind != alert.KindWebhook {
						t.Errorf("claim without its channel: %+v", c.Channel)
					}
				}
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	if len(seen) != 40 {
		t.Fatalf("claimed %d of 40", len(seen))
	}
	// A finished row, a released row and an expired lease.
	var any int64
	for id := range seen {
		any = id
		break
	}
	code := 204
	if err := f.st.FinishNotification(ctx, any, seen[any], DeliveryResult{Status: alert.OutSent, ResponseStatus: &code, NextAttemptAt: now, CountAttempt: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.FinishNotification(ctx, any, "other-worker", DeliveryResult{Status: alert.OutFailed, NextAttemptAt: now}); err != nil {
		t.Fatal(err)
	}
	if n := count("SELECT count(*) FROM alert_notifications WHERE status = 'sent' AND attempts = 1 AND response_status = 204"); n != 1 {
		t.Fatalf("finish: %d", n)
	}
	if err := f.st.ReleaseClaims(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	moved, err := f.st.RequeueExpired(ctx, now.Add(2*time.Minute))
	if err != nil || count("SELECT count(*) FROM alert_notifications WHERE status = 'pending'") != 39 {
		t.Fatalf("requeue %d %v, pending %d", moved, err, count("SELECT count(*) FROM alert_notifications WHERE status = 'pending'"))
	}
	// Deleting the channel cancels its pending rows.
	if err := f.st.DeleteChannel(ctx, f.project.ID, f.channel.ID, AuditEntry{Action: "channel.delete"}); err != nil {
		t.Fatal(err)
	}
	if n := count("SELECT count(*) FROM alert_notifications WHERE status = 'cancelled'"); n != 39 {
		t.Fatalf("cancelled %d", n)
	}
}

func TestTestSendLimitAndLock(t *testing.T) {
	f, _ := newAlertFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for i := 0; i < TestSendLimit; i++ {
		if _, err := f.st.QueueTestNotification(ctx, f.project.ID, f.channel, fmt.Sprintf("t%d", i), []byte(`{}`), now, AuditEntry{Action: "channel.test"}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := f.st.QueueTestNotification(ctx, f.project.ID, f.channel, "t9", []byte(`{}`), now.Add(time.Minute), AuditEntry{Action: "channel.test"})
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter != 9*time.Minute {
		t.Fatalf("limit: %v", err)
	}
	// One evaluator holds the lock; a second gets nothing until it is released.
	a, err := f.st.TryEvaluatorLock(ctx)
	if err != nil || a == nil {
		t.Fatalf("first lock: %v", err)
	}
	b, err := f.st.TryEvaluatorLock(ctx)
	if err != nil || b != nil {
		t.Fatalf("second lock must fail: %v %v", b, err)
	}
	if !a.Alive(ctx) {
		t.Fatal("lock connection not alive")
	}
	a.Release(ctx)
	c, err := f.st.TryEvaluatorLock(ctx)
	if err != nil || c == nil {
		t.Fatalf("lock after release: %v", err)
	}
	c.Release(ctx)
}
