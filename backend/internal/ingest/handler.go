package ingest

import (
	"bytes"
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
	"strconv"
	"sync/atomic"
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

	accepted         atomic.Int64 // events answered with 202
	rejectedOverload atomic.Int64 // requests answered with 429
	publishErrors    atomic.Int64 // requests answered with 503
}

// RetryAfterSeconds is the Retry-After value sent with 429 responses.
const RetryAfterSeconds = 1

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
		if errors.Is(err, ErrOverloaded) {
			// Backpressure: nothing was written; the client should slow down.
			h.rejectedOverload.Add(1)
			w.Header().Set("Retry-After", strconv.Itoa(RetryAfterSeconds))
			writeError(w, http.StatusTooManyRequests, "ingest queue is full, retry after the Retry-After delay", -1)
			return
		}
		h.publishErrors.Add(1)
		h.log.ErrorContext(r.Context(), "publish events", "count", len(events), "err", err)
		writeError(w, http.StatusServiceUnavailable, "events could not be accepted, retry later", -1)
		return
	}

	h.accepted.Add(int64(len(events)))
	h.log.DebugContext(r.Context(), "events accepted", "count", len(events))
	writeJSON(w, http.StatusAccepted, map[string]int{"accepted": len(events)})
}

// Stats is the ingest state reported on GET /stats.
type Stats struct {
	AcceptedTotal         int64  `json:"accepted_total"`          // events answered with 202
	RejectedOverloadTotal int64  `json:"rejected_overload_total"` // requests answered with 429
	PublishErrorsTotal    int64  `json:"publish_errors_total"`    // requests answered with 503
	StreamLengthCached    *int64 `json:"stream_length_cached"`    // last XLEN seen; null before the first refresh or with the memory publisher
	MaxStreamLen          *int64 `json:"max_stream_len"`          // backpressure limit; null with the memory publisher
}

// lengthReporter is implemented by publishers with backpressure.
type lengthReporter interface {
	CachedStreamLength() (int64, bool)
}

// Stats returns the counters and, for the Redis publisher, the cached length.
func (h *Handler) Stats() Stats {
	s := Stats{
		AcceptedTotal:         h.accepted.Load(),
		RejectedOverloadTotal: h.rejectedOverload.Load(),
		PublishErrorsTotal:    h.publishErrors.Load(),
	}
	if lr, ok := h.pub.(lengthReporter); ok {
		if n, known := lr.CachedStreamLength(); known {
			s.StreamLengthCached = &n
		}
	}
	if rp, ok := h.pub.(*RedisPublisher); ok {
		limit := rp.opts.MaxStreamLen
		s.MaxStreamLen = &limit
	}
	return s
}

// RegisterStats mounts GET /stats on mux. Like /healthz it needs no key and
// holds no secrets; in Docker it is only published on 127.0.0.1.
func (h *Handler) RegisterStats(mux *http.ServeMux) {
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, h.Stats())
	})
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

// decode reads exactly one JSON object from the size-limited body.
func (h *Handler) decode(w http.ResponseWriter, r *http.Request) (batchRequest, error) {
	return decodeBatch(http.MaxBytesReader(w, r.Body, h.lim.MaxBodyBytes))
}

// ParseBatch decodes and validates body exactly like POST /v1/ingest
// (strict JSON, then the event rules in lim), without the HTTP layer and
// the body size check. Tools such as cmd/loadgen use it to check payloads.
func ParseBatch(body []byte, lim Limits) ([]Event, error) {
	req, err := decodeBatch(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	return validateBatch(req, lim)
}

// decodeBatch reads exactly one JSON object. Unknown fields and trailing
// data are rejected.
func decodeBatch(body io.Reader) (batchRequest, error) {
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
