package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"time"
)

// seedOptions configure "loadgen seed": history for the dashboard in one go.
// Unlike demo, event timestamps lie in the past: a baseline spread over the
// last Window, plus an incident inside the last 15 minutes in which payment
// gateway timeouts in payments cascade into checkout and api errors.
type seedOptions struct {
	URL, Key     string
	Window       time.Duration // baseline history
	Rate         float64       // baseline events per second of history
	IncidentAgo  time.Duration // incident start, before now
	IncidentFor  time.Duration // incident length
	IncidentRate float64       // payments timeouts per second at the peak
	Seed         uint64
	Batch        int
}

// maxSeedEvents keeps demo data small.
const maxSeedEvents = 100_000

func defaultSeedOptions() seedOptions {
	return seedOptions{
		URL: "http://127.0.0.1:8080/v1/ingest", Window: time.Hour, Rate: 15,
		IncidentAgo: 9 * time.Minute, IncidentFor: 5 * time.Minute, IncidentRate: 30,
		Seed: 1, Batch: 500,
	}
}

func (o seedOptions) validate() error {
	switch {
	case o.Window <= 0 || o.Rate <= 0 || o.Batch <= 0 || o.Batch > 1000:
		return errors.New("seed: --window and --rate must be positive and --batch 1..1000")
	case o.IncidentFor <= 0 || o.IncidentRate < 0:
		return errors.New("seed: --incident-for must be positive and --incident-rate not negative")
	case o.IncidentAgo > 15*time.Minute || o.IncidentAgo < o.IncidentFor:
		return errors.New("seed: the incident must lie inside the last 15 minutes (--incident-for <= --incident-ago <= 15m)")
	case o.IncidentAgo > o.Window:
		return errors.New("seed: --incident-ago must not exceed --window")
	}
	if n := o.expectedEvents(); n > maxSeedEvents {
		return fmt.Errorf("seed: about %d events planned, more than %d; lower --rate, --window or --incident-rate", n, maxSeedEvents)
	}
	return nil
}

// incidentShape is the intensity (0..1) at fraction f of the incident: a
// one-minute-like ramp up, a plateau, and a tail-off, so the spike looks
// like a real outage rather than a block.
func incidentShape(f float64) float64 {
	switch {
	case f < 0 || f >= 1:
		return 0
	case f < 0.2:
		return f / 0.2
	case f < 0.75:
		return 1
	default:
		return (1 - f) / 0.25
	}
}

// Per payments timeout at the peak, the cascade adds these rates of events.
const (
	checkoutPerTimeout = 0.5 // "charge failed: upstream payments returned 504"
	apiPerTimeout      = 0.3 // "POST /v1/checkout 502"
	retryPerTimeout    = 0.4 // payments warn: retrying
)

func (o seedOptions) expectedEvents() int {
	incident := o.IncidentRate * (1 + checkoutPerTimeout + apiPerTimeout + retryPerTimeout) * o.IncidentFor.Seconds() * 0.775
	return int(o.Rate*o.Window.Seconds() + incident)
}

// planSeed returns every event of the seed: the baseline, then the
// incident. It is deterministic for a given seed and now.
func planSeed(o seedOptions, now time.Time) []wireEvent {
	gen := NewGenerator(o.Seed)
	r := rand.New(rand.NewPCG(o.Seed, 7))
	start := now.Add(-o.Window)
	var events []wireEvent

	n := int(o.Rate * o.Window.Seconds())
	for i := 0; i < n; i++ {
		events = append(events, gen.Event(start.Add(time.Duration(r.Int64N(int64(o.Window))))))
	}

	incStart := now.Add(-o.IncidentAgo)
	secs := int(o.IncidentFor / time.Second)
	for s := 0; s < secs; s++ {
		level := incidentShape(float64(s) / float64(secs))
		count := poissonish(r, o.IncidentRate*level)
		for i := 0; i < count; i++ {
			ts := incStart.Add(time.Duration(s)*time.Second + time.Duration(r.Int64N(int64(time.Second))))
			events = append(events, cascade(r, ts)...)
		}
	}
	return events
}

// poissonish rounds a rate to a count with the right average.
func poissonish(r *rand.Rand, rate float64) int {
	n := int(math.Floor(rate))
	if r.Float64() < rate-float64(n) {
		n++
	}
	return n
}

var regions = []string{"eu-west-1", "eu-central-1"}

// cascade returns one failing payment and what it causes: the payments
// timeout, sometimes a retry warning before it, and the checkout and api
// errors of the same request, all sharing one trace id.
func cascade(r *rand.Rand, ts time.Time) []wireEvent {
	trace := hexID(r, 32)
	order := fmt.Sprintf("ord_%s", hexID(r, 6))
	attrs := func(extra map[string]string) map[string]string {
		a := map[string]string{"env": "prod", "region": regions[r.IntN(len(regions))], "order_id": order}
		for k, v := range extra {
			a[k] = v
		}
		return a
	}
	at := func(d time.Duration) string { return ts.Add(d).UTC().Format(time.RFC3339Nano) }
	level := "error"
	if r.IntN(40) == 0 {
		level = "fatal"
	}
	timeout := 5000
	out := []wireEvent{{
		Timestamp: at(0), Level: level, Service: "payments", TraceID: trace,
		Message:    fmt.Sprintf("payment gateway timeout after %d ms (psp-eu-2)", timeout),
		Attributes: attrs(map[string]string{"gateway.host": "psp-eu-2.example.net", "timeout_ms": "5000", "http.route": "/v1/charges"}),
	}}
	if r.Float64() < retryPerTimeout {
		out = append(out, wireEvent{
			Timestamp: at(-time.Duration(800+r.IntN(400)) * time.Millisecond), Level: "warn", Service: "payments", TraceID: trace,
			Message:    fmt.Sprintf("retrying charge for %s, attempt %d of 3", order, 2+r.IntN(2)),
			Attributes: attrs(map[string]string{"gateway.host": "psp-eu-2.example.net"}),
		})
	}
	if r.Float64() < checkoutPerTimeout {
		out = append(out, wireEvent{
			Timestamp: at(time.Duration(5+r.IntN(30)) * time.Millisecond), Level: "error", Service: "checkout", TraceID: trace,
			Message:    "charge failed: upstream payments returned 504",
			Attributes: attrs(map[string]string{"peer.service": "payments", "http.status_code": "504"}),
		})
	}
	if r.Float64() < apiPerTimeout {
		out = append(out, wireEvent{
			Timestamp: at(time.Duration(40+r.IntN(80)) * time.Millisecond), Level: "error", Service: "api", TraceID: trace,
			Message:    fmt.Sprintf("POST /v1/checkout 502 in %d ms", timeout+40+r.IntN(200)),
			Attributes: attrs(map[string]string{"http.method": "POST", "http.route": "/v1/checkout", "http.status_code": "502"}),
		})
	}
	return out
}

func cmdSeed(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	fs := flag.NewFlagSet("seed", flag.ContinueOnError)
	o := defaultSeedOptions()
	fs.StringVar(&o.URL, "url", o.URL, "ingest endpoint")
	fs.DurationVar(&o.Window, "window", o.Window, "baseline history ending now")
	fs.Float64Var(&o.Rate, "rate", o.Rate, "baseline events per second of history")
	fs.DurationVar(&o.IncidentAgo, "incident-ago", o.IncidentAgo, "the incident starts this long before now (at most 15m)")
	fs.DurationVar(&o.IncidentFor, "incident-for", o.IncidentFor, "incident length")
	fs.Float64Var(&o.IncidentRate, "incident-rate", o.IncidentRate, "payments timeouts per second at the incident peak")
	fs.Uint64Var(&o.Seed, "seed", o.Seed, "random seed")
	fs.IntVar(&o.Batch, "batch", o.Batch, "events per ingest request")
	allowRemote := fs.Bool("allow-remote", false, "allow non-localhost targets")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := o.validate(); err != nil {
		return err
	}
	if err := checkTarget(o.URL, *allowRemote); err != nil {
		return err
	}
	key, err := requireKey(getenv, "LOADGEN_INGEST_KEY")
	if err != nil {
		return err
	}
	o.Key = key
	return runSeed(ctx, o, time.Now(), &http.Client{Timeout: 30 * time.Second}, out)
}

// runSeed plans the events for now and posts them in batches, retrying a
// batch that gets 429 (backpressure) or a transient error.
func runSeed(ctx context.Context, o seedOptions, now time.Time, client *http.Client, out io.Writer) error {
	events := planSeed(o, now)
	fmt.Fprintf(out, "seed: %d events from %s to %s UTC; incident %s to %s UTC\n", len(events),
		now.Add(-o.Window).UTC().Format("15:04:05"), now.UTC().Format("15:04:05"),
		now.Add(-o.IncidentAgo).UTC().Format("15:04:05"), now.Add(-o.IncidentAgo+o.IncidentFor).UTC().Format("15:04:05"))
	sent := 0
	for i := 0; i < len(events); i += o.Batch {
		end := min(i+o.Batch, len(events))
		body, _ := json.Marshal(wireBatch{Events: events[i:end]})
		for attempt := 0; ; attempt++ {
			code, got, err := postBatch(ctx, client, o.URL, o.Key, body)
			if err == nil && code == http.StatusAccepted {
				sent += got
				break
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if attempt >= 8 || (err == nil && code != http.StatusTooManyRequests && code < 500) {
				return fmt.Errorf("seed: batch at event %d failed (status %d, %v) after %d attempts", i, code, err, attempt+1)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(100<<attempt) * time.Millisecond):
			}
		}
	}
	fmt.Fprintf(out, "seed: %d events accepted\n", sent)
	return nil
}
