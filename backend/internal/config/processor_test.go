package config

import (
	"testing"
	"time"
)

func TestLoadProcessor(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		check   func(t *testing.T, c Config)
		wantErr bool
	}{
		{
			name: "defaults",
			env:  map[string]string{},
			check: func(t *testing.T, c Config) {
				want := ClickHouseConfig{URL: "http://localhost:8123", Database: "logsage", User: "default", Timeout: 10 * time.Second, TTLDays: 30}
				if c.ClickHouse != want {
					t.Errorf("ClickHouse = %+v, want %+v", c.ClickHouse, want)
				}
				p := c.Processor
				if p.BatchSize != 1000 || p.FlushInterval != time.Second || p.ClaimIdle != 30*time.Second ||
					p.DeadStream != "logsage:events:dead" || p.HTTPAddr != ":8081" || p.Consumer != "" ||
					p.StatsInterval != 10*time.Second {
					t.Errorf("Processor defaults = %+v", p)
				}
			},
		},
		{
			name: "overrides",
			env: map[string]string{
				"CLICKHOUSE_URL": "https://ch.example:8443/", "CLICKHOUSE_DB": "logs_test", "CLICKHOUSE_USER": "ingest",
				"CLICKHOUSE_PASSWORD": " s3cret ", "CLICKHOUSE_TIMEOUT": "3s", "CLICKHOUSE_TTL_DAYS": "7",
				"PROCESSOR_HTTP_ADDR": ":9000", "PROCESSOR_CONSUMER": "worker-1", "PROCESSOR_DEAD_STREAM": "dead",
				"PROCESSOR_BATCH_SIZE": "50", "PROCESSOR_READ_COUNT": "20", "PROCESSOR_READ_BLOCK": "250ms",
				"PROCESSOR_FLUSH_INTERVAL": "500ms", "PROCESSOR_CLAIM_IDLE": "1m", "PROCESSOR_CLAIM_INTERVAL": "5s",
				"PROCESSOR_RETRY_INITIAL": "100ms", "PROCESSOR_RETRY_MAX": "5s", "PROCESSOR_STATS_INTERVAL": "1s",
				"PROCESSOR_SHUTDOWN_TIMEOUT": "4s",
			},
			check: func(t *testing.T, c Config) {
				want := ClickHouseConfig{URL: "https://ch.example:8443", Database: "logs_test", User: "ingest", Password: " s3cret ", Timeout: 3 * time.Second, TTLDays: 7}
				if c.ClickHouse != want {
					t.Errorf("ClickHouse = %+v, want %+v", c.ClickHouse, want)
				}
				wantP := ProcessorConfig{
					HTTPAddr: ":9000", Consumer: "worker-1", DeadStream: "dead", BatchSize: 50, ReadCount: 20,
					ReadBlock: 250 * time.Millisecond, FlushInterval: 500 * time.Millisecond, ClaimIdle: time.Minute,
					ClaimInterval: 5 * time.Second, RetryInitial: 100 * time.Millisecond, RetryMax: 5 * time.Second,
					StatsInterval: time.Second, ShutdownTimeout: 4 * time.Second,
				}
				if c.Processor != wantP {
					t.Errorf("Processor = %+v, want %+v", c.Processor, wantP)
				}
			},
		},
		{name: "url without scheme", env: map[string]string{"CLICKHOUSE_URL": "localhost:8123"}, wantErr: true},
		{name: "url with credentials", env: map[string]string{"CLICKHOUSE_URL": "http://u:p@localhost:8123"}, wantErr: true},
		{name: "url native protocol", env: map[string]string{"CLICKHOUSE_URL": "clickhouse://localhost:9000"}, wantErr: true},
		{name: "db with sql", env: map[string]string{"CLICKHOUSE_DB": "logs; DROP"}, wantErr: true},
		{name: "db starting with digit", env: map[string]string{"CLICKHOUSE_DB": "1logs"}, wantErr: true},
		{name: "zero ttl", env: map[string]string{"CLICKHOUSE_TTL_DAYS": "0"}, wantErr: true},
		{name: "zero batch size", env: map[string]string{"PROCESSOR_BATCH_SIZE": "0"}, wantErr: true},
		{name: "bad flush interval", env: map[string]string{"PROCESSOR_FLUSH_INTERVAL": "1"}, wantErr: true},
		{name: "negative claim idle", env: map[string]string{"PROCESSOR_CLAIM_IDLE": "-1s"}, wantErr: true},
		{name: "sub-millisecond block", env: map[string]string{"PROCESSOR_READ_BLOCK": "10us"}, wantErr: true},
		{name: "retry initial above max", env: map[string]string{"PROCESSOR_RETRY_INITIAL": "10s", "PROCESSOR_RETRY_MAX": "1s"}, wantErr: true},
		{name: "dead stream equals main stream", env: map[string]string{"PROCESSOR_DEAD_STREAM": "logsage:events"}, wantErr: true},
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
				t.Fatalf("Load() error = %v", err)
			}
			tt.check(t, got)
		})
	}
}

func TestValidateProcessor(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		wantErr bool
	}{
		{name: "redis addr set", addr: "localhost:6379"},
		{name: "redis addr missing", addr: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Load(func(k string) string {
				if k == "REDIS_ADDR" {
					return tt.addr
				}
				return ""
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := c.ValidateProcessor(); (err != nil) != tt.wantErr {
				t.Fatalf("ValidateProcessor() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
