package config

import (
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
				"LOGIN_MAX_FAILURES": "3", "LOGIN_DELAY_BASE": "2s", "LOGIN_DELAY_MAX": "1m", "LOGIN_LIMITER_SIZE": "500",
			},
			check: func(t *testing.T, a APIConfig) {
				want := APIConfig{
					HTTPAddr: "127.0.0.1:9000", SessionIdleTTL: time.Hour, SessionAbsoluteTTL: 24 * time.Hour,
					CookieSecure: false, RegistrationEnabled: true,
					AllowedOrigins:  "https://app.example.com,http://localhost:5173",
					Argon2MemoryKiB: 19456, Argon2Iterations: 2, Argon2Parallelism: 1, Argon2MaxConcurrent: 8,
					LoginMaxFailures: 3, LoginDelayBase: 2 * time.Second, LoginDelayMax: time.Minute, LoginLimiterSize: 500,
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
	cfg, _ := Load(func(string) string { return "" })
	if cfg.ValidateAPI() == nil {
		t.Error("API without DATABASE_URL accepted")
	}
	cfg, _ = Load(func(k string) string {
		if k == "DATABASE_URL" {
			return "postgres://u:p@localhost:5432/db"
		}
		return ""
	})
	if err := cfg.ValidateAPI(); err != nil {
		t.Error(err)
	}
}
