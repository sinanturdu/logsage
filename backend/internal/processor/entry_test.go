package processor

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/fingerprint"
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
				// The fixture is an error line, so it is grouped (ADR 0022).
				if row.EventID != "1700000000000-1" || row.ProjectID != "default" || row.Attributes == nil ||
					row.Template != "timeout" || row.Fingerprint != fingerprint.Of("checkout", "timeout", "") {
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
	body, err := encodeRows(rows, time.Date(2026, 11, 6, 9, 0, 1, 500_000_000, time.UTC))
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
		"fingerprint": 0.0, "trace_id": "t1", "attributes": map[string]any{}, "ingested_at": "2026-11-06 09:00:00.000", "inserted_at": "2026-11-06 09:00:01.500",
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

func TestParseEntryProjectID(t *testing.T) {
	tests := []struct {
		project    any
		wantReason string
	}{
		{project: "default"},
		{project: "6f1e2d3c-4b5a-4f60-8a91-0b2c3d4e5f60"},
		{project: "6F1E2D3C-4B5A-4F60-8A91-0B2C3D4E5F60", wantReason: "neither a project UUID"},
		{project: "other-project", wantReason: "neither a project UUID"},
		{project: "' OR 1=1 --", wantReason: "neither a project UUID"},
		{project: "6f1e2d3c4b5a4f608a910b2c3d4e5f60", wantReason: "neither a project UUID"},
		{project: 42, wantReason: "missing project_id"},
	}
	for _, tt := range tests {
		v := validEntry("1-0").Values
		v["project_id"] = tt.project
		row, reason := parseEntry(Entry{ID: "1-0", Values: v})
		if tt.wantReason == "" {
			if reason != "" || row.ProjectID != tt.project {
				t.Errorf("project %v: reason %q, row project %q", tt.project, reason, row.ProjectID)
			}
			continue
		}
		if !strings.Contains(reason, tt.wantReason) {
			t.Errorf("project %v: reason %q, want %q", tt.project, reason, tt.wantReason)
		}
	}
}

func TestParseEntryFingerprint(t *testing.T) {
	entry := func(level, msg string, attrs map[string]string) Entry {
		b, _ := json.Marshal(map[string]any{"timestamp": "2026-11-06T10:15:30Z", "level": level, "service": "payments", "message": msg, "attributes": attrs})
		return Entry{ID: "1-0", Values: map[string]any{"payload": string(b), "project_id": "default", "ingested_at": "2026-11-06T10:15:31.5Z"}}
	}
	cases := []struct {
		level, msg   string
		attrs        map[string]string
		wantTemplate string
		grouped      bool
	}{
		{"error", "payment 7c1e0b9a42 declined: gateway timeout after 7086ms", nil, "payment <id> declined: gateway timeout after <n>ms", true},
		{"warn", "retrying charge for ord_7Q2K9M, attempt 2 of 3", nil, "retrying charge for ord_<id>, attempt <n> of <n>", true},
		{"fatal", "panic: runtime error: index out of range [5] with length 3\ngoroutine 1 [running]:", nil, "panic: runtime error: index out of range [<n>] with length <n>", true},
		{"error", "boom", map[string]string{"error.type": "TimeoutError"}, "boom", true},
		{"info", "user 31337 logged in", nil, "", false},
		{"debug", "cache miss for key product:55123", nil, "", false},
	}
	for _, c := range cases {
		row, reason := parseEntry(entry(c.level, c.msg, c.attrs))
		if reason != "" {
			t.Fatalf("%s: %s", c.msg, reason)
		}
		if row.Template != c.wantTemplate {
			t.Errorf("%q: template %q, want %q", c.msg, row.Template, c.wantTemplate)
		}
		if (row.Fingerprint != 0) != c.grouped {
			t.Errorf("%q: fingerprint %x, grouped %v", c.msg, row.Fingerprint, c.grouped)
		}
		if c.grouped && row.Fingerprint != fingerprint.Of("payments", c.wantTemplate, fingerprint.ErrorType(c.attrs)) {
			t.Errorf("%q: fingerprint does not match fingerprint.Of", c.msg)
		}
	}
	// The JSON row for ClickHouse carries both.
	row, _ := parseEntry(entry("error", "boom 42", nil))
	b, err := encodeRows([]Row{row}, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Fingerprint uint64 `json:"fingerprint"`
		Template    string `json:"template"`
	}
	if err := json.Unmarshal(b, &got); err != nil || got.Template != "boom <n>" || got.Fingerprint != row.Fingerprint {
		t.Fatalf("encoded = %s (%v)", b, err)
	}
}
