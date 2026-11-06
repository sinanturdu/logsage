package ingest

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func validEvent() eventRequest {
	return eventRequest{
		Timestamp:  "2026-11-06T10:15:30.123Z",
		Level:      "error",
		Service:    "checkout",
		Message:    "payment gateway timeout",
		TraceID:    "abc123",
		Attributes: map[string]string{"env": "prod"},
	}
}

func manyAttrs(n int) map[string]string {
	m := make(map[string]string, n)
	for i := 0; i < n; i++ {
		m[fmt.Sprintf("k%d", i)] = "v"
	}
	return m
}

func TestValidateEvent(t *testing.T) {
	lim := DefaultLimits()

	tests := []struct {
		name       string
		mutate     func(*eventRequest)
		wantReason string // substring; empty means valid
	}{
		{name: "valid", mutate: func(*eventRequest) {}},
		{name: "valid without optional fields", mutate: func(e *eventRequest) { e.TraceID = ""; e.Attributes = nil }},
		{name: "rfc3339 with offset", mutate: func(e *eventRequest) { e.Timestamp = "2026-11-06T12:15:30+02:00" }},
		{name: "rfc3339 without fraction", mutate: func(e *eventRequest) { e.Timestamp = "2026-11-06T10:15:30Z" }},
		{name: "missing timestamp", mutate: func(e *eventRequest) { e.Timestamp = "" }, wantReason: "timestamp is required"},
		{name: "non-rfc3339 timestamp", mutate: func(e *eventRequest) { e.Timestamp = "2026-11-06 10:15:30" }, wantReason: "timestamp must be RFC3339"},
		{name: "unix timestamp", mutate: func(e *eventRequest) { e.Timestamp = "1762424130" }, wantReason: "timestamp must be RFC3339"},
		{name: "each level accepted", mutate: func(e *eventRequest) { e.Level = "fatal" }},
		{name: "missing level", mutate: func(e *eventRequest) { e.Level = "" }, wantReason: "level must be one of"},
		{name: "unknown level", mutate: func(e *eventRequest) { e.Level = "warning" }, wantReason: "level must be one of"},
		{name: "uppercase level", mutate: func(e *eventRequest) { e.Level = "ERROR" }, wantReason: "level must be one of"},
		{name: "missing service", mutate: func(e *eventRequest) { e.Service = "" }, wantReason: "service is required"},
		{name: "blank service", mutate: func(e *eventRequest) { e.Service = "   " }, wantReason: "service is required"},
		{name: "missing message", mutate: func(e *eventRequest) { e.Message = "" }, wantReason: "message is required"},
		{name: "message at limit", mutate: func(e *eventRequest) { e.Message = strings.Repeat("a", lim.MaxMessageBytes) }},
		{name: "message over limit", mutate: func(e *eventRequest) { e.Message = strings.Repeat("a", lim.MaxMessageBytes+1) }, wantReason: "message exceeds 8192 bytes"},
		{name: "trace_id at limit", mutate: func(e *eventRequest) { e.TraceID = strings.Repeat("t", 128) }},
		{name: "trace_id over limit", mutate: func(e *eventRequest) { e.TraceID = strings.Repeat("t", 129) }, wantReason: "trace_id exceeds 128 characters"},
		{name: "multibyte trace_id counted in characters", mutate: func(e *eventRequest) { e.TraceID = strings.Repeat("ü", 128) }},
		{name: "attributes at limit", mutate: func(e *eventRequest) { e.Attributes = manyAttrs(lim.MaxAttributes) }},
		{name: "too many attributes", mutate: func(e *eventRequest) { e.Attributes = manyAttrs(lim.MaxAttributes + 1) }, wantReason: "too many attributes: 33, max 32"},
		{name: "empty attribute key", mutate: func(e *eventRequest) { e.Attributes = map[string]string{"": "v"} }, wantReason: "attribute key must not be empty"},
		{name: "attribute key at limit", mutate: func(e *eventRequest) { e.Attributes = map[string]string{strings.Repeat("k", 64): "v"} }},
		{name: "attribute key over limit", mutate: func(e *eventRequest) { e.Attributes = map[string]string{strings.Repeat("k", 65): "v"} }, wantReason: "exceeds 64 characters"},
		{name: "multibyte key counted in characters", mutate: func(e *eventRequest) { e.Attributes = map[string]string{strings.Repeat("é", 64): "v"} }},
		{name: "attribute value at limit", mutate: func(e *eventRequest) { e.Attributes = map[string]string{"k": strings.Repeat("v", 1024)} }},
		{name: "attribute value over limit", mutate: func(e *eventRequest) { e.Attributes = map[string]string{"k": strings.Repeat("v", 1025)} }, wantReason: `attribute "k" value exceeds 1024 characters`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			er := validEvent()
			tt.mutate(&er)
			ev, reason := validateEvent(er, lim)
			if tt.wantReason == "" {
				if reason != "" {
					t.Fatalf("validateEvent() reason = %q, want valid", reason)
				}
				if ev.Service != er.Service || ev.Message != er.Message || ev.Level != er.Level {
					t.Errorf("validateEvent() = %+v, fields not copied from %+v", ev, er)
				}
				return
			}
			if !strings.Contains(reason, tt.wantReason) {
				t.Fatalf("validateEvent() reason = %q, want it to contain %q", reason, tt.wantReason)
			}
		})
	}
}

func TestValidateEventTimestampUTC(t *testing.T) {
	ev, reason := validateEvent(eventRequest{
		Timestamp: "2026-11-06T12:15:30+02:00", Level: "info", Service: "s", Message: "m",
	}, DefaultLimits())
	if reason != "" {
		t.Fatalf("unexpected reason %q", reason)
	}
	want := time.Date(2026, 11, 6, 10, 15, 30, 0, time.UTC)
	if !ev.Timestamp.Equal(want) || ev.Timestamp.Location() != time.UTC {
		t.Errorf("Timestamp = %v, want %v in UTC", ev.Timestamp, want)
	}
}

func TestValidateBatch(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxEvents = 3

	bad := validEvent()
	bad.Level = "nope"

	tests := []struct {
		name      string
		events    []eventRequest
		wantN     int
		wantIndex int    // checked only when wantErr is set
		wantErr   string // substring of err.Error(); empty means valid
	}{
		{name: "single event", events: []eventRequest{validEvent()}, wantN: 1},
		{name: "at max events", events: []eventRequest{validEvent(), validEvent(), validEvent()}, wantN: 3},
		{name: "empty batch", events: nil, wantIndex: -1, wantErr: "at least one event"},
		{name: "too many events", events: []eventRequest{validEvent(), validEvent(), validEvent(), validEvent()}, wantIndex: -1, wantErr: "too many events: 4, max 3"},
		{name: "first event invalid", events: []eventRequest{bad, validEvent()}, wantIndex: 0, wantErr: "events[0]: level"},
		{name: "later event invalid rejects batch", events: []eventRequest{validEvent(), validEvent(), bad}, wantIndex: 2, wantErr: "events[2]: level"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateBatch(batchRequest{Events: tt.events}, lim)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateBatch() error = %v", err)
				}
				if len(got) != tt.wantN {
					t.Fatalf("len = %d, want %d", len(got), tt.wantN)
				}
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("validateBatch() error = %v, want *ValidationError", err)
			}
			if ve.Index != tt.wantIndex {
				t.Errorf("Index = %d, want %d", ve.Index, tt.wantIndex)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
			if got != nil {
				t.Errorf("events = %v, want nil on error", got)
			}
		})
	}
}
