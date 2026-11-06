package ingest

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Event is a validated log event ready to be published. Its JSON form is the
// payload written to the queue (timestamp as RFC3339 in UTC).
type Event struct {
	Timestamp  time.Time         `json:"timestamp"`
	Level      string            `json:"level"`
	Service    string            `json:"service"`
	Message    string            `json:"message"`
	TraceID    string            `json:"trace_id,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// Limits bounds what a single ingest request may contain.
type Limits struct {
	MaxBodyBytes      int64
	MaxEvents         int
	MaxMessageBytes   int
	MaxAttributes     int
	MaxAttrKeyChars   int
	MaxAttrValueChars int
	MaxTraceIDChars   int
}

// DefaultLimits returns the v0.1 contract defaults.
func DefaultLimits() Limits {
	return Limits{
		MaxBodyBytes:      1 << 20,
		MaxEvents:         1000,
		MaxMessageBytes:   8 << 10,
		MaxAttributes:     32,
		MaxAttrKeyChars:   64,
		MaxAttrValueChars: 1024,
		MaxTraceIDChars:   128,
	}
}

// validLevel reports whether l is an accepted Event.Level.
func validLevel(l string) bool {
	switch l {
	case "debug", "info", "warn", "error", "fatal":
		return true
	}
	return false
}

// batchRequest is the wire format of POST /v1/ingest.
type batchRequest struct {
	Events []eventRequest `json:"events"`
}

// eventRequest is the wire format of one event.
type eventRequest struct {
	Timestamp  string            `json:"timestamp"`
	Level      string            `json:"level"`
	Service    string            `json:"service"`
	Message    string            `json:"message"`
	TraceID    string            `json:"trace_id"`
	Attributes map[string]string `json:"attributes"`
}

// ValidationError reports why a batch was rejected. Index is the position of
// the failing event, or -1 when the problem is with the batch as a whole.
type ValidationError struct {
	Index  int
	Reason string
}

func (e *ValidationError) Error() string {
	if e.Index < 0 {
		return e.Reason
	}
	return fmt.Sprintf("events[%d]: %s", e.Index, e.Reason)
}

// validateBatch checks every event against lim and converts the batch to
// Events. It rejects the whole batch on the first invalid event.
func validateBatch(req batchRequest, lim Limits) ([]Event, error) {
	if len(req.Events) == 0 {
		return nil, &ValidationError{Index: -1, Reason: "events must contain at least one event"}
	}
	if len(req.Events) > lim.MaxEvents {
		return nil, &ValidationError{Index: -1, Reason: fmt.Sprintf("too many events: %d, max %d", len(req.Events), lim.MaxEvents)}
	}

	out := make([]Event, len(req.Events))
	for i, er := range req.Events {
		ev, reason := validateEvent(er, lim)
		if reason != "" {
			return nil, &ValidationError{Index: i, Reason: reason}
		}
		out[i] = ev
	}
	return out, nil
}

// validateEvent returns the converted event, or a non-empty reason.
func validateEvent(er eventRequest, lim Limits) (Event, string) {
	if er.Timestamp == "" {
		return Event{}, "timestamp is required"
	}
	ts, err := time.Parse(time.RFC3339Nano, er.Timestamp)
	if err != nil {
		return Event{}, "timestamp must be RFC3339"
	}
	if !validLevel(er.Level) {
		return Event{}, "level must be one of debug, info, warn, error, fatal"
	}
	if strings.TrimSpace(er.Service) == "" {
		return Event{}, "service is required"
	}
	if strings.TrimSpace(er.Message) == "" {
		return Event{}, "message is required"
	}
	if len(er.Message) > lim.MaxMessageBytes {
		return Event{}, fmt.Sprintf("message exceeds %d bytes", lim.MaxMessageBytes)
	}
	if utf8.RuneCountInString(er.TraceID) > lim.MaxTraceIDChars {
		return Event{}, fmt.Sprintf("trace_id exceeds %d characters", lim.MaxTraceIDChars)
	}
	if len(er.Attributes) > lim.MaxAttributes {
		return Event{}, fmt.Sprintf("too many attributes: %d, max %d", len(er.Attributes), lim.MaxAttributes)
	}
	for k, v := range er.Attributes {
		if k == "" {
			return Event{}, "attribute key must not be empty"
		}
		if utf8.RuneCountInString(k) > lim.MaxAttrKeyChars {
			return Event{}, fmt.Sprintf("attribute key %q exceeds %d characters", truncate(k, lim.MaxAttrKeyChars), lim.MaxAttrKeyChars)
		}
		if utf8.RuneCountInString(v) > lim.MaxAttrValueChars {
			return Event{}, fmt.Sprintf("attribute %q value exceeds %d characters", k, lim.MaxAttrValueChars)
		}
	}

	return Event{
		Timestamp:  ts.UTC(),
		Level:      er.Level,
		Service:    er.Service,
		Message:    er.Message,
		TraceID:    er.TraceID,
		Attributes: er.Attributes,
	}, ""
}

// truncate shortens s to at most n runes so error messages stay small.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "..."
}
