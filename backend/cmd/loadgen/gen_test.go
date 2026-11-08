package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/ingest"
)

var fixedEnd = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

func TestGeneratorIsDeterministic(t *testing.T) {
	a := NewGenerator(42).Batch(500, fixedEnd, time.Hour)
	b := NewGenerator(42).Batch(500, fixedEnd, time.Hour)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same seed produced different batches")
	}
	c := NewGenerator(43).Batch(500, fixedEnd, time.Hour)
	if reflect.DeepEqual(a, c) {
		t.Fatal("different seeds produced identical batches")
	}
}

func TestGeneratorDistributions(t *testing.T) {
	const n = 50_000
	b := NewGenerator(7).Batch(n, fixedEnd, 24*time.Hour)
	levels := map[string]int{}
	svc := map[string]int{}
	traced, messages := 0, map[string]bool{}
	for _, e := range b.Events {
		levels[e.Level]++
		svc[e.Service]++
		if e.TraceID != "" {
			traced++
		}
		messages[e.Message] = true
		ts, err := time.Parse(time.RFC3339Nano, e.Timestamp)
		if err != nil || ts.After(fixedEnd) || ts.Before(fixedEnd.Add(-24*time.Hour)) {
			t.Fatalf("timestamp %q outside the 24h spread", e.Timestamp)
		}
	}
	frac := func(c int) float64 { return float64(c) / n }
	checks := []struct {
		name     string
		got      float64
		min, max float64
	}{
		{"info", frac(levels["info"]), 0.67, 0.73},
		{"debug", frac(levels["debug"]), 0.08, 0.12},
		{"warn", frac(levels["warn"]), 0.11, 0.15},
		{"error", frac(levels["error"]), 0.05, 0.07},
		{"fatal", frac(levels["fatal"]), 0.005, 0.015},
		{"traced", frac(traced), 0.37, 0.43},
	}
	for _, c := range checks {
		if c.got < c.min || c.got > c.max {
			t.Errorf("%s fraction = %.3f, want in [%.3f, %.3f]", c.name, c.got, c.min, c.max)
		}
	}
	if len(svc) != len(services) {
		t.Errorf("services seen = %v, want all %d", svc, len(services))
	}
	for s, c := range svc {
		if frac(c) < 0.14 || frac(c) > 0.19 {
			t.Errorf("service %s fraction %.3f, want roughly uniform", s, frac(c))
		}
	}
	if len(messages) < n/2 {
		t.Errorf("only %d distinct messages in %d events; variable parts missing", len(messages), n)
	}
}

// TestPayloadsPassIngestValidation checks generated batches with the real
// ingest decoder and validation rules.
func TestPayloadsPassIngestValidation(t *testing.T) {
	lim := ingest.DefaultLimits()
	g := NewGenerator(99)
	for i := 0; i < 20; i++ {
		b := g.Batch(lim.MaxEvents, fixedEnd, 7*24*time.Hour)
		for j := 0; j < 10; j++ {
			b.Events[j] = g.IncidentEvent(fixedEnd, "payments")
		}
		body, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(body)) > lim.MaxBodyBytes {
			t.Fatalf("batch of %d events is %d bytes, above the %d byte body limit", lim.MaxEvents, len(body), lim.MaxBodyBytes)
		}
		evs, err := ingest.ParseBatch(body, lim)
		if err != nil {
			t.Fatalf("batch %d rejected by ingest validation: %v", i, err)
		}
		if len(evs) != lim.MaxEvents {
			t.Fatalf("parsed %d events, want %d", len(evs), lim.MaxEvents)
		}
	}
}

func TestIncidentEvents(t *testing.T) {
	g := NewGenerator(5)
	for i := 0; i < 1000; i++ {
		e := g.IncidentEvent(fixedEnd, "payments")
		if e.Service != "payments" || (e.Level != "error" && e.Level != "fatal") || e.TraceID == "" {
			t.Fatalf("incident event = %+v", e)
		}
		if !strings.Contains(e.Message, "bank-api") && !strings.Contains(e.Message, "gateway timeout") {
			t.Fatalf("unexpected incident message %q", e.Message)
		}
	}
}

func TestGroupedShare(t *testing.T) {
	for _, share := range []float64{0, 0.05, 0.4, 1} {
		g := NewGenerator(3).WithGroupedShare(share)
		n, grouped := 20000, 0
		for i := 0; i < n; i++ {
			if isGrouped(g.Event(time.Now()).Level) {
				grouped++
			}
		}
		if got := float64(grouped) / float64(n); got < share-0.02 || got > share+0.02 {
			t.Errorf("share %.2f: got %.3f", share, got)
		}
	}
}
