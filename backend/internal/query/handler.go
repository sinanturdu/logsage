package query

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/auth"
	"github.com/sinanturdu/logsage/backend/internal/keys"
)

// Path is the route served by Handler.
const Path = "/v1/logs"

// Handler serves GET /v1/logs: API-key authentication in front of Service.
type Handler struct {
	svc  *Service
	auth auth.Authenticator
	log  *slog.Logger
	now  func() time.Time
}

// NewHandler returns a Handler that accepts a query key in X-API-Key.
func NewHandler(store Store, authn auth.Authenticator, opts Options, log *slog.Logger) (*Handler, error) {
	if authn == nil {
		return nil, errors.New("query: authenticator is nil")
	}
	svc, err := NewService(store, opts)
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Handler{svc: svc, auth: authn, log: log, now: time.Now}, nil
}

// Register mounts the handler on mux at Path.
func (h *Handler) Register(mux *http.ServeMux) { mux.Handle(Path, h) }

// ServeHTTP checks method (405), API key (401), parameters (400), then runs
// the query; storage failures map to 503, timeouts to 504.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := h.now()
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed, use GET")
		return
	}
	principal, err := h.auth.Authenticate(r.Context(), r.Header.Get("X-API-Key"), keys.KindQuery)
	if err != nil {
		if errors.Is(err, auth.ErrUnavailable) {
			h.log.WarnContext(r.Context(), "key verification unavailable", "err", err)
			writeError(w, http.StatusServiceUnavailable, "authentication temporarily unavailable, retry later")
			return
		}
		writeError(w, http.StatusUnauthorized, "missing or invalid API key")
		return
	}
	f, err := h.svc.ParseSearch(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	events, next, err := h.svc.Search(r.Context(), ScopeFor(principal.ProjectID), f)
	if err != nil {
		status, msg := Classify(err)
		h.log.WarnContext(r.Context(), "log query failed", "status", status, "took_ms", h.now().Sub(start).Milliseconds(), "err", err)
		writeError(w, status, msg)
		return
	}
	writeJSON(w, http.StatusOK, Response{Events: events, NextCursor: next, TookMs: h.now().Sub(start).Milliseconds()})
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
