package query

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/auth"
)

// Filter is a validated log search.
type Filter struct {
	From, To time.Time // from inclusive, to exclusive
	Levels   []string  // empty: all levels
	Services []string  // exact match on any of them, empty: any
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

// projectScope is the condition every statement starts with. Alias is the
// legacy value "default" for the default project and equals the project id
// for every other project, so the SQL is the same for all projects.
const projectScope = "(project_id = {project_id:String} OR project_id = {project_alias:String})"

// LegacyDefaultProject is the project_id of v0.1 rows (ADR 0011/0012).
const LegacyDefaultProject = "default"

// MaxServices is the largest number of values in a service filter.
const MaxServices = 20

// Levels lists the log levels in severity order.
var Levels = []string{"debug", "info", "warn", "error", "fatal"}

// Scope is the project a query is limited to. It comes from the verified
// key or the caller's membership (ScopeFor), never from the request.
type Scope struct {
	ProjectID string
	Alias     string
}

// ScopeFor returns the scope for a project id: the default project also
// matches its legacy id "default".
func ScopeFor(projectID string) Scope {
	if projectID == auth.DefaultProjectID {
		return Scope{ProjectID: projectID, Alias: LegacyDefaultProject}
	}
	return Scope{ProjectID: projectID, Alias: projectID}
}

// skip names a filter left out of a WHERE clause (facet exclusion rule).
type skip int

const (
	skipNone skip = iota
	skipLevels
	skipServices
)

// where returns the conditions and parameters shared by every statement.
// The project scope and the time range are always present.
func where(scope Scope, f Filter, s skip) ([]string, map[string]string, error) {
	if scope.ProjectID == "" || scope.Alias == "" {
		return nil, nil, errors.New("query: project scope is required")
	}
	if !f.From.Before(f.To) {
		return nil, nil, errors.New("query: from must be before to")
	}
	conds := []string{
		projectScope,
		"timestamp >= {from:DateTime64(3, 'UTC')}",
		"timestamp < {to:DateTime64(3, 'UTC')}",
	}
	params := map[string]string{
		"project_id":    scope.ProjectID,
		"project_alias": scope.Alias,
		"from":          f.From.UTC().Format(chTime),
		"to":            f.To.UTC().Format(chTime),
	}
	if len(f.Levels) > 0 && s != skipLevels {
		quoted := make([]string, len(f.Levels))
		for i, l := range f.Levels {
			if !validLevel(l) {
				return nil, nil, fmt.Errorf("query: invalid level %q", l)
			}
			quoted[i] = "'" + l + "'" // safe: l is one of five fixed words
		}
		conds = append(conds, "level IN {levels:Array(String)}")
		params["levels"] = "[" + strings.Join(quoted, ",") + "]"
	}
	if len(f.Services) > MaxServices {
		return nil, nil, fmt.Errorf("query: more than %d services", MaxServices)
	}
	switch {
	case s == skipServices || len(f.Services) == 0:
	case len(f.Services) == 1:
		// One service keeps the v0.1 statement unchanged.
		conds = append(conds, "service = {service:String}")
		params["service"] = f.Services[0]
	default:
		conds = append(conds, "service IN {services:Array(String)}")
		params["services"] = arrayParam(f.Services)
	}
	if f.Text != "" {
		conds = append(conds, "positionCaseInsensitiveUTF8(message, {q:String}) > 0")
		params["q"] = f.Text
	}
	if f.TraceID != "" {
		conds = append(conds, "trace_id = {trace_id:String}")
		params["trace_id"] = f.TraceID
	}
	return conds, params, nil
}

// arrayElem escapes one string for a quoted element of an Array(String)
// parameter value. The whole value is escaped again for transport by the
// ClickHouse client, so backslashes are doubled on both levels.
var arrayElem = strings.NewReplacer(`\`, `\\`, `'`, `\'`)

// arrayParam renders values as an Array(String) parameter value. It is a
// value, never SQL: ClickHouse parses it as data of the declared type.
func arrayParam(values []string) string {
	q := make([]string, len(values))
	for i, v := range values {
		q[i] = "'" + arrayElem.Replace(v) + "'"
	}
	return "[" + strings.Join(q, ",") + "]"
}

func joinWhere(conds []string) string { return "WHERE " + strings.Join(conds, "\n  AND ") + "\n" }

// Build is the only place that produces log search SQL. Every statement is
// scoped to the project, reads FROM logs FINAL (redelivered rows collapsed),
// orders newest first with event_id as tie-break, and fetches Limit+1 rows.
// All values are passed as parameters; the SQL is assembled only from
// constant fragments.
func Build(scope Scope, f Filter) (Statement, error) {
	if f.Limit <= 0 {
		return Statement{}, errors.New("query: limit must be positive")
	}
	conds, params, err := where(scope, f, skipNone)
	if err != nil {
		return Statement{}, err
	}
	params["limit"] = strconv.Itoa(f.Limit + 1)
	if f.After != nil {
		conds = append(conds, "(timestamp, event_id) < ({cursor_ts:DateTime64(3, 'UTC')}, {cursor_id:String})")
		params["cursor_ts"] = f.After.Timestamp.UTC().Format(chTime)
		params["cursor_id"] = f.After.EventID
	}
	sql := "SELECT event_id, timestamp, level, service, message, trace_id, attributes\n" +
		"FROM logs FINAL\n" +
		joinWhere(conds) +
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
