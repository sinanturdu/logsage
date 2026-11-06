package query

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Filter is a validated log search.
type Filter struct {
	From, To time.Time // from inclusive, to exclusive
	Levels   []string  // empty: all levels
	Service  string    // exact match, empty: any
	Text     string    // case-insensitive substring of message, empty: any
	TraceID  string    // exact match, empty: any
	Limit    int       // rows per page; Build asks for Limit+1
	After    *Cursor   // rows strictly older than this position
}

// Statement is SQL with server-side placeholders plus their values. The SQL
// text never contains caller input; values travel only in Params.
type Statement struct {
	SQL    string
	Params map[string]string
}

// chTime is the parameter format of DateTime64(3, 'UTC').
const chTime = "2006-01-02 15:04:05.000"

// projectScope is the condition every statement starts with.
const projectScope = "project_id = {project_id:String}"

// Build is the only place that produces log search SQL. Every statement is
// scoped to projectID, reads FROM logs FINAL (redelivered rows collapsed),
// orders newest first with event_id as tie-break, and fetches Limit+1 rows.
// All values are passed as parameters; the SQL is assembled only from the
// constant fragments below.
func Build(projectID string, f Filter) (Statement, error) {
	if projectID == "" {
		return Statement{}, errors.New("query: project id is required")
	}
	if f.Limit <= 0 {
		return Statement{}, errors.New("query: limit must be positive")
	}
	if !f.From.Before(f.To) {
		return Statement{}, errors.New("query: from must be before to")
	}

	where := []string{
		projectScope,
		"timestamp >= {from:DateTime64(3, 'UTC')}",
		"timestamp < {to:DateTime64(3, 'UTC')}",
	}
	params := map[string]string{
		"project_id": projectID,
		"from":       f.From.UTC().Format(chTime),
		"to":         f.To.UTC().Format(chTime),
		"limit":      strconv.Itoa(f.Limit + 1),
	}

	if len(f.Levels) > 0 {
		quoted := make([]string, len(f.Levels))
		for i, l := range f.Levels {
			if !validLevel(l) {
				return Statement{}, fmt.Errorf("query: invalid level %q", l)
			}
			quoted[i] = "'" + l + "'" // safe: l is one of five fixed words
		}
		where = append(where, "level IN {levels:Array(String)}")
		params["levels"] = "[" + strings.Join(quoted, ",") + "]"
	}
	if f.Service != "" {
		where = append(where, "service = {service:String}")
		params["service"] = f.Service
	}
	if f.Text != "" {
		where = append(where, "positionCaseInsensitiveUTF8(message, {q:String}) > 0")
		params["q"] = f.Text
	}
	if f.TraceID != "" {
		where = append(where, "trace_id = {trace_id:String}")
		params["trace_id"] = f.TraceID
	}
	if f.After != nil {
		where = append(where, "(timestamp, event_id) < ({cursor_ts:DateTime64(3, 'UTC')}, {cursor_id:String})")
		params["cursor_ts"] = f.After.Timestamp.UTC().Format(chTime)
		params["cursor_id"] = f.After.EventID
	}

	sql := "SELECT event_id, timestamp, level, service, message, trace_id, attributes\n" +
		"FROM logs FINAL\n" +
		"WHERE " + strings.Join(where, "\n  AND ") + "\n" +
		"ORDER BY timestamp DESC, event_id DESC\n" +
		"LIMIT {limit:UInt32}\n" +
		"FORMAT JSONEachRow"
	return Statement{SQL: sql, Params: params}, nil
}

func validLevel(l string) bool {
	switch l {
	case "debug", "info", "warn", "error", "fatal":
		return true
	}
	return false
}
