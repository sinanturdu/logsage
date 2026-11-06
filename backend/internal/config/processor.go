package config

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ClickHouseConfig configures the ClickHouse HTTP client.
type ClickHouseConfig struct {
	URL      string        // CLICKHOUSE_URL, HTTP interface, e.g. http://localhost:8123
	Database string        // CLICKHOUSE_DB
	User     string        // CLICKHOUSE_USER
	Password string        // CLICKHOUSE_PASSWORD, secret: never log it
	Timeout  time.Duration // CLICKHOUSE_TIMEOUT, per HTTP request
	TTLDays  int           // CLICKHOUSE_TTL_DAYS, retention of the logs table
}

// ProcessorConfig configures cmd/processor.
type ProcessorConfig struct {
	HTTPAddr        string        // PROCESSOR_HTTP_ADDR
	Consumer        string        // PROCESSOR_CONSUMER, empty means hostname-pid
	DeadStream      string        // PROCESSOR_DEAD_STREAM
	BatchSize       int           // PROCESSOR_BATCH_SIZE
	ReadCount       int           // PROCESSOR_READ_COUNT, XREADGROUP COUNT
	ReadBlock       time.Duration // PROCESSOR_READ_BLOCK, XREADGROUP BLOCK
	FlushInterval   time.Duration // PROCESSOR_FLUSH_INTERVAL
	ClaimIdle       time.Duration // PROCESSOR_CLAIM_IDLE, XAUTOCLAIM min-idle-time
	ClaimInterval   time.Duration // PROCESSOR_CLAIM_INTERVAL
	RetryInitial    time.Duration // PROCESSOR_RETRY_INITIAL, first backoff
	RetryMax        time.Duration // PROCESSOR_RETRY_MAX, backoff cap
	StatsInterval   time.Duration // PROCESSOR_STATS_INTERVAL
	ShutdownTimeout time.Duration // PROCESSOR_SHUTDOWN_TIMEOUT, final flush budget
	TrimInterval    time.Duration // PROCESSOR_TRIM_INTERVAL, safe XTRIM MINID cadence
	LagWarnAge      time.Duration // PROCESSOR_LAG_WARN_AGE, warn when the oldest unprocessed entry is older
	DeadMaxLen      int64         // PROCESSOR_DEAD_MAXLEN, approximate cap on the dead-letter stream
}

// Processor and ClickHouse defaults.
const (
	DefaultClickHouseURL     = "http://localhost:8123"
	DefaultClickHouseDB      = "logsage"
	DefaultClickHouseUser    = "default"
	DefaultClickHouseTimeout = 10 * time.Second
	DefaultClickHouseTTLDays = 30
	DefaultProcessorHTTPAddr = ":8081"
	DefaultDeadStream        = "logsage:events:dead"
)

// DefaultProcessorConfig returns the processor defaults.
func DefaultProcessorConfig() ProcessorConfig {
	return ProcessorConfig{
		HTTPAddr:        DefaultProcessorHTTPAddr,
		DeadStream:      DefaultDeadStream,
		BatchSize:       1000,
		ReadCount:       1000,
		ReadBlock:       time.Second,
		FlushInterval:   time.Second,
		ClaimIdle:       30 * time.Second,
		ClaimInterval:   10 * time.Second,
		RetryInitial:    200 * time.Millisecond,
		RetryMax:        30 * time.Second,
		StatsInterval:   10 * time.Second,
		ShutdownTimeout: 10 * time.Second,
		TrimInterval:    5 * time.Second,
		LagWarnAge:      30 * time.Second,
		DeadMaxLen:      100_000,
	}
}

// DefaultClickHouseConfig returns the ClickHouse defaults.
func DefaultClickHouseConfig() ClickHouseConfig {
	return ClickHouseConfig{
		URL:      DefaultClickHouseURL,
		Database: DefaultClickHouseDB,
		User:     DefaultClickHouseUser,
		Timeout:  DefaultClickHouseTimeout,
		TTLDays:  DefaultClickHouseTTLDays,
	}
}

// identifier matches safe ClickHouse database names; they are interpolated
// into SQL, so anything else is rejected.
var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// loadProcessor reads the CLICKHOUSE_* and PROCESSOR_* variables.
func loadProcessor(getenv func(string) string, cfg *Config) error {
	ch := &cfg.ClickHouse
	p := &cfg.Processor

	if v := strings.TrimSpace(getenv("CLICKHOUSE_URL")); v != "" {
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("config: invalid CLICKHOUSE_URL %q: want http(s)://host:port", v)
		}
		if u.User != nil {
			return errors.New("config: CLICKHOUSE_URL must not contain credentials; use CLICKHOUSE_USER and CLICKHOUSE_PASSWORD")
		}
		ch.URL = strings.TrimRight(v, "/")
	}
	if v := strings.TrimSpace(getenv("CLICKHOUSE_DB")); v != "" {
		if !identifier.MatchString(v) {
			return fmt.Errorf("config: invalid CLICKHOUSE_DB %q: want letters, digits and underscores", v)
		}
		ch.Database = v
	}
	if v := strings.TrimSpace(getenv("CLICKHOUSE_USER")); v != "" {
		ch.User = v
	}
	// The password is taken as is (no trimming) and may be empty.
	ch.Password = getenv("CLICKHOUSE_PASSWORD")

	if v := strings.TrimSpace(getenv("PROCESSOR_HTTP_ADDR")); v != "" {
		p.HTTPAddr = v
	}
	p.Consumer = strings.TrimSpace(getenv("PROCESSOR_CONSUMER"))
	if v := strings.TrimSpace(getenv("PROCESSOR_DEAD_STREAM")); v != "" {
		p.DeadStream = v
	}

	ints := []struct {
		name string
		dst  *int
	}{
		{"CLICKHOUSE_TTL_DAYS", &ch.TTLDays},
		{"PROCESSOR_BATCH_SIZE", &p.BatchSize},
		{"PROCESSOR_READ_COUNT", &p.ReadCount},
	}
	for _, it := range ints {
		n, ok, err := positiveInt(getenv, it.name)
		if err != nil {
			return err
		}
		if ok {
			*it.dst = int(n)
		}
	}

	durations := []struct {
		name string
		dst  *time.Duration
	}{
		{"CLICKHOUSE_TIMEOUT", &ch.Timeout},
		{"PROCESSOR_READ_BLOCK", &p.ReadBlock},
		{"PROCESSOR_FLUSH_INTERVAL", &p.FlushInterval},
		{"PROCESSOR_CLAIM_IDLE", &p.ClaimIdle},
		{"PROCESSOR_CLAIM_INTERVAL", &p.ClaimInterval},
		{"PROCESSOR_RETRY_INITIAL", &p.RetryInitial},
		{"PROCESSOR_RETRY_MAX", &p.RetryMax},
		{"PROCESSOR_STATS_INTERVAL", &p.StatsInterval},
		{"PROCESSOR_SHUTDOWN_TIMEOUT", &p.ShutdownTimeout},
		{"PROCESSOR_TRIM_INTERVAL", &p.TrimInterval},
		{"PROCESSOR_LAG_WARN_AGE", &p.LagWarnAge},
	}
	for _, d := range durations {
		if err := positiveDuration(getenv, d.name, d.dst); err != nil {
			return err
		}
	}

	n, ok, err := positiveInt(getenv, "PROCESSOR_DEAD_MAXLEN")
	if err != nil {
		return err
	}
	if ok {
		p.DeadMaxLen = n
	}

	if p.ReadBlock < time.Millisecond {
		return errors.New("config: PROCESSOR_READ_BLOCK must be at least 1ms (0 would block forever)")
	}
	if p.RetryInitial > p.RetryMax {
		return fmt.Errorf("config: PROCESSOR_RETRY_INITIAL (%s) must not exceed PROCESSOR_RETRY_MAX (%s)", p.RetryInitial, p.RetryMax)
	}
	if p.DeadStream == cfg.Redis.Stream {
		return errors.New("config: PROCESSOR_DEAD_STREAM must differ from REDIS_STREAM")
	}
	return nil
}

// ValidateProcessor checks the settings cmd/processor needs beyond Load.
func (c Config) ValidateProcessor() error {
	if c.Redis.Addr == "" {
		return errors.New("config: REDIS_ADDR is required for the processor")
	}
	return nil
}

// positiveDuration parses name as a Go duration > 0 into dst when set.
func positiveDuration(getenv func(string) string, name string, dst *time.Duration) error {
	v := strings.TrimSpace(getenv(name))
	if v == "" {
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return fmt.Errorf("config: invalid %s %q: want a positive duration such as 500ms or 2s", name, v)
	}
	*dst = d
	return nil
}
