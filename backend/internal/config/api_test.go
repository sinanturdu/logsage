package config

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLoadAPI(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		check   func(t *testing.T, a APIConfig)
		wantErr bool
	}{
		{name: "defaults", env: map[string]string{}, check: func(t *testing.T, a APIConfig) {
			if a != DefaultAPIConfig() || !a.CookieSecure || a.RegistrationEnabled || a.Origins() != nil {
				t.Errorf("defaults = %+v", a)
			}
		}},
		{
			name: "overrides",
			env: map[string]string{
				"API_HTTP_ADDR": "127.0.0.1:9000", "SESSION_IDLE_TTL": "1h", "SESSION_ABSOLUTE_TTL": "24h",
				"COOKIE_SECURE": "false", "REGISTRATION_ENABLED": "true",
				"ALLOWED_ORIGINS":   " https://App.Example.com , http://localhost:5173/ ,",
				"ARGON2_MEMORY_KIB": "19456", "ARGON2_ITERATIONS": "2", "ARGON2_PARALLELISM": "1", "ARGON2_MAX_CONCURRENT": "8",
				"LOGIN_MAX_FAILURES": "3", "LOGIN_DELAY_BASE": "2s", "LOGIN_DELAY_MAX": "1m", "LOGIN_LIMITER_SIZE": "500", "API_MAX_CONCURRENT_QUERIES": "8",
				"WEB_ENABLED": "false", "PUBLIC_INGEST_URL": "https://ingest.example.com/",
			},
			check: func(t *testing.T, a APIConfig) {
				want := APIConfig{
					HTTPAddr: "127.0.0.1:9000", SessionIdleTTL: time.Hour, SessionAbsoluteTTL: 24 * time.Hour,
					CookieSecure: false, RegistrationEnabled: true,
					AllowedOrigins:  "https://app.example.com,http://localhost:5173",
					Argon2MemoryKiB: 19456, Argon2Iterations: 2, Argon2Parallelism: 1, Argon2MaxConcurrent: 8,
					LoginMaxFailures: 3, LoginDelayBase: 2 * time.Second, LoginDelayMax: time.Minute, LoginLimiterSize: 500, MaxConcurrentQueries: 8,
					WebEnabled: false, PublicIngestURL: "https://ingest.example.com",
				}
				if a != want {
					t.Errorf("got  %+v\nwant %+v", a, want)
				}
				if o := a.Origins(); len(o) != 2 {
					t.Errorf("Origins = %v", o)
				}
			},
		},
		{name: "idle longer than absolute", env: map[string]string{"SESSION_IDLE_TTL": "48h", "SESSION_ABSOLUTE_TTL": "24h"}, wantErr: true},
		{name: "zero idle", env: map[string]string{"SESSION_IDLE_TTL": "0s"}, wantErr: true},
		{name: "bad bool", env: map[string]string{"COOKIE_SECURE": "maybe"}, wantErr: true},
		{name: "wildcard origin", env: map[string]string{"ALLOWED_ORIGINS": "*"}, wantErr: true},
		{name: "origin with path", env: map[string]string{"ALLOWED_ORIGINS": "https://a.example/app"}, wantErr: true},
		{name: "origin without scheme", env: map[string]string{"ALLOWED_ORIGINS": "a.example"}, wantErr: true},
		{name: "origin with userinfo", env: map[string]string{"ALLOWED_ORIGINS": "https://u@a.example"}, wantErr: true},
		{name: "ftp origin", env: map[string]string{"ALLOWED_ORIGINS": "ftp://a.example"}, wantErr: true},
		{name: "argon2 memory below OWASP minimum", env: map[string]string{"ARGON2_MEMORY_KIB": "8192"}, wantErr: true},
		{name: "argon2 zero iterations", env: map[string]string{"ARGON2_ITERATIONS": "0"}, wantErr: true},
		{name: "argon2 zero concurrency", env: map[string]string{"ARGON2_MAX_CONCURRENT": "0"}, wantErr: true},
		{name: "delay base above max", env: map[string]string{"LOGIN_DELAY_BASE": "1h", "LOGIN_DELAY_MAX": "1m"}, wantErr: true},
		{name: "tiny limiter", env: map[string]string{"LOGIN_LIMITER_SIZE": "5"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(func(k string) string { return tt.env[k] })
			if tt.wantErr {
				if err == nil {
					t.Fatal("Load() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, cfg.API)
		})
	}
}

func TestValidateAPI(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	cfg, _ := Load(get)
	if err := cfg.ValidateAPI(get); err == nil || !strings.Contains(err.Error(), "DATABASE_URL, CLICKHOUSE_URL, CLICKHOUSE_DB, CLICKHOUSE_USER, CLICKHOUSE_PASSWORD") {
		t.Errorf("empty env: %v", err)
	}
	env["DATABASE_URL"] = "postgres://u:p@localhost:5432/db"
	env["CLICKHOUSE_URL"], env["CLICKHOUSE_DB"], env["CLICKHOUSE_USER"] = "http://localhost:8123", "logsage", "logsage"
	cfg, _ = Load(get)
	if err := cfg.ValidateAPI(get); err == nil || err.Error() != "config: the management API needs CLICKHOUSE_PASSWORD" {
		t.Errorf("API without ClickHouse password: %v", err)
	}
	env["CLICKHOUSE_PASSWORD"] = "secret"
	cfg, _ = Load(get)
	if err := cfg.ValidateAPI(get); err != nil {
		t.Error(err)
	}
	if cfg.API.MaxConcurrentQueries != 4 {
		t.Errorf("default MaxConcurrentQueries = %d", cfg.API.MaxConcurrentQueries)
	}
	for v, ok := range map[string]bool{"1": true, "16": true, "0": false, "x": false, "1001": false} {
		env["API_MAX_CONCURRENT_QUERIES"] = v
		cfg, err := Load(get)
		if (err == nil) != ok {
			t.Errorf("API_MAX_CONCURRENT_QUERIES=%s: err = %v", v, err)
		}
		if ok && strconv.Itoa(cfg.API.MaxConcurrentQueries) != v {
			t.Errorf("API_MAX_CONCURRENT_QUERIES=%s: got %d", v, cfg.API.MaxConcurrentQueries)
		}
	}
}

func TestWebConfig(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		enabled bool
		ingest  string
		wantErr bool
	}{
		{"defaults", nil, true, "http://localhost:8080", false},
		{"disabled", map[string]string{"WEB_ENABLED": "false"}, false, "http://localhost:8080", false},
		{"bad bool", map[string]string{"WEB_ENABLED": "maybe"}, true, "", true},
		{"https with port and path", map[string]string{"PUBLIC_INGEST_URL": "https://logs.example.com:8443/ingest/"}, true, "https://logs.example.com:8443/ingest", false},
		{"trailing slash", map[string]string{"PUBLIC_INGEST_URL": "http://localhost:8080/"}, true, "http://localhost:8080", false},
		{"ftp", map[string]string{"PUBLIC_INGEST_URL": "ftp://example.com"}, true, "", true},
		{"no scheme", map[string]string{"PUBLIC_INGEST_URL": "example.com:8080"}, true, "", true},
		{"credentials", map[string]string{"PUBLIC_INGEST_URL": "https://user:pass@example.com"}, true, "", true},
		{"query", map[string]string{"PUBLIC_INGEST_URL": "https://example.com/?key=1"}, true, "", true},
		{"empty query", map[string]string{"PUBLIC_INGEST_URL": "https://example.com/?"}, true, "", true},
		{"fragment", map[string]string{"PUBLIC_INGEST_URL": "https://example.com/#x"}, true, "", true},
		{"javascript", map[string]string{"PUBLIC_INGEST_URL": "javascript:alert(1)"}, true, "", true},
		{"no host", map[string]string{"PUBLIC_INGEST_URL": "http:///path"}, true, "", true},
		{"port only", map[string]string{"PUBLIC_INGEST_URL": "http://:8080"}, true, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := Load(func(k string) string { return c.env[k] })
			if c.wantErr {
				if err == nil {
					t.Fatalf("want an error, got %+v", cfg.API)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.API.WebEnabled != c.enabled || cfg.API.PublicIngestURL != c.ingest {
				t.Fatalf("got enabled=%v ingest=%q, want %v %q", cfg.API.WebEnabled, cfg.API.PublicIngestURL, c.enabled, c.ingest)
			}
		})
	}
}
