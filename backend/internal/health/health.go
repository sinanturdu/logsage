// Package health exposes liveness and readiness endpoints for LogSage binaries.
package health

import (
	"context"
	"log/slog"
	"net/http"
)

// Register mounts GET /healthz on mux. It reports process liveness only and
// does not check dependencies; GET /readyz does that.
func Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", handleHealthz)
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeStatus(w, http.StatusOK, "ok")
}

// ReadinessChecker reports whether a dependency can serve traffic.
type ReadinessChecker interface {
	Ready(ctx context.Context) error
}

// RegisterReady mounts GET /readyz on mux. It answers 200 {"status":"ready"}
// when every checker succeeds and 503 {"status":"unavailable"} otherwise.
// With no checkers it is always ready. Failure details are logged, not
// returned, so the endpoint does not expose internal addresses.
func RegisterReady(mux *http.ServeMux, log *slog.Logger, checkers ...ReadinessChecker) {
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		for _, c := range checkers {
			if err := c.Ready(r.Context()); err != nil {
				if log != nil {
					log.WarnContext(r.Context(), "readiness check failed", "err", err)
				}
				writeStatus(w, http.StatusServiceUnavailable, "unavailable")
				return
			}
		}
		writeStatus(w, http.StatusOK, "ready")
	})
}

func writeStatus(w http.ResponseWriter, code int, status string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(`{"status":"` + status + `"}`))
}
