package query

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sinanturdu/logsage/backend/internal/storage/clickhouse"
)

// Path is the route served by Handler.
const Path = "/v1/logs"

// DefaultProjectID scopes every query until projects exist (v0.2).
const DefaultProjectID = "default"

// Store runs a parameterized read query and reports readiness.
type Store interface {
	QueryWithParams(ctx context.Context, sql string, params, settings map[string]string) ([]byte, error)
	Ready(ctx context.Context) error
}

// Options bounds what a request may ask for.
type Options struct {
	ProjectID    string
	MaxWindow    time.Duration
	DefaultLimit int
	MaxLimit     int
	MaxTextLen   int
	Timeout      time.Duration // ClickHouse max_execution_time
}

// Handler serves GET /v1/logs.
type Handler struct {
	store     Store
	keyDigest [sha256.Size]byte
	opts      Options
	log       *slog.Logger
	now       func() time.Time
}

// NewHandler returns a Handler that accepts apiKey in X-API-Key.
func NewHandler(store Store, apiKey string, opts Options, log *slog.Logger) (*Handler, error) {
	switch {
	case store == nil:
		return nil, errors.New("query: store is nil")
	case apiKey == "":
		return nil, errors.New("query: API key is empty")
	case opts.ProjectID == "":
		return nil, errors.New("query: project id is empty")
	case opts.MaxWindow <= 0 || opts.MaxLimit <= 0 || opts.MaxTextLen <= 0 || opts.Timeout < time.Second:
		return nil, errors.New("query: invalid limits")
	case opts.DefaultLimit <= 0 || opts.DefaultLimit > opts.MaxLimit:
		return nil, errors.New("query: default limit must be between 1 and max limit")
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Handler{store: store, keyDigest: sha256.Sum256([]byte(apiKey)), opts: opts, log: log, now: time.Now}, nil
}

// Register mounts the handler on mux at Path.
func (h *Handler) Register(mux *http.ServeMux) { mux.Handle(Path, h) }

// Event is one log event in a response.
type Event struct {
	EventID    string            `json:"event_id"`
	Timestamp  string            `json:"timestamp"`
	Level      string            `json:"level"`
	Service    string            `json:"service"`
	Message    string            `json:"message"`
	TraceID    string            `json:"trace_id"`
	Attributes map[string]string `json:"attributes"`
}

// Response is the 200 body.
type Response struct {
	Events     []Event `json:"events"`
	NextCursor *string `json:"next_cursor"`
	TookMs     int64   `json:"took_ms"`
}

// ServeHTTP checks method (405), API key (401), parameters (400), then runs
// the query; storage failures map to 503, timeouts to 504.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := h.now()
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed, use GET")
		return
	}
	if !h.authorized(r.Header.Get("X-API-Key")) {
		writeError(w, http.StatusUnauthorized, "missing or invalid API key")
		return
	}
	f, err := h.parseFilter(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	events, next, err := h.search(r.Context(), f)
	if err != nil {
		status, msg := classify(err)
		h.log.WarnContext(r.Context(), "log query failed", "status", status, "took_ms", h.now().Sub(start).Milliseconds(), "err", err)
		writeError(w, status, msg)
		return
	}
	writeJSON(w, http.StatusOK, Response{Events: events, NextCursor: next, TookMs: h.now().Sub(start).Milliseconds()})
}

// search builds the statement (always project-scoped) and runs it.
func (h *Handler) search(ctx context.Context, f Filter) ([]Event, *string, error) {
	stmt, err := Build(h.opts.ProjectID, f)
	if err != nil {
		return nil, nil, err
	}
	// The request deadline is a little longer than ClickHouse's own limit so
	// ClickHouse normally reports the timeout itself.
	ctx, cancel := context.WithTimeout(ctx, h.opts.Timeout+2*time.Second)
	defer cancel()

	settings := map[string]string{
		"readonly":              "1",
		"max_execution_time":    strconv.Itoa(int(math.Ceil(h.opts.Timeout.Seconds()))),
		"timeout_overflow_mode": "throw",
		"max_result_rows":       strconv.Itoa(h.opts.MaxLimit + 1),
		"result_overflow_mode":  "throw",
	}
	body, err := h.store.QueryWithParams(ctx, stmt.SQL, stmt.Params, settings)
	if err != nil {
		if ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, nil, fmt.Errorf("%w: %w", errDeadline, err)
		}
		return nil, nil, err
	}

	events, err := decodeRows(body)
	if err != nil {
		return nil, nil, err
	}
	var next *string
	if len(events) > f.Limit {
		events = events[:f.Limit]
		last := events[len(events)-1]
		ts, _ := time.Parse(cursorTimeFmt, last.Timestamp)
		c := EncodeCursor(Cursor{Timestamp: ts, EventID: last.EventID})
		next = &c
	}
	return events, next, nil
}

var errDeadline = errors.New("query deadline exceeded")

// rowJSON is one JSONEachRow line from ClickHouse.
type rowJSON struct {
	EventID    string            `json:"event_id"`
	Timestamp  string            `json:"timestamp"`
	Level      string            `json:"level"`
	Service    string            `json:"service"`
	Message    string            `json:"message"`
	TraceID    string            `json:"trace_id"`
	Attributes map[string]string `json:"attributes"`
}

func decodeRows(body []byte) ([]Event, error) {
	events := []Event{}
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var r rowJSON
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("decode row: %w", err)
		}
		ts, err := time.ParseInLocation(chTime, r.Timestamp, time.UTC)
		if err != nil {
			return nil, fmt.Errorf("decode row timestamp: %w", err)
		}
		attrs := r.Attributes
		if attrs == nil {
			attrs = map[string]string{}
		}
		events = append(events, Event{
			EventID: r.EventID, Timestamp: ts.UTC().Format(cursorTimeFmt), Level: r.Level,
			Service: r.Service, Message: r.Message, TraceID: r.TraceID, Attributes: attrs,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read rows: %w", err)
	}
	return events, nil
}

// classify maps a storage error to a status and a client-safe message.
// ClickHouse text, SQL and addresses are never returned to the client.
func classify(err error) (int, string) {
	var ce *clickhouse.Error
	switch {
	case errors.Is(err, errDeadline), errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "query timed out; narrow the time range or add filters"
	case errors.As(err, &ce) && (ce.Code == "159" || ce.Code == "160"):
		return http.StatusGatewayTimeout, "query timed out; narrow the time range or add filters"
	case errors.As(err, &ce) && ce.Status >= 400 && ce.Status < 500:
		return http.StatusInternalServerError, "internal error"
	default:
		return http.StatusServiceUnavailable, "log storage unavailable, retry later"
	}
}

// authorized compares SHA-256 digests in constant time.
func (h *Handler) authorized(key string) bool {
	if key == "" {
		return false
	}
	got := sha256.Sum256([]byte(key))
	return subtle.ConstantTimeCompare(got[:], h.keyDigest[:]) == 1
}

var allowedParams = map[string]bool{
	"from": true, "to": true, "level": true, "service": true,
	"q": true, "trace_id": true, "limit": true, "cursor": true,
}

// parseFilter validates the query string. Errors name the bad parameter.
func (h *Handler) parseFilter(rawQuery string) (Filter, error) {
	vals, err := url.ParseQuery(rawQuery)
	if err != nil {
		return Filter{}, errors.New("malformed query string")
	}
	for k, v := range vals {
		if !allowedParams[k] {
			return Filter{}, fmt.Errorf("unknown parameter %s; allowed: from, to, level, service, q, trace_id, limit, cursor", quoteShort(k))
		}
		if len(v) > 1 {
			return Filter{}, fmt.Errorf("%s: given more than once", k)
		}
	}
	has := func(k string) bool { _, ok := vals[k]; return ok }

	var f Filter
	if f.From, err = parseTime(vals.Get("from"), "from"); err != nil {
		return Filter{}, err
	}
	if f.To, err = parseTime(vals.Get("to"), "to"); err != nil {
		return Filter{}, err
	}
	if !f.From.Before(f.To) {
		return Filter{}, errors.New("to: must be after from")
	}
	if w := f.To.Sub(f.From); w > h.opts.MaxWindow {
		return Filter{}, fmt.Errorf("to: range from..to is %s, max %s", w, h.opts.MaxWindow)
	}

	if has("level") {
		seen := map[string]bool{}
		for _, l := range strings.Split(vals.Get("level"), ",") {
			if !validLevel(l) {
				return Filter{}, fmt.Errorf("level: unknown value %s; allowed: debug, info, warn, error, fatal (comma separated)", quoteShort(l))
			}
			if !seen[l] {
				seen[l] = true
				f.Levels = append(f.Levels, l)
			}
		}
	}
	for _, p := range []struct {
		name string
		dst  *string
	}{{"service", &f.Service}, {"q", &f.Text}, {"trace_id", &f.TraceID}} {
		if !has(p.name) {
			continue
		}
		v := vals.Get(p.name)
		switch {
		case v == "":
			return Filter{}, fmt.Errorf("%s: must not be empty", p.name)
		case !utf8.ValidString(v):
			return Filter{}, fmt.Errorf("%s: must be valid UTF-8", p.name)
		case utf8.RuneCountInString(v) > h.opts.MaxTextLen:
			return Filter{}, fmt.Errorf("%s: longer than %d characters", p.name, h.opts.MaxTextLen)
		}
		*p.dst = v
	}

	f.Limit = h.opts.DefaultLimit
	if has("limit") {
		n, err := strconv.Atoi(vals.Get("limit"))
		if err != nil || n < 1 || n > h.opts.MaxLimit {
			return Filter{}, fmt.Errorf("limit: must be an integer between 1 and %d", h.opts.MaxLimit)
		}
		f.Limit = n
	}

	if has("cursor") {
		c, err := DecodeCursor(vals.Get("cursor"))
		if err != nil {
			return Filter{}, err
		}
		if c.Timestamp.Before(f.From.Truncate(time.Millisecond)) || !c.Timestamp.Before(f.To) {
			return Filter{}, errors.New("cursor: outside the from..to range; repeat the same query with next_cursor")
		}
		f.After = &c
	}
	return f, nil
}

func parseTime(v, name string) (time.Time, error) {
	if v == "" {
		return time.Time{}, fmt.Errorf("%s: required, RFC3339 such as 2026-11-06T10:00:00Z", name)
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: must be RFC3339 such as 2026-11-06T10:00:00Z", name)
	}
	return t.UTC(), nil
}

// quoteShort quotes s for an error message, cut to 32 characters.
func quoteShort(s string) string {
	if utf8.RuneCountInString(s) > 32 {
		s = string([]rune(s)[:32]) + "..."
	}
	return strconv.Quote(s)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
