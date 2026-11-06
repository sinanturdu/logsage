// Package clickhouse is a minimal client for the ClickHouse HTTP interface
// built on net/http (see ADR 0008 for why no driver is used).
package clickhouse

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// maxErrorBytes bounds how much of an error response is read and returned.
const maxErrorBytes = 300

// identifier matches table and database names that are safe to interpolate.
var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// Options configures Client.
type Options struct {
	URL      string // http(s)://host:port of the HTTP interface
	Database string
	User     string
	Password string // sent in a header, never in the URL
	Timeout  time.Duration
}

// Client talks to one ClickHouse server over HTTP.
type Client struct {
	base     *url.URL
	database string
	user     string
	password string
	hc       *http.Client
}

// New validates opts and returns a Client. It does not connect.
func New(opts Options) (*Client, error) {
	u, err := url.Parse(opts.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("clickhouse: invalid URL %q", opts.URL)
	}
	if u.User != nil {
		return nil, errors.New("clickhouse: URL must not contain credentials")
	}
	if !identifier.MatchString(opts.Database) {
		return nil, fmt.Errorf("clickhouse: invalid database name %q", opts.Database)
	}
	if opts.Timeout <= 0 {
		return nil, errors.New("clickhouse: timeout must be positive")
	}
	return &Client{
		base:     u,
		database: opts.Database,
		user:     opts.User,
		password: opts.Password,
		hc:       &http.Client{Timeout: opts.Timeout},
	}, nil
}

// Database returns the configured database name.
func (c *Client) Database() string { return c.database }

// Ping checks that the server answers GET /ping.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodGet, "/ping", nil, nil)
	return err
}

// Ready implements a readiness check (same as Ping).
func (c *Client) Ready(ctx context.Context) error { return c.Ping(ctx) }

// Exec runs one statement that returns no data, such as DDL. Statements must
// use fully qualified table names; no default database is set.
func (c *Client) Exec(ctx context.Context, query string) error {
	_, err := c.do(ctx, http.MethodPost, "/", nil, strings.NewReader(query))
	return err
}

// Query runs one statement in the configured database and returns the raw
// response body (use FORMAT ... in the query to choose the shape).
func (c *Client) Query(ctx context.Context, query string) ([]byte, error) {
	params := url.Values{"database": {c.database}}
	return c.do(ctx, http.MethodPost, "/", params, strings.NewReader(query))
}

// QueryWithParams runs one read statement in the configured database. The
// SQL must reference values only as server-side placeholders {name:Type};
// params maps name to value and is sent as param_<name>, never spliced into
// the SQL. settings are ClickHouse settings for this request (for example
// readonly, max_execution_time). Names must be identifiers.
func (c *Client) QueryWithParams(ctx context.Context, query string, params, settings map[string]string) ([]byte, error) {
	values := url.Values{"database": {c.database}}
	for name, v := range settings {
		if !identifier.MatchString(name) || name == "database" || strings.HasPrefix(name, "param_") {
			return nil, fmt.Errorf("clickhouse: invalid setting name %q", name)
		}
		values.Set(name, v)
	}
	for name, v := range params {
		if !identifier.MatchString(name) {
			return nil, fmt.Errorf("clickhouse: invalid parameter name %q", name)
		}
		values.Set("param_"+name, EscapeParam(v))
	}
	return c.do(ctx, http.MethodPost, "/", values, strings.NewReader(query))
}

// paramEscaper escapes values for query parameters, which ClickHouse parses
// in the TSV "Escaped" format: without it a backslash sequence such as \t
// in user input would be turned into a tab.
var paramEscaper = strings.NewReplacer(`\`, `\\`, "\t", `\t`, "\n", `\n`, "\r", `\r`, "\x00", `\0`)

// EscapeParam returns v escaped for use as a query parameter value.
func EscapeParam(v string) string { return paramEscaper.Replace(v) }

// InsertJSONEachRow inserts body (one JSON object per line) into table in
// the configured database with a single request.
func (c *Client) InsertJSONEachRow(ctx context.Context, table string, body []byte) error {
	if !identifier.MatchString(table) {
		return fmt.Errorf("clickhouse: invalid table name %q", table)
	}
	params := url.Values{
		"database": {c.database},
		"query":    {"INSERT INTO " + table + " FORMAT JSONEachRow"},
	}
	_, err := c.do(ctx, http.MethodPost, "/", params, bytes.NewReader(body))
	return err
}

func (c *Client) do(ctx context.Context, method, path string, params url.Values, body io.Reader) ([]byte, error) {
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	if params != nil {
		u.RawQuery = params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: build request: %w", err)
	}
	if c.user != "" {
		req.Header.Set("X-ClickHouse-User", c.user)
		req.Header.Set("X-ClickHouse-Key", c.password)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		// url.Error includes the URL, which holds no credentials.
		return nil, fmt.Errorf("clickhouse: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &Error{Status: resp.StatusCode, Code: resp.Header.Get("X-ClickHouse-Exception-Code"), Message: readErrorMessage(resp.Body)}
	}
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: read response: %w", err)
	}
	return out, nil
}

// Error is a non-200 answer from ClickHouse.
type Error struct {
	Status  int
	Code    string // ClickHouse exception code, if any
	Message string // first line of the server message, truncated
}

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("clickhouse: HTTP %d (code %s): %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("clickhouse: HTTP %d: %s", e.Status, e.Message)
}

// readErrorMessage returns the first line of the body, truncated. ClickHouse
// parse errors can quote input data, so only a short prefix is kept.
func readErrorMessage(r io.Reader) string {
	line, _ := bufio.NewReader(io.LimitReader(r, 4096)).ReadString('\n')
	line = strings.TrimSpace(line)
	if len(line) > maxErrorBytes {
		line = line[:maxErrorBytes] + "..."
	}
	return line
}
