package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// AuthConfig configures API key authentication for ingest and query.
type AuthConfig struct {
	CacheTTL    time.Duration // AUTH_CACHE_TTL: how long a verified key stays valid without a database check
	NegativeTTL time.Duration // AUTH_NEGATIVE_TTL: how long a rejected key is remembered
	CacheSize   int           // AUTH_CACHE_SIZE: max cached keys
	// AllowLegacy (ALLOW_LEGACY_STATIC_KEYS) keeps INGEST_API_KEY and
	// QUERY_API_KEY working, mapped to the default project. Deprecated.
	AllowLegacy bool
}

// DefaultAuthConfig returns the auth defaults.
func DefaultAuthConfig() AuthConfig {
	return AuthConfig{CacheTTL: 30 * time.Second, NegativeTTL: 5 * time.Second, CacheSize: 10_000, AllowLegacy: false}
}

func loadAuth(getenv func(string) string, cfg *Config) error {
	a := &cfg.Auth
	if err := positiveDuration(getenv, "AUTH_CACHE_TTL", &a.CacheTTL); err != nil {
		return err
	}
	if err := positiveDuration(getenv, "AUTH_NEGATIVE_TTL", &a.NegativeTTL); err != nil {
		return err
	}
	if v := strings.TrimSpace(getenv("AUTH_CACHE_SIZE")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 10_000_000 {
			return fmt.Errorf("config: invalid AUTH_CACHE_SIZE %q: want 1..10000000", v)
		}
		a.CacheSize = n
	}
	if v := strings.TrimSpace(getenv("ALLOW_LEGACY_STATIC_KEYS")); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("config: invalid ALLOW_LEGACY_STATIC_KEYS %q: want true or false", v)
		}
		a.AllowLegacy = b
	}
	return nil
}

// validateKeySource checks that a service has a way to verify keys: a
// database, or an allowed legacy static key. The two legacy keys must differ.
func (c Config) validateKeySource(service, legacyVar, legacyKey string) error {
	if c.Auth.AllowLegacy && c.IngestAPIKey != "" && c.IngestAPIKey == c.Query.APIKey {
		return errors.New("config: QUERY_API_KEY must differ from INGEST_API_KEY")
	}
	if c.Postgres.URL != "" {
		return nil
	}
	if c.Auth.AllowLegacy && legacyKey != "" {
		return nil
	}
	return fmt.Errorf("config: the %s API needs DATABASE_URL for per-project keys (or ALLOW_LEGACY_STATIC_KEYS=true and %s)", service, legacyVar)
}

// ValidateIngest checks what cmd/ingest needs to authenticate requests.
func (c Config) ValidateIngest() error {
	return c.validateKeySource("ingest", "INGEST_API_KEY", c.IngestAPIKey)
}
