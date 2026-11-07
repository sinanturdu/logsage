package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PostgresConfig configures the PostgreSQL connection (cmd/admin today; the
// services from v0.2 package H on).
type PostgresConfig struct {
	// URL is DATABASE_URL, or built from POSTGRES_HOST/PORT/USER/PASSWORD/DB.
	// It contains the password: log only Redacted().
	URL            string
	MaxConns       int32         // DATABASE_MAX_CONNS
	ConnectTimeout time.Duration // DATABASE_CONNECT_TIMEOUT
	QueryTimeout   time.Duration // DATABASE_QUERY_TIMEOUT, per statement
}

// DefaultPostgresConfig returns the database defaults (no URL).
func DefaultPostgresConfig() PostgresConfig {
	return PostgresConfig{MaxConns: 10, ConnectTimeout: 5 * time.Second, QueryTimeout: 5 * time.Second}
}

// Redacted returns URL with the password replaced, safe for logs.
func (p PostgresConfig) Redacted() string {
	if p.URL == "" {
		return ""
	}
	u, err := url.Parse(p.URL)
	if err != nil {
		return "(unparseable database URL)"
	}
	return u.Redacted()
}

// Host returns the host name of URL ("" if unparseable).
func (p PostgresConfig) Host() string {
	u, err := url.Parse(p.URL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// IsLocal reports whether the database host is localhost or a loopback IP.
func (p PostgresConfig) IsLocal() bool {
	h := p.Host()
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// loadPostgres reads DATABASE_URL (or the POSTGRES_* parts) and the pool
// settings. An unset database is fine here; ValidateDatabase requires it.
func loadPostgres(getenv func(string) string, cfg *Config) error {
	p := &cfg.Postgres
	p.URL = strings.TrimSpace(getenv("DATABASE_URL"))
	if p.URL == "" {
		if user := strings.TrimSpace(getenv("POSTGRES_USER")); user != "" {
			p.URL = urlFromParts(getenv, user)
		}
	}
	if p.URL != "" {
		if err := validateDatabaseURL(p.URL); err != nil {
			return err
		}
	}

	if v := strings.TrimSpace(getenv("DATABASE_MAX_CONNS")); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n < 1 || n > 1000 {
			return fmt.Errorf("config: invalid DATABASE_MAX_CONNS %q: want 1..1000", v)
		}
		p.MaxConns = int32(n)
	}
	if err := positiveDuration(getenv, "DATABASE_CONNECT_TIMEOUT", &p.ConnectTimeout); err != nil {
		return err
	}
	return positiveDuration(getenv, "DATABASE_QUERY_TIMEOUT", &p.QueryTimeout)
}

// urlFromParts builds a postgres URL from POSTGRES_HOST (localhost),
// POSTGRES_PORT (5432), POSTGRES_USER, POSTGRES_PASSWORD and POSTGRES_DB
// (default: the user name). Values are escaped.
func urlFromParts(getenv func(string) string, user string) string {
	or := func(k, def string) string {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
		return def
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, getenv("POSTGRES_PASSWORD")),
		Host:     net.JoinHostPort(or("POSTGRES_HOST", "localhost"), or("POSTGRES_PORT", "5432")),
		Path:     "/" + or("POSTGRES_DB", user),
		RawQuery: "sslmode=" + or("POSTGRES_SSLMODE", "disable"),
	}
	return u.String()
}

// validateDatabaseURL checks the shape of a postgres URL. Error messages
// never contain the URL (it holds the password).
func validateDatabaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("config: DATABASE_URL is not a valid URL (value not shown: it may contain a password)")
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return errors.New("config: DATABASE_URL must start with postgres:// or postgresql://")
	}
	if u.Hostname() == "" {
		return fmt.Errorf("config: DATABASE_URL has no host: %s", u.Redacted())
	}
	if strings.Trim(u.Path, "/") == "" {
		return fmt.Errorf("config: DATABASE_URL has no database name: %s", u.Redacted())
	}
	if u.User == nil || u.User.Username() == "" {
		return fmt.Errorf("config: DATABASE_URL has no user: %s", u.Redacted())
	}
	return nil
}

// ValidateDatabase checks the settings a binary that talks to PostgreSQL needs.
func (c Config) ValidateDatabase() error {
	if c.Postgres.URL == "" {
		return errors.New("config: DATABASE_URL (or POSTGRES_USER and friends) is required")
	}
	return nil
}
