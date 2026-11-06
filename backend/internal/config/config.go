// Package config loads service configuration from environment variables.
package config

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
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
}

// IngestLimits bounds the size of ingest requests.
type IngestLimits struct {
	MaxBodyBytes      int64 // INGEST_MAX_BODY_BYTES
	MaxEvents         int   // INGEST_MAX_EVENTS
	MaxMessageBytes   int   // INGEST_MAX_MESSAGE_BYTES
	MaxAttributes     int   // INGEST_MAX_ATTRIBUTES
	MaxAttrKeyChars   int   // INGEST_MAX_ATTR_KEY_CHARS
	MaxAttrValueChars int   // INGEST_MAX_ATTR_VALUE_CHARS
}

// Defaults used when a variable is unset or empty.
const (
	DefaultHTTPAddr  = ":8080"
	DefaultLogLevel  = slog.LevelInfo
	DefaultLogFormat = "json"
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
	}
}

// Load builds a Config using getenv to read variables (pass os.Getenv in production).
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		HTTPAddr:  DefaultHTTPAddr,
		LogLevel:  DefaultLogLevel,
		LogFormat: DefaultLogFormat,
		Ingest:    DefaultIngestLimits(),
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

	return cfg, nil
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
