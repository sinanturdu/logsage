package processor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Entry is one Redis stream entry as read from the consumer group.
type Entry struct {
	ID     string         // stream entry ID, e.g. 1793990495049-0
	Values map[string]any // field -> value (strings for entries written by ingest)
}

// Row is one row of the ClickHouse logs table.
type Row struct {
	ProjectID   string
	EventID     string
	Timestamp   time.Time
	Level       string
	Service     string
	Message     string
	Fingerprint uint64
	TraceID     string
	Attributes  map[string]string
	IngestedAt  time.Time
}

// payload is the event JSON written by the ingest API. Unknown fields are
// ignored so a newer ingest version cannot dead-letter everything.
type payload struct {
	Timestamp  string            `json:"timestamp"`
	Level      string            `json:"level"`
	Service    string            `json:"service"`
	Message    string            `json:"message"`
	TraceID    string            `json:"trace_id"`
	Attributes map[string]string `json:"attributes"`
}

// parseEntry converts a stream entry into a Row. A non-empty reason means the
// entry is unusable and must be dead-lettered. Reasons never contain event
// contents.
func parseEntry(e Entry) (Row, string) {
	if len(e.Values) == 0 {
		return Row{}, "entry has no fields (deleted or trimmed before processing)"
	}
	raw, ok := stringField(e.Values, "payload")
	if !ok || raw == "" {
		return Row{}, "missing payload field"
	}
	projectID, _ := stringField(e.Values, "project_id")
	if strings.TrimSpace(projectID) == "" {
		return Row{}, "missing project_id field"
	}
	ingestedRaw, _ := stringField(e.Values, "ingested_at")
	ingestedAt, err := time.Parse(time.RFC3339Nano, ingestedRaw)
	if err != nil {
		return Row{}, "ingested_at is not RFC3339"
	}

	var p payload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return Row{}, "payload is not a valid event JSON object"
	}
	ts, err := time.Parse(time.RFC3339Nano, p.Timestamp)
	if err != nil {
		return Row{}, "payload timestamp is not RFC3339"
	}
	switch p.Level {
	case "debug", "info", "warn", "error", "fatal":
	default:
		return Row{}, "payload level is not one of debug, info, warn, error, fatal"
	}
	if strings.TrimSpace(p.Service) == "" {
		return Row{}, "payload service is empty"
	}
	if strings.TrimSpace(p.Message) == "" {
		return Row{}, "payload message is empty"
	}

	attrs := p.Attributes
	if attrs == nil {
		attrs = map[string]string{}
	}
	return Row{
		ProjectID:   projectID,
		EventID:     e.ID,
		Timestamp:   ts.UTC(),
		Level:       p.Level,
		Service:     p.Service,
		Message:     p.Message,
		Fingerprint: 0, // computed in a later milestone
		TraceID:     p.TraceID,
		Attributes:  attrs,
		IngestedAt:  ingestedAt.UTC(),
	}, ""
}

func stringField(v map[string]any, key string) (string, bool) {
	s, ok := v[key].(string)
	return s, ok
}

// chTime is the DateTime64(3, 'UTC') text format ClickHouse parses by default.
const chTime = "2006-01-02 15:04:05.000"

// jsonRow is the JSONEachRow shape of Row.
type jsonRow struct {
	ProjectID   string            `json:"project_id"`
	EventID     string            `json:"event_id"`
	Timestamp   string            `json:"timestamp"`
	Level       string            `json:"level"`
	Service     string            `json:"service"`
	Message     string            `json:"message"`
	Fingerprint uint64            `json:"fingerprint"`
	TraceID     string            `json:"trace_id"`
	Attributes  map[string]string `json:"attributes"`
	IngestedAt  string            `json:"ingested_at"`
}

// encodeRows renders rows as JSONEachRow (one object per line).
func encodeRows(rows []Row) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for i, r := range rows {
		attrs := r.Attributes
		if attrs == nil {
			attrs = map[string]string{}
		}
		if err := enc.Encode(jsonRow{
			ProjectID:   r.ProjectID,
			EventID:     r.EventID,
			Timestamp:   r.Timestamp.UTC().Format(chTime),
			Level:       r.Level,
			Service:     r.Service,
			Message:     r.Message,
			Fingerprint: r.Fingerprint,
			TraceID:     r.TraceID,
			Attributes:  attrs,
			IngestedAt:  r.IngestedAt.UTC().Format(chTime),
		}); err != nil {
			return nil, fmt.Errorf("encode row %d: %w", i, err)
		}
	}
	return buf.Bytes(), nil
}
