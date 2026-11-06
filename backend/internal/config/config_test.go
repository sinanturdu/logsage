package config

import (
	"log/slog"
	"testing"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr bool
	}{
		{
			name: "defaults",
			env:  map[string]string{},
			want: Config{HTTPAddr: ":8080", LogLevel: slog.LevelInfo, LogFormat: "json"},
		},
		{
			name: "overrides",
			env:  map[string]string{"HTTP_ADDR": "127.0.0.1:9090", "LOG_LEVEL": "debug", "LOG_FORMAT": "TEXT"},
			want: Config{HTTPAddr: "127.0.0.1:9090", LogLevel: slog.LevelDebug, LogFormat: "text"},
		},
		{
			name: "whitespace treated as unset",
			env:  map[string]string{"HTTP_ADDR": "  ", "LOG_LEVEL": " "},
			want: Config{HTTPAddr: ":8080", LogLevel: slog.LevelInfo, LogFormat: "json"},
		},
		{
			name:    "invalid log level",
			env:     map[string]string{"LOG_LEVEL": "verbose"},
			wantErr: true,
		},
		{
			name:    "invalid log format",
			env:     map[string]string{"LOG_FORMAT": "xml"},
			wantErr: true,
		},
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
