package config

import (
	"testing"
	"time"
)

func TestLoadAuth(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    AuthConfig
		wantErr bool
	}{
		// Legacy static keys are off by default since v0.2.0 (ADR 0021).
		{name: "defaults", env: map[string]string{}, want: AuthConfig{CacheTTL: 30 * time.Second, NegativeTTL: 5 * time.Second, CacheSize: 10000, AllowLegacy: false}},
		{name: "overrides", env: map[string]string{"AUTH_CACHE_TTL": "2s", "AUTH_NEGATIVE_TTL": "500ms", "AUTH_CACHE_SIZE": "50", "ALLOW_LEGACY_STATIC_KEYS": "true"},
			want: AuthConfig{CacheTTL: 2 * time.Second, NegativeTTL: 500 * time.Millisecond, CacheSize: 50, AllowLegacy: true}},
		{name: "zero ttl", env: map[string]string{"AUTH_CACHE_TTL": "0s"}, wantErr: true},
		{name: "ttl without unit", env: map[string]string{"AUTH_NEGATIVE_TTL": "5"}, wantErr: true},
		{name: "zero size", env: map[string]string{"AUTH_CACHE_SIZE": "0"}, wantErr: true},
		{name: "bad bool", env: map[string]string{"ALLOW_LEGACY_STATIC_KEYS": "maybe"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Load(func(k string) string { return tt.env[k] })
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && c.Auth != tt.want {
				t.Errorf("Auth = %+v, want %+v", c.Auth, tt.want)
			}
		})
	}
}

func TestValidateKeySource(t *testing.T) {
	db := "postgres://u:p@localhost/db"
	tests := []struct {
		name                string
		env                 map[string]string
		ingestErr, queryErr bool
	}{
		{name: "database only", env: map[string]string{"DATABASE_URL": db}},
		{name: "legacy keys only", env: map[string]string{"INGEST_API_KEY": "i", "QUERY_API_KEY": "q", "ALLOW_LEGACY_STATIC_KEYS": "true"}},
		{name: "legacy keys without the flag (the default)", env: map[string]string{"INGEST_API_KEY": "i", "QUERY_API_KEY": "q"}, ingestErr: true, queryErr: true},
		{name: "nothing", env: map[string]string{}, ingestErr: true, queryErr: true},
		{name: "legacy keys but flag off", env: map[string]string{"INGEST_API_KEY": "i", "QUERY_API_KEY": "q", "ALLOW_LEGACY_STATIC_KEYS": "false"}, ingestErr: true, queryErr: true},
		{name: "database with flag off", env: map[string]string{"DATABASE_URL": db, "ALLOW_LEGACY_STATIC_KEYS": "false"}},
		{name: "equal legacy keys", env: map[string]string{"DATABASE_URL": db, "INGEST_API_KEY": "same", "QUERY_API_KEY": "same", "ALLOW_LEGACY_STATIC_KEYS": "true"}, ingestErr: true, queryErr: true},
		{name: "equal legacy keys ignored when flag off", env: map[string]string{"DATABASE_URL": db, "INGEST_API_KEY": "same", "QUERY_API_KEY": "same", "ALLOW_LEGACY_STATIC_KEYS": "false"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Load(func(k string) string { return tt.env[k] })
			if err != nil {
				t.Fatal(err)
			}
			if err := c.ValidateIngest(); (err != nil) != tt.ingestErr {
				t.Errorf("ValidateIngest() = %v, wantErr %v", err, tt.ingestErr)
			}
			if err := c.ValidateQuery(); (err != nil) != tt.queryErr {
				t.Errorf("ValidateQuery() = %v, wantErr %v", err, tt.queryErr)
			}
		})
	}
}
