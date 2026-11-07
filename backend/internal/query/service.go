package query

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sinanturdu/logsage/backend/internal/storage/clickhouse"
)

// Store runs a parameterized read query and reports readiness.
type Store interface {
	QueryWithParams(ctx context.Context, sql string, params, settings map[string]string) ([]byte, error)
	Ready(ctx context.Context) error
}

// Options bounds what a request may ask for.
type Options struct {
	MaxWindow    time.Duration
	DefaultLimit int
	MaxLimit     int
	MaxTextLen   int
	Timeout      time.Duration // ClickHouse max_execution_time
}

// Service is the one log read path: it validates request parameters and
// runs project-scoped statements. The API-key endpoint (/v1/logs) and the
// session endpoints of cmd/api both call it, so validation, scoping and
// error mapping are identical (ADR 0014).
type Service struct {
	store Store
	opts  Options
}

// NewService checks the limits and returns a Service.
func NewService(store Store, opts Options) (*Service, error) {
	switch {
	case store == nil:
		return nil, errors.New("query: store is nil")
	case opts.MaxWindow <= 0 || opts.MaxLimit <= 0 || opts.MaxTextLen <= 0 || opts.Timeout < time.Second:
		return nil, errors.New("query: invalid limits")
	case opts.DefaultLimit <= 0 || opts.DefaultLimit > opts.MaxLimit:
		return nil, errors.New("query: default limit must be between 1 and max limit")
	}
	return &Service{store: store, opts: opts}, nil
}

// Ready reports whether log storage answers.
func (s *Service) Ready(ctx context.Context) error { return s.store.Ready(ctx) }

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

// Response is the 200 body of a log search.
type Response struct {
	Events     []Event `json:"events"`
	NextCursor *string `json:"next_cursor"`
	TookMs     int64   `json:"took_ms"`
}

// ---- parameters ----

// Parameter sets per endpoint. Unknown or repeated parameters are a 400.
var (
	searchParams    = []string{"from", "to", "level", "service", "q", "trace_id", "limit", "cursor"}
	histogramParams = []string{"from", "to", "level", "service", "q", "trace_id", "bucket"}
	facetParams     = []string{"from", "to", "level", "service", "q", "trace_id"}
)

// ParseSearch validates a log search query string. Errors are client-safe
// and name the bad parameter.
func (s *Service) ParseSearch(rawQuery string) (Filter, error) {
	vals, f, err := s.parse(rawQuery, searchParams)
	if err != nil {
		return Filter{}, err
	}
	has := func(k string) bool { _, ok := vals[k]; return ok }
	f.Limit = s.opts.DefaultLimit
	if has("limit") {
		n, err := strconv.Atoi(vals.Get("limit"))
		if err != nil || n < 1 || n > s.opts.MaxLimit {
			return Filter{}, fmt.Errorf("limit: must be an integer between 1 and %d", s.opts.MaxLimit)
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

// parse checks the parameter names, then the filters every endpoint shares.
func (s *Service) parse(rawQuery string, allowed []string) (url.Values, Filter, error) {
	vals, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil, Filter{}, errors.New("malformed query string")
	}
	for k, v := range vals {
		if !contains(allowed, k) {
			return nil, Filter{}, fmt.Errorf("unknown parameter %s; allowed: %s", quoteShort(k), strings.Join(allowed, ", "))
		}
		if len(v) > 1 {
			return nil, Filter{}, fmt.Errorf("%s: given more than once", k)
		}
	}
	has := func(k string) bool { _, ok := vals[k]; return ok }

	var f Filter
	if f.From, err = parseTime(vals.Get("from"), "from"); err != nil {
		return nil, Filter{}, err
	}
	if f.To, err = parseTime(vals.Get("to"), "to"); err != nil {
		return nil, Filter{}, err
	}
	if !f.From.Before(f.To) {
		return nil, Filter{}, errors.New("to: must be after from")
	}
	if w := f.To.Sub(f.From); w > s.opts.MaxWindow {
		return nil, Filter{}, fmt.Errorf("to: range from..to is %s, max %s", w, s.opts.MaxWindow)
	}

	if has("level") {
		seen := map[string]bool{}
		for _, l := range strings.Split(vals.Get("level"), ",") {
			if !validLevel(l) {
				return nil, Filter{}, fmt.Errorf("level: unknown value %s; allowed: debug, info, warn, error, fatal (comma separated)", quoteShort(l))
			}
			if !seen[l] {
				seen[l] = true
				f.Levels = append(f.Levels, l)
			}
		}
	}
	if has("service") {
		if f.Services, err = s.parseServices(vals.Get("service")); err != nil {
			return nil, Filter{}, err
		}
	}
	for _, p := range []struct {
		name string
		dst  *string
	}{{"q", &f.Text}, {"trace_id", &f.TraceID}} {
		if !has(p.name) {
			continue
		}
		v := vals.Get(p.name)
		if err := s.checkText(p.name, v); err != nil {
			return nil, Filter{}, err
		}
		*p.dst = v
	}
	return vals, f, nil
}

// parseServices validates the service filter: one name, as in v0.1.
func (s *Service) parseServices(v string) ([]string, error) {
	if err := s.checkText("service", v); err != nil {
		return nil, err
	}
	return []string{v}, nil
}

func (s *Service) checkText(name, v string) error {
	switch {
	case v == "":
		return fmt.Errorf("%s: must not be empty", name)
	case !utf8.ValidString(v):
		return fmt.Errorf("%s: must be valid UTF-8", name)
	case utf8.RuneCountInString(v) > s.opts.MaxTextLen:
		return fmt.Errorf("%s: longer than %d characters", name, s.opts.MaxTextLen)
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
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

// ---- running statements ----

// run executes stmt with the read-only settings: readonly=1, the execution
// time limit and a result row limit.
func (s *Service) run(ctx context.Context, stmt Statement, maxRows int) ([]byte, error) {
	// The request deadline is a little longer than ClickHouse's own limit so
	// ClickHouse normally reports the timeout itself.
	ctx, cancel := context.WithTimeout(ctx, s.opts.Timeout+2*time.Second)
	defer cancel()
	settings := map[string]string{
		"readonly":              "1",
		"max_execution_time":    strconv.Itoa(int(math.Ceil(s.opts.Timeout.Seconds()))),
		"timeout_overflow_mode": "throw",
		"max_result_rows":       strconv.Itoa(maxRows),
		"result_overflow_mode":  "throw",
	}
	body, err := s.store.QueryWithParams(ctx, stmt.SQL, stmt.Params, settings)
	if err != nil {
		if ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: %w", errDeadline, err)
		}
		return nil, err
	}
	return body, nil
}

// Search runs a log search (always project-scoped), newest first.
func (s *Service) Search(ctx context.Context, scope Scope, f Filter) ([]Event, *string, error) {
	stmt, err := Build(scope, f)
	if err != nil {
		return nil, nil, err
	}
	body, err := s.run(ctx, stmt, s.opts.MaxLimit+1)
	if err != nil {
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
	err := eachLine(body, func(line []byte) error {
		var r rowJSON
		if err := json.Unmarshal(line, &r); err != nil {
			return fmt.Errorf("decode row: %w", err)
		}
		ts, err := time.ParseInLocation(chTime, r.Timestamp, time.UTC)
		if err != nil {
			return fmt.Errorf("decode row timestamp: %w", err)
		}
		attrs := r.Attributes
		if attrs == nil {
			attrs = map[string]string{}
		}
		events = append(events, Event{
			EventID: r.EventID, Timestamp: ts.UTC().Format(cursorTimeFmt), Level: r.Level,
			Service: r.Service, Message: r.Message, TraceID: r.TraceID, Attributes: attrs,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return events, nil
}

// eachLine calls fn for every non-empty JSONEachRow line.
func eachLine(body []byte, fn func([]byte) error) error {
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if err := fn(line); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read rows: %w", err)
	}
	return nil
}

// Classify maps a storage error to a status and a client-safe message.
// ClickHouse text, SQL and addresses are never returned to the client.
func Classify(err error) (int, string) {
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
