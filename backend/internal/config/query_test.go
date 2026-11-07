package config

import (
	"testing"
	"time"
)

func TestLoadQuery(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    QueryConfig
		wantErr bool
	}{
		{name: "defaults", env: map[string]string{}, want: QueryConfig{HTTPAddr: ":8082", MaxWindow: 168 * time.Hour, MaxLimit: 1000, Timeout: 10 * time.Second, MaxTextLen: 256}},
		{
			name: "overrides",
			env: map[string]string{
				"QUERY_HTTP_ADDR": "127.0.0.1:9100", "QUERY_API_KEY": " read-key ", "QUERY_MAX_WINDOW": "24h",
				"QUERY_MAX_LIMIT": "50", "QUERY_TIMEOUT": "3s", "QUERY_MAX_TEXT_LEN": "64",
			},
			want: QueryConfig{HTTPAddr: "127.0.0.1:9100", APIKey: "read-key", MaxWindow: 24 * time.Hour, MaxLimit: 50, Timeout: 3 * time.Second, MaxTextLen: 64},
		},
		{name: "zero window", env: map[string]string{"QUERY_MAX_WINDOW": "0s"}, wantErr: true},
		{name: "window without unit", env: map[string]string{"QUERY_MAX_WINDOW": "168"}, wantErr: true},
		{name: "zero limit", env: map[string]string{"QUERY_MAX_LIMIT": "0"}, wantErr: true},
		{name: "non-numeric limit", env: map[string]string{"QUERY_MAX_LIMIT": "lots"}, wantErr: true},
		{name: "sub-second timeout", env: map[string]string{"QUERY_TIMEOUT": "500ms"}, wantErr: true},
		{name: "negative text len", env: map[string]string{"QUERY_MAX_TEXT_LEN": "-1"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Load(func(k string) string { return tt.env[k] })
			if tt.wantErr {
				if err == nil {
					t.Fatal("Load() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Query != tt.want {
				t.Errorf("Query = %+v, want %+v", got.Query, tt.want)
			}
		})
	}
}

func TestValidateQuery(t *testing.T) {
	tests := []struct {
		name      string
		queryKey  string
		ingestKey string
		wantErr   bool
	}{
		{name: "separate keys", queryKey: "read", ingestKey: "write"},
		{name: "query key only", queryKey: "read"},
		{name: "missing query key", ingestKey: "write", wantErr: true},
		{name: "same key for both", queryKey: "shared", ingestKey: "shared", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Load(func(k string) string {
				return map[string]string{"QUERY_API_KEY": tt.queryKey, "INGEST_API_KEY": tt.ingestKey, "ALLOW_LEGACY_STATIC_KEYS": "true"}[k]
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := c.ValidateQuery(); (err != nil) != tt.wantErr {
				t.Fatalf("ValidateQuery() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
