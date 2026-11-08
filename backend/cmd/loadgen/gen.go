package main

import (
	"fmt"
	"math/rand/v2"
	"time"
)

// wireEvent is the ingest API's event JSON.
type wireEvent struct {
	Timestamp  string            `json:"timestamp"`
	Level      string            `json:"level"`
	Service    string            `json:"service"`
	Message    string            `json:"message"`
	TraceID    string            `json:"trace_id,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

type wireBatch struct {
	Events []wireEvent `json:"events"`
}

// services are the simulated applications.
var services = []string{"checkout", "payments", "search", "auth", "inventory", "notifications"}

// levelWeights: mostly info, some debug and warn, few error and fatal.
var levelWeights = []struct {
	level  string
	weight int
}{
	{"info", 70}, {"debug", 10}, {"warn", 13}, {"error", 6}, {"fatal", 1},
}

// templates produce messages with variable parts; each func gets the rng.
type template func(r *rand.Rand) string

var templates = map[string][]template{
	"info": {
		func(r *rand.Rand) string {
			return fmt.Sprintf("GET /api/v1/orders/%d completed in %dms", r.IntN(900000)+100000, r.IntN(120)+3)
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("user %d logged in from 10.%d.%d.%d", r.IntN(50000), r.IntN(256), r.IntN(256), r.IntN(256))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("order %s created with %d items, total %d.%02d EUR", hexID(r, 8), r.IntN(9)+1, r.IntN(500)+5, r.IntN(100))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("search query returned %d results in %dms", r.IntN(2000), r.IntN(80)+2)
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("email %s queued for user %d", hexID(r, 12), r.IntN(50000))
		},
	},
	"debug": {
		func(r *rand.Rand) string { return fmt.Sprintf("cache miss for key product:%d", r.IntN(100000)) },
		func(r *rand.Rand) string {
			return fmt.Sprintf("loaded config from /etc/app/%s.yaml in %dus", []string{"db", "cache", "flags"}[r.IntN(3)], r.IntN(900)+50)
		},
	},
	"warn": {
		func(r *rand.Rand) string {
			return fmt.Sprintf("slow query on table orders took %dms (threshold 500ms)", r.IntN(4000)+500)
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("retrying request to payment gateway, attempt %d of 3", r.IntN(3)+1)
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("rate limit at %d%% for client %s", r.IntN(20)+80, hexID(r, 6))
		},
	},
	"error": {
		func(r *rand.Rand) string {
			return fmt.Sprintf("payment %s declined: gateway timeout after %dms", hexID(r, 10), r.IntN(3000)+5000)
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("failed to write /var/data/exports/%d.json: disk quota exceeded", r.IntN(10000))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("connection to db-%d.internal:5432 refused", r.IntN(4)+1)
		},
	},
	"fatal": {
		func(r *rand.Rand) string {
			return fmt.Sprintf("panic: runtime error: nil pointer dereference in handler %s", []string{"CreateOrder", "Refund", "Login"}[r.IntN(3)])
		},
	},
}

// incidentTemplates are used for the burst of errors in demo --incident.
var incidentTemplates = []template{
	func(r *rand.Rand) string {
		return fmt.Sprintf("upstream bank-api returned 503 for transaction %s", hexID(r, 10))
	},
	func(r *rand.Rand) string {
		return fmt.Sprintf("payment %s declined: gateway timeout after %dms", hexID(r, 10), r.IntN(3000)+5000)
	},
	func(r *rand.Rand) string {
		return fmt.Sprintf("circuit breaker open for bank-api, %d requests rejected", r.IntN(50)+1)
	},
}

func hexID(r *rand.Rand, n int) string {
	const digits = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = digits[r.IntN(16)]
	}
	return string(b)
}

// Generator produces realistic ingest events. The same seed yields the same
// sequence of events (timestamps are supplied by the caller).
type Generator struct {
	r *rand.Rand
	// groupedShare, when in [0,1], is the share of warn, error and fatal
	// events (their relative weights kept); below 0 the default weights apply.
	groupedShare float64
}

// NewGenerator returns a generator seeded with seed.
func NewGenerator(seed uint64) *Generator {
	return &Generator{r: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)), groupedShare: -1}
}

// WithGroupedShare sets the share of warn, error and fatal events (0..1).
func (g *Generator) WithGroupedShare(share float64) *Generator {
	g.groupedShare = share
	return g
}

func (g *Generator) level() string {
	if g.groupedShare >= 0 {
		grouped := g.r.Float64() < g.groupedShare
		total := 0
		for _, lw := range levelWeights {
			if isGrouped(lw.level) == grouped {
				total += lw.weight
			}
		}
		n := g.r.IntN(total)
		for _, lw := range levelWeights {
			if isGrouped(lw.level) != grouped {
				continue
			}
			if n < lw.weight {
				return lw.level
			}
			n -= lw.weight
		}
	}
	n := g.r.IntN(100)
	for _, lw := range levelWeights {
		if n < lw.weight {
			return lw.level
		}
		n -= lw.weight
	}
	return "info"
}

func isGrouped(level string) bool { return level == "warn" || level == "error" || level == "fatal" }

func (g *Generator) attributes() map[string]string {
	attrs := map[string]string{
		"env":    []string{"prod", "prod", "prod", "staging"}[g.r.IntN(4)],
		"region": []string{"eu-1", "eu-2", "us-1"}[g.r.IntN(3)],
	}
	if g.r.IntN(3) == 0 {
		attrs["version"] = fmt.Sprintf("1.%d.%d", g.r.IntN(5), g.r.IntN(20))
	}
	return attrs
}

// Event returns one event with timestamp ts.
func (g *Generator) Event(ts time.Time) wireEvent {
	level := g.level()
	tpls := templates[level]
	ev := wireEvent{
		Timestamp:  ts.UTC().Format(time.RFC3339Nano),
		Level:      level,
		Service:    services[g.r.IntN(len(services))],
		Message:    tpls[g.r.IntN(len(tpls))](g.r),
		Attributes: g.attributes(),
	}
	if g.r.IntN(10) < 4 { // about 40% carry a trace id
		ev.TraceID = "tr-" + hexID(g.r, 16)
	}
	return ev
}

// IncidentEvent returns an error (sometimes fatal) event for service.
func (g *Generator) IncidentEvent(ts time.Time, service string) wireEvent {
	level := "error"
	if g.r.IntN(20) == 0 {
		level = "fatal"
	}
	return wireEvent{
		Timestamp:  ts.UTC().Format(time.RFC3339Nano),
		Level:      level,
		Service:    service,
		Message:    incidentTemplates[g.r.IntN(len(incidentTemplates))](g.r),
		TraceID:    "tr-" + hexID(g.r, 16),
		Attributes: map[string]string{"env": "prod", "region": "eu-1", "incident": "simulated"},
	}
}

// Batch returns n events. With spread > 0, timestamps are spread uniformly
// over [end-spread, end); otherwise every event gets end.
func (g *Generator) Batch(n int, end time.Time, spread time.Duration) wireBatch {
	b := wireBatch{Events: make([]wireEvent, n)}
	for i := range b.Events {
		ts := end
		if spread > 0 {
			ts = end.Add(-time.Duration(g.r.Int64N(int64(spread))))
		}
		b.Events[i] = g.Event(ts)
	}
	return b
}
