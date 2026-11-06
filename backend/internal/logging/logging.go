// Package logging builds the structured slog logger used by LogSage binaries.
package logging

import (
	"io"
	"log/slog"
)

// New returns a logger writing to w at the given level. format is "text" for
// human-readable output; anything else produces JSON.
func New(w io.Writer, level slog.Level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	if format == "text" {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}
