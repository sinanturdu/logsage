package alert

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/alert/outbound"
	"github.com/sinanturdu/logsage/backend/internal/alert/secret"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) add(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

type fakeOutbox struct {
	mu       sync.Mutex
	rows     []Claimed
	results  map[int64]DeliveryResult
	released []string
	claimed  int
}

func (f *fakeOutbox) ClaimNotifications(_ context.Context, _ string, n int, _ time.Time, _ time.Duration) ([]Claimed, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := min(n, len(f.rows))
	out := f.rows[:k]
	f.rows = f.rows[k:]
	f.claimed += k
	return out, nil
}
func (f *fakeOutbox) FinishNotification(_ context.Context, id int64, _ string, r DeliveryResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[id] = r
	return nil
}
func (f *fakeOutbox) ReleaseClaims(_ context.Context, w string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, w)
	return nil
}
func (f *fakeOutbox) RequeueExpired(context.Context, time.Time) (int64, error) { return 0, nil }

type sent struct {
	url    string
	header http.Header
	body   []byte
}

type fakePoster struct {
	mu    sync.Mutex
	got   []sent
	reply func(url string) (outbound.Response, error)
}

func (p *fakePoster) Post(_ context.Context, url string, h http.Header, body []byte) (outbound.Response, error) {
	p.mu.Lock()
	p.got = append(p.got, sent{url, h, body})
	p.mu.Unlock()
	return p.reply(url)
}

var testKey = []byte("0123456789abcdef0123456789abcdef")

func sealedChannel(t *testing.T, kind, url, sec string) SealedChannel {
	t.Helper()
	s, _ := secret.NewSealer(testKey)
	u, _ := s.Seal([]byte(url), secret.AD("p1", "c1", "url"))
	ch := SealedChannel{Channel: Channel{ID: "c1", Name: "ops", Kind: kind}, ProjectID: "p1", URL: u}
	if sec != "" {
		ch.Secret, _ = s.Seal([]byte(sec), secret.AD("p1", "c1", "secret"))
	}
	return ch
}

func payloadBytes(t *testing.T) []byte {
	b, _ := json.Marshal(Payload{Version: 1, Event: EventFiring, Project: "demo", State: "firing", Rule: PayloadRule{Name: "<!channel> & co", Type: "error_rate"},
		Value: 12, Threshold: 5, Unit: "percent", Group: &PayloadGroup{Template: "a <n> `b`"}, URL: "https://x.test/p?a=1&b=2"})
	return b
}

func newDeliverer(t *testing.T, rows []Claimed, reply func(string) (outbound.Response, error), opts DeliverOptions) (*Deliverer, *fakeOutbox, *fakePoster, *fakeClock) {
	store := &fakeOutbox{rows: rows, results: map[int64]DeliveryResult{}}
	post := &fakePoster{reply: reply}
	sealer, _ := secret.NewSealer(testKey)
	clock := &fakeClock{now: t0}
	return NewDeliverer(store, post, sealer, clock, "w1", opts, func() float64 { return 0.5 }, nil), store, post, clock
}

func ok204(string) (outbound.Response, error) { return outbound.Response{Status: 204}, nil }

func TestWebhookDeliveryIsSigned(t *testing.T) {
	body := payloadBytes(t)
	row := Claimed{ID: 1, ProjectID: "p1", Event: EventFiring, IdempotencyKey: "key-1", Payload: body, Channel: sealedChannel(t, KindWebhook, "https://hooks.example.com/h", "lsw_s")}
	d, store, post, _ := newDeliverer(t, []Claimed{row}, ok204, DeliverOptions{})
	if n, err := d.RunOnce(context.Background()); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	g := post.got[0]
	if g.url != "https://hooks.example.com/h" || string(g.body) != string(body) {
		t.Fatalf("sent %s %s", g.url, g.body)
	}
	if g.header.Get("X-LogSage-Event") != EventFiring || g.header.Get("X-LogSage-Delivery") != "key-1" {
		t.Fatalf("headers %v", g.header)
	}
	if err := secret.Verify("lsw_s", g.header.Get("X-LogSage-Signature"), g.body, t0, 5*time.Minute); err != nil {
		t.Fatalf("signature: %v", err)
	}
	if r := store.results[1]; r.Status != OutSent || *r.ResponseStatus != 204 || !r.CountAttempt {
		t.Fatalf("result %+v", r)
	}
}

func TestSlackBodyIsEscaped(t *testing.T) {
	row := Claimed{ID: 1, ProjectID: "p1", Event: EventFiring, Payload: payloadBytes(t), Channel: sealedChannel(t, KindSlack, "https://hooks.slack.com/services/T/B/X", "")}
	d, _, post, _ := newDeliverer(t, []Claimed{row}, ok204, DeliverOptions{})
	_, _ = d.RunOnce(context.Background())
	var m map[string]string
	if err := json.Unmarshal(post.got[0].body, &m); err != nil {
		t.Fatal(err)
	}
	text := m["text"]
	if strings.Contains(text, "<!channel>") || !strings.Contains(text, "`&lt;!channel&gt; &amp; co`") || !strings.Contains(text, "`a &lt;n&gt; 'b'`") {
		t.Fatalf("text %q", text)
	}
	if post.got[0].header.Get("X-LogSage-Signature") != "" {
		t.Fatal("slack is not signed")
	}
}

func TestRetriesBackoffAndFinalFailure(t *testing.T) {
	for _, c := range []struct {
		name   string
		reply  func(string) (outbound.Response, error)
		status string
	}{
		{"500 retries", func(string) (outbound.Response, error) { return outbound.Response{Status: 500}, nil }, OutPending},
		{"429 retries", func(string) (outbound.Response, error) { return outbound.Response{Status: 429}, nil }, OutPending},
		{"408 retries", func(string) (outbound.Response, error) { return outbound.Response{Status: 408}, nil }, OutPending},
		{"timeout retries", func(string) (outbound.Response, error) { return outbound.Response{}, errors.New("timed out") }, OutPending},
		{"404 fails at once", func(string) (outbound.Response, error) { return outbound.Response{Status: 404}, nil }, OutFailed},
		{"302 fails at once", func(string) (outbound.Response, error) { return outbound.Response{Status: 302}, nil }, OutFailed},
		{"blocked address fails at once", func(string) (outbound.Response, error) { return outbound.Response{}, outbound.ErrAddress }, OutFailed},
	} {
		row := Claimed{ID: 1, ProjectID: "p1", Attempts: 2, Payload: payloadBytes(t), Channel: sealedChannel(t, KindWebhook, "https://h.example.com", "s")}
		d, store, _, _ := newDeliverer(t, []Claimed{row}, c.reply, DeliverOptions{MaxAttempts: 8})
		_, _ = d.RunOnce(context.Background())
		r := store.results[1]
		if r.Status != c.status || !r.CountAttempt {
			t.Errorf("%s: %+v", c.name, r)
		}
		if c.status == OutPending && !r.NextAttemptAt.Equal(t0.Add(40*time.Second)) { // attempt 2: 10 s × 4, jitter 0.5 = ×1.0
			t.Errorf("%s: next attempt %v", c.name, r.NextAttemptAt.Sub(t0))
		}
	}
	// The last attempt fails for good.
	row := Claimed{ID: 1, ProjectID: "p1", Attempts: 7, Payload: payloadBytes(t), Channel: sealedChannel(t, KindWebhook, "https://h.example.com", "s")}
	d, store, _, _ := newDeliverer(t, []Claimed{row}, func(string) (outbound.Response, error) { return outbound.Response{Status: 503}, nil }, DeliverOptions{MaxAttempts: 8})
	_, _ = d.RunOnce(context.Background())
	if r := store.results[1]; r.Status != OutFailed || r.ErrorCategory != ErrHTTP5xx || r.ResponseStatus == nil || *r.ResponseStatus != 503 {
		t.Fatalf("final %+v", r)
	}
}

func TestBackoff(t *testing.T) {
	for _, c := range []struct {
		attempt int
		jitter  float64
		want    time.Duration
	}{
		{0, 0.5, 10 * time.Second},
		{0, 0, 8 * time.Second},
		{0, 1, 12 * time.Second},
		{3, 0.5, 80 * time.Second},
		{20, 0.5, time.Hour},
	} {
		if got := Backoff(c.attempt, c.jitter); got != c.want {
			t.Errorf("Backoff(%d, %v) = %v, want %v", c.attempt, c.jitter, got, c.want)
		}
	}
}

func TestPerChannelRateLimitDefersWithoutAnAttempt(t *testing.T) {
	var rows []Claimed
	for i := int64(1); i <= 4; i++ {
		rows = append(rows, Claimed{ID: i, ProjectID: "p1", Payload: payloadBytes(t), Channel: sealedChannel(t, KindWebhook, "https://h.example.com", "s")})
	}
	d, store, post, clock := newDeliverer(t, rows, ok204, DeliverOptions{PerChannelPerMinute: 3, Concurrency: 1, Batch: 4})
	_, _ = d.RunOnce(context.Background())
	if len(post.got) != 3 {
		t.Fatalf("%d sends, want 3", len(post.got))
	}
	deferred := store.results[4]
	if deferred.Status != OutPending || deferred.CountAttempt || !deferred.NextAttemptAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("deferred %+v", deferred)
	}
	clock.add(time.Minute)
	if wait, ok := d.take("c1", clock.Now()); !ok || wait != 0 {
		t.Fatal("the limit must free up after a minute")
	}
}

func TestDecryptFailureFails(t *testing.T) {
	ch := sealedChannel(t, KindWebhook, "https://h.example.com", "s")
	ch.URL[len(ch.URL)-1] ^= 1
	d, store, post, _ := newDeliverer(t, []Claimed{{ID: 1, ProjectID: "p1", Payload: payloadBytes(t), Channel: ch}}, ok204, DeliverOptions{})
	_, _ = d.RunOnce(context.Background())
	if r := store.results[1]; r.Status != OutFailed || r.ErrorCategory != ErrUnknown || len(post.got) != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestShutdownReleasesClaims(t *testing.T) {
	d, store, _, _ := newDeliverer(t, nil, ok204, DeliverOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	tick := make(chan time.Time)
	done := make(chan struct{})
	go func() { d.Run(ctx, tick); close(done) }()
	tick <- t0 // one full round has run
	cancel()
	<-done
	if len(store.released) != 1 || store.released[0] != "w1" {
		t.Fatalf("released %v", store.released)
	}
}

func TestSlackTextForTestsAndResolved(t *testing.T) {
	if got := SlackText(Payload{Test: true, Project: "demo"}); got != "Test notification from LogSage for project `demo`." {
		t.Fatalf("%q", got)
	}
	got := SlackText(Payload{State: "resolved", Project: "demo", Rule: PayloadRule{Name: "x", Type: "log_volume"}, Reason: ReasonRuleDisabled, Unit: "logs"})
	if !strings.HasPrefix(got, "Resolved: `x`") || !strings.Contains(got, "Reason: rule_disabled.") {
		t.Fatalf("%q", got)
	}
}
