package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/alert/secret"
)

// AlertsConfig configures cmd/alerts and the alert endpoints of cmd/api
// (ADR 0024). The secret key is never logged.
type AlertsConfig struct {
	// Enabled (ALERTS_ENABLED) turns alerts on; off by default.
	Enabled bool
	// SecretKey (ALERTS_SECRET_KEY, 32 bytes base64) seals channel URLs and
	// secrets. KeyErr is set when alerts are enabled and the key is missing
	// or malformed: the evaluator refuses to start and the API answers 503.
	SecretKey []byte
	KeyErr    error

	HTTPAddr            string        // ALERTS_HTTP_ADDR (:8084)
	Tick                time.Duration // ALERTS_TICK (5s)
	Lateness            time.Duration // ALERTS_LATENESS (60s, 0..10m)
	QueriesPerCycle     int           // ALERTS_QUERIES_PER_CYCLE (200)
	Concurrency         int           // ALERTS_CONCURRENCY (2)
	DeliveryConcurrency int           // ALERTS_DELIVERY_CONCURRENCY (4)
	MaxAttempts         int           // ALERTS_MAX_ATTEMPTS (8)
	PerChannelPerMinute int           // ALERTS_CHANNEL_RATE (30)
	Retention           time.Duration // ALERTS_RETENTION (720h)
	// AllowInsecureLocal (ALERTS_ALLOW_INSECURE_LOCAL) permits plain http to
	// localhost webhooks: development and tests only.
	AllowInsecureLocal bool
	AllowedPorts       []int  // ALERTS_ALLOWED_PORTS, https ports besides 443
	DashboardURL       string // PUBLIC_DASHBOARD_URL, for links in payloads
}

// ErrAlertKey explains a missing or malformed key without showing it.
var ErrAlertKey = errors.New("ALERTS_SECRET_KEY is missing or not 32 bytes base64; it is required when ALERTS_ENABLED=true")

// DefaultAlertsConfig returns the defaults (alerts off).
func DefaultAlertsConfig() AlertsConfig {
	return AlertsConfig{
		HTTPAddr: ":8084", Tick: 5 * time.Second, Lateness: time.Minute, QueriesPerCycle: 200, Concurrency: 2,
		DeliveryConcurrency: 4, MaxAttempts: 8, PerChannelPerMinute: 30, Retention: 30 * 24 * time.Hour,
	}
}

func boundedDuration(getenv func(string) string, name string, lo, hi time.Duration, dst *time.Duration) error {
	v := strings.TrimSpace(getenv(name))
	if v == "" {
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < lo || d > hi {
		return fmt.Errorf("config: invalid %s %q: want a duration between %s and %s", name, v, lo, hi)
	}
	*dst = d
	return nil
}

// LoadAlerts reads the alert settings. A bad value is an error; a bad key
// with alerts enabled is reported in KeyErr instead, so the API can start
// and say what is wrong.
func LoadAlerts(getenv func(string) string) (AlertsConfig, error) {
	c := DefaultAlertsConfig()
	if err := parseBool(getenv, "ALERTS_ENABLED", &c.Enabled); err != nil {
		return c, err
	}
	if err := parseBool(getenv, "ALERTS_ALLOW_INSECURE_LOCAL", &c.AllowInsecureLocal); err != nil {
		return c, err
	}
	if v := strings.TrimSpace(getenv("ALERTS_HTTP_ADDR")); v != "" {
		c.HTTPAddr = v
	}
	for _, d := range []struct {
		name   string
		lo, hi time.Duration
		dst    *time.Duration
	}{
		{"ALERTS_TICK", time.Second, time.Minute, &c.Tick},
		{"ALERTS_LATENESS", 0, 10 * time.Minute, &c.Lateness},
		{"ALERTS_RETENTION", 24 * time.Hour, 365 * 24 * time.Hour, &c.Retention},
	} {
		if err := boundedDuration(getenv, d.name, d.lo, d.hi, d.dst); err != nil {
			return c, err
		}
	}
	for _, n := range []struct {
		name   string
		lo, hi int
		dst    *int
	}{
		{"ALERTS_QUERIES_PER_CYCLE", 1, 10_000, &c.QueriesPerCycle},
		{"ALERTS_CONCURRENCY", 1, 32, &c.Concurrency},
		{"ALERTS_DELIVERY_CONCURRENCY", 1, 64, &c.DeliveryConcurrency},
		{"ALERTS_MAX_ATTEMPTS", 1, 20, &c.MaxAttempts},
		{"ALERTS_CHANNEL_RATE", 1, 600, &c.PerChannelPerMinute},
	} {
		if err := boundedInt(getenv, n.name, n.lo, n.hi, n.dst); err != nil {
			return c, err
		}
	}
	if v := strings.TrimSpace(getenv("ALERTS_ALLOWED_PORTS")); v != "" {
		for _, part := range strings.Split(v, ",") {
			p, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil || p < 1 || p > 65535 {
				return c, fmt.Errorf("config: invalid ALERTS_ALLOWED_PORTS %q: want comma-separated ports", v)
			}
			c.AllowedPorts = append(c.AllowedPorts, p)
		}
	}
	if v := strings.TrimSpace(getenv("PUBLIC_DASHBOARD_URL")); v != "" {
		u, err := NormalizePublicURL(v)
		if err != nil {
			return c, fmt.Errorf("config: invalid PUBLIC_DASHBOARD_URL: %w", err)
		}
		c.DashboardURL = u
	}
	if c.Enabled {
		key, err := secret.ParseKey(getenv("ALERTS_SECRET_KEY"))
		if err != nil {
			c.KeyErr = ErrAlertKey
		} else {
			c.SecretKey = key
		}
	}
	return c, nil
}

// RequireKey is what the evaluator checks before starting.
func (c AlertsConfig) RequireKey() error {
	if !c.Enabled {
		return errors.New("alerts are disabled: set ALERTS_ENABLED=true")
	}
	return c.KeyErr
}
