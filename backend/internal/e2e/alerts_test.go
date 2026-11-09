package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/alert"
	"github.com/sinanturdu/logsage/backend/internal/alert/outbound"
	"github.com/sinanturdu/logsage/backend/internal/alert/secret"
	"github.com/sinanturdu/logsage/backend/internal/fingerprint"
	"github.com/sinanturdu/logsage/backend/internal/keys"
	"github.com/sinanturdu/logsage/backend/internal/query"
	"github.com/sinanturdu/logsage/backend/internal/storage/postgres"
)

// Alerts end to end (ADR 0024): logs go through ingest, Redis, the
// processor and ClickHouse; the evaluator (fake clock) fires rules; the
// delivery worker posts to a local sink that checks every signature.

type clk struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clk) Now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clk) set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

type delivery struct {
	path, key, event string
	verified         bool
	payload          alert.Payload
}

type sink struct {
	mu      sync.Mutex
	secrets map[string]string // path -> channel secret
	got     []delivery
	srv     *httptest.Server
	now     func() time.Time
}

func newSink(t *testing.T, now func() time.Time) *sink {
	s := &sink{secrets: map[string]string{}, now: now}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		defer s.mu.Unlock()
		d := delivery{path: r.URL.Path, key: r.Header.Get("X-LogSage-Delivery"), event: r.Header.Get("X-LogSage-Event")}
		d.verified = secret.Verify(s.secrets[r.URL.Path], r.Header.Get("X-LogSage-Signature"), body, s.now(), 5*time.Minute) == nil
		_ = json.Unmarshal(body, &d.payload)
		s.got = append(s.got, d)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *sink) deliveries() []delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]delivery(nil), s.got...)
}

type alertEnv struct {
	*env
	clock  *clk
	sealer *secret.Sealer
	sink   *sink
	eval   *alert.Evaluator
	deliv  *alert.Deliverer
	locker alert.Locker
	svc    *query.Service
}

func newAlertEnv(t *testing.T, start time.Time) *alertEnv {
	e := newEnv(t)
	clock := &clk{t: start}
	sealer, _ := secret.NewSealer([]byte("0123456789abcdef0123456789abcdef"))
	svc, err := query.NewService(e.ch, query.Options{MaxWindow: 168 * time.Hour, DefaultLimit: 1, MaxLimit: 1000, MaxTextLen: 256, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	locker := func(ctx context.Context) (alert.Lock, error) {
		l, err := e.store.TryEvaluatorLock(ctx)
		if err != nil || l == nil {
			return nil, err
		}
		return l, nil
	}
	a := &alertEnv{env: e, clock: clock, sealer: sealer, svc: svc, locker: locker}
	a.sink = newSink(t, clock.Now)
	a.eval = alert.NewEvaluator(e.store, svc, clock, locker, alert.EvalOptions{Lateness: 0}, nil)
	client := outbound.New(outbound.Options{AllowInsecureLocal: true})
	a.deliv = alert.NewDeliverer(e.store, client, sealer, clock, "e2e-worker", alert.DeliverOptions{Batch: 50}, func() float64 { return 0.5 }, nil)
	t.Cleanup(func() { a.release() })
	return a
}

// release lets go of the evaluator lock (Run does it on shutdown).
func (a *alertEnv) release() {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.eval.Run(ctx, nil)
}

// project makes a project with a webhook channel to the sink and an ingest key.
func (a *alertEnv) project(slug string) (postgres.Project, string, string) {
	a.t.Helper()
	ctx := context.Background()
	p, err := a.store.CreateProject(ctx, slug, slug)
	if err != nil {
		a.t.Fatal(err)
	}
	id, _ := postgres.NewID()
	sec, _ := secret.NewWebhookSecret()
	path := "/hook/" + slug
	u, _ := a.sealer.Seal([]byte(a.sink.srv.URL+path), secret.AD(p.ID, id, "url"))
	s, _ := a.sealer.Seal([]byte(sec), secret.AD(p.ID, id, "secret"))
	if _, err := a.store.CreateChannel(ctx, alert.SealedChannel{Channel: alert.Channel{ID: id, Name: "sink", Kind: alert.KindWebhook, URLHost: "http://127.0.0.1"},
		ProjectID: p.ID, URL: u, Secret: s}, "", postgres.AuditEntry{Action: "channel.create"}); err != nil {
		a.t.Fatal(err)
	}
	a.sink.mu.Lock()
	a.sink.secrets[path] = sec
	a.sink.mu.Unlock()
	key, _ := a.newKey(slug, keys.KindIngest)
	return p, id, key
}

func (a *alertEnv) rule(p postgres.Project, channel, name string, tp alert.Type, params alert.Params, window time.Duration) alert.Rule {
	a.t.Helper()
	r := alert.Rule{ProjectID: p.ID, Name: name, Type: tp, Params: params, Interval: time.Minute, Window: window, Enabled: true, Channels: []string{channel}}
	r.Normalize()
	if err := alert.Validate(r); err != nil {
		a.t.Fatal(err)
	}
	out, err := a.store.CreateRule(context.Background(), r, "", postgres.AuditEntry{Action: "alert_rule.create"})
	if err != nil {
		a.t.Fatal(err)
	}
	return out
}

// step evaluates at now and delivers everything due.
func (a *alertEnv) step(now time.Time) {
	a.t.Helper()
	a.clock.set(now)
	a.eval.Tick(context.Background())
	for {
		n, err := a.deliv.RunOnce(context.Background())
		if err != nil {
			a.t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
}

const payMsg = "payment gateway timeout after %d ms (psp-eu-2)"

func incident(t0 time.Time, n int) []ev {
	var out []ev
	for i := 0; i < n; i++ {
		at := t0.Add(-4 * time.Minute).Add(time.Duration(i) * 3 * time.Second)
		if i%5 < 2 { // 40% errors
			out = append(out, ev{Timestamp: at.Format(time.RFC3339Nano), Level: "error", Service: "payments", Message: fmt.Sprintf(payMsg, 5000+i)})
		} else {
			out = append(out, ev{Timestamp: at.Format(time.RFC3339Nano), Level: "info", Service: "checkout", Message: fmt.Sprintf("cart %d converted", i)})
		}
	}
	return out
}

func count(ds []delivery, f func(delivery) bool) int {
	n := 0
	for _, d := range ds {
		if f(d) {
			n++
		}
	}
	return n
}

func TestAlertsEndToEnd(t *testing.T) {
	T := time.Now().UTC().Truncate(time.Second)
	a := newAlertEnv(t, T)
	ctx := context.Background()
	type proj struct {
		p       postgres.Project
		channel string
		key     string
		rules   map[string]alert.Rule
	}
	projects := map[string]*proj{}
	for _, slug := range []string{"shop", "mirror"} { // identical data and rules: isolation
		p, ch, key := a.project(slug)
		pr := &proj{p: p, channel: ch, key: key, rules: map[string]alert.Rule{}}
		pr.rules["errors"] = a.rule(p, ch, "errors", alert.ErrorRate, alert.Params{ThresholdPercent: 20, MinLogs: 10}, 5*time.Minute)
		pr.rules["new"] = a.rule(p, ch, "new groups", alert.NewGroup, alert.Params{MinEvents: 3}, 5*time.Minute)
		pr.rules["silence"] = a.rule(p, ch, "silence", alert.LogVolume, alert.Params{Op: "below", Count: 1}, 5*time.Minute)
		pr.rules["regression"] = a.rule(p, ch, "regression", alert.Regression, alert.Params{}, 24*time.Hour)
		projects[slug] = pr
		if code := post(t, a.ingest, key, incident(T, 40)); code != http.StatusAccepted {
			t.Fatalf("ingest %s: %d", slug, code)
		}
	}
	a.waitRows(80)

	// T+30s (new rules are due from their creation time): the incident fires
	// error_rate and new_group in both projects, once each.
	a.step(T.Add(30 * time.Second))
	// A second evaluator instance is a standby and fires nothing.
	second := alert.NewEvaluator(a.store, a.svc, a.clock, a.locker, alert.EvalOptions{}, nil)
	second.Tick(ctx)
	if second.Stats().HoldsLock || second.Stats().Cycles != 0 {
		t.Fatalf("second instance ran: %+v", second.Stats())
	}
	got := a.sink.deliveries()
	if len(got) != 4 {
		t.Fatalf("%d deliveries at T: %+v", len(got), got)
	}
	payFP, payTpl := fingerprint.Compute("error", "payments", fmt.Sprintf(payMsg, 5000), nil)
	for _, slug := range []string{"shop", "mirror"} {
		mine := func(d delivery) bool { return d.path == "/hook/"+slug }
		if n := count(got, func(d delivery) bool { return mine(d) && d.payload.Project == slug }); n != 2 {
			t.Fatalf("%s: %d deliveries for its own project", slug, n)
		}
		if count(got, func(d delivery) bool {
			return mine(d) && d.payload.Rule.Type == "error_rate" && d.payload.Value == 40 && d.event == alert.EventFiring
		}) != 1 {
			t.Fatalf("%s: error_rate firing missing: %+v", slug, got)
		}
		if count(got, func(d delivery) bool {
			return mine(d) && d.payload.Rule.Type == "new_group" && d.payload.Group != nil && d.payload.Group.Fingerprint == fingerprint.Hex(payFP) &&
				d.payload.Group.Template == payTpl && d.payload.Sample == nil
		}) != 1 {
			t.Fatalf("%s: new_group firing missing: %+v", slug, got)
		}
	}
	for _, d := range got {
		if !d.verified || d.key == "" {
			t.Fatalf("unsigned or unkeyed delivery: %+v", d)
		}
	}

	// T+1m30s: still firing, nothing new.
	a.step(T.Add(90 * time.Second))
	if n := len(a.sink.deliveries()); n != 4 {
		t.Fatalf("re-notified: %d", n)
	}

	// T+4m: ingest stopped, so the silence rule fires. The new group stopped
	// being new in its window at T+1m30s and has been clear since: resolved.
	a.step(T.Add(4 * time.Minute))
	got = a.sink.deliveries()
	if n := count(got, func(d delivery) bool { return d.payload.Rule.Type == "log_volume" && d.event == alert.EventFiring }); n != 2 {
		t.Fatalf("silence: %d of %d", n, len(got))
	}
	if n := count(got, func(d delivery) bool { return d.payload.Rule.Type == "new_group" && d.event == alert.EventResolved }); n != 2 || len(got) != 8 {
		t.Fatalf("new group resolved: %d of %d", n, len(got))
	}
	// T+5m: the error rate has been clear for its clear_for: resolved.
	a.step(T.Add(5 * time.Minute))
	got = a.sink.deliveries()
	if n := count(got, func(d delivery) bool {
		return d.payload.Rule.Type == "error_rate" && d.event == alert.EventResolved && d.payload.ResolvedAt != nil
	}); n != 2 || len(got) != 10 {
		t.Fatalf("error rate resolved: %d of %d", n, len(got))
	}

	// Regression: shop resolves the payments group, which then appears again.
	g, err := a.store.SetGroupStatus(ctx, projects["shop"].p.ID, payFP, "resolved", "fixed", "", postgres.AuditEntry{Action: "group.status"})
	if err != nil {
		t.Fatal(err)
	}
	again := []ev{}
	for i := 0; i < 3; i++ {
		again = append(again, ev{Timestamp: g.ResolvedAt.Add(time.Second).Format(time.RFC3339Nano), Level: "error", Service: "payments", Message: fmt.Sprintf(payMsg, 9000+i)})
	}
	if code := post(t, a.ingest, projects["shop"].key, again); code != http.StatusAccepted {
		t.Fatalf("ingest: %d", code)
	}
	a.waitRows(83)
	a.step(T.Add(7 * time.Minute))
	got = a.sink.deliveries()
	reg := count(got, func(d delivery) bool { return d.payload.Rule.Type == "regression" })
	if reg != 1 || count(got, func(d delivery) bool {
		return d.payload.Rule.Type == "regression" && d.path == "/hook/shop" && d.payload.Group.Fingerprint == fingerprint.Hex(payFP)
	}) != 1 {
		t.Fatalf("regression deliveries %d: %+v", reg, got)
	}

	// Disabling a firing rule sends a final resolved notice with the reason.
	closer := postgres.Closer(alert.FinalNotifications(alert.ReasonRuleDisabled, "", a.clock.Now))
	if _, err := a.store.SetRuleEnabled(ctx, projects["mirror"].p.ID, projects["mirror"].rules["silence"].ID, false, closer, postgres.AuditEntry{Action: "alert_rule.disable"}); err != nil {
		t.Fatal(err)
	}
	a.step(T.Add(8 * time.Minute))
	got = a.sink.deliveries()
	if count(got, func(d delivery) bool {
		return d.path == "/hook/mirror" && d.payload.Reason == "rule_disabled" && d.event == alert.EventResolved
	}) != 1 {
		t.Fatalf("final notice: %+v", got[len(got)-1])
	}

	// Every delivery arrived exactly once, signed.
	seen := map[string]bool{}
	for _, d := range got {
		if seen[d.key] || !d.verified {
			t.Fatalf("duplicate or unverified delivery %+v", d)
		}
		seen[d.key] = true
	}
}
