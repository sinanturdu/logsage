package config

import (
	"log/slog"
	"testing"
)

func TestLoad(t *testing.T) {
	base := Config{HTTPAddr: ":8080", LogLevel: slog.LevelInfo, LogFormat: "json", Ingest: DefaultIngestLimits()}
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
			},
			want: with(func(c *Config) {
				c.IngestAPIKey = "secret"
				c.Ingest = IngestLimits{MaxBodyBytes: 2048, MaxEvents: 10, MaxMessageBytes: 100, MaxAttributes: 4, MaxAttrKeyChars: 8, MaxAttrValueChars: 16}
			}),
		},
		{name: "invalid log level", env: map[string]string{"LOG_LEVEL": "verbose"}, wantErr: true},
		{name: "invalid log format", env: map[string]string{"LOG_FORMAT": "xml"}, wantErr: true},
		{name: "non-numeric limit", env: map[string]string{"INGEST_MAX_EVENTS": "many"}, wantErr: true},
		{name: "zero limit", env: map[string]string{"INGEST_MAX_ATTRIBUTES": "0"}, wantErr: true},
		{name: "negative limit", env: map[string]string{"INGEST_MAX_BODY_BYTES": "-1"}, wantErr: true},
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
