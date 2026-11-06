// Package config loads service configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// Config holds runtime settings shared by the backend binaries.
type Config struct {
	HTTPAddr  string
	LogLevel  slog.Level
	LogFormat string // "json" or "text"

	// IngestAPIKey is the static API key accepted by the ingest API.
	// It is a secret: never log it.
	IngestAPIKey string
	Ingest       IngestLimits
	// IngestMaxStreamLen is the stream length at which ingest answers 429
	// (soft limit, checked against a cached XLEN). INGEST_MAX_STREAM_LEN.
	IngestMaxStreamLen int64

	// Publisher selects where accepted events go: "memory" or "redis".
	Publisher string
	Redis     RedisConfig

	ClickHouse ClickHouseConfig
	Processor  ProcessorConfig
	Query      QueryConfig
}

// RedisConfig configures the Redis Streams publisher.
type RedisConfig struct {
	Addr    string        // REDIS_ADDR, required when Publisher is "redis"
	Stream  string        // REDIS_STREAM
	Timeout time.Duration // REDIS_TIMEOUT, per operation
}

// IngestLimits bounds the size of ingest requests.
type IngestLimits struct {
	MaxBodyBytes      int64 // INGEST_MAX_BODY_BYTES
	MaxEvents         int   // INGEST_MAX_EVENTS
	MaxMessageBytes   int   // INGEST_MAX_MESSAGE_BYTES
	MaxAttributes     int   // INGEST_MAX_ATTRIBUTES
	MaxAttrKeyChars   int   // INGEST_MAX_ATTR_KEY_CHARS
	MaxAttrValueChars int   // INGEST_MAX_ATTR_VALUE_CHARS
	MaxTraceIDChars   int   // INGEST_MAX_TRACE_ID_LEN
}

// Defaults used when a variable is unset or empty.
const (
	DefaultHTTPAddr  = ":8080"
	DefaultLogLevel  = slog.LevelInfo
	DefaultLogFormat = "json"

	PublisherMemory = "memory"
	PublisherRedis  = "redis"

	DefaultPublisher          = PublisherMemory
	DefaultRedisStream        = "logsage:events"
	DefaultIngestMaxStreamLen = 500_000
	DefaultRedisTimeout       = 2 * time.Second
)

// DefaultIngestLimits returns the default ingest limits.
func DefaultIngestLimits() IngestLimits {
	return IngestLimits{
		MaxBodyBytes:      1 << 20, // 1 MiB
		MaxEvents:         1000,
		MaxMessageBytes:   8 << 10, // 8 KiB
		MaxAttributes:     32,
		MaxAttrKeyChars:   64,
		MaxAttrValueChars: 1024,
		MaxTraceIDChars:   128,
	}
}

// Load builds a Config using getenv to read variables (pass os.Getenv in production).
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		HTTPAddr:  DefaultHTTPAddr,
		LogLevel:  DefaultLogLevel,
		LogFormat: DefaultLogFormat,
		Ingest:    DefaultIngestLimits(),

		IngestMaxStreamLen: DefaultIngestMaxStreamLen,
		Publisher:          DefaultPublisher,
		Redis: RedisConfig{
			Stream:  DefaultRedisStream,
			Timeout: DefaultRedisTimeout,
		},
		ClickHouse: DefaultClickHouseConfig(),
		Processor:  DefaultProcessorConfig(),
		Query:      DefaultQueryConfig(),
	}

	if v := strings.TrimSpace(getenv("HTTP_ADDR")); v != "" {
		cfg.HTTPAddr = v
	}

	if v := strings.TrimSpace(getenv("LOG_LEVEL")); v != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(v)); err != nil {
			return Config{}, fmt.Errorf("config: invalid LOG_LEVEL %q: %w", v, err)
		}
	}

	if v := strings.ToLower(strings.TrimSpace(getenv("LOG_FORMAT"))); v != "" {
		if v != "json" && v != "text" {
			return Config{}, fmt.Errorf("config: invalid LOG_FORMAT %q: want json or text", v)
		}
		cfg.LogFormat = v
	}

	cfg.IngestAPIKey = strings.TrimSpace(getenv("INGEST_API_KEY"))

	ints := []struct {
		name string
		dst  *int
	}{
		{"INGEST_MAX_EVENTS", &cfg.Ingest.MaxEvents},
		{"INGEST_MAX_MESSAGE_BYTES", &cfg.Ingest.MaxMessageBytes},
		{"INGEST_MAX_ATTRIBUTES", &cfg.Ingest.MaxAttributes},
		{"INGEST_MAX_ATTR_KEY_CHARS", &cfg.Ingest.MaxAttrKeyChars},
		{"INGEST_MAX_ATTR_VALUE_CHARS", &cfg.Ingest.MaxAttrValueChars},
		{"INGEST_MAX_TRACE_ID_LEN", &cfg.Ingest.MaxTraceIDChars},
	}
	for _, it := range ints {
		n, ok, err := positiveInt(getenv, it.name)
		if err != nil {
			return Config{}, err
		}
		if ok {
			*it.dst = int(n)
		}
	}
	n, ok, err := positiveInt(getenv, "INGEST_MAX_BODY_BYTES")
	if err != nil {
		return Config{}, err
	}
	if ok {
		cfg.Ingest.MaxBodyBytes = n
	}

	if v := strings.TrimSpace(getenv("INGEST_MAX_STREAM_LEN")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("config: invalid INGEST_MAX_STREAM_LEN %q: want a positive integer", v)
		}
		cfg.IngestMaxStreamLen = n
	}

	if err := loadPublisher(getenv, &cfg); err != nil {
		return Config{}, err
	}
	if err := loadProcessor(getenv, &cfg); err != nil {
		return Config{}, err
	}
	if err := loadQuery(getenv, &cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// loadPublisher reads PUBLISHER and the REDIS_* settings. Redis values are
// validated even when the memory publisher is selected, so typos fail fast.
func loadPublisher(getenv func(string) string, cfg *Config) error {
	if v := strings.ToLower(strings.TrimSpace(getenv("PUBLISHER"))); v != "" {
		if v != PublisherMemory && v != PublisherRedis {
			return fmt.Errorf("config: invalid PUBLISHER %q: want memory or redis", v)
		}
		cfg.Publisher = v
	}

	cfg.Redis.Addr = strings.TrimSpace(getenv("REDIS_ADDR"))

	if v := strings.TrimSpace(getenv("REDIS_STREAM")); v != "" {
		cfg.Redis.Stream = v
	}

	// REDIS_STREAM_MAXLEN was removed: trimming by length at publish time
	// dropped unread events. Fail instead of silently ignoring an old value.
	if v := strings.TrimSpace(getenv("REDIS_STREAM_MAXLEN")); v != "" {
		return errors.New("config: REDIS_STREAM_MAXLEN is no longer supported (length trimming lost unread events); " +
			"remove it and use INGEST_MAX_STREAM_LEN for backpressure, see ADR 0010")
	}

	if v := strings.TrimSpace(getenv("REDIS_TIMEOUT")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return fmt.Errorf("config: invalid REDIS_TIMEOUT %q: want a positive duration such as 2s or 500ms", v)
		}
		cfg.Redis.Timeout = d
	}

	if cfg.Publisher == PublisherRedis && cfg.Redis.Addr == "" {
		return fmt.Errorf("config: REDIS_ADDR is required when PUBLISHER=redis")
	}
	return nil
}

// positiveInt reads name and parses it as an integer > 0. ok is false when unset.
func positiveInt(getenv func(string) string, name string) (n int64, ok bool, err error) {
	v := strings.TrimSpace(getenv(name))
	if v == "" {
		return 0, false, nil
	}
	n, err = strconv.ParseInt(v, 10, 32)
	if err != nil || n <= 0 {
		return 0, false, fmt.Errorf("config: invalid %s %q: want a positive integer", name, v)
	}
	return n, true, nil
}
