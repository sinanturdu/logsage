package processor

import (
	"encoding/json"
	"net/http"
)

// RegisterStats mounts GET /stats on mux. It always answers 200 with the
// counters; stream figures are null (and stream_error is set) when Redis
// does not answer.
func (p *Processor) RegisterStats(mux *http.ServeMux) {
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(p.Stats(r.Context()))
	})
}
