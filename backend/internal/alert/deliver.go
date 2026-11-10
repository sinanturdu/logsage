package alert

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/alert/outbound"
	"github.com/sinanturdu/logsage/backend/internal/alert/secret"
)

// OutboxStore is what the delivery worker needs from storage.
type OutboxStore interface {
	ClaimNotifications(ctx context.Context, worker string, n int, now time.Time, lease time.Duration) ([]Claimed, error)
	FinishNotification(ctx context.Context, id int64, worker string, r DeliveryResult) error
	ReleaseClaims(ctx context.Context, worker string) error
	RequeueExpired(ctx context.Context, now time.Time) (int64, error)
}

// Poster sends a request (outbound.Client in production).
type Poster interface {
	Post(ctx context.Context, url string, h http.Header, body []byte) (outbound.Response, error)
}

// Opener decrypts sealed channel fields (secret.Sealer in production).
type Opener interface {
	Open(sealed, ad []byte) ([]byte, error)
}

// Clock is the time source; tests use a fake.
type Clock interface{ Now() time.Time }

// DeliverOptions bound delivery.
type DeliverOptions struct {
	Concurrency         int           // parallel sends (4)
	MaxAttempts         int           // then failed (8)
	PerChannelPerMinute int           // sends per channel per minute (30)
	Batch               int           // rows claimed per round (20)
	Lease               time.Duration // claim lease (60 s)
}

// Deliverer sends outbox rows: at least once, with backoff and jitter, a
// per-channel rate limit and a final failed state.
type Deliverer struct {
	store  OutboxStore
	post   Poster
	sealer Opener
	clock  Clock
	worker string
	opts   DeliverOptions
	log    *slog.Logger
	jitter func() float64 // in [0, 1)

	mu    sync.Mutex
	sends map[string][]time.Time // per channel, the sends of the last minute
}

// NewDeliverer returns a worker named worker (unique per process).
func NewDeliverer(store OutboxStore, post Poster, sealer Opener, clock Clock, worker string, opts DeliverOptions, jitter func() float64, log *slog.Logger) *Deliverer {
	if opts.Concurrency <= 0 {
		opts.Concurrency = 4
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 8
	}
	if opts.PerChannelPerMinute <= 0 {
		opts.PerChannelPerMinute = 30
	}
	if opts.Batch <= 0 {
		opts.Batch = 20
	}
	if opts.Lease <= 0 {
		opts.Lease = time.Minute
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Deliverer{store: store, post: post, sealer: sealer, clock: clock, worker: worker, opts: opts, log: log, jitter: jitter,
		sends: map[string][]time.Time{}}
}

// Backoff is the wait after a failed attempt: 10 s doubling per attempt,
// within ±20 % jitter, at most an hour.
func Backoff(attempt int, jitter float64) time.Duration {
	base := 10 * time.Second * time.Duration(math.Pow(2, float64(min(attempt, 12))))
	d := time.Duration(float64(base) * (0.8 + 0.4*jitter))
	return min(d, time.Hour)
}

// take records a send on ch, or says how long to wait for the per-minute limit.
func (d *Deliverer) take(ch string, now time.Time) (time.Duration, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	recent := d.sends[ch][:0]
	for _, t := range d.sends[ch] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= d.opts.PerChannelPerMinute {
		d.sends[ch] = recent
		return recent[0].Add(time.Minute).Sub(now), false
	}
	d.sends[ch] = append(recent, now)
	return 0, true
}

// RunOnce claims due rows and sends them; it returns how many it handled.
func (d *Deliverer) RunOnce(ctx context.Context) (int, error) {
	claimed, err := d.store.ClaimNotifications(ctx, d.worker, d.opts.Batch, d.clock.Now(), d.opts.Lease)
	if err != nil || len(claimed) == 0 {
		return 0, err
	}
	sem := make(chan struct{}, d.opts.Concurrency)
	var wg sync.WaitGroup
	for _, c := range claimed {
		wg.Add(1)
		sem <- struct{}{}
		go func(c Claimed) {
			defer wg.Done()
			defer func() { <-sem }()
			res := d.deliver(ctx, c)
			// The result is written even if ctx ended meanwhile.
			wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if err := d.store.FinishNotification(wctx, c.ID, d.worker, res); err != nil {
				d.log.Warn("alert delivery: write result failed", "notification", c.ID, "err", err)
			}
		}(c)
	}
	wg.Wait()
	return len(claimed), nil
}

// Run delivers until ctx ends, then returns unfinished claims to pending.
func (d *Deliverer) Run(ctx context.Context, every <-chan time.Time) {
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := d.store.ReleaseClaims(rctx, d.worker); err != nil {
			d.log.Warn("alert delivery: release claims failed", "err", err)
		}
	}()
	for {
		if _, err := d.store.RequeueExpired(ctx, d.clock.Now()); err != nil && ctx.Err() == nil {
			d.log.Warn("alert delivery: requeue failed", "err", err)
		}
		for {
			n, err := d.RunOnce(ctx)
			if err != nil && ctx.Err() == nil {
				d.log.Warn("alert delivery: claim failed", "err", err)
			}
			if n == 0 || ctx.Err() != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-every:
		}
	}
}

func (d *Deliverer) deliver(ctx context.Context, c Claimed) DeliveryResult {
	now := d.clock.Now()
	attempt := c.Attempts + 1
	retry := func(category string, code *int) DeliveryResult {
		if attempt >= d.opts.MaxAttempts {
			return DeliveryResult{Status: OutFailed, ResponseStatus: code, ErrorCategory: category, NextAttemptAt: now, CountAttempt: true}
		}
		return DeliveryResult{Status: OutPending, ResponseStatus: code, ErrorCategory: category, NextAttemptAt: now.Add(Backoff(c.Attempts, d.jitter())), CountAttempt: true}
	}
	if wait, ok := d.take(c.Channel.ID, now); !ok {
		return DeliveryResult{Status: OutPending, ErrorCategory: ErrRateLimited, NextAttemptAt: now.Add(wait)}
	}
	url, err := d.sealer.Open(c.Channel.URL, secret.AD(c.ProjectID, c.Channel.ID, "url"))
	if err != nil {
		d.log.Warn("alert delivery: cannot decrypt the channel URL (was ALERTS_SECRET_KEY changed?)", "notification", c.ID)
		return DeliveryResult{Status: OutFailed, ErrorCategory: ErrUnknown, NextAttemptAt: now, CountAttempt: true}
	}
	body, header, err := d.request(c, now)
	if err != nil {
		d.log.Warn("alert delivery: cannot build the request", "notification", c.ID, "err", err)
		return DeliveryResult{Status: OutFailed, ErrorCategory: ErrUnknown, NextAttemptAt: now, CountAttempt: true}
	}
	res, err := d.post.Post(ctx, string(url), header, body)
	if err != nil {
		category := Classify(err)
		if category == ErrBlockedAddress {
			return DeliveryResult{Status: OutFailed, ErrorCategory: category, NextAttemptAt: now, CountAttempt: true}
		}
		return retry(category, nil)
	}
	code := res.Status
	switch {
	case code >= 200 && code < 300:
		return DeliveryResult{Status: OutSent, ResponseStatus: &code, NextAttemptAt: now, CountAttempt: true}
	case code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500:
		return retry(StatusCategory(code), &code)
	default: // 3xx (redirects are never followed) and other 4xx will not change
		return DeliveryResult{Status: OutFailed, ResponseStatus: &code, ErrorCategory: StatusCategory(code), NextAttemptAt: now, CountAttempt: true}
	}
}

// Delivery failure categories (ADR 0024 addendum): the only failure
// information stored, logged or shown besides the HTTP status code.
const (
	ErrHTTP4xx        = "http_4xx"
	ErrHTTP5xx        = "http_5xx"
	ErrTimeout        = "timeout"
	ErrConnect        = "connect_error"
	ErrTLS            = "tls_error"
	ErrBlockedAddress = "blocked_address"
	ErrRateLimited    = "rate_limited"
	ErrCanceled       = "canceled"
	ErrUnknown        = "unknown"
)

// ErrorCategories is the closed list (the database checks it too).
var ErrorCategories = []string{ErrHTTP4xx, ErrHTTP5xx, ErrTimeout, ErrConnect, ErrTLS, ErrBlockedAddress, ErrRateLimited, ErrCanceled, ErrUnknown}

// StatusCategory is the category of a non-2xx answer.
func StatusCategory(code int) string {
	switch {
	case code == http.StatusTooManyRequests:
		return ErrRateLimited
	case code == http.StatusRequestTimeout:
		return ErrTimeout
	case code >= 400 && code < 500:
		return ErrHTTP4xx
	case code >= 500 && code < 600:
		return ErrHTTP5xx
	}
	return ErrUnknown // 1xx or 3xx: redirects are never followed
}

// Classify is the category of a failed request. It looks only at the
// error's type, never at its text.
func Classify(err error) string {
	for _, blocked := range []error{outbound.ErrAddress, outbound.ErrScheme, outbound.ErrUser, outbound.ErrFrag, outbound.ErrPort, outbound.ErrHost, outbound.ErrURL} {
		if errors.Is(err, blocked) {
			return ErrBlockedAddress
		}
	}
	var ne net.Error
	var tlsErr *tls.CertificateVerificationError
	var unknownAuth x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var certErr x509.CertificateInvalidError
	var recErr tls.RecordHeaderError
	var opErr *net.OpError
	switch {
	case errors.Is(err, context.Canceled):
		return ErrCanceled
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return ErrTimeout
	case errors.As(err, &tlsErr), errors.As(err, &unknownAuth), errors.As(err, &hostErr), errors.As(err, &certErr), errors.As(err, &recErr):
		return ErrTLS
	case errors.Is(err, outbound.ErrResolve), errors.As(err, &opErr):
		return ErrConnect
	}
	return ErrUnknown
}

// request builds the body and headers for the channel kind.
func (d *Deliverer) request(c Claimed, now time.Time) ([]byte, http.Header, error) {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("User-Agent", "LogSage-Alerts/1")
	switch c.Channel.Kind {
	case KindSlack:
		var p Payload
		if err := json.Unmarshal(c.Payload, &p); err != nil {
			return nil, nil, fmt.Errorf("bad payload: %w", err)
		}
		body, err := json.Marshal(map[string]string{"text": SlackText(p)})
		return body, h, err
	case KindWebhook:
		sec, err := d.sealer.Open(c.Channel.Secret, secret.AD(c.ProjectID, c.Channel.ID, "secret"))
		if err != nil {
			return nil, nil, errors.New("cannot decrypt the signing secret (was ALERTS_SECRET_KEY changed?)")
		}
		h.Set("X-LogSage-Event", c.Event)
		h.Set("X-LogSage-Delivery", c.IdempotencyKey)
		h.Set("X-LogSage-Signature", secret.Sign(string(sec), now, c.Payload))
		return c.Payload, h, nil
	}
	return nil, nil, fmt.Errorf("unknown channel kind %q", c.Channel.Kind)
}

// slackEscape follows Slack's rules: &, < and > are entities.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// SlackText is the message for a Slack-compatible webhook. Names and
// templates are escaped and set in code spans so they cannot add mentions
// or links.
func SlackText(p Payload) string {
	code := func(s string) string { return "`" + slackEscape(strings.ReplaceAll(s, "`", "'")) + "`" }
	var b strings.Builder
	switch {
	case p.Test:
		b.WriteString("Test notification from LogSage for project " + code(p.Project) + ".")
		return b.String()
	case p.State == "resolved":
		b.WriteString("Resolved: ")
	default:
		b.WriteString("Firing: ")
	}
	b.WriteString(code(p.Rule.Name) + " in project " + code(p.Project))
	b.WriteString(fmt.Sprintf(" (%s, value %g %s, threshold %g).", p.Rule.Type, p.Value, p.Unit, p.Threshold))
	if p.Group != nil {
		b.WriteString(" Group " + code(p.Group.Template) + ".")
	}
	if p.Reason != "" {
		b.WriteString(" Reason: " + slackEscape(p.Reason) + ".")
	}
	if p.URL != "" {
		b.WriteString(" <" + slackEscape(p.URL) + "|Open in LogSage>")
	}
	return b.String()
}
