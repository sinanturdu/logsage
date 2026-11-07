package config

import (
	"fmt"
	"time"
)

// TailConfig configures the live tail of cmd/api (ADR 0015).
type TailConfig struct {
	PollInterval   time.Duration // TAIL_POLL_INTERVAL
	Batch          int           // TAIL_BATCH: rows per poll; a full batch polls again at once
	Heartbeat      time.Duration // TAIL_HEARTBEAT
	MaxDuration    time.Duration // TAIL_MAX_DURATION: then the stream closes and the client resumes
	AuthRecheck    time.Duration // TAIL_AUTH_RECHECK: session and membership re-check
	MaxReplay      time.Duration // TAIL_MAX_REPLAY: older Last-Event-IDs get a reset
	MaxLateness    time.Duration // TAIL_MAX_LATENESS: older event timestamps are not streamed live
	Overlap        time.Duration // TAIL_OVERLAP: how much later than its inserted_at a row may become visible
	WriteTimeout   time.Duration // TAIL_WRITE_TIMEOUT: a client slower than this per write is dropped
	RetryMax       time.Duration // TAIL_RETRY_MAX: backoff cap while ClickHouse fails
	MaxConnUser    int           // TAIL_MAX_CONN_USER
	MaxConnProject int           // TAIL_MAX_CONN_PROJECT
	MaxConnTotal   int           // TAIL_MAX_CONN_TOTAL
	MaxSeen        int           // TAIL_MAX_SEEN: delivered ids remembered per stream
}

// DefaultTailConfig returns the live tail defaults.
func DefaultTailConfig() TailConfig {
	return TailConfig{
		PollInterval: time.Second, Batch: 200, Heartbeat: 15 * time.Second, MaxDuration: time.Hour,
		AuthRecheck: 30 * time.Second, MaxReplay: 5 * time.Minute, MaxLateness: 10 * time.Minute,
		Overlap: 30 * time.Second, WriteTimeout: 10 * time.Second, RetryMax: 30 * time.Second,
		MaxConnUser: 2, MaxConnProject: 10, MaxConnTotal: 50, MaxSeen: 50_000,
	}
}

func loadTail(getenv func(string) string, cfg *Config) error {
	t := &cfg.Tail
	for _, d := range []struct {
		name string
		dst  *time.Duration
	}{
		{"TAIL_POLL_INTERVAL", &t.PollInterval}, {"TAIL_HEARTBEAT", &t.Heartbeat}, {"TAIL_MAX_DURATION", &t.MaxDuration},
		{"TAIL_AUTH_RECHECK", &t.AuthRecheck}, {"TAIL_MAX_REPLAY", &t.MaxReplay}, {"TAIL_MAX_LATENESS", &t.MaxLateness},
		{"TAIL_OVERLAP", &t.Overlap}, {"TAIL_WRITE_TIMEOUT", &t.WriteTimeout}, {"TAIL_RETRY_MAX", &t.RetryMax},
	} {
		if err := positiveDuration(getenv, d.name, d.dst); err != nil {
			return err
		}
	}
	for _, it := range []struct {
		name   string
		lo, hi int
		dst    *int
	}{
		{"TAIL_BATCH", 1, 1000, &t.Batch},
		{"TAIL_MAX_CONN_USER", 1, 100, &t.MaxConnUser},
		{"TAIL_MAX_CONN_PROJECT", 1, 10_000, &t.MaxConnProject},
		{"TAIL_MAX_CONN_TOTAL", 1, 100_000, &t.MaxConnTotal},
		{"TAIL_MAX_SEEN", 1000, 1_000_000, &t.MaxSeen},
	} {
		if err := boundedInt(getenv, it.name, it.lo, it.hi, it.dst); err != nil {
			return err
		}
	}
	return t.validate()
}

// validate checks the relations between the settings.
func (t TailConfig) validate() error {
	checks := []struct {
		ok  bool
		msg string
	}{
		{t.PollInterval >= 100*time.Millisecond, "TAIL_POLL_INTERVAL must be at least 100ms"},
		{t.Heartbeat >= t.PollInterval, "TAIL_HEARTBEAT must not be below TAIL_POLL_INTERVAL"},
		{t.MaxDuration >= time.Minute && t.MaxDuration > t.Heartbeat && t.MaxDuration >= t.AuthRecheck, "TAIL_MAX_DURATION must be at least 1m and above TAIL_HEARTBEAT and TAIL_AUTH_RECHECK"},
		{t.Overlap >= 2*t.PollInterval, "TAIL_OVERLAP must be at least twice TAIL_POLL_INTERVAL"},
		{t.MaxLateness >= t.Overlap, "TAIL_MAX_LATENESS must not be below TAIL_OVERLAP"},
		{t.MaxReplay >= t.PollInterval, "TAIL_MAX_REPLAY must not be below TAIL_POLL_INTERVAL"},
		{t.WriteTimeout >= time.Second, "TAIL_WRITE_TIMEOUT must be at least 1s"},
		{t.RetryMax >= t.PollInterval, "TAIL_RETRY_MAX must not be below TAIL_POLL_INTERVAL"},
		{t.MaxConnUser <= t.MaxConnProject && t.MaxConnProject <= t.MaxConnTotal, "need TAIL_MAX_CONN_USER <= TAIL_MAX_CONN_PROJECT <= TAIL_MAX_CONN_TOTAL"},
	}
	for _, c := range checks {
		if !c.ok {
			return fmt.Errorf("config: %s", c.msg)
		}
	}
	return nil
}
