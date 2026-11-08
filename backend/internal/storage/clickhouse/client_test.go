package clickhouse

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/sinanturdu/logsage/backend/migrations"
)

// recorded is one request seen by the fake server.
type recorded struct {
	method, path, query string
	database, user, key string
	body                string
}

// fakeServer records requests and answers with status/body from respond.
type fakeServer struct {
	mu      sync.Mutex
	reqs    []recorded
	respond func(r recorded) (int, string)
}

func (f *fakeServer) handler(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	rec := recorded{
		method: r.Method, path: r.URL.Path, query: r.URL.Query().Get("query"),
		database: r.URL.Query().Get("database"),
		user:     r.Header.Get("X-ClickHouse-User"), key: r.Header.Get("X-ClickHouse-Key"),
		body: string(b),
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, rec)
	f.mu.Unlock()
	status, body := http.StatusOK, "Ok.\n"
	if f.respond != nil {
		status, body = f.respond(rec)
	}
	if status != http.StatusOK {
		w.Header().Set("X-ClickHouse-Exception-Code", "27")
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func newTestClient(t *testing.T, f *fakeServer) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	c, err := New(Options{URL: srv.URL, Database: "logsage", User: "writer", Password: "pw-secret", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c, srv
}

func TestNewValidation(t *testing.T) {
	ok := Options{URL: "http://localhost:8123", Database: "logsage", Timeout: time.Second}
	tests := []struct {
		name    string
		mutate  func(*Options)
		wantErr bool
	}{
		{name: "valid", mutate: func(*Options) {}},
		{name: "https", mutate: func(o *Options) { o.URL = "https://ch:8443" }},
		{name: "bad scheme", mutate: func(o *Options) { o.URL = "tcp://ch:9000" }, wantErr: true},
		{name: "no host", mutate: func(o *Options) { o.URL = "http://" }, wantErr: true},
		{name: "credentials in url", mutate: func(o *Options) { o.URL = "http://u:p@ch:8123" }, wantErr: true},
		{name: "bad database", mutate: func(o *Options) { o.Database = "x;DROP" }, wantErr: true},
		{name: "zero timeout", mutate: func(o *Options) { o.Timeout = 0 }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := ok
			tt.mutate(&o)
			if _, err := New(o); (err != nil) != tt.wantErr {
				t.Fatalf("New() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestInsertJSONEachRow(t *testing.T) {
	f := &fakeServer{}
	c, _ := newTestClient(t, f)
	body := []byte("{\"a\":1}\n{\"a\":2}\n")

	if err := c.InsertJSONEachRow(context.Background(), "logs", body); err != nil {
		t.Fatalf("InsertJSONEachRow: %v", err)
	}
	if len(f.reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(f.reqs))
	}
	r := f.reqs[0]
	if r.method != http.MethodPost || r.path != "/" {
		t.Errorf("request = %s %s, want POST /", r.method, r.path)
	}
	if r.query != "INSERT INTO logs FORMAT JSONEachRow" || r.database != "logsage" {
		t.Errorf("query = %q database = %q", r.query, r.database)
	}
	if r.user != "writer" || r.key != "pw-secret" {
		t.Errorf("auth headers user=%q key=%q", r.user, r.key)
	}
	if r.body != string(body) {
		t.Errorf("body = %q, want %q", r.body, body)
	}

	if err := c.InsertJSONEachRow(context.Background(), "logs; DROP TABLE x", body); err == nil {
		t.Error("invalid table name accepted")
	}
}

func TestQueryWithParams(t *testing.T) {
	var got *http.Request
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got, body = r, string(b)
		_, _ = io.WriteString(w, "{\"x\":1}\n")
	}))
	defer srv.Close()
	c, err := New(Options{URL: srv.URL, Database: "logsage", User: "reader", Password: "pw", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}

	sql := "SELECT {q:String} AS x FROM logs WHERE project_id = {project_id:String} FORMAT JSONEachRow"
	out, err := c.QueryWithParams(context.Background(), sql,
		map[string]string{"q": "it's a\\tb\n; DROP TABLE logs --", "project_id": "default"},
		map[string]string{"readonly": "1", "max_execution_time": "10"})
	if err != nil {
		t.Fatalf("QueryWithParams: %v", err)
	}
	if string(out) != "{\"x\":1}\n" {
		t.Errorf("out = %q", out)
	}
	if body != sql {
		t.Errorf("body = %q, want the SQL unchanged", body)
	}
	q := got.URL.Query()
	if q.Get("param_q") != `it's a\\tb\n; DROP TABLE logs --` {
		t.Errorf("param_q = %q, want escaped value", q.Get("param_q"))
	}
	if q.Get("param_project_id") != "default" || q.Get("database") != "logsage" {
		t.Errorf("params = %v", q)
	}
	if q.Get("readonly") != "1" || q.Get("max_execution_time") != "10" {
		t.Errorf("settings = %v", q)
	}
	if strings.Contains(got.URL.RawQuery, "pw") {
		t.Error("password leaked into URL")
	}

	bad := []struct {
		name             string
		params, settings map[string]string
	}{
		{name: "param name with quote", params: map[string]string{"q'": "x"}},
		{name: "param name with brace", params: map[string]string{"q}": "x"}},
		{name: "setting overrides database", settings: map[string]string{"database": "other"}},
		{name: "setting smuggles a param", settings: map[string]string{"param_q": "x"}},
		{name: "setting name with ampersand", settings: map[string]string{"a&b": "1"}},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := c.QueryWithParams(context.Background(), "SELECT 1", tt.params, tt.settings); err == nil {
				t.Fatal("QueryWithParams() error = nil, want error")
			}
		})
	}
}

func TestEscapeParam(t *testing.T) {
	tests := []struct{ in, want string }{
		{in: "plain", want: "plain"},
		{in: `a\b`, want: `a\\b`},
		{in: "tab\there", want: `tab\there`},
		{in: "line\nbreak\r", want: `line\nbreak\r`},
		{in: "nul\x00byte", want: `nul\0byte`},
		{in: `'"; DROP --`, want: `'"; DROP --`},
		{in: "ünïcödé ✓", want: "ünïcödé ✓"},
	}
	for _, tt := range tests {
		if got := EscapeParam(tt.in); got != tt.want {
			t.Errorf("EscapeParam(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestErrors(t *testing.T) {
	long := "Code: 27. DB::Exception: Cannot parse input: " + strings.Repeat("x", 1000) + "\nsecond line"
	tests := []struct {
		name     string
		respond  func(recorded) (int, string)
		call     func(*Client) error
		wantCode int
	}{
		{
			name:     "insert rejected",
			respond:  func(recorded) (int, string) { return http.StatusBadRequest, long },
			call:     func(c *Client) error { return c.InsertJSONEachRow(context.Background(), "logs", []byte("{}")) },
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "ping server error",
			respond:  func(recorded) (int, string) { return http.StatusServiceUnavailable, "down" },
			call:     func(c *Client) error { return c.Ping(context.Background()) },
			wantCode: http.StatusServiceUnavailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, &fakeServer{respond: tt.respond})
			err := tt.call(c)
			var ce *Error
			if !errors.As(err, &ce) {
				t.Fatalf("error = %v, want *Error", err)
			}
			if ce.Status != tt.wantCode || ce.Code != "27" {
				t.Errorf("Error = %+v", ce)
			}
			if len(ce.Message) > maxErrorBytes+3 || strings.Contains(ce.Message, "second line") {
				t.Errorf("message not truncated to first line: %d bytes", len(ce.Message))
			}
			if strings.Contains(err.Error(), "pw-secret") {
				t.Error("error leaks password")
			}
		})
	}
}

func TestPingUnreachableAndTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block)

	c, err := New(Options{URL: srv.URL, Database: "logsage", Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("Ping() = nil, want timeout error")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Ping took %v, want it bounded by the timeout", d)
	}
}

func TestMigrateEmbedded(t *testing.T) {
	f := &fakeServer{}
	c, _ := newTestClient(t, f)
	if err := c.Migrate(context.Background(), migrations.ClickHouse(), MigrationData{Database: "logsage_x", TTLDays: 7}); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if len(f.reqs) != 5 {
		t.Fatalf("statements = %d, want 5", len(f.reqs))
	}
	// template (ADR 0022): empty for rows that are not grouped.
	if tpl := f.reqs[4].body; !strings.Contains(tpl, "ADD COLUMN IF NOT EXISTS template String DEFAULT ''") {
		t.Errorf("template migration = %q", tpl)
	}
	// inserted_at (ADR 0015): old rows get the fixed epoch, never now().
	if ins := f.reqs[2].body; !strings.Contains(ins, "ADD COLUMN IF NOT EXISTS inserted_at DateTime64(3, 'UTC') DEFAULT toDateTime64(0, 3, 'UTC')") || strings.Contains(ins, "DEFAULT now") {
		t.Errorf("inserted_at migration = %q", ins)
	}
	if idx := f.reqs[3].body; !strings.Contains(idx, "ADD INDEX IF NOT EXISTS idx_inserted_at inserted_at TYPE minmax") {
		t.Errorf("inserted_at index migration = %q", idx)
	}
	if !strings.Contains(f.reqs[0].body, "CREATE DATABASE IF NOT EXISTS logsage_x") {
		t.Errorf("first statement = %q", f.reqs[0].body)
	}
	logs := f.reqs[1].body
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS logsage_x.logs",
		"project_id  LowCardinality(String)",
		"event_id    String",
		"ENGINE = ReplacingMergeTree",
		"ORDER BY (project_id, service, timestamp, event_id)",
		"INTERVAL 7 DAY",
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs DDL missing %q:\n%s", want, logs)
		}
	}
	for _, r := range f.reqs {
		if r.database != "" {
			t.Errorf("migration sent with database=%q, want none", r.database)
		}
		if strings.HasSuffix(strings.TrimSpace(r.body), ";") || strings.Contains(r.body, "{{") {
			t.Errorf("statement not rendered cleanly: %q", r.body)
		}
	}
}

func TestMigrateErrors(t *testing.T) {
	tests := []struct {
		name    string
		fsys    fstest.MapFS
		data    MigrationData
		respond func(recorded) (int, string)
	}{
		{name: "bad database", fsys: fstest.MapFS{"1.sql": {Data: []byte("SELECT 1")}}, data: MigrationData{Database: "a-b", TTLDays: 1}},
		{name: "zero ttl", fsys: fstest.MapFS{"1.sql": {Data: []byte("SELECT 1")}}, data: MigrationData{Database: "a", TTLDays: 0}},
		{name: "no files", fsys: fstest.MapFS{}, data: MigrationData{Database: "a", TTLDays: 1}},
		{name: "unknown template field", fsys: fstest.MapFS{"1.sql": {Data: []byte("SELECT {{.Nope}}")}}, data: MigrationData{Database: "a", TTLDays: 1}},
		{name: "empty file", fsys: fstest.MapFS{"1.sql": {Data: []byte(" ; ")}}, data: MigrationData{Database: "a", TTLDays: 1}},
		{
			name: "server rejects", fsys: fstest.MapFS{"1.sql": {Data: []byte("SELECT 1")}}, data: MigrationData{Database: "a", TTLDays: 1},
			respond: func(recorded) (int, string) { return http.StatusBadRequest, "syntax error" },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t, &fakeServer{respond: tt.respond})
			if err := c.Migrate(context.Background(), tt.fsys, tt.data); err == nil {
				t.Fatal("Migrate() = nil, want error")
			}
		})
	}
}
