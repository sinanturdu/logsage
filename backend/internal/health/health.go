// Package health exposes liveness endpoints for LogSage binaries.
package health

import "net/http"

// Register mounts GET /healthz on mux. It reports process liveness only and
// does not check dependencies (a separate /readyz will do that).
func Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", handleHealthz)
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
