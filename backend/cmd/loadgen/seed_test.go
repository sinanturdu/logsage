package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

var seedNow = time.Date(2026, 11, 7, 2, 17, 0, 0, time.UTC)

func TestPlanSeed(t *testing.T) {
	o := defaultSeedOptions()
	events := planSeed(o, seedNow)
	if len(events) > maxSeedEvents || len(events) < 54_000 {
		t.Fatalf("%d events", len(events))
	}
	if d := len(events) - o.expectedEvents(); d < -1500 || d > 1500 {
		t.Errorf("%d events, expected about %d", len(events), o.expectedEvents())
	}
	incStart, incEnd := seedNow.Add(-o.IncidentAgo), seedNow.Add(-o.IncidentAgo+o.IncidentFor)
	byService := map[string]int{}
	traces := map[string]map[string]bool{}
	for _, e := range events {
		ts, err := time.Parse(time.RFC3339Nano, e.Timestamp)
		if err != nil {
			t.Fatal(err)
		}
		if ts.Before(seedNow.Add(-o.Window)) || !ts.Before(seedNow) {
			t.Fatalf("event at %s outside the last hour", ts)
		}
		if strings.HasPrefix(e.Message, "payment gateway timeout") {
			// Retries are stamped up to 1.2s before their timeout.
			if ts.Before(incStart) || !ts.Before(incEnd) {
				t.Fatalf("incident event at %s outside %s..%s", ts, incStart, incEnd)
			}
			byService["timeout"]++
			if traces[e.TraceID] == nil {
				traces[e.TraceID] = map[string]bool{}
			}
		}
		if e.Message == "charge failed: upstream payments returned 504" {
			byService["checkout"]++
		}
		if strings.HasPrefix(e.Message, "POST /v1/checkout 502") {
			byService["api"]++
		}
	}
	// The cascade: checkout and api errors follow the payments timeouts and
	// share their trace ids.
	for _, e := range events {
		if (e.Service == "checkout" || e.Service == "api") && e.Level == "error" && traces[e.TraceID] != nil {
			traces[e.TraceID][e.Service] = true
		}
	}
	cascaded := 0
	for _, s := range traces {
		if s["checkout"] || s["api"] {
			cascaded++
		}
	}
	if byService["timeout"] < 4000 || byService["checkout"] < byService["timeout"]/3 || byService["api"] < byService["timeout"]/5 {
		t.Errorf("incident counts %v", byService)
	}
	if cascaded < len(traces)/2 {
		t.Errorf("%d of %d timeouts cascaded with a shared trace id", cascaded, len(traces))
	}

	// Deterministic for a seed and now.
	again := planSeed(o, seedNow)
	if len(again) != len(events) || again[100].Message != events[100].Message || again[len(again)-1].Timestamp != events[len(events)-1].Timestamp {
		t.Error("planSeed is not deterministic")
	}
}

func TestIncidentShape(t *testing.T) {
	for f, want := range map[float64]float64{-0.1: 0, 0: 0, 0.1: 0.5, 0.2: 1, 0.5: 1, 0.875: 0.5, 1: 0} {
		if got := incidentShape(f); got < want-1e-9 || got > want+1e-9 {
			t.Errorf("shape(%v) = %v, want %v", f, got, want)
		}
	}
}

func TestSeedOptionsValidate(t *testing.T) {
	if err := defaultSeedOptions().validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*seedOptions){
		"incident older than 15m": func(o *seedOptions) { o.IncidentAgo = 20 * time.Minute },
		"incident ends in future": func(o *seedOptions) { o.IncidentAgo, o.IncidentFor = 2*time.Minute, 5*time.Minute },
		"too many events":         func(o *seedOptions) { o.Rate = 100 },
		"batch too big":           func(o *seedOptions) { o.Batch = 5000 },
		"zero window":             func(o *seedOptions) { o.Window = 0 },
	} {
		o := defaultSeedOptions()
		mut(&o)
		if o.validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRunSeedRetriesBackpressure(t *testing.T) {
	var mu sync.Mutex
	got, calls := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if r.Header.Get("X-API-Key") != "k" {
			w.WriteHeader(401)
			return
		}
		if calls == 2 { // one 429 on the way
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		var b wireBatch
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil || len(b.Events) > 100 {
			w.WriteHeader(400)
			return
		}
		got += len(b.Events)
		w.WriteHeader(202)
		_ = json.NewEncoder(w).Encode(map[string]int{"accepted": len(b.Events)})
	}))
	defer srv.Close()
	o := defaultSeedOptions()
	o.URL, o.Key, o.Window, o.Rate, o.IncidentRate, o.Batch = srv.URL, "k", 10*time.Minute, 1, 2, 100
	var out strings.Builder
	if err := runSeed(context.Background(), o, seedNow, srv.Client(), &out); err != nil {
		t.Fatal(err)
	}
	want := len(planSeed(o, seedNow))
	if got != want || !strings.Contains(out.String(), "events accepted") {
		t.Fatalf("server got %d events, want %d; output %q", got, want, out.String())
	}

	o.Key = "bad"
	if err := runSeed(context.Background(), o, seedNow, srv.Client(), &out); err == nil || !strings.Contains(err.Error(), "status 401") {
		t.Fatalf("401 must stop the seed: %v", err)
	}
}
