package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// QueryConfig configures cmd/query.
type QueryConfig struct {
	HTTPAddr   string        // QUERY_HTTP_ADDR
	APIKey     string        // QUERY_API_KEY, secret: never log it
	MaxWindow  time.Duration // QUERY_MAX_WINDOW, largest to-from range
	MaxLimit   int           // QUERY_MAX_LIMIT, largest page size
	Timeout    time.Duration // QUERY_TIMEOUT, ClickHouse max_execution_time
	MaxTextLen int           // QUERY_MAX_TEXT_LEN, max characters of q, service, trace_id
}

// Query API defaults.
const (
	DefaultQueryHTTPAddr   = ":8082"
	DefaultQueryMaxWindow  = 168 * time.Hour
	DefaultQueryMaxLimit   = 1000
	DefaultQueryTimeout    = 10 * time.Second
	DefaultQueryMaxTextLen = 256
)

// DefaultQueryConfig returns the query API defaults (no key).
func DefaultQueryConfig() QueryConfig {
	return QueryConfig{
		HTTPAddr:   DefaultQueryHTTPAddr,
		MaxWindow:  DefaultQueryMaxWindow,
		MaxLimit:   DefaultQueryMaxLimit,
		Timeout:    DefaultQueryTimeout,
		MaxTextLen: DefaultQueryMaxTextLen,
	}
}

// loadQuery reads the QUERY_* variables.
func loadQuery(getenv func(string) string, cfg *Config) error {
	q := &cfg.Query
	if v := strings.TrimSpace(getenv("QUERY_HTTP_ADDR")); v != "" {
		q.HTTPAddr = v
	}
	q.APIKey = strings.TrimSpace(getenv("QUERY_API_KEY"))

	for _, it := range []struct {
		name string
		dst  *int
	}{
		{"QUERY_MAX_LIMIT", &q.MaxLimit},
		{"QUERY_MAX_TEXT_LEN", &q.MaxTextLen},
	} {
		n, ok, err := positiveInt(getenv, it.name)
		if err != nil {
			return err
		}
		if ok {
			*it.dst = int(n)
		}
	}
	if err := positiveDuration(getenv, "QUERY_MAX_WINDOW", &q.MaxWindow); err != nil {
		return err
	}
	if err := positiveDuration(getenv, "QUERY_TIMEOUT", &q.Timeout); err != nil {
		return err
	}
	if q.Timeout < time.Second {
		return fmt.Errorf("config: QUERY_TIMEOUT must be at least 1s (ClickHouse max_execution_time is in seconds), got %s", q.Timeout)
	}
	return nil
}

// ValidateQuery checks the settings cmd/query needs beyond Load: a query
// key that differs from the ingest key, so neither key works on the other API.
func (c Config) ValidateQuery() error {
	if c.Query.APIKey == "" {
		return errors.New("config: QUERY_API_KEY is required for the query API")
	}
	if c.IngestAPIKey != "" && c.Query.APIKey == c.IngestAPIKey {
		return errors.New("config: QUERY_API_KEY must differ from INGEST_API_KEY")
	}
	return nil
}
