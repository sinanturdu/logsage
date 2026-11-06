package query

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/storage/clickhouse"
)

const testKey = "query-key-0123456789"

// fakeRow is a stored row in the fake store.
type fakeRow struct {
	project, id, level, service, message, trace string
	ts                                          time.Time
}

// fakeStore evaluates the statement parameters against in-memory rows the
// way the real query does (scope, range, keyset, order, limit), and records
// every call.
type fakeStore struct {
	mu       sync.Mutex
	rows     []fakeRow
	err      error
	calls    int
	sql      string
	params   map[string]string
	settings map[string]string
}

func (s *fakeStore) QueryWithParams(_ context.Context, sql string, params, settings map[string]string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.sql, s.params, s.settings = sql, params, settings
	if s.err != nil {
		return nil, s.err
	}
	parse := func(k string) time.Time {
		t, _ := time.ParseInLocation(chTime, params[k], time.UTC)
		return t
	}
	from, to := parse("from"), parse("to")
	var match []fakeRow
	for _, r := range s.rows {
		if r.project != params["project_id"] || r.ts.Before(from) || !r.ts.Before(to) {
			continue
		}
		if v, ok := params["service"]; ok && r.service != v {
			continue
		}
		if v, ok := params["trace_id"]; ok && r.trace != v {
			continue
		}
		if v, ok := params["q"]; ok && !strings.Contains(strings.ToLower(r.message), strings.ToLower(v)) {
			continue
		}
		if v, ok := params["levels"]; ok && !strings.Contains(v, "'"+r.level+"'") {
			continue
		}
		if cid, ok := params["cursor_id"]; ok {
			cts := parse("cursor_ts")
			if !(r.ts.Before(cts) || (r.ts.Equal(cts) && r.id < cid)) {
				continue
			}
		}
		match = append(match, r)
	}
	sort.Slice(match, func(i, j int) bool {
		if !match[i].ts.Equal(match[j].ts) {
			return match[i].ts.After(match[j].ts)
		}
		return match[i].id > match[j].id
	})
	limit, _ := strconv.Atoi(params["limit"])
	if len(match) > limit {
		match = match[:limit]
	}
	var buf bytes.Buffer
	for _, r := range match {
		_ = json.NewEncoder(&buf).Encode(map[string]any{
			"event_id": r.id, "timestamp": r.ts.Format(chTime), "level": r.level, "service": r.service,
			"message": r.message, "trace_id": r.trace, "attributes": map[string]string{"k": "v"},
		})
	}
	return buf.Bytes(), nil
}

func (s *fakeStore) Ready(context.Context) error { return nil }

func testOpts() Options {
	return Options{ProjectID: "default", MaxWindow: 168 * time.Hour, DefaultLimit: 100, MaxLimit: 1000, MaxTextLen: 256, Timeout: 10 * time.Second}
}

var day = time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)

func newTestHandler(t *testing.T, store *fakeStore) (*Handler, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	h, err := NewHandler(store, testKey, testOpts(), slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatal(err)
	}
	return h, &logs
}

func do(h *Handler, method, query, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, Path+"?"+query, nil)
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	h.Register(mux)
	mux.ServeHTTP(rec, req)
	return rec
}

const okRange = "from=2026-11-06T00:00:00Z&to=2026-11-07T00:00:00Z"

func TestHandlerStatuses(t *testing.T) {
	long := strings.Repeat("x", 257)
	tests := []struct {
		name       string
		method     string
		query      string
		key        string
		storeErr   error
		wantStatus int
		wantError  string // substring of "error"
	}{
		{name: "ok", query: okRange, wantStatus: 200},
		{name: "ok all filters", query: okRange + "&level=error,fatal&service=checkout&q=timeout&trace_id=t1&limit=5", wantStatus: 200},
		{name: "rfc3339 with offset and nanos", query: "from=2026-11-06T03:00:00.123456789%2B03:00&to=2026-11-06T12:00:00Z", wantStatus: 200},

		{name: "POST", method: http.MethodPost, query: okRange, wantStatus: 405, wantError: "use GET"},
		{name: "DELETE", method: http.MethodDelete, query: okRange, wantStatus: 405, wantError: "use GET"},

		{name: "missing key", query: okRange, key: "-", wantStatus: 401, wantError: "API key"},
		{name: "wrong key", query: okRange, key: "ingest-key", wantStatus: 401, wantError: "API key"},
		{name: "key prefix", query: okRange, key: testKey[:6], wantStatus: 401, wantError: "API key"},
		{name: "auth before validation", query: "from=bad", key: "wrong", wantStatus: 401, wantError: "API key"},

		{name: "missing from", query: "to=2026-11-07T00:00:00Z", wantStatus: 400, wantError: "from: required"},
		{name: "missing to", query: "from=2026-11-06T00:00:00Z", wantStatus: 400, wantError: "to: required"},
		{name: "from not rfc3339", query: "from=2026-11-06&to=2026-11-07T00:00:00Z", wantStatus: 400, wantError: "from: must be RFC3339"},
		{name: "to unix seconds", query: "from=2026-11-06T00:00:00Z&to=1762473600", wantStatus: 400, wantError: "to: must be RFC3339"},
		{name: "from equals to", query: "from=2026-11-06T00:00:00Z&to=2026-11-06T00:00:00Z", wantStatus: 400, wantError: "to: must be after from"},
		{name: "reversed range", query: "from=2026-11-07T00:00:00Z&to=2026-11-06T00:00:00Z", wantStatus: 400, wantError: "to: must be after from"},
		{name: "window too large", query: "from=2026-11-01T00:00:00Z&to=2026-11-08T00:00:01Z", wantStatus: 400, wantError: "max 168h"},
		{name: "unknown level", query: okRange + "&level=loud", wantStatus: 400, wantError: `level: unknown value "loud"`},
		{name: "empty level", query: okRange + "&level=", wantStatus: 400, wantError: "level: unknown value"},
		{name: "trailing comma level", query: okRange + "&level=info,", wantStatus: 400, wantError: "level: unknown value"},
		{name: "uppercase level", query: okRange + "&level=ERROR", wantStatus: 400, wantError: "level:"},
		{name: "level injection", query: okRange + "&level=" + url.QueryEscape("info') OR 1=1 --"), wantStatus: 400, wantError: "level:"},
		{name: "empty service", query: okRange + "&service=", wantStatus: 400, wantError: "service: must not be empty"},
		{name: "empty q", query: okRange + "&q=", wantStatus: 400, wantError: "q: must not be empty"},
		{name: "q too long", query: okRange + "&q=" + long, wantStatus: 400, wantError: "q: longer than 256"},
		{name: "service too long", query: okRange + "&service=" + long, wantStatus: 400, wantError: "service: longer than 256"},
		{name: "trace_id too long", query: okRange + "&trace_id=" + long, wantStatus: 400, wantError: "trace_id: longer than 256"},
		{name: "q 256 multibyte chars ok", query: okRange + "&q=" + url.QueryEscape(strings.Repeat("ü", 256)), wantStatus: 200},
		{name: "q invalid utf-8", query: okRange + "&q=%ff%fe", wantStatus: 400, wantError: "q: must be valid UTF-8"},
		{name: "limit zero", query: okRange + "&limit=0", wantStatus: 400, wantError: "limit: must be an integer between 1 and 1000"},
		{name: "limit too big", query: okRange + "&limit=1001", wantStatus: 400, wantError: "limit:"},
		{name: "limit not a number", query: okRange + "&limit=10x", wantStatus: 400, wantError: "limit:"},
		{name: "raw semicolon in query", query: okRange + "&limit=10;", wantStatus: 400, wantError: "malformed query string"},
		{name: "bad cursor", query: okRange + "&cursor=abc", wantStatus: 400, wantError: "cursor: invalid"},
		{name: "cursor sql", query: okRange + "&cursor=" + url.QueryEscape("' OR 1=1 --"), wantStatus: 400, wantError: "cursor: invalid"},
		{name: "cursor outside range", query: okRange + "&cursor=" + EncodeCursor(Cursor{Timestamp: day.Add(-time.Hour), EventID: "1-0"}), wantStatus: 400, wantError: "cursor: outside"},
		{name: "unknown param", query: okRange + "&project_id=other", wantStatus: 400, wantError: `unknown parameter "project_id"`},
		{name: "duplicate param", query: okRange + "&service=a&service=b", wantStatus: 400, wantError: "service: given more than once"},
		{name: "malformed query string", query: okRange + "&q=%zz", wantStatus: 400, wantError: "malformed query string"},

		{name: "clickhouse unreachable", query: okRange, storeErr: fmt.Errorf("clickhouse: %w", &url.Error{Op: "Post", URL: "http://10.0.0.5:8123/?query=SELECT", Err: errors.New("connection refused")}), wantStatus: 503, wantError: "log storage unavailable"},
		{name: "clickhouse server error", query: okRange, storeErr: &clickhouse.Error{Status: 500, Code: "241", Message: "Memory limit exceeded while executing SELECT event_id FROM logs"}, wantStatus: 503, wantError: "log storage unavailable"},
		{name: "clickhouse timeout 159", query: okRange, storeErr: &clickhouse.Error{Status: 500, Code: "159", Message: "Timeout exceeded"}, wantStatus: 504, wantError: "timed out"},
		{name: "clickhouse too slow 160", query: okRange, storeErr: &clickhouse.Error{Status: 500, Code: "160", Message: "Estimated query execution time is too long"}, wantStatus: 504, wantError: "timed out"},
		{name: "client deadline", query: okRange, storeErr: fmt.Errorf("clickhouse: %w", context.DeadlineExceeded), wantStatus: 504, wantError: "timed out"},
		{name: "clickhouse rejects query", query: okRange, storeErr: &clickhouse.Error{Status: 400, Code: "62", Message: "Syntax error: failed at position 1 (SELECT event_id"}, wantStatus: 500, wantError: "internal error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{err: tt.storeErr}
			h, logs := newTestHandler(t, store)
			method := tt.method
			if method == "" {
				method = http.MethodGet
			}
			key := tt.key
			switch key {
			case "":
				key = testKey
			case "-":
				key = ""
			}
			rec := do(h, method, tt.query, key)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q", ct)
			}
			body := rec.Body.String()
			if strings.Contains(logs.String(), testKey) {
				t.Error("API key logged")
			}
			if tt.wantStatus == 200 {
				var resp Response
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Events == nil {
					t.Fatalf("body = %s (err %v), want events array", body, err)
				}
				return
			}
			var e map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || len(e) != 1 {
				t.Fatalf("error body = %s, want {\"error\": ...}", body)
			}
			if !strings.Contains(e["error"], tt.wantError) {
				t.Errorf("error = %q, want it to contain %q", e["error"], tt.wantError)
			}
			for _, leak := range []string{"10.0.0.5", "SELECT", "Memory limit", "Syntax error", "DB::", "connection refused"} {
				if strings.Contains(body, leak) {
					t.Errorf("response leaks %q: %s", leak, body)
				}
			}
			if tt.storeErr != nil && !strings.Contains(logs.String(), "level=WARN") {
				t.Errorf("storage error not logged at warn: %q", logs.String())
			}
			if tt.wantStatus == 400 || tt.wantStatus == 401 || tt.wantStatus == 405 {
				if store.calls != 0 {
					t.Error("store was queried for a rejected request")
				}
			}
			if tt.wantStatus == 405 && rec.Header().Get("Allow") != "GET" {
				t.Errorf("Allow = %q", rec.Header().Get("Allow"))
			}
		})
	}
}

func TestHandlerPassesQuerySettings(t *testing.T) {
	store := &fakeStore{}
	h, _ := newTestHandler(t, store)
	if rec := do(h, http.MethodGet, okRange+"&service=checkout", testKey); rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	want := map[string]string{"readonly": "1", "max_execution_time": "10", "max_result_rows": "1001", "result_overflow_mode": "throw"}
	for k, v := range want {
		if store.settings[k] != v {
			t.Errorf("setting %s = %q, want %q", k, store.settings[k], v)
		}
	}
	if store.params["project_id"] != "default" || store.params["service"] != "checkout" {
		t.Errorf("params = %v", store.params)
	}
}

func TestHandlerInjectionValuesStayParameters(t *testing.T) {
	for _, value := range injection[:len(injection)-1] { // the 10k string exceeds MaxTextLen
		for _, param := range []string{"q", "service", "trace_id"} {
			store := &fakeStore{}
			h, _ := newTestHandler(t, store)
			rec := do(h, http.MethodGet, okRange+"&"+param+"="+url.QueryEscape(value), testKey)
			if rec.Code != 200 {
				t.Fatalf("%s=%q: status %d (%s), want 200", param, value, rec.Code, rec.Body.String())
			}
			if store.params[param] != value {
				t.Errorf("%s param = %q, want raw value", param, store.params[param])
			}
			if strings.Contains(store.sql, "DROP") || strings.Contains(store.sql, "UNION") || strings.Contains(store.sql, "--") {
				t.Errorf("%s=%q reached the SQL text:\n%s", param, value, store.sql)
			}
		}
	}
}

func TestPaginationWithEqualTimestamps(t *testing.T) {
	store := &fakeStore{}
	ts := day.Add(10 * time.Hour)
	// 11 rows: 7 share one timestamp, plus an other-project row that must never appear.
	for i := 0; i < 7; i++ {
		store.rows = append(store.rows, fakeRow{project: "default", id: fmt.Sprintf("100-%d", i), ts: ts, level: "info", service: "s", message: "m"})
	}
	for i := 0; i < 4; i++ {
		store.rows = append(store.rows, fakeRow{project: "default", id: fmt.Sprintf("%d-0", 200+i), ts: ts.Add(time.Duration(i+1) * time.Second), level: "info", service: "s", message: "m"})
	}
	store.rows = append(store.rows, fakeRow{project: "other", id: "999-0", ts: ts, level: "info", service: "s", message: "m"})

	h, _ := newTestHandler(t, store)
	seen := map[string]int{}
	var order []string
	cursor := ""
	for page := 0; page < 10; page++ {
		q := okRange + "&limit=3"
		if cursor != "" {
			q += "&cursor=" + cursor
		}
		rec := do(h, http.MethodGet, q, testKey)
		if rec.Code != 200 {
			t.Fatalf("page %d: %d %s", page, rec.Code, rec.Body.String())
		}
		var resp Response
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		for _, e := range resp.Events {
			seen[e.EventID]++
			order = append(order, e.EventID)
		}
		if resp.NextCursor == nil {
			if len(resp.Events) > 3 {
				t.Fatalf("last page has %d events", len(resp.Events))
			}
			break
		}
		if len(resp.Events) != 3 {
			t.Fatalf("page %d has %d events with a next cursor, want 3", page, len(resp.Events))
		}
		cursor = *resp.NextCursor
	}
	if len(seen) != 11 || len(order) != 11 {
		t.Fatalf("saw %d distinct / %d total events, want 11 / 11: %v", len(seen), len(order), order)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("event %s returned %d times", id, n)
		}
	}
	if seen["999-0"] != 0 {
		t.Error("row from another project returned")
	}
	want := []string{"203-0", "202-0", "201-0", "200-0", "100-6", "100-5", "100-4", "100-3", "100-2", "100-1", "100-0"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", order, want)
	}
}

func TestResponseFormat(t *testing.T) {
	store := &fakeStore{rows: []fakeRow{{project: "default", id: "1-0", ts: day.Add(90*time.Minute + 250*time.Millisecond), level: "warn", service: "api", message: "slow", trace: "t9"}}}
	h, _ := newTestHandler(t, store)
	rec := do(h, http.MethodGet, okRange, testKey)
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["next_cursor"]; !ok || raw["next_cursor"] != nil {
		t.Errorf("next_cursor = %v, want explicit null", raw["next_cursor"])
	}
	if _, ok := raw["took_ms"].(float64); !ok {
		t.Errorf("took_ms = %v", raw["took_ms"])
	}
	ev := raw["events"].([]any)[0].(map[string]any)
	want := map[string]any{"event_id": "1-0", "timestamp": "2026-11-06T01:30:00.250Z", "level": "warn", "service": "api", "message": "slow", "trace_id": "t9"}
	for k, v := range want {
		if ev[k] != v {
			t.Errorf("%s = %v, want %v", k, ev[k], v)
		}
	}

	// Empty result: events is [], not null.
	store.rows = nil
	rec = do(h, http.MethodGet, okRange, testKey)
	if !strings.Contains(rec.Body.String(), `"events":[]`) {
		t.Errorf("empty body = %s", rec.Body.String())
	}
}

func TestNewHandlerValidation(t *testing.T) {
	tests := []struct {
		name   string
		store  Store
		key    string
		mutate func(*Options)
	}{
		{name: "nil store", key: testKey, mutate: func(*Options) {}},
		{name: "empty key", store: &fakeStore{}, mutate: func(*Options) {}},
		{name: "no project", store: &fakeStore{}, key: testKey, mutate: func(o *Options) { o.ProjectID = "" }},
		{name: "default above max", store: &fakeStore{}, key: testKey, mutate: func(o *Options) { o.DefaultLimit = 2000 }},
		{name: "sub-second timeout", store: &fakeStore{}, key: testKey, mutate: func(o *Options) { o.Timeout = time.Millisecond }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := testOpts()
			tt.mutate(&o)
			if _, err := NewHandler(tt.store, tt.key, o, nil); err == nil {
				t.Fatal("NewHandler() error = nil, want error")
			}
		})
	}
}

// newTestSlog routes logs to t.Log so integration failures show the cause.
func newTestSlog(t *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(tWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

type tWriter struct{ t *testing.T }

func (w tWriter) Write(b []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(b)))
	return len(b), nil
}
