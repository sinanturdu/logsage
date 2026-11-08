package api

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/query"
	"github.com/sinanturdu/logsage/backend/internal/storage/postgres"
)

// DefaultMaxConcurrentQueries is the per-user cap on running log reads.
const DefaultMaxConcurrentQueries = 4

// LogReader is what the read endpoints need from log storage
// (*query.Service): the same validation and project-scoped statements as
// the API-key endpoint /v1/logs (ADR 0014).
type LogReader interface {
	ParseSearch(rawQuery string) (query.Filter, error)
	ParseHistogram(rawQuery string) (query.Filter, time.Duration, error)
	ParseFacets(rawQuery string) (query.Filter, error)
	Search(ctx context.Context, scope query.Scope, f query.Filter) ([]query.Event, *string, error)
	Histogram(ctx context.Context, scope query.Scope, f query.Filter, bucket time.Duration) (query.Histogram, error)
	Facets(ctx context.Context, scope query.Scope, f query.Filter) (query.Facets, error)

	// Error groups (ADR 0022).
	ParseGroups(rawQuery string) (query.GroupQuery, error)
	ParseGroupWindow(rawQuery string) (query.Filter, error)
	Groups(ctx context.Context, scope query.Scope, q query.GroupQuery) (query.GroupList, error)
	GroupDetail(ctx context.Context, scope query.Scope, f query.Filter, fp uint64) (query.GroupDetail, error)
	GroupExists(ctx context.Context, scope query.Scope, fp uint64) (bool, error)
}

// queryGate caps the log reads running at once per user. It is per
// instance, like the login limiter.
type queryGate struct {
	mu      sync.Mutex
	max     int
	running map[string]int
}

func newQueryGate(max int) *queryGate {
	if max <= 0 {
		max = DefaultMaxConcurrentQueries
	}
	return &queryGate{max: max, running: map[string]int{}}
}

// acquire takes a slot for user; ok is false when the user is at the cap.
func (g *queryGate) acquire(user string) (release func(), ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running[user] >= g.max {
		return nil, false
	}
	g.running[user]++
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.running[user]--; g.running[user] <= 0 {
			delete(g.running, user)
		}
	}, true
}

// readFunc runs a validated read; start is when the request arrived.
type readFunc func(ctx context.Context, start time.Time) (any, error)

// readLogs wraps one read endpoint: membership with view rights (404 for
// non-members and unknown slugs, before any parameter is looked at), then
// parameter validation (400 with the /v1/logs messages), then the per-user
// cap (429), then the query scoped to the member's project.
func (s *Server) readLogs(prepare func(scope query.Scope, r *http.Request) (readFunc, error)) func(http.ResponseWriter, *http.Request, postgres.Session) {
	return func(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
		start := s.opts.Now()
		if s.logs == nil {
			writeError(w, http.StatusNotFound)
			return
		}
		m, ok := s.member(w, r, sess, actView)
		if !ok {
			return
		}
		read, err := prepare(query.ScopeFor(m.ID), r)
		if err != nil {
			writeInvalid(w, err.Error())
			return
		}
		release, ok := s.gate.acquire(sess.UserID)
		if !ok {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests)
			return
		}
		defer release()
		body, err := read(r.Context(), start)
		if err != nil {
			status, msg := query.Classify(err)
			s.log.Warn("log read failed", "path", r.URL.Path, "status", status, "took_ms", s.since(start), "err", err)
			writeJSON(w, status, map[string]string{"error": msg})
			return
		}
		writeJSON(w, http.StatusOK, body)
	}
}

func (s *Server) since(start time.Time) int64 { return s.opts.Now().Sub(start).Milliseconds() }

// prepareLogs serves GET /api/projects/{slug}/logs: the /v1/logs search.
func (s *Server) prepareLogs(scope query.Scope, r *http.Request) (readFunc, error) {
	f, err := s.logs.ParseSearch(r.URL.RawQuery)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, start time.Time) (any, error) {
		events, next, err := s.logs.Search(ctx, scope, f)
		if err != nil {
			return nil, err
		}
		return query.Response{Events: events, NextCursor: next, TookMs: s.since(start)}, nil
	}, nil
}

// prepareHistogram serves GET /api/projects/{slug}/histogram.
func (s *Server) prepareHistogram(scope query.Scope, r *http.Request) (readFunc, error) {
	f, bucket, err := s.logs.ParseHistogram(r.URL.RawQuery)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, start time.Time) (any, error) {
		h, err := s.logs.Histogram(ctx, scope, f, bucket)
		if err != nil {
			return nil, err
		}
		h.TookMs = s.since(start)
		return h, nil
	}, nil
}

// prepareFacets serves GET /api/projects/{slug}/facets.
func (s *Server) prepareFacets(scope query.Scope, r *http.Request) (readFunc, error) {
	f, err := s.logs.ParseFacets(r.URL.RawQuery)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, start time.Time) (any, error) {
		fc, err := s.logs.Facets(ctx, scope, f)
		if err != nil {
			return nil, err
		}
		fc.TookMs = s.since(start)
		return fc, nil
	}, nil
}
