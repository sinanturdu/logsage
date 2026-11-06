package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadPostgres(t *testing.T) {
	// DATABASE_URL passwords must be percent-encoded; this one needs no escaping.
	const secret = "s3cr3tPassw0rd-x9"
	tests := []struct {
		name      string
		env       map[string]string
		wantURL   string
		wantRedac string
		wantPool  PostgresConfig // URL ignored
		wantErr   string
	}{
		{name: "unset is allowed", env: map[string]string{}, wantPool: DefaultPostgresConfig()},
		{
			name:      "database url",
			env:       map[string]string{"DATABASE_URL": " postgres://app:" + secret + "@localhost:5432/logsage?sslmode=disable "},
			wantURL:   "postgres://app:" + secret + "@localhost:5432/logsage?sslmode=disable",
			wantRedac: "postgres://app:xxxxx@localhost:5432/logsage?sslmode=disable",
			wantPool:  DefaultPostgresConfig(),
		},
		{
			name:      "parts with escaping and defaults",
			env:       map[string]string{"POSTGRES_USER": "logsage", "POSTGRES_PASSWORD": "p@ss word/#?"},
			wantURL:   "postgres://logsage:p%40ss%20word%2F%23%3F@localhost:5432/logsage?sslmode=disable",
			wantRedac: "postgres://logsage:xxxxx@localhost:5432/logsage?sslmode=disable",
			wantPool:  DefaultPostgresConfig(),
		},
		{
			name: "database url wins over parts, pool overrides",
			env: map[string]string{
				"DATABASE_URL": "postgresql://u:p@db.internal:6543/x", "POSTGRES_USER": "ignored",
				"DATABASE_MAX_CONNS": "25", "DATABASE_CONNECT_TIMEOUT": "2s", "DATABASE_QUERY_TIMEOUT": "750ms",
			},
			wantURL:   "postgresql://u:p@db.internal:6543/x",
			wantRedac: "postgresql://u:xxxxx@db.internal:6543/x",
			wantPool:  PostgresConfig{MaxConns: 25, ConnectTimeout: 2 * time.Second, QueryTimeout: 750 * time.Millisecond},
		},
		{name: "wrong scheme", env: map[string]string{"DATABASE_URL": "mysql://u:" + secret + "@h/db"}, wantErr: "must start with postgres://"},
		{name: "no host", env: map[string]string{"DATABASE_URL": "postgres://u:" + secret + "@/db"}, wantErr: "no host"},
		{name: "no database", env: map[string]string{"DATABASE_URL": "postgres://u:" + secret + "@localhost:5432"}, wantErr: "no database name"},
		{name: "no user", env: map[string]string{"DATABASE_URL": "postgres://localhost/db"}, wantErr: "no user"},
		{name: "unparseable", env: map[string]string{"DATABASE_URL": "postgres://u:" + secret + "@local host:%zz/db"}, wantErr: "not a valid URL"},
		{name: "keyword dsn not supported", env: map[string]string{"DATABASE_URL": "host=localhost password=" + secret}, wantErr: "postgres://"},
		{name: "zero max conns", env: map[string]string{"DATABASE_MAX_CONNS": "0"}, wantErr: "DATABASE_MAX_CONNS"},
		{name: "too many conns", env: map[string]string{"DATABASE_MAX_CONNS": "5000"}, wantErr: "DATABASE_MAX_CONNS"},
		{name: "bad connect timeout", env: map[string]string{"DATABASE_CONNECT_TIMEOUT": "5"}, wantErr: "DATABASE_CONNECT_TIMEOUT"},
		{name: "zero query timeout", env: map[string]string{"DATABASE_QUERY_TIMEOUT": "0s"}, wantErr: "DATABASE_QUERY_TIMEOUT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Load(func(k string) string { return tt.env[k] })
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load() error = %v, want %q", err, tt.wantErr)
				}
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaks the password: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.Postgres.URL != tt.wantURL || c.Postgres.Redacted() != tt.wantRedac {
				t.Errorf("URL = %q redacted %q, want %q / %q", c.Postgres.URL, c.Postgres.Redacted(), tt.wantURL, tt.wantRedac)
			}
			got := c.Postgres
			got.URL = ""
			if got != tt.wantPool {
				t.Errorf("pool = %+v, want %+v", got, tt.wantPool)
			}
			if strings.Contains(c.Postgres.Redacted(), secret) || strings.Contains(c.Postgres.Redacted(), "p%40ss") {
				t.Errorf("Redacted() leaks the password: %s", c.Postgres.Redacted())
			}
		})
	}
}

func TestPostgresLocalityAndValidate(t *testing.T) {
	tests := []struct {
		url   string
		local bool
	}{
		{"postgres://u:p@localhost:5432/db", true},
		{"postgres://u:p@127.0.0.1/db", true},
		{"postgres://u:p@[::1]:5432/db", true},
		{"postgres://u:p@postgres:5432/db", false},
		{"postgres://u:p@db.example.com/db", false},
		{"postgres://u:p@10.0.0.5/db", false},
	}
	for _, tt := range tests {
		if got := (PostgresConfig{URL: tt.url}).IsLocal(); got != tt.local {
			t.Errorf("IsLocal(%s) = %v, want %v", tt.url, got, tt.local)
		}
	}
	if err := (Config{}).ValidateDatabase(); err == nil {
		t.Error("ValidateDatabase() accepted an empty URL")
	}
	if err := (Config{Postgres: PostgresConfig{URL: "postgres://u:p@localhost/db"}}).ValidateDatabase(); err != nil {
		t.Errorf("ValidateDatabase() = %v", err)
	}
}
