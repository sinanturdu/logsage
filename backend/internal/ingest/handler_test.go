package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testKey = "test-key-0123456789"

type failingPublisher struct{}

func (failingPublisher) Publish(context.Context, []Event) error {
	return errors.New("queue unavailable")
}

func batchJSON(n int) string {
	ev := `{"timestamp":"2026-11-06T10:15:30.123Z","level":"info","service":"checkout","message":"ok","trace_id":"abc","attributes":{"env":"prod"}}`
	parts := make([]string, n)
	for i := range parts {
		parts[i] = ev
	}
	return `{"events":[` + strings.Join(parts, ",") + `]}`
}

func TestHandler(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxBodyBytes = 4096
	lim.MaxEvents = 5

	tests := []struct {
		name        string
		method      string
		key         *string // nil: send testKey
		noKey       bool    // omit the X-API-Key header
		contentType string
		body        string
		pub         Publisher
		wantStatus  int
		wantError   string // substring of "error"; empty for 202
		wantIndex   *int
		wantAccept  int
	}{
		{name: "accepted single", wantStatus: http.StatusAccepted, body: batchJSON(1), wantAccept: 1},
		{name: "accepted at max events", wantStatus: http.StatusAccepted, body: batchJSON(5), wantAccept: 5},
		{name: "content type with charset", contentType: "application/json; charset=utf-8", body: batchJSON(2), wantStatus: http.StatusAccepted, wantAccept: 2},

		{name: "GET not allowed", method: http.MethodGet, wantStatus: http.StatusMethodNotAllowed, wantError: "use POST"},
		{name: "PUT not allowed", method: http.MethodPut, body: batchJSON(1), wantStatus: http.StatusMethodNotAllowed, wantError: "use POST"},

		{name: "missing key", noKey: true, body: batchJSON(1), wantStatus: http.StatusUnauthorized, wantError: "API key"},
		{name: "empty key", key: ptr(""), body: batchJSON(1), wantStatus: http.StatusUnauthorized, wantError: "API key"},
		{name: "wrong key", key: ptr("wrong"), body: batchJSON(1), wantStatus: http.StatusUnauthorized, wantError: "API key"},
		{name: "key prefix", key: ptr(testKey[:5]), body: batchJSON(1), wantStatus: http.StatusUnauthorized, wantError: "API key"},
		{name: "auth checked before body", key: ptr("wrong"), body: "not json", contentType: "text/plain", wantStatus: http.StatusUnauthorized, wantError: "API key"},

		{name: "missing content type", contentType: "-", body: batchJSON(1), wantStatus: http.StatusUnsupportedMediaType, wantError: "application/json"},
		{name: "text content type", contentType: "text/plain", body: batchJSON(1), wantStatus: http.StatusUnsupportedMediaType, wantError: "application/json"},
		{name: "malformed content type", contentType: "application/json; ;", body: batchJSON(1), wantStatus: http.StatusUnsupportedMediaType, wantError: "application/json"},

		{name: "body too large", body: `{"events":[],"pad":"` + strings.Repeat("x", 5000) + `"}`, wantStatus: http.StatusRequestEntityTooLarge, wantError: "exceeds 4096 bytes"},

		{name: "empty body", body: "", wantStatus: http.StatusBadRequest, wantError: "empty"},
		{name: "malformed json", body: `{"events":[`, wantStatus: http.StatusBadRequest, wantError: "invalid JSON"},
		{name: "unknown field", body: `{"events":[],"extra":1}`, wantStatus: http.StatusBadRequest, wantError: "unknown field"},
		{name: "trailing data", body: batchJSON(1) + `{}`, wantStatus: http.StatusBadRequest, wantError: "unexpected data"},
		{name: "non-string attribute", body: `{"events":[{"timestamp":"2026-11-06T10:15:30Z","level":"info","service":"s","message":"m","attributes":{"n":1}}]}`, wantStatus: http.StatusBadRequest, wantError: "invalid JSON"},
		{name: "no events", body: `{"events":[]}`, wantStatus: http.StatusBadRequest, wantError: "at least one event"},
		{name: "too many events", body: batchJSON(6), wantStatus: http.StatusBadRequest, wantError: "too many events"},
		{
			name:       "invalid event reports index",
			body:       `{"events":[` + strings.TrimSuffix(strings.TrimPrefix(batchJSON(1), `{"events":[`), `]}`) + `,{"timestamp":"yesterday","level":"info","service":"s","message":"m"}]}`,
			wantStatus: http.StatusBadRequest, wantError: "events[1]: timestamp must be RFC3339", wantIndex: ptr(1),
		},

		{name: "publisher failure", body: batchJSON(1), pub: failingPublisher{}, wantStatus: http.StatusServiceUnavailable, wantError: "retry later"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pub := tt.pub
			mem := NewMemoryPublisher(0)
			if pub == nil {
				pub = mem
			}
			var logs bytes.Buffer
			h, err := NewHandler(pub, testKey, lim, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			if err != nil {
				t.Fatalf("NewHandler: %v", err)
			}
			mux := http.NewServeMux()
			h.Register(mux)

			method := tt.method
			if method == "" {
				method = http.MethodPost
			}
			req := httptest.NewRequest(method, Path, strings.NewReader(tt.body))
			switch {
			case tt.noKey:
			case tt.key != nil:
				req.Header.Set("X-API-Key", *tt.key)
			default:
				req.Header.Set("X-API-Key", testKey)
			}
			switch tt.contentType {
			case "":
				req.Header.Set("Content-Type", "application/json")
			case "-":
			default:
				req.Header.Set("Content-Type", tt.contentType)
			}

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			if strings.Contains(logs.String(), testKey) {
				t.Errorf("logs contain the API key: %s", logs.String())
			}

			if tt.wantStatus == http.StatusAccepted {
				var got map[string]int
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("decode body: %v", err)
				}
				if got["accepted"] != tt.wantAccept {
					t.Errorf("accepted = %d, want %d", got["accepted"], tt.wantAccept)
				}
				if n := len(mem.Events()); n != tt.wantAccept {
					t.Errorf("published %d events, want %d", n, tt.wantAccept)
				}
				return
			}

			var got struct {
				Error string `json:"error"`
				Index *int   `json:"index"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
			}
			if !strings.Contains(got.Error, tt.wantError) {
				t.Errorf("error = %q, want it to contain %q", got.Error, tt.wantError)
			}
			if fmt.Sprint(deref(got.Index)) != fmt.Sprint(deref(tt.wantIndex)) {
				t.Errorf("index = %v, want %v", deref(got.Index), deref(tt.wantIndex))
			}
			if n := len(mem.Events()); n != 0 {
				t.Errorf("published %d events on error, want 0", n)
			}
			if tt.wantStatus == http.StatusMethodNotAllowed && rec.Header().Get("Allow") != http.MethodPost {
				t.Errorf("Allow = %q, want POST", rec.Header().Get("Allow"))
			}
		})
	}
}

func TestHandlerBodyTooLarge(t *testing.T) {
	lim := DefaultLimits()
	lim.MaxBodyBytes = 64

	tests := []struct {
		name          string
		contentLength int64 // -1 simulates a chunked body with unknown length
	}{
		{name: "declared content length over limit", contentLength: int64(len(batchJSON(1)))},
		{name: "unknown length, body over limit", contentLength: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pub := NewMemoryPublisher(0)
			h, err := NewHandler(pub, testKey, lim, nil)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, Path, strings.NewReader(batchJSON(1)))
			req.ContentLength = tt.contentLength
			req.Header.Set("X-API-Key", testKey)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413 (body %s)", rec.Code, rec.Body.String())
			}
			if n := len(pub.Events()); n != 0 {
				t.Errorf("published %d events, want 0", n)
			}
		})
	}
}

func TestNewHandlerRejectsBadArgs(t *testing.T) {
	tests := []struct {
		name string
		pub  Publisher
		key  string
	}{
		{name: "nil publisher", pub: nil, key: testKey},
		{name: "empty key", pub: NewMemoryPublisher(0), key: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewHandler(tt.pub, tt.key, DefaultLimits(), nil); err == nil {
				t.Fatal("NewHandler() error = nil, want error")
			}
		})
	}
}

func TestMemoryPublisherCap(t *testing.T) {
	p := NewMemoryPublisher(3)
	for i := 0; i < 5; i++ {
		if err := p.Publish(context.Background(), []Event{{Message: fmt.Sprint(i)}}); err != nil {
			t.Fatal(err)
		}
	}
	got := p.Events()
	if len(got) != 3 || got[0].Message != "2" || got[2].Message != "4" {
		t.Errorf("Events() = %+v, want messages 2..4", got)
	}
}

func ptr[T any](v T) *T { return &v }

func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
