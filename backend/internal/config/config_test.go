package config

import (
	"log/slog"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	base := Config{
		HTTPAddr: ":8080", LogLevel: slog.LevelInfo, LogFormat: "json", Ingest: DefaultIngestLimits(),
		Publisher: "memory",
		Redis:     RedisConfig{Stream: "logsage:events", Timeout: 2 * time.Second},

		IngestMaxStreamLen: 500000,
		ClickHouse:         DefaultClickHouseConfig(),
		Processor:          DefaultProcessorConfig(),
		Query:              DefaultQueryConfig(),
		Postgres:           DefaultPostgresConfig(),
	}
	with := func(f func(*Config)) Config {
		c := base
		f(&c)
		return c
	}

	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr bool
	}{
		{
			name: "defaults",
			env:  map[string]string{},
			want: base,
		},
		{
			name: "overrides",
			env:  map[string]string{"HTTP_ADDR": "127.0.0.1:9090", "LOG_LEVEL": "debug", "LOG_FORMAT": "TEXT"},
			want: with(func(c *Config) { c.HTTPAddr = "127.0.0.1:9090"; c.LogLevel = slog.LevelDebug; c.LogFormat = "text" }),
		},
		{
			name: "whitespace treated as unset",
			env:  map[string]string{"HTTP_ADDR": "  ", "LOG_LEVEL": " ", "INGEST_MAX_EVENTS": " "},
			want: base,
		},
		{
			name: "ingest key and limits",
			env: map[string]string{
				"INGEST_API_KEY":              " secret ",
				"INGEST_MAX_BODY_BYTES":       "2048",
				"INGEST_MAX_EVENTS":           "10",
				"INGEST_MAX_MESSAGE_BYTES":    "100",
				"INGEST_MAX_ATTRIBUTES":       "4",
				"INGEST_MAX_ATTR_KEY_CHARS":   "8",
				"INGEST_MAX_ATTR_VALUE_CHARS": "16",
				"INGEST_MAX_TRACE_ID_LEN":     "32",
			},
			want: with(func(c *Config) {
				c.IngestAPIKey = "secret"
				c.Ingest = IngestLimits{MaxBodyBytes: 2048, MaxEvents: 10, MaxMessageBytes: 100, MaxAttributes: 4, MaxAttrKeyChars: 8, MaxAttrValueChars: 16, MaxTraceIDChars: 32}
			}),
		},
		{
			name: "redis publisher",
			env: map[string]string{
				"PUBLISHER": " Redis ", "REDIS_ADDR": "localhost:6379", "REDIS_STREAM": "s",
				"INGEST_MAX_STREAM_LEN": "5000000000", "REDIS_TIMEOUT": "500ms",
			},
			want: with(func(c *Config) {
				c.Publisher = "redis"
				c.Redis = RedisConfig{Addr: "localhost:6379", Stream: "s", Timeout: 500 * time.Millisecond}
				c.IngestMaxStreamLen = 5_000_000_000
			}),
		},
		{
			name: "memory publisher keeps redis addr",
			env:  map[string]string{"PUBLISHER": "memory", "REDIS_ADDR": "localhost:6379"},
			want: with(func(c *Config) { c.Redis.Addr = "localhost:6379" }),
		},
		{name: "invalid publisher", env: map[string]string{"PUBLISHER": "kafka"}, wantErr: true},
		{name: "redis without addr", env: map[string]string{"PUBLISHER": "redis"}, wantErr: true},
		{name: "redis with blank addr", env: map[string]string{"PUBLISHER": "redis", "REDIS_ADDR": "  "}, wantErr: true},
		{name: "removed REDIS_STREAM_MAXLEN fails fast", env: map[string]string{"REDIS_STREAM_MAXLEN": "100000"}, wantErr: true},
		{name: "zero max stream len", env: map[string]string{"INGEST_MAX_STREAM_LEN": "0"}, wantErr: true},
		{name: "negative max stream len", env: map[string]string{"INGEST_MAX_STREAM_LEN": "-5"}, wantErr: true},
		{name: "non-numeric max stream len", env: map[string]string{"INGEST_MAX_STREAM_LEN": "lots"}, wantErr: true},
		{name: "timeout without unit", env: map[string]string{"REDIS_TIMEOUT": "2"}, wantErr: true},
		{name: "zero timeout", env: map[string]string{"REDIS_TIMEOUT": "0s"}, wantErr: true},
		{name: "negative timeout", env: map[string]string{"REDIS_TIMEOUT": "-1s"}, wantErr: true},
		{name: "invalid log level", env: map[string]string{"LOG_LEVEL": "verbose"}, wantErr: true},
		{name: "invalid log format", env: map[string]string{"LOG_FORMAT": "xml"}, wantErr: true},
		{name: "non-numeric limit", env: map[string]string{"INGEST_MAX_EVENTS": "many"}, wantErr: true},
		{name: "zero limit", env: map[string]string{"INGEST_MAX_ATTRIBUTES": "0"}, wantErr: true},
		{name: "negative limit", env: map[string]string{"INGEST_MAX_BODY_BYTES": "-1"}, wantErr: true},
		{name: "zero trace id limit", env: map[string]string{"INGEST_MAX_TRACE_ID_LEN": "0"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Load(func(k string) string { return tt.env[k] })
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Load() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("Load() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
