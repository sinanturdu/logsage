package config

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// APIConfig configures the management API (cmd/api, ADR 0013).
type APIConfig struct {
	HTTPAddr           string        // API_HTTP_ADDR
	SessionIdleTTL     time.Duration // SESSION_IDLE_TTL
	SessionAbsoluteTTL time.Duration // SESSION_ABSOLUTE_TTL
	// CookieSecure (COOKIE_SECURE) sets the Secure flag and the __Host-
	// cookie prefix. Only local plain-http setups should turn it off.
	CookieSecure bool
	// AllowedOrigins (ALLOWED_ORIGINS) is a normalized, comma-separated
	// list of scheme://host[:port] origins allowed for CORS and CSRF
	// checks. A string keeps Config comparable; use Origins().
	AllowedOrigins      string
	RegistrationEnabled bool // REGISTRATION_ENABLED

	Argon2MemoryKiB     int // ARGON2_MEMORY_KIB
	Argon2Iterations    int // ARGON2_ITERATIONS
	Argon2Parallelism   int // ARGON2_PARALLELISM
	Argon2MaxConcurrent int // ARGON2_MAX_CONCURRENT: hashes computed at once

	LoginMaxFailures int           // LOGIN_MAX_FAILURES: free failures before delays start
	LoginDelayBase   time.Duration // LOGIN_DELAY_BASE: first delay, doubled per further failure
	LoginDelayMax    time.Duration // LOGIN_DELAY_MAX
	LoginLimiterSize int           // LOGIN_LIMITER_SIZE: tracked IPs plus email hashes

	// MaxConcurrentQueries (API_MAX_CONCURRENT_QUERIES) caps the log reads
	// one user may run at once; the limits of each read are the QUERY_* ones.
	MaxConcurrentQueries int
}

// DefaultAPIConfig returns the management API defaults.
func DefaultAPIConfig() APIConfig {
	return APIConfig{
		HTTPAddr:           ":8083",
		SessionIdleTTL:     12 * time.Hour,
		SessionAbsoluteTTL: 7 * 24 * time.Hour,
		CookieSecure:       true,

		Argon2MemoryKiB:     64 * 1024,
		Argon2Iterations:    3,
		Argon2Parallelism:   2,
		Argon2MaxConcurrent: 4,

		LoginMaxFailures: 5,
		LoginDelayBase:   time.Second,
		LoginDelayMax:    15 * time.Minute,
		LoginLimiterSize: 10_000,

		MaxConcurrentQueries: 4,
	}
}

// Origins returns AllowedOrigins as a list (nil when empty).
func (a APIConfig) Origins() []string {
	if a.AllowedOrigins == "" {
		return nil
	}
	return strings.Split(a.AllowedOrigins, ",")
}

// NormalizeOrigin checks that s is scheme://host[:port] with scheme http or
// https and nothing else, and returns it in lower case.
func NormalizeOrigin(s string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || s == "*" {
		return "", fmt.Errorf("invalid origin %q: want scheme://host[:port]", s)
	}
	return strings.ToLower(u.Scheme + "://" + u.Host), nil
}

func parseBool(getenv func(string) string, name string, dst *bool) error {
	v := strings.TrimSpace(getenv(name))
	if v == "" {
		return nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fmt.Errorf("config: invalid %s %q: want true or false", name, v)
	}
	*dst = b
	return nil
}

func boundedInt(getenv func(string) string, name string, lo, hi int, dst *int) error {
	v := strings.TrimSpace(getenv(name))
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return fmt.Errorf("config: invalid %s %q: want %d..%d", name, v, lo, hi)
	}
	*dst = n
	return nil
}

func loadAPI(getenv func(string) string, cfg *Config) error {
	a := &cfg.API
	if v := strings.TrimSpace(getenv("API_HTTP_ADDR")); v != "" {
		a.HTTPAddr = v
	}
	for _, d := range []struct {
		name string
		dst  *time.Duration
	}{
		{"SESSION_IDLE_TTL", &a.SessionIdleTTL},
		{"SESSION_ABSOLUTE_TTL", &a.SessionAbsoluteTTL},
		{"LOGIN_DELAY_BASE", &a.LoginDelayBase},
		{"LOGIN_DELAY_MAX", &a.LoginDelayMax},
	} {
		if err := positiveDuration(getenv, d.name, d.dst); err != nil {
			return err
		}
	}
	if err := parseBool(getenv, "COOKIE_SECURE", &a.CookieSecure); err != nil {
		return err
	}
	if err := parseBool(getenv, "REGISTRATION_ENABLED", &a.RegistrationEnabled); err != nil {
		return err
	}
	if v := strings.TrimSpace(getenv("ALLOWED_ORIGINS")); v != "" {
		var out []string
		for _, o := range strings.Split(v, ",") {
			if strings.TrimSpace(o) == "" {
				continue
			}
			n, err := NormalizeOrigin(o)
			if err != nil {
				return fmt.Errorf("config: ALLOWED_ORIGINS: %w", err)
			}
			out = append(out, n)
		}
		a.AllowedOrigins = strings.Join(out, ",")
	}
	ints := []struct {
		name   string
		lo, hi int
		dst    *int
	}{
		// OWASP minimum m=19 MiB; 4 GiB upper bound against typos.
		{"ARGON2_MEMORY_KIB", 19 * 1024, 4 * 1024 * 1024, &a.Argon2MemoryKiB},
		{"ARGON2_ITERATIONS", 1, 20, &a.Argon2Iterations},
		{"ARGON2_PARALLELISM", 1, 16, &a.Argon2Parallelism},
		{"ARGON2_MAX_CONCURRENT", 1, 256, &a.Argon2MaxConcurrent},
		{"LOGIN_MAX_FAILURES", 1, 1000, &a.LoginMaxFailures},
		{"LOGIN_LIMITER_SIZE", 100, 10_000_000, &a.LoginLimiterSize},
		{"API_MAX_CONCURRENT_QUERIES", 1, 1000, &a.MaxConcurrentQueries},
	}
	for _, it := range ints {
		if err := boundedInt(getenv, it.name, it.lo, it.hi, it.dst); err != nil {
			return err
		}
	}
	if a.SessionIdleTTL > a.SessionAbsoluteTTL {
		return errors.New("config: SESSION_IDLE_TTL must not exceed SESSION_ABSOLUTE_TTL")
	}
	if a.LoginDelayBase > a.LoginDelayMax {
		return errors.New("config: LOGIN_DELAY_BASE must not exceed LOGIN_DELAY_MAX")
	}
	return nil
}

// APIRequiredEnv are the variables cmd/api needs set explicitly: users and
// sessions live in PostgreSQL, and the log read endpoints query ClickHouse,
// where the defaults (localhost, user default, no password) are never what
// a deployment wants.
var APIRequiredEnv = []string{"DATABASE_URL", "CLICKHOUSE_URL", "CLICKHOUSE_DB", "CLICKHOUSE_USER", "CLICKHOUSE_PASSWORD"}

// ValidateAPI checks what cmd/api needs beyond Load. getenv is the same
// lookup Load used; every APIRequiredEnv variable must be non-empty.
func (c Config) ValidateAPI(getenv func(string) string) error {
	var unset []string
	for _, name := range APIRequiredEnv {
		if strings.TrimSpace(getenv(name)) == "" {
			unset = append(unset, name)
		}
	}
	if len(unset) > 0 {
		return fmt.Errorf("config: the management API needs %s", strings.Join(unset, ", "))
	}
	if c.Postgres.URL == "" {
		return errors.New("config: the management API needs DATABASE_URL")
	}
	return nil
}
