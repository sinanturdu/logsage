package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadTail(t *testing.T) {
	cfg, err := Load(func(string) string { return "" })
	if err != nil || cfg.Tail != DefaultTailConfig() {
		t.Fatalf("defaults = %+v, %v", cfg.Tail, err)
	}
	env := map[string]string{
		"TAIL_POLL_INTERVAL": "500ms", "TAIL_BATCH": "50", "TAIL_HEARTBEAT": "5s", "TAIL_MAX_DURATION": "10m",
		"TAIL_AUTH_RECHECK": "10s", "TAIL_MAX_REPLAY": "1m", "TAIL_MAX_LATENESS": "2m", "TAIL_OVERLAP": "5s",
		"TAIL_WRITE_TIMEOUT": "2s", "TAIL_RETRY_MAX": "10s", "TAIL_MAX_CONN_USER": "1", "TAIL_MAX_CONN_PROJECT": "3",
		"TAIL_MAX_CONN_TOTAL": "9", "TAIL_MAX_SEEN": "2000",
	}
	cfg, err = Load(func(k string) string { return env[k] })
	want := TailConfig{PollInterval: 500 * time.Millisecond, Batch: 50, Heartbeat: 5 * time.Second, MaxDuration: 10 * time.Minute,
		AuthRecheck: 10 * time.Second, MaxReplay: time.Minute, MaxLateness: 2 * time.Minute, Overlap: 5 * time.Second,
		WriteTimeout: 2 * time.Second, RetryMax: 10 * time.Second, MaxConnUser: 1, MaxConnProject: 3, MaxConnTotal: 9, MaxSeen: 2000}
	if err != nil || cfg.Tail != want {
		t.Fatalf("overrides = %+v, %v", cfg.Tail, err)
	}

	for name, tt := range map[string]struct {
		env  map[string]string
		want string
	}{
		"bad duration":           {map[string]string{"TAIL_POLL_INTERVAL": "soon"}, "TAIL_POLL_INTERVAL"},
		"negative":               {map[string]string{"TAIL_HEARTBEAT": "-1s"}, "TAIL_HEARTBEAT"},
		"batch too big":          {map[string]string{"TAIL_BATCH": "5000"}, "TAIL_BATCH"},
		"batch zero":             {map[string]string{"TAIL_BATCH": "0"}, "TAIL_BATCH"},
		"poll too fast":          {map[string]string{"TAIL_POLL_INTERVAL": "10ms"}, "at least 100ms"},
		"heartbeat below poll":   {map[string]string{"TAIL_POLL_INTERVAL": "20s", "TAIL_OVERLAP": "1m"}, "TAIL_HEARTBEAT must not be below"},
		"short overlap":          {map[string]string{"TAIL_OVERLAP": "1s"}, "TAIL_OVERLAP must be at least twice"},
		"lateness below overlap": {map[string]string{"TAIL_MAX_LATENESS": "10s"}, "TAIL_MAX_LATENESS"},
		"duration too short":     {map[string]string{"TAIL_MAX_DURATION": "10s"}, "TAIL_MAX_DURATION"},
		"limits out of order":    {map[string]string{"TAIL_MAX_CONN_USER": "20"}, "TAIL_MAX_CONN_USER <= TAIL_MAX_CONN_PROJECT"},
		"total below project":    {map[string]string{"TAIL_MAX_CONN_TOTAL": "5"}, "TAIL_MAX_CONN_PROJECT <= TAIL_MAX_CONN_TOTAL"},
		"tiny seen set":          {map[string]string{"TAIL_MAX_SEEN": "10"}, "TAIL_MAX_SEEN"},
	} {
		_, err := Load(func(k string) string { return tt.env[k] })
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tt.want)
		}
	}
}
