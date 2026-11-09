package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/alert"
	"github.com/sinanturdu/logsage/backend/internal/alert/outbound"
	"github.com/sinanturdu/logsage/backend/internal/alert/secret"
	"github.com/sinanturdu/logsage/backend/internal/config"
	"github.com/sinanturdu/logsage/backend/internal/storage/postgres"
)

// fakeAlerts keeps rules and channels in memory, with the store's limits.
type fakeAlerts struct {
	mu       sync.Mutex
	channels map[string]alert.SealedChannel
	rules    map[string]alert.Rule
	audits   []postgres.AuditEntry
	tests    int
	closers  []postgres.Closer
	seq      int
}

func newFakeAlerts() *fakeAlerts {
	return &fakeAlerts{channels: map[string]alert.SealedChannel{}, rules: map[string]alert.Rule{}}
}

func (f *fakeAlerts) CreateChannel(_ context.Context, c alert.SealedChannel, _ string, a postgres.AuditEntry) (alert.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, x := range f.channels {
		if x.ProjectID == c.ProjectID {
			n++
		}
	}
	if n >= alert.MaxChannelsPerProject {
		return alert.Channel{}, postgres.ErrLimit
	}
	f.channels[c.ID] = c
	f.audits = append(f.audits, a)
	return c.Channel, nil
}
func (f *fakeAlerts) ListChannels(_ context.Context, pid string) ([]alert.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []alert.Channel{}
	for _, c := range f.channels {
		if c.ProjectID == pid {
			out = append(out, c.Channel)
		}
	}
	return out, nil
}
func (f *fakeAlerts) Channel(_ context.Context, pid, id string) (alert.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.channels[id]
	if !ok || c.ProjectID != pid {
		return alert.Channel{}, postgres.ErrNotFound
	}
	return c.Channel, nil
}
func (f *fakeAlerts) UpdateChannel(_ context.Context, pid, id string, u postgres.ChannelUpdate, a postgres.AuditEntry) (alert.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.channels[id]
	if !ok || c.ProjectID != pid {
		return alert.Channel{}, postgres.ErrNotFound
	}
	if u.Name != nil {
		c.Name = *u.Name
	}
	if u.URL != nil {
		c.URL, c.URLHost = u.URL, *u.URLHost
	}
	if u.Secret != nil {
		c.Secret = u.Secret
	}
	f.channels[id] = c
	f.audits = append(f.audits, a)
	return c.Channel, nil
}
func (f *fakeAlerts) DeleteChannel(_ context.Context, pid, id string, a postgres.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.channels[id]; !ok || c.ProjectID != pid {
		return postgres.ErrNotFound
	}
	delete(f.channels, id)
	f.audits = append(f.audits, a)
	return nil
}
func (f *fakeAlerts) QueueTestNotification(_ context.Context, _ string, _ alert.Channel, _ string, _ []byte, _ time.Time, a postgres.AuditEntry) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tests >= postgres.TestSendLimit {
		return 0, &postgres.RateLimitError{RetryAfter: 7 * time.Minute}
	}
	f.tests++
	f.audits = append(f.audits, a)
	return int64(f.tests), nil
}
func (f *fakeAlerts) CreateRule(_ context.Context, r alert.Rule, _ string, a postgres.AuditEntry) (alert.Rule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range r.Channels {
		if ch, ok := f.channels[c]; !ok || ch.ProjectID != r.ProjectID {
			return alert.Rule{}, postgres.ErrBadChannel
		}
	}
	if len(f.rules) >= alert.MaxRulesPerProject {
		return alert.Rule{}, postgres.ErrLimit
	}
	f.seq++
	r.ID, r.Version = "rule-"+string(rune('a'+f.seq)), 1
	f.rules[r.ID] = r
	f.audits = append(f.audits, a)
	return r, nil
}
func (f *fakeAlerts) ListRules(_ context.Context, pid string) ([]alert.Rule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []alert.Rule{}
	for _, r := range f.rules {
		if r.ProjectID == pid {
			out = append(out, r)
		}
	}
	return out, nil
}
func (f *fakeAlerts) Rule(_ context.Context, pid, id string) (alert.Rule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rules[id]
	if !ok || r.ProjectID != pid {
		return alert.Rule{}, postgres.ErrNotFound
	}
	return r, nil
}
func (f *fakeAlerts) UpdateRule(_ context.Context, r alert.Rule, a postgres.AuditEntry) (alert.Rule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r.Version = f.rules[r.ID].Version + 1
	f.rules[r.ID] = r
	f.audits = append(f.audits, a)
	return r, nil
}
func (f *fakeAlerts) SetRuleEnabled(_ context.Context, pid, id string, en bool, c postgres.Closer, a postgres.AuditEntry) (alert.Rule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rules[id]
	if !ok || r.ProjectID != pid {
		return alert.Rule{}, postgres.ErrNotFound
	}
	r.Enabled = en
	f.rules[id] = r
	f.closers = append(f.closers, c)
	f.audits = append(f.audits, a)
	return r, nil
}
func (f *fakeAlerts) DeleteRule(_ context.Context, pid, id string, c postgres.Closer, a postgres.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.rules[id]; !ok || r.ProjectID != pid {
		return postgres.ErrNotFound
	}
	delete(f.rules, id)
	f.closers = append(f.closers, c)
	f.audits = append(f.audits, a)
	return nil
}
func (f *fakeAlerts) AlertStates(context.Context, string) ([]alert.StateRecord, error) {
	return []alert.StateRecord{}, nil
}
func (f *fakeAlerts) AlertEvents(context.Context, string, string, int64, int) ([]alert.EventRecord, error) {
	return []alert.EventRecord{}, nil
}
func (f *fakeAlerts) Notifications(context.Context, string, string, string, string, int64, int) ([]alert.NotificationRecord, error) {
	return []alert.NotificationRecord{}, nil
}

type mapResolver map[string]string

func (m mapResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if a, ok := m[host]; ok {
		return []netip.Addr{netip.MustParseAddr(a)}, nil
	}
	return nil, io.EOF
}

const shopProject = "11111111-1111-1111-1111-111111111111"

func alertFixture(t *testing.T, alerts *AlertsOptions) *readFixture {
	return newReadFixture(t, 4, func(o *Options) { o.Alerts = alerts })
}

func enabledAlerts(t *testing.T) (*AlertsOptions, *fakeAlerts) {
	sealer, err := secret.NewSealer([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	store := newFakeAlerts()
	urls := outbound.New(outbound.Options{Resolver: mapResolver{"hooks.example.com": "93.184.216.34", "internal.example.com": "10.0.0.5"}})
	return &AlertsOptions{Store: store, Sealer: sealer, URLs: urls}, store
}

func (f *readFixture) send(t *testing.T, method, path, token, body string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", f.srv.URL)
	if token != "" {
		req.AddCookie(&http.Cookie{Name: cookieName(false), Value: f.tokens[token]})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

const base = "/api/projects/shop/alerts"

func createChannel(t *testing.T, f *readFixture, body string) (string, string) {
	t.Helper()
	status, out, _ := f.send(t, "POST", base+"/channels", "tok-owner", body)
	if status != 201 {
		t.Fatalf("create channel: %d %s", status, out)
	}
	var r struct {
		Channel alert.Channel `json:"channel"`
		Secret  string        `json:"secret"`
	}
	_ = json.Unmarshal([]byte(out), &r)
	return r.Channel.ID, r.Secret
}

func TestAlertsDisabledOrUnconfigured(t *testing.T) {
	for _, c := range []struct {
		opts *AlertsOptions
		want string
	}{
		{nil, "alerts are disabled"},
		{&AlertsOptions{KeyErr: config.ErrAlertKey}, "ALERTS_SECRET_KEY is missing or not 32 bytes base64"},
	} {
		f := alertFixture(t, c.opts)
		status, body, _ := f.get(t, base+"/rules", "tok-owner")
		if status != 503 || !strings.Contains(body, c.want) {
			t.Errorf("%d %s", status, body)
		}
		// Membership is checked first: outsiders still get 404.
		if status, _, _ := f.get(t, base+"/rules", "tok-outsider"); status != 404 {
			t.Errorf("outsider: %d", status)
		}
	}
}

func TestAlertsRoleMatrix(t *testing.T) {
	opts, _ := enabledAlerts(t)
	f := alertFixture(t, opts)
	for _, path := range []string{"/rules", "/channels", "/states", "/events", "/notifications"} {
		for _, c := range []struct {
			token string
			want  int
		}{{"tok-owner", 200}, {"tok-admin", 200}, {"tok-viewer", 200}, {"tok-outsider", 404}, {"", 401}} {
			if status, body, _ := f.get(t, base+path, c.token); status != c.want {
				t.Errorf("GET %s as %s: %d %s", path, c.token, status, body)
			}
		}
	}
	for _, c := range []struct {
		token string
		want  int
	}{{"tok-viewer", 403}, {"tok-outsider", 404}, {"", 401}, {"tok-admin", 201}} {
		status, body, _ := f.send(t, "POST", base+"/channels", c.token, `{"name":"ops `+c.token+`","kind":"webhook","url":"https://hooks.example.com/h"}`)
		if status != c.want {
			t.Errorf("create channel as %s: %d %s", c.token, status, body)
		}
	}
}

func TestChannelSecretShownOnceAndSealed(t *testing.T) {
	opts, store := enabledAlerts(t)
	f := alertFixture(t, opts)
	id, sec := createChannel(t, f, `{"name":"ops","kind":"webhook","url":"https://hooks.example.com/services/T0/B0/TOKEN?x=1","include_sample":false}`)
	if !strings.HasPrefix(sec, "lsw_") {
		t.Fatalf("secret %q", sec)
	}
	stored := store.channels[id]
	if bytes.Contains(stored.URL, []byte("TOKEN")) || bytes.Contains(stored.Secret, []byte(sec)) || stored.URLHost != "https://hooks.example.com" {
		t.Fatalf("stored in clear: %+v", stored.Channel)
	}
	plain, err := opts.Sealer.Open(stored.Secret, secret.AD(shopProject, id, "secret"))
	if err != nil || string(plain) != sec {
		t.Fatalf("sealed secret: %v", err)
	}
	for _, path := range []string{"/channels", "/channels/" + id} {
		_, body, _ := f.get(t, base+path, "tok-viewer")
		if strings.Contains(body, sec) || strings.Contains(body, "TOKEN") || strings.Contains(body, `"secret"`) {
			t.Fatalf("GET %s leaks: %s", path, body)
		}
	}
	for _, a := range store.audits {
		if strings.Contains(a.Detail+a.Target, "TOKEN") {
			t.Fatalf("audit leaks the URL: %+v", a)
		}
	}
	// Rotate shows a new secret once.
	status, body, _ := f.send(t, "POST", base+"/channels/"+id+"/rotate-secret", "tok-owner", `{}`)
	if status != 200 || !strings.Contains(body, `"secret":"lsw_`) || strings.Contains(body, sec) {
		t.Fatalf("rotate: %d %s", status, body)
	}
	// Slack channels have no secret.
	_, slackSecret := createChannel(t, f, `{"name":"chat","kind":"slack","url":"https://hooks.example.com/services/x"}`)
	if slackSecret != "" {
		t.Fatal("slack channel got a secret")
	}
}

func TestChannelURLRejections(t *testing.T) {
	opts, _ := enabledAlerts(t)
	f := alertFixture(t, opts)
	for url, want := range map[string]string{
		"http://169.254.169.254/latest/meta-data/": "url: must use https",
		"https://169.254.169.254/latest/meta-data": "url: host resolves to a private or reserved address",
		"https://[::ffff:169.254.169.254]/":        "url: host resolves to a private or reserved address",
		"http://localhost:9000/hook":               "url: must use https",
		"https://localhost/hook":                   "url: host resolves to a private or reserved address",
		"https://internal.example.com/hook":        "url: host resolves to a private or reserved address",
		"https://user:pw@hooks.example.com/h":      "url: must not contain a user name or password",
		"https://hooks.example.com:22/h":           "url: port is not allowed",
		"https://hooks.example.com/h#x":            "url: must not contain a fragment",
		"https://2130706433/":                      "url: host is not allowed",
		"https://nxdomain.example.com/":            "url: host cannot be resolved",
	} {
		body := `{"name":"x","kind":"webhook","url":` + mustJSON(url) + `}`
		status, out, _ := f.send(t, "POST", base+"/channels", "tok-owner", body)
		if status != 400 || !strings.Contains(out, want) {
			t.Errorf("%s: %d %s, want %q", url, status, out, want)
		}
	}
	for body, want := range map[string]string{
		`{"kind":"webhook","url":"https://hooks.example.com"}`:                  "name:",
		`{"name":"x","kind":"email","url":"https://hooks.example.com"}`:         "kind:",
		`{"name":"x","kind":"webhook"}`:                                         "url: required",
		`{"name":"x","kind":"webhook","url":"https://hooks.example.com","a":1}`: "",
	} {
		if status, out, _ := f.send(t, "POST", base+"/channels", "tok-owner", body); status != 400 || !strings.Contains(out, want) {
			t.Errorf("%s: %d %s", body, status, out)
		}
	}
}

func mustJSON(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestRuleValidationAndLimits(t *testing.T) {
	opts, store := enabledAlerts(t)
	f := alertFixture(t, opts)
	ch, _ := createChannel(t, f, `{"name":"ops","kind":"webhook","url":"https://hooks.example.com/h"}`)
	good := `{"name":"Checkout errors","type":"error_rate","params":{"threshold_percent":5,"services":["checkout"]},"interval":"1m","window":"5m","channels":["` + ch + `"]}`
	status, body, _ := f.send(t, "POST", base+"/rules", "tok-owner", good)
	if status != 201 || !strings.Contains(body, `"min_logs":50`) || !strings.Contains(body, `"clear_for":"1m0s"`) {
		t.Fatalf("create: %d %s", status, body)
	}
	for in, want := range map[string]string{
		`{"name":"x","type":"error_rate","params":{"threshold_percent":5},"window":"30s","channels":["` + ch + `"]}`:   "window: must be between 1m and 24h",
		`{"name":"x","type":"error_rate","params":{"threshold_percent":5},"window":"5 min","channels":["` + ch + `"]}`: "window: must be a duration",
		`{"name":"x","type":"error_rate","params":{"threshold_percent":500},"channels":["` + ch + `"]}`:                "threshold_percent:",
		`{"name":"x","type":"anomaly","channels":["` + ch + `"]}`:                                                      "type:",
		`{"name":"x","type":"error_rate","params":{"threshold_percent":5}}`:                                            "channels: at least one",
		`{"name":"x","type":"error_rate","params":{"threshold_percent":5},"channels":["nope"]}`:                        "channels: unknown channel",
		`{"name":"x","type":"log_volume","params":{"op":"equal"},"channels":["` + ch + `"]}`:                           "op:",
	} {
		if status, out, _ := f.send(t, "POST", base+"/rules", "tok-owner", in); status != 400 || !strings.Contains(out, want) {
			t.Errorf("%s: %d %s, want %q", in, status, out, want)
		}
	}
	for i := len(store.rules); i < alert.MaxRulesPerProject; i++ {
		store.rules["filler"+string(rune('A'+i))] = alert.Rule{ProjectID: shopProject}
	}
	if status, out, _ := f.send(t, "POST", base+"/rules", "tok-owner", strings.Replace(good, "Checkout errors", "one more", 1)); status != 409 || !strings.Contains(out, "rules: at most 50") {
		t.Fatalf("limit: %d %s", status, out)
	}
}

func TestRuleLifecycleAndAudit(t *testing.T) {
	opts, store := enabledAlerts(t)
	f := alertFixture(t, opts)
	ch, _ := createChannel(t, f, `{"name":"ops","kind":"webhook","url":"https://hooks.example.com/h"}`)
	_, body, _ := f.send(t, "POST", base+"/rules", "tok-owner", `{"name":"silence","type":"log_volume","params":{"op":"below","count":1},"window":"10m","channels":["`+ch+`"]}`)
	var created struct {
		Rule ruleJSON `json:"rule"`
	}
	_ = json.Unmarshal([]byte(body), &created)
	id := created.Rule.ID
	if created.Rule.State != "ok" || created.Rule.LastChangeAt != nil || !strings.Contains(body, `"last_change_at":null`) {
		t.Fatalf("new rule summary: %s", body)
	}
	if status, _, _ := f.send(t, "PUT", base+"/rules/"+id, "tok-viewer", `{}`); status != 403 {
		t.Fatalf("viewer update: %d", status)
	}
	status, out, _ := f.send(t, "PUT", base+"/rules/"+id, "tok-admin", `{"name":"silence","type":"log_volume","params":{"op":"below","count":5},"window":"15m","channels":["`+ch+`"]}`)
	if status != 200 || !strings.Contains(out, `"window":"15m0s"`) {
		t.Fatalf("update: %d %s", status, out)
	}
	if status, _, _ := f.send(t, "POST", base+"/rules/"+id+"/disable", "tok-owner", `{}`); status != 200 {
		t.Fatalf("disable: %d", status)
	}
	if status, _, _ := f.send(t, "DELETE", base+"/rules/"+id, "tok-owner", ``); status != 204 {
		t.Fatalf("delete: %d", status)
	}
	if status, _, _ := f.get(t, base+"/rules/"+id, "tok-owner"); status != 404 {
		t.Fatalf("after delete: %d", status)
	}
	// Disable and delete pass a closer that builds final resolved notices with the reason.
	if len(store.closers) != 2 || store.closers[0] == nil || store.closers[1] == nil {
		t.Fatalf("closers %d", len(store.closers))
	}
	notes, err := store.closers[1](alert.Rule{ID: id, Type: alert.LogVolume}, []alert.Channel{{ID: ch}},
		[]alert.Notice{{Key: alert.RuleKey, Event: alert.EventResolved, FiredAt: time.Now()}})
	if err != nil || len(notes) != 1 || !strings.Contains(string(notes[0].Payload), `"reason":"rule_deleted"`) {
		t.Fatalf("final notes %v %v", notes, err)
	}
	var actions []string
	for _, a := range store.audits {
		actions = append(actions, a.Action)
	}
	if got := strings.Join(actions, ","); got != "channel.create,alert_rule.create,alert_rule.update,alert_rule.disable,alert_rule.delete" {
		t.Fatalf("audit %s", got)
	}
	// Another project's rule id is a 404.
	store.rules["foreign"] = alert.Rule{ID: "foreign", ProjectID: "22222222-2222-2222-2222-222222222222"}
	if status, _, _ := f.get(t, base+"/rules/foreign", "tok-owner"); status != 404 {
		t.Fatalf("foreign rule: %d", status)
	}
}

func TestTestSendIsLimited(t *testing.T) {
	opts, store := enabledAlerts(t)
	f := alertFixture(t, opts)
	ch, _ := createChannel(t, f, `{"name":"ops","kind":"webhook","url":"https://hooks.example.com/h"}`)
	for i := 0; i < postgres.TestSendLimit; i++ {
		if status, out, _ := f.send(t, "POST", base+"/channels/"+ch+"/test", "tok-admin", `{}`); status != 202 || !strings.Contains(out, "notification_id") {
			t.Fatalf("test %d: %d %s", i, status, out)
		}
	}
	status, _, h := f.send(t, "POST", base+"/channels/"+ch+"/test", "tok-admin", `{}`)
	if status != 429 || h.Get("Retry-After") != "420" {
		t.Fatalf("limit: %d %v", status, h.Get("Retry-After"))
	}
	if store.tests != postgres.TestSendLimit {
		t.Fatalf("tests %d", store.tests)
	}
}

func TestHistoryValidation(t *testing.T) {
	opts, _ := enabledAlerts(t)
	f := alertFixture(t, opts)
	for _, q := range []string{"/events?limit=0", "/events?limit=101", "/events?before=-1", "/events?before=x", "/notifications?status=lost", "/events?rule=" + strings.Repeat("x", 65), "/notifications?rule=" + strings.Repeat("x", 65)} {
		if status, _, _ := f.get(t, base+q, "tok-viewer"); status != 400 {
			t.Errorf("%s: %d", q, status)
		}
	}
	if status, body, _ := f.get(t, base+"/notifications?status=failed&limit=10", "tok-viewer"); status != 200 || !strings.Contains(body, `"next_before":null`) {
		t.Errorf("%d %s", status, body)
	}
}
