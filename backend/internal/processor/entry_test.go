package processor

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseEntry(t *testing.T) {
	good := validEntry("1-0").Values
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for kk, vv := range good {
			m[kk] = vv
		}
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
		return m
	}
	payload := func(mutate func(map[string]any)) string {
		var p map[string]any
		_ = json.Unmarshal([]byte(good["payload"].(string)), &p)
		mutate(p)
		b, _ := json.Marshal(p)
		return string(b)
	}

	tests := []struct {
		name       string
		values     map[string]any
		wantReason string // substring; empty means valid
	}{
		{name: "valid", values: good},
		{name: "unknown payload fields ignored", values: with("payload", payload(func(p map[string]any) { p["new_field"] = 1 }))},
		{name: "no attributes", values: with("payload", payload(func(p map[string]any) { delete(p, "attributes") }))},
		{name: "no fields (trimmed)", values: map[string]any{}, wantReason: "no fields"},
		{name: "missing payload", values: with("payload", nil), wantReason: "missing payload"},
		{name: "non-string payload", values: with("payload", 42), wantReason: "missing payload"},
		{name: "missing project", values: with("project_id", nil), wantReason: "missing project_id"},
		{name: "bad ingested_at", values: with("ingested_at", "yesterday"), wantReason: "ingested_at"},
		{name: "payload not json", values: with("payload", "{"), wantReason: "not a valid event JSON"},
		{name: "payload array", values: with("payload", "[]"), wantReason: "not a valid event JSON"},
		{name: "bad timestamp", values: with("payload", payload(func(p map[string]any) { p["timestamp"] = "now" })), wantReason: "timestamp"},
		{name: "bad level", values: with("payload", payload(func(p map[string]any) { p["level"] = "loud" })), wantReason: "level"},
		{name: "empty service", values: with("payload", payload(func(p map[string]any) { p["service"] = " " })), wantReason: "service"},
		{name: "empty message", values: with("payload", payload(func(p map[string]any) { p["message"] = "" })), wantReason: "message"},
		{name: "attribute not string", values: with("payload", payload(func(p map[string]any) { p["attributes"] = map[string]any{"n": 1} })), wantReason: "not a valid event JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row, reason := parseEntry(Entry{ID: "1700000000000-1", Values: tt.values})
			if tt.wantReason == "" {
				if reason != "" {
					t.Fatalf("reason = %q, want valid", reason)
				}
				if row.EventID != "1700000000000-1" || row.ProjectID != "default" || row.Fingerprint != 0 || row.Attributes == nil {
					t.Errorf("row = %+v", row)
				}
				return
			}
			if !strings.Contains(reason, tt.wantReason) {
				t.Fatalf("reason = %q, want it to contain %q", reason, tt.wantReason)
			}
			if strings.Contains(reason, "timeout") || strings.Contains(reason, "checkout") {
				t.Errorf("reason leaks event contents: %q", reason)
			}
		})
	}
}

func TestEncodeRows(t *testing.T) {
	rows := []Row{{
		ProjectID: "default", EventID: "1-0",
		Timestamp: time.Date(2026, 11, 6, 12, 15, 30, 123_456_789, time.FixedZone("TRT", 3*3600)),
		Level:     "error", Service: "checkout", Message: "a \"quoted\" <msg>\nline2",
		TraceID: "t1", IngestedAt: time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC),
	}}
	body, err := encodeRows(rows)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("lines = %d, want 1 per row (newlines in message must be escaped)", len(lines))
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"project_id": "default", "event_id": "1-0", "timestamp": "2026-11-06 09:15:30.123",
		"level": "error", "service": "checkout", "message": "a \"quoted\" <msg>\nline2",
		"fingerprint": 0.0, "trace_id": "t1", "attributes": map[string]any{}, "ingested_at": "2026-11-06 09:00:00.000",
	}
	for k, v := range want {
		if gv, ok := got[k]; !ok || !equalJSON(gv, v) {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
}

func equalJSON(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}
