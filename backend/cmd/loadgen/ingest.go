package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type ingestOptions struct {
	URL          string
	Key          string
	Duration     time.Duration // 0: until MaxEvents
	Concurrency  int
	BatchSize    int
	Rate         float64 // events/s cap, 0 = unlimited
	Seed         uint64
	MaxEvents    int64         // 0: until Duration
	Spread       time.Duration // spread event timestamps over [now-spread, now)
	WaitDrain    string        // processor /stats URL, empty: skip
	DrainTimeout time.Duration
}

type ingestResult struct {
	Target         string           `json:"target"`
	Concurrency    int              `json:"concurrency"`
	BatchSize      int              `json:"batch_size"`
	RateLimit      float64          `json:"rate_limit_events_per_s"`
	Seed           uint64           `json:"seed"`
	DurationS      float64          `json:"duration_s"`
	Requests       int64            `json:"requests"`
	EventsAccepted int64            `json:"events_accepted"`
	RequestsPerS   float64          `json:"requests_per_s"`
	EventsPerS     float64          `json:"events_per_s"`
	Latency        LatencySummary   `json:"latency"`
	Status         map[string]int64 `json:"status_codes"`
	Errors         int64            `json:"transport_errors"`
	ErrorSamples   []string         `json:"error_samples,omitempty"`
	Drain          *drainResult     `json:"drain,omitempty"`
}

type drainResult struct {
	Baseline       int64   `json:"processed_baseline"`
	Target         int64   `json:"processed_target"`
	Reached        int64   `json:"processed_reached"`
	DrainS         float64 `json:"drain_after_send_s"`
	EndToEndS      float64 `json:"end_to_end_s"`
	EndToEndEventS float64 `json:"end_to_end_events_per_s"`
	TimedOut       bool    `json:"timed_out"`
}

// limiter spaces requests so the total event rate stays at or below rate.
type limiter struct {
	mu       sync.Mutex
	interval time.Duration // per event
	next     time.Time
}

func newLimiter(rate float64) *limiter {
	if rate <= 0 {
		return nil
	}
	return &limiter{interval: time.Duration(float64(time.Second) / rate)}
}

func (l *limiter) wait(ctx context.Context, events int) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}
	at := l.next
	l.next = l.next.Add(time.Duration(events) * l.interval)
	l.mu.Unlock()
	if d := time.Until(at); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
	return nil
}

func newHTTPClient(concurrency int) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConns = concurrency * 2
	tr.MaxIdleConnsPerHost = concurrency * 2
	return &http.Client{Transport: tr, Timeout: 30 * time.Second}
}

func runIngest(ctx context.Context, o ingestOptions) (ingestResult, error) {
	if o.Concurrency < 1 || o.BatchSize < 1 || (o.Duration <= 0 && o.MaxEvents <= 0) {
		return ingestResult{}, errors.New("need --concurrency >= 1, --batch-size >= 1 and --duration or --max-events")
	}
	client := newHTTPClient(o.Concurrency)
	res := ingestResult{Target: o.URL, Concurrency: o.Concurrency, BatchSize: o.BatchSize, RateLimit: o.Rate, Seed: o.Seed, Status: map[string]int64{}}

	var baseline int64
	if o.WaitDrain != "" {
		var err error
		if baseline, err = processedTotal(ctx, client, o.WaitDrain); err != nil {
			return res, fmt.Errorf("read processor stats: %w", err)
		}
	}

	runCtx := ctx
	if o.Duration > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, o.Duration)
		defer cancel()
	}

	lim := newLimiter(o.Rate)
	var (
		reserved, accepted, requests, transportErrs atomic.Int64
		mu                                          sync.Mutex
		latencies                                   []time.Duration
		status                                      = map[string]int64{}
		samples                                     []string
		wg                                          sync.WaitGroup
	)
	start := time.Now()
	for w := 0; w < o.Concurrency; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			gen := NewGenerator(o.Seed + uint64(worker))
			var local []time.Duration
			defer func() {
				mu.Lock()
				latencies = append(latencies, local...)
				mu.Unlock()
			}()
			for runCtx.Err() == nil {
				n := o.BatchSize
				if o.MaxEvents > 0 {
					after := reserved.Add(int64(n))
					if after-int64(n) >= o.MaxEvents {
						return
					}
					if after > o.MaxEvents {
						n = int(o.MaxEvents - (after - int64(n)))
					}
				}
				if lim.wait(runCtx, n) != nil {
					return
				}
				body, _ := json.Marshal(gen.Batch(n, time.Now(), o.Spread))
				t0 := time.Now()
				code, got, err := postBatch(runCtx, client, o.URL, o.Key, body)
				if err != nil && runCtx.Err() != nil {
					return // cut off by --duration; not a server error
				}
				local = append(local, time.Since(t0))
				requests.Add(1)
				mu.Lock()
				if err != nil {
					transportErrs.Add(1)
					if len(samples) < 3 {
						samples = append(samples, err.Error())
					}
				} else {
					status[strconv.Itoa(code)]++
				}
				mu.Unlock()
				accepted.Add(int64(got))
			}
		}(w)
	}
	wg.Wait()
	sendDur := time.Since(start)

	res.DurationS = round2(sendDur.Seconds())
	res.Requests = requests.Load()
	res.EventsAccepted = accepted.Load()
	res.RequestsPerS = round2(float64(res.Requests) / sendDur.Seconds())
	res.EventsPerS = round2(float64(res.EventsAccepted) / sendDur.Seconds())
	res.Latency = summarize(latencies)
	res.Status = status
	res.Errors = transportErrs.Load()
	res.ErrorSamples = samples

	if o.WaitDrain != "" {
		d, err := waitDrain(ctx, client, o.WaitDrain, baseline, res.EventsAccepted, sendDur, o.DrainTimeout)
		if err != nil {
			return res, err
		}
		res.Drain = &d
	}
	return res, nil
}

// postBatch sends one batch and returns the status and accepted count.
func postBatch(ctx context.Context, c *http.Client, url, key string, body []byte) (int, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", key)
	resp, err := c.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return resp.StatusCode, 0, nil
	}
	var out struct {
		Accepted int `json:"accepted"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return resp.StatusCode, 0, nil
	}
	return resp.StatusCode, out.Accepted, nil
}

func processedTotal(ctx context.Context, c *http.Client, statsURL string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statsURL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var s struct {
		Processed *int64 `json:"processed_total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil || s.Processed == nil {
		return 0, fmt.Errorf("unexpected stats response from %s", statsURL)
	}
	return *s.Processed, nil
}

// waitDrain polls the processor until it has processed everything that was
// accepted (baseline + accepted) or timeout passes.
func waitDrain(ctx context.Context, c *http.Client, statsURL string, baseline, accepted int64, sendDur, timeout time.Duration) (drainResult, error) {
	d := drainResult{Baseline: baseline, Target: baseline + accepted}
	start := time.Now()
	for {
		n, err := processedTotal(ctx, c, statsURL)
		if err == nil {
			d.Reached = n
			if n >= d.Target {
				break
			}
		}
		if time.Since(start) > timeout {
			d.TimedOut = true
			break
		}
		select {
		case <-ctx.Done():
			return d, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	drain := time.Since(start)
	d.DrainS = round2(drain.Seconds())
	d.EndToEndS = round2((sendDur + drain).Seconds())
	if e2e := (sendDur + drain).Seconds(); e2e > 0 {
		d.EndToEndEventS = round2(float64(d.Reached-d.Baseline) / e2e)
	}
	return d, nil
}

func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

func (r ingestResult) print(w io.Writer) {
	fmt.Fprintf(w, "ingest -> %s\n", r.Target)
	fmt.Fprintf(w, "  concurrency %d, batch size %d, rate limit %v ev/s, seed %d\n", r.Concurrency, r.BatchSize, r.RateLimit, r.Seed)
	fmt.Fprintf(w, "  sent for %.2fs: %d requests (%.1f req/s), %d events accepted (%.0f events/s)\n", r.DurationS, r.Requests, r.RequestsPerS, r.EventsAccepted, r.EventsPerS)
	fmt.Fprintf(w, "  latency %s\n", r.Latency)
	fmt.Fprintf(w, "  status codes %v, transport errors %d\n", r.Status, r.Errors)
	for _, s := range r.ErrorSamples {
		fmt.Fprintf(w, "    error: %s\n", s)
	}
	if d := r.Drain; d != nil {
		state := "drained"
		if d.TimedOut {
			state = "TIMED OUT"
		}
		fmt.Fprintf(w, "  processor %s: processed %d/%d new events, %.2fs after sending stopped; end-to-end %.2fs = %.0f events/s\n",
			state, d.Reached-d.Baseline, d.Target-d.Baseline, d.DrainS, d.EndToEndS, d.EndToEndEventS)
	}
}
