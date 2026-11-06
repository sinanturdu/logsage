package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name     string
		level    slog.Level
		format   string
		logDebug bool
		wantJSON bool
		wantOut  bool
	}{
		{name: "json info", level: slog.LevelInfo, format: "json", wantJSON: true, wantOut: true},
		{name: "text info", level: slog.LevelInfo, format: "text", wantOut: true},
		{name: "debug filtered at info", level: slog.LevelInfo, format: "json", logDebug: true},
		{name: "debug shown at debug", level: slog.LevelDebug, format: "json", logDebug: true, wantJSON: true, wantOut: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := New(&buf, tt.level, tt.format)
			if tt.logDebug {
				log.Debug("hello", "k", "v")
			} else {
				log.Info("hello", "k", "v")
			}

			out := buf.String()
			if !tt.wantOut {
				if out != "" {
					t.Fatalf("expected no output, got %q", out)
				}
				return
			}
			if tt.wantJSON {
				var m map[string]any
				if err := json.Unmarshal([]byte(out), &m); err != nil {
					t.Fatalf("output is not JSON: %v (%q)", err, out)
				}
				if m["msg"] != "hello" || m["k"] != "v" {
					t.Errorf("unexpected fields: %v", m)
				}
			} else if !strings.Contains(out, "msg=hello") || !strings.Contains(out, "k=v") {
				t.Errorf("unexpected text output: %q", out)
			}
		})
	}
}
