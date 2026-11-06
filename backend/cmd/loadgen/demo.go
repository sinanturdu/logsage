package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

type demoOptions struct {
	URL              string
	Key              string
	Rate             int // events/s of normal traffic
	Seed             uint64
	Duration         time.Duration // 0: until interrupted
	Incident         bool
	IncidentService  string
	IncidentAfter    time.Duration
	IncidentDuration time.Duration
	IncidentRate     int // extra error events/s during the incident
}

// runDemo sends one batch per second: Rate normal events, plus IncidentRate
// error events for IncidentService while the incident window is active.
func runDemo(ctx context.Context, o demoOptions, w io.Writer) error {
	if o.Rate < 1 {
		return fmt.Errorf("--rate must be at least 1")
	}
	if o.Duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Duration)
		defer cancel()
	}
	client := newHTTPClient(1)
	gen := NewGenerator(o.Seed)
	start := time.Now()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	var sent, incidentSent, failed int64
	lastReport := start
	fmt.Fprintf(w, "demo -> %s: %d events/s", o.URL, o.Rate)
	if o.Incident {
		fmt.Fprintf(w, ", incident in %q from +%s for %s at +%d errors/s", o.IncidentService, o.IncidentAfter, o.IncidentDuration, o.IncidentRate)
	}
	fmt.Fprintln(w, " (Ctrl-C to stop)")

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintf(w, "demo stopped after %s: %d events sent (%d incident), %d failed requests\n", time.Since(start).Round(time.Second), sent, incidentSent, failed)
			return nil
		case now := <-tick.C:
			b := gen.Batch(o.Rate, now, time.Second)
			elapsed := now.Sub(start)
			inIncident := o.Incident && elapsed >= o.IncidentAfter && elapsed < o.IncidentAfter+o.IncidentDuration
			if inIncident {
				for i := 0; i < o.IncidentRate; i++ {
					b.Events = append(b.Events, gen.IncidentEvent(now.Add(-time.Duration(i)*time.Second/time.Duration(o.IncidentRate+1)), o.IncidentService))
				}
			}
			body, _ := json.Marshal(b)
			code, got, err := postBatch(ctx, client, o.URL, o.Key, body)
			if err != nil || code != 202 {
				if ctx.Err() == nil {
					failed++
				}
				continue
			}
			sent += int64(got)
			if inIncident {
				incidentSent += int64(o.IncidentRate)
			}
			if now.Sub(lastReport) >= 10*time.Second {
				state := "normal"
				if inIncident {
					state = "INCIDENT"
				}
				fmt.Fprintf(w, "  +%s %s: %d events sent (%d incident), %d failed requests\n", elapsed.Round(time.Second), state, sent, incidentSent, failed)
				lastReport = now
			}
		}
	}
}
