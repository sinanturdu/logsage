package ingest

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
)

// Path is the route served by Handler.
const Path = "/v1/ingest"

// Publisher hands validated events to the next stage (a queue in production).
type Publisher interface {
	Publish(ctx context.Context, events []Event) error
}

// Handler serves POST /v1/ingest.
type Handler struct {
	pub       Publisher
	keyDigest [sha256.Size]byte
	lim       Limits
	log       *slog.Logger
}

// NewHandler returns a Handler that accepts requests carrying apiKey in the
// X-API-Key header. apiKey must not be empty.
func NewHandler(pub Publisher, apiKey string, lim Limits, log *slog.Logger) (*Handler, error) {
	if pub == nil {
		return nil, errors.New("ingest: publisher is nil")
	}
	if apiKey == "" {
		return nil, errors.New("ingest: API key is empty")
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Handler{pub: pub, keyDigest: sha256.Sum256([]byte(apiKey)), lim: lim, log: log}, nil
}

// Register mounts the handler on mux at Path.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.Handle(Path, h)
}

// ServeHTTP checks, in order: method, API key, content type, body size, then
// JSON shape and event validity. Valid batches are published and answered
// with 202 {"accepted": N}.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed, use POST", -1)
		return
	}

	if !h.authorized(r.Header.Get("X-API-Key")) {
		writeError(w, http.StatusUnauthorized, "missing or invalid API key", -1)
		return
	}

	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json", -1)
		return
	}

	if r.ContentLength > h.lim.MaxBodyBytes {
		writeError(w, http.StatusRequestEntityTooLarge, h.tooLargeMsg(), -1)
		return
	}

	req, err := h.decode(w, r)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, h.tooLargeMsg(), -1)
			return
		}
		writeError(w, http.StatusBadRequest, err.Error(), -1)
		return
	}

	events, err := validateBatch(req, h.lim)
	if err != nil {
		var ve *ValidationError
		if errors.As(err, &ve) {
			writeError(w, http.StatusBadRequest, ve.Error(), ve.Index)
			return
		}
		writeError(w, http.StatusBadRequest, err.Error(), -1)
		return
	}

	if err := h.pub.Publish(r.Context(), events); err != nil {
		h.log.ErrorContext(r.Context(), "publish events", "count", len(events), "err", err)
		writeError(w, http.StatusServiceUnavailable, "events could not be accepted, retry later", -1)
		return
	}

	h.log.DebugContext(r.Context(), "events accepted", "count", len(events))
	writeJSON(w, http.StatusAccepted, map[string]int{"accepted": len(events)})
}

// authorized compares key with the configured key in constant time. Hashing
// both sides first keeps the comparison independent of the key length.
func (h *Handler) authorized(key string) bool {
	if key == "" {
		return false
	}
	got := sha256.Sum256([]byte(key))
	return subtle.ConstantTimeCompare(got[:], h.keyDigest[:]) == 1
}

// decode reads exactly one JSON object from the size-limited body. Unknown
// fields and trailing data are rejected.
func (h *Handler) decode(w http.ResponseWriter, r *http.Request) (batchRequest, error) {
	body := http.MaxBytesReader(w, r.Body, h.lim.MaxBodyBytes)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()

	var req batchRequest
	if err := dec.Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return batchRequest{}, err
		}
		if errors.Is(err, io.EOF) {
			return batchRequest{}, errors.New("request body is empty")
		}
		return batchRequest{}, fmt.Errorf("invalid JSON body: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return batchRequest{}, err
		}
		return batchRequest{}, errors.New("invalid JSON body: unexpected data after the top-level object")
	}
	return req, nil
}

func (h *Handler) tooLargeMsg() string {
	return fmt.Sprintf("request body exceeds %d bytes", h.lim.MaxBodyBytes)
}

// errorBody is the JSON shape of every error response.
type errorBody struct {
	Error string `json:"error"`
	Index *int   `json:"index,omitempty"`
}

// writeError writes {"error": msg} plus "index" when index >= 0.
func writeError(w http.ResponseWriter, status int, msg string, index int) {
	body := errorBody{Error: msg}
	if index >= 0 {
		body.Index = &index
	}
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
