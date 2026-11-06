// Package config loads service configuration from environment variables.
package config

import (
	"fmt"
	"log/slog"
	"strings"
)

// Config holds runtime settings shared by the backend binaries.
type Config struct {
	HTTPAddr  string
	LogLevel  slog.Level
	LogFormat string // "json" or "text"
}

// Defaults used when a variable is unset or empty.
const (
	DefaultHTTPAddr  = ":8080"
	DefaultLogLevel  = slog.LevelInfo
	DefaultLogFormat = "json"
)

// Load builds a Config using getenv to read variables (pass os.Getenv in production).
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		HTTPAddr:  DefaultHTTPAddr,
		LogLevel:  DefaultLogLevel,
		LogFormat: DefaultLogFormat,
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

	return cfg, nil
}
