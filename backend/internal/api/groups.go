package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sinanturdu/logsage/backend/internal/fingerprint"
	"github.com/sinanturdu/logsage/backend/internal/query"
	"github.com/sinanturdu/logsage/backend/internal/storage/postgres"
)

// Error groups (ADR 0022): the lists and details come from ClickHouse
// through the shared query service; the status lives in PostgreSQL.

const apiTime = "2006-01-02T15:04:05.000Z"

// GroupsResponse is the 200 body of GET /api/projects/{slug}/groups.
type GroupsResponse struct {
	Groups             []query.Group `json:"groups"`
	NextCursor         *string       `json:"next_cursor"`
	SparkBucketSeconds float64       `json:"sparkline_bucket_seconds"`
	TookMs             int64         `json:"took_ms"`
}

// GroupDetailResponse is the 200 body of GET /api/projects/{slug}/groups/{fingerprint}.
type GroupDetailResponse struct {
	query.GroupDetail
	TookMs int64 `json:"took_ms"`
}

var errBadFingerprint = errorString("fingerprint: must be 16 hexadecimal characters")

type errorString string

func (e errorString) Error() string { return string(e) }

// applyState fills status, resolved_at and regressed from a stored state.
func applyState(g *query.Group, st postgres.GroupState, ok bool) {
	g.Status, g.Regressed, g.ResolvedAt = query.StatusOpen, false, nil
	if !ok {
		return
	}
	g.Status = st.Status
	if st.ResolvedAt != nil {
		at := st.ResolvedAt.UTC().Format(apiTime)
		g.ResolvedAt = &at
		if last, err := time.Parse(apiTime, g.LastSeen); err == nil && last.After(*st.ResolvedAt) {
			g.Regressed = st.Status == query.StatusResolved // seen again after it was resolved
		}
	}
}

// prepareGroups serves GET /api/projects/{slug}/groups.
func (s *Server) prepareGroups(scope query.Scope, r *http.Request) (readFunc, error) {
	q, err := s.logs.ParseGroups(r.URL.RawQuery)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, start time.Time) (any, error) {
		states, err := s.store.GroupStates(ctx, scope.ProjectID)
		if err != nil {
			return nil, err
		}
		byFP := make(map[uint64]postgres.GroupState, len(states))
		for _, st := range states {
			byFP[st.Fingerprint] = st
		}
		// The status filter becomes a fingerprint list: a stored status, or
		// "open" = every group without an ignored or resolved row.
		switch q.Status {
		case query.StatusIgnored, query.StatusResolved:
			q.Include = []uint64{}
			for _, st := range states {
				if st.Status == q.Status {
					q.Include = append(q.Include, st.Fingerprint)
				}
			}
		case query.StatusOpen:
			for _, st := range states {
				if st.Status != query.StatusOpen {
					q.Exclude = append(q.Exclude, st.Fingerprint)
				}
			}
		}
		list, err := s.logs.Groups(ctx, scope, q)
		if err != nil {
			return nil, err
		}
		for i := range list.Groups {
			fp, _ := fingerprint.ParseHex(list.Groups[i].Fingerprint)
			st, ok := byFP[fp]
			applyState(&list.Groups[i], st, ok)
		}
		return GroupsResponse{Groups: list.Groups, NextCursor: list.NextCursor, SparkBucketSeconds: list.SparkBucketSeconds, TookMs: s.since(start)}, nil
	}, nil
}

// prepareGroupDetail serves GET /api/projects/{slug}/groups/{fingerprint}.
func (s *Server) prepareGroupDetail(scope query.Scope, r *http.Request) (readFunc, error) {
	fp, err := fingerprint.ParseHex(r.PathValue("fingerprint"))
	if err != nil {
		return nil, errBadFingerprint
	}
	f, err := s.logs.ParseGroupWindow(r.URL.RawQuery)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, start time.Time) (any, error) {
		d, err := s.logs.GroupDetail(ctx, scope, f, fp)
		if err != nil {
			return nil, err
		}
		st, err := s.store.GroupState(ctx, scope.ProjectID, fp)
		switch {
		case err == nil:
			applyState(&d.Group, st, true)
			d.Note, d.UpdatedBy = st.Note, st.UpdatedBy
		case !errors.Is(err, postgres.ErrNotFound):
			return nil, err
		}
		return GroupDetailResponse{GroupDetail: d, TookMs: s.since(start)}, nil
	}, nil
}

// groupStateJSON is the status part of a group, returned by the status endpoint.
type groupStateJSON struct {
	Fingerprint string  `json:"fingerprint"`
	Status      string  `json:"status"`
	ResolvedAt  *string `json:"resolved_at"`
	Note        string  `json:"note"`
	UpdatedBy   string  `json:"updated_by"`
	UpdatedAt   string  `json:"updated_at"`
}

// handleGroupStatus serves POST /api/projects/{slug}/groups/{fingerprint}/status
// (owner and admin): stores the status and note and writes an audit row.
// Only groups the project has seen can get a status.
func (s *Server) handleGroupStatus(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	if s.logs == nil {
		writeError(w, http.StatusNotFound)
		return
	}
	m, ok := s.member(w, r, sess, actManageGroups)
	if !ok {
		return
	}
	fp, err := fingerprint.ParseHex(r.PathValue("fingerprint"))
	if err != nil {
		writeInvalid(w, errBadFingerprint.Error())
		return
	}
	var in struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	switch in.Status {
	case query.StatusOpen, query.StatusIgnored, query.StatusResolved:
	default:
		writeInvalid(w, "status: must be open, ignored or resolved")
		return
	}
	in.Note = strings.TrimSpace(in.Note)
	if !utf8.ValidString(in.Note) || utf8.RuneCountInString(in.Note) > postgres.MaxGroupNote {
		writeInvalid(w, "note: at most 500 characters")
		return
	}
	scope := query.ScopeFor(m.ID)
	exists, err := s.logs.GroupExists(r.Context(), scope, fp)
	if err != nil {
		status, msg := query.Classify(err)
		s.log.Warn("group lookup failed", "status", status, "err", err)
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound)
		return
	}
	hexFP := fingerprint.Hex(fp)
	st, err := s.store.SetGroupStatus(r.Context(), m.ID, fp, in.Status, in.Note, sess.UserID,
		postgres.AuditEntry{UserID: sess.UserID, Action: "group.status", Target: m.Slug + "/" + hexFP, Detail: in.Status})
	if err != nil {
		s.storeError(w, "set group status", err)
		return
	}
	out := groupStateJSON{Fingerprint: hexFP, Status: st.Status, Note: st.Note, UpdatedBy: st.UpdatedBy, UpdatedAt: st.UpdatedAt.UTC().Format(apiTime)}
	if st.ResolvedAt != nil {
		at := st.ResolvedAt.UTC().Format(apiTime)
		out.ResolvedAt = &at
	}
	writeJSON(w, http.StatusOK, map[string]any{"group": out})
}
