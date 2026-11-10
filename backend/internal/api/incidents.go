package api

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sinanturdu/logsage/backend/internal/alert"
	"github.com/sinanturdu/logsage/backend/internal/fingerprint"
	"github.com/sinanturdu/logsage/backend/internal/incident"
	"github.com/sinanturdu/logsage/backend/internal/query"
	"github.com/sinanturdu/logsage/backend/internal/storage/postgres"
)

// Incident endpoints (ADR 0026): viewers read, owners and admins write,
// non-members get 404, every change is audited (never with user text).
// Titles, summaries and comments are stored and returned as plain text.

// IncidentStore is what the incident endpoints need from storage.
type IncidentStore interface {
	CreateIncident(ctx context.Context, projectID string, n incident.New, actor string, a postgres.AuditEntry) (incident.Incident, error)
	ListIncidents(ctx context.Context, projectID string, f incident.Filter, after *postgres.IncidentCursor, limit int) ([]incident.Incident, error)
	IncidentSummary(ctx context.Context, projectID string) (incident.Summary, error)
	Incident(ctx context.Context, projectID string, number int) (postgres.IncidentDetail, error)
	IncidentEvents(ctx context.Context, incidentID string, before int64, limit int) ([]incident.Event, error)
	UpdateIncident(ctx context.Context, projectID string, number, version int, p incident.Patch, actor string, a postgres.AuditEntry) (incident.Incident, error)
	ChangeStatus(ctx context.Context, projectID string, number, version int, action, note, actor string, a postgres.AuditEntry) (incident.Incident, error)
	Assign(ctx context.Context, projectID string, number int, userID *string, actor string, a postgres.AuditEntry) (incident.Incident, error)
	AddComment(ctx context.Context, projectID string, number int, body, actor string) (incident.Event, error)
	DeleteComment(ctx context.Context, projectID string, number int, eventID int64, actor string, moderate bool, a postgres.AuditEntry) error
	LinkGroup(ctx context.Context, projectID string, number int, fp uint64, template, actor string, a postgres.AuditEntry) (incident.Incident, error)
	UnlinkGroup(ctx context.Context, projectID string, number int, fp uint64, actor string, a postgres.AuditEntry) (incident.Incident, error)
	ProjectMembers(ctx context.Context, projectID string) ([]postgres.ProjectMember, error)
}

const (
	incidentPage    = 50
	maxIncidentPage = 100
	suggestedGroups = 5
	suggestLead     = 15 * time.Minute // the suggestion window starts this long before the incident opened
)

func (s *Server) routeIncidents(m *http.ServeMux) {
	if s.opts.Incidents == nil {
		return
	}
	b := "/api/projects/{slug}/incidents"
	m.HandleFunc("GET /api/projects/{slug}/members", s.authed(s.handleMembers))
	m.HandleFunc("GET "+b, s.authed(s.handleListIncidents))
	m.HandleFunc("GET "+b+"/summary", s.authed(s.handleIncidentSummary))
	m.HandleFunc("POST "+b, s.authed(s.handleCreateIncident))
	m.HandleFunc("GET "+b+"/{number}", s.authed(s.handleGetIncident))
	m.HandleFunc("GET "+b+"/{number}/events", s.authed(s.handleIncidentEvents))
	m.HandleFunc("PATCH "+b+"/{number}", s.authed(s.handlePatchIncident))
	for _, a := range []string{"acknowledge", "resolve", "reopen"} {
		m.HandleFunc("POST "+b+"/{number}/"+a, s.authed(s.handleIncidentStatus(a)))
	}
	m.HandleFunc("POST "+b+"/{number}/assign", s.authed(s.handleAssign(true)))
	m.HandleFunc("POST "+b+"/{number}/unassign", s.authed(s.handleAssign(false)))
	m.HandleFunc("POST "+b+"/{number}/comments", s.authed(s.handleAddComment))
	m.HandleFunc("DELETE "+b+"/{number}/comments/{id}", s.authed(s.handleDeleteComment))
	m.HandleFunc("POST "+b+"/{number}/groups/{fingerprint}", s.authed(s.handleIncidentGroup(true)))
	m.HandleFunc("DELETE "+b+"/{number}/groups/{fingerprint}", s.authed(s.handleIncidentGroup(false)))
}

// incidentError maps store errors to answers with fixed messages.
func (s *Server) incidentError(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, alert.ErrStale):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "version: the incident changed; reload it and try again"})
	case errors.Is(err, postgres.ErrTransition):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "status: " + strings.TrimSuffix(err.Error(), ": "+postgres.ErrTransition.Error()) + " is not allowed"})
	case errors.Is(err, postgres.ErrLimit):
		writeJSON(w, http.StatusConflict, map[string]string{"error": limitText(err)})
	case errors.Is(err, postgres.ErrNotMember):
		writeInvalid(w, "assignee: not a member of this project")
	case errors.Is(err, postgres.ErrForbidden):
		writeError(w, http.StatusForbidden)
	default:
		s.storeError(w, op, err)
	}
}

func limitText(err error) string {
	switch {
	case strings.HasPrefix(err.Error(), "incidents"):
		return "incidents: at most " + strconv.Itoa(incident.MaxOpenIncidents) + " open incidents per project"
	case strings.HasPrefix(err.Error(), "groups"):
		return "groups: at most " + strconv.Itoa(incident.MaxGroups) + " per incident"
	}
	return "events: this incident has reached " + strconv.Itoa(incident.MaxEvents) + " timeline events"
}

func incidentNumber(r *http.Request) (int, bool) {
	n, err := strconv.Atoi(r.PathValue("number"))
	return n, err == nil && n > 0 && n < 1<<30
}

func (s *Server) incidentMember(w http.ResponseWriter, r *http.Request, sess postgres.Session, act action) (postgres.Membership, int, bool) {
	m, ok := s.member(w, r, sess, act)
	if !ok {
		return m, 0, false
	}
	n, ok := incidentNumber(r)
	if !ok {
		writeError(w, http.StatusNotFound)
		return m, 0, false
	}
	return m, n, true
}

func incidentAudit(sess postgres.Session, m postgres.Membership, action string, number int, detail string) postgres.AuditEntry {
	return postgres.AuditEntry{UserID: sess.UserID, Action: action, Target: m.Slug + "#" + strconv.Itoa(number), Detail: detail}
}

func validText(name, v string, lo, hi int) (string, error) {
	v = strings.TrimSpace(v)
	n := utf8.RuneCountInString(v)
	if !utf8.ValidString(v) || n < lo || n > hi {
		if lo > 0 {
			return "", errors.New(name + ": " + strconv.Itoa(lo) + " to " + strconv.Itoa(hi) + " characters")
		}
		return "", errors.New(name + ": at most " + strconv.Itoa(hi) + " characters")
	}
	return v, nil
}

func validSeverity(v string) (incident.Severity, bool) {
	sev := incident.Severity(strings.ToLower(strings.TrimSpace(v)))
	return sev, slices.Contains(incident.Severities, sev)
}

// --- members ---

func (s *Server) handleMembers(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, ok := s.member(w, r, sess, actView)
	if !ok {
		return
	}
	ms, err := s.opts.Incidents.ProjectMembers(r.Context(), m.ID)
	if err != nil {
		s.storeError(w, "members", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": ms})
}

// --- list and summary ---

func encodeIncidentCursor(in incident.Incident) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(in.LastActivityAt.UnixNano(), 10) + "|" + in.ID))
}

func decodeIncidentCursor(v string) (*postgres.IncidentCursor, bool) {
	if v == "" {
		return nil, true
	}
	if len(v) > 200 {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		return nil, false
	}
	ns, id, ok := strings.Cut(string(raw), "|")
	n, err := strconv.ParseInt(ns, 10, 64)
	if !ok || err != nil || len(id) != 36 {
		return nil, false
	}
	return &postgres.IncidentCursor{At: time.Unix(0, n).UTC(), ID: id}, true
}

func parseIncidentFilter(q url.Values, me string) (incident.Filter, error) {
	var f incident.Filter
	for _, v := range strings.Split(q.Get("status"), ",") {
		if v == "" {
			continue
		}
		st := incident.Status(v)
		if st != incident.StatusOpen && st != incident.StatusAcknowledged && st != incident.StatusResolved {
			return f, errors.New("status: must be open, acknowledged or resolved")
		}
		f.Status = append(f.Status, st)
	}
	for _, v := range strings.Split(q.Get("severity"), ",") {
		if v == "" {
			continue
		}
		sev, ok := validSeverity(v)
		if !ok {
			return f, errors.New("severity: must be critical, high, medium or low")
		}
		f.Severity = append(f.Severity, sev)
	}
	switch a := q.Get("assignee"); {
	case a == "" || a == "none":
		f.Assignee = a
	case a == "me":
		f.Assignee = me
	case len(a) == 36:
		f.Assignee = a
	default:
		return f, errors.New("assignee: must be me, none or a user id")
	}
	if v := q.Get("service"); v != "" {
		if utf8.RuneCountInString(v) > 256 {
			return f, errors.New("service: at most 256 characters")
		}
		f.Service = v
	}
	for _, p := range []struct {
		name string
		dst  **time.Time
	}{{"from", &f.From}, {"to", &f.To}} {
		if v := q.Get(p.name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return f, errors.New(p.name + ": must be an RFC 3339 time")
			}
			*p.dst = &t
		}
	}
	if v := q.Get("q"); v != "" {
		if utf8.RuneCountInString(v) > incident.MaxTitle {
			return f, errors.New("q: at most 200 characters")
		}
		f.Q = v
	}
	return f, nil
}

func (s *Server) handleListIncidents(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, ok := s.member(w, r, sess, actView)
	if !ok {
		return
	}
	q := r.URL.Query()
	f, err := parseIncidentFilter(q, sess.UserID)
	if err != nil {
		writeInvalid(w, err.Error())
		return
	}
	after, ok := decodeIncidentCursor(q.Get("cursor"))
	if !ok {
		writeInvalid(w, "cursor: not a cursor from next_cursor")
		return
	}
	limit := incidentPage
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxIncidentPage {
			writeInvalid(w, "limit: must be between 1 and 100")
			return
		}
		limit = n
	}
	list, err := s.opts.Incidents.ListIncidents(r.Context(), m.ID, f, after, limit)
	if err != nil {
		s.incidentError(w, "list incidents", err)
		return
	}
	var next *string
	if len(list) == limit {
		c := encodeIncidentCursor(list[len(list)-1])
		next = &c
	}
	writeJSON(w, http.StatusOK, map[string]any{"incidents": list, "next_cursor": next})
}

func (s *Server) handleIncidentSummary(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, ok := s.member(w, r, sess, actView)
	if !ok {
		return
	}
	sum, err := s.opts.Incidents.IncidentSummary(r.Context(), m.ID)
	if err != nil {
		s.incidentError(w, "incident summary", err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

// --- create, read, patch ---

type createIncidentInput struct {
	Title    string   `json:"title"`
	Severity string   `json:"severity"`
	Summary  string   `json:"summary"`
	Services []string `json:"services"`
	Assignee string   `json:"assignee"`
}

func (s *Server) handleCreateIncident(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, ok := s.member(w, r, sess, actManageIncidents)
	if !ok {
		return
	}
	var in createIncidentInput
	if !decode(w, r, &in) {
		return
	}
	n, err := in.incident()
	if err != nil {
		writeInvalid(w, err.Error())
		return
	}
	out, err := s.opts.Incidents.CreateIncident(r.Context(), m.ID, n, sess.UserID, postgres.AuditEntry{UserID: sess.UserID, Action: "incident.create", Target: m.Slug, Detail: string(n.Severity)})
	if err != nil {
		s.incidentError(w, "create incident", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"incident": out})
}

func (in createIncidentInput) incident() (incident.New, error) {
	title, err := validText("title", in.Title, 1, incident.MaxTitle)
	if err != nil {
		return incident.New{}, err
	}
	summary, err := validText("summary", in.Summary, 0, incident.MaxSummary)
	if err != nil {
		return incident.New{}, err
	}
	sev := incident.High
	if in.Severity != "" {
		var ok bool
		if sev, ok = validSeverity(in.Severity); !ok {
			return incident.New{}, errors.New("severity: must be critical, high, medium or low")
		}
	}
	if len(in.Services) > incident.MaxServices {
		return incident.New{}, errors.New("services: at most 20")
	}
	for _, sv := range in.Services {
		if sv == "" || utf8.RuneCountInString(sv) > 256 || !utf8.ValidString(sv) {
			return incident.New{}, errors.New("services: each name must be 1 to 256 characters")
		}
	}
	if in.Assignee != "" && len(in.Assignee) != 36 {
		return incident.New{}, errors.New("assignee: not a member of this project")
	}
	return incident.New{Title: title, Severity: sev, Summary: summary, Services: in.Services, Assignee: in.Assignee}, nil
}

// suggestion is a suggested error group: active in the incident's services
// and time. It is never a cause, and it never carries a raw log message.
type suggestion struct {
	Fingerprint string   `json:"fingerprint"`
	Title       string   `json:"title"`
	Services    []string `json:"services"`
	Levels      []string `json:"levels"`
	Count       uint64   `json:"count"`
	IsNew       bool     `json:"is_new"`
	Regressed   bool     `json:"regressed"`
	FirstSeen   string   `json:"first_seen"`
	LastSeen    string   `json:"last_seen"`
}

// suggest returns the top groups of the incident's services and window,
// without the linked ones; nil and a reason when they cannot be computed.
func (s *Server) suggest(ctx context.Context, m postgres.Membership, d postgres.IncidentDetail) ([]suggestion, string) {
	if s.logs == nil {
		return nil, "log storage is not configured"
	}
	in := d.Incident
	to := time.Now().UTC()
	if in.ResolvedAt != nil {
		to = *in.ResolvedAt
	}
	from := in.OpenedAt.Add(-suggestLead)
	if !from.Before(to) {
		from = to.Add(-suggestLead)
	}
	v := url.Values{}
	v.Set("from", from.UTC().Format(time.RFC3339))
	v.Set("to", to.UTC().Add(time.Second).Format(time.RFC3339))
	v.Set("level", "error,fatal")
	v.Set("status", "all")
	v.Set("sort", "count")
	v.Set("limit", strconv.Itoa(suggestedGroups+len(d.Groups)))
	if len(in.Services) > 0 {
		v.Set("service", strings.Join(in.Services, ","))
	}
	gq, err := s.logs.ParseGroups(v.Encode())
	if err != nil {
		return nil, "the incident window cannot be searched"
	}
	list, err := s.logs.Groups(ctx, query.ScopeFor(m.ID), gq)
	if err != nil {
		return nil, "log storage unavailable, retry later"
	}
	states, err := s.store.GroupStates(ctx, m.ID)
	if err != nil {
		return nil, "group states unavailable, retry later"
	}
	byFP := make(map[uint64]postgres.GroupState, len(states))
	for _, st := range states {
		byFP[st.Fingerprint] = st
	}
	linked := map[string]bool{}
	for _, g := range d.Groups {
		linked[g.Fingerprint] = true
	}
	out := []suggestion{}
	for _, g := range list.Groups {
		if linked[g.Fingerprint] || len(out) == suggestedGroups {
			continue
		}
		fp, _ := fingerprint.ParseHex(g.Fingerprint)
		st, ok := byFP[fp]
		applyState(&g, st, ok)
		out = append(out, suggestion{Fingerprint: g.Fingerprint, Title: g.Title, Services: g.Services, Levels: g.Levels, Count: g.Count,
			IsNew: g.IsNew, Regressed: g.Regressed, FirstSeen: g.FirstSeen, LastSeen: g.LastSeen})
	}
	return out, ""
}

func (s *Server) handleGetIncident(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, n, ok := s.incidentMember(w, r, sess, actView)
	if !ok {
		return
	}
	d, err := s.opts.Incidents.Incident(r.Context(), m.ID, n)
	if err != nil {
		s.incidentError(w, "incident", err)
		return
	}
	evs, err := s.opts.Incidents.IncidentEvents(r.Context(), d.Incident.ID, 0, incidentPage)
	if err != nil {
		s.incidentError(w, "incident events", err)
		return
	}
	sugg, reason := s.suggest(r.Context(), m, d)
	body := map[string]any{"incident": d.Incident, "alerts": d.Alerts, "groups": d.Groups, "suggested_groups": sugg,
		"events": evs, "next_before": nextBefore(evs, incidentPage)}
	if reason != "" {
		body["suggested_groups_error"] = reason
	}
	writeJSON(w, http.StatusOK, body)
}

func nextBefore(evs []incident.Event, limit int) *int64 {
	if len(evs) < limit || len(evs) == 0 {
		return nil
	}
	id := evs[len(evs)-1].ID
	return &id
}

func (s *Server) handleIncidentEvents(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, n, ok := s.incidentMember(w, r, sess, actView)
	if !ok {
		return
	}
	before, limit, ok := pageParams(w, r)
	if !ok {
		return
	}
	d, err := s.opts.Incidents.Incident(r.Context(), m.ID, n)
	if err != nil {
		s.incidentError(w, "incident", err)
		return
	}
	evs, err := s.opts.Incidents.IncidentEvents(r.Context(), d.Incident.ID, before, limit)
	if err != nil {
		s.incidentError(w, "incident events", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": evs, "next_before": nextBefore(evs, limit)})
}

type patchIncidentInput struct {
	Title    *string `json:"title"`
	Severity *string `json:"severity"`
	Summary  *string `json:"summary"`
	Version  int     `json:"version"`
}

func (s *Server) handlePatchIncident(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, n, ok := s.incidentMember(w, r, sess, actManageIncidents)
	if !ok {
		return
	}
	var in patchIncidentInput
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 {
		writeInvalid(w, "version: required (from the incident you edit)")
		return
	}
	var p incident.Patch
	var changed []string
	if in.Title != nil {
		t, err := validText("title", *in.Title, 1, incident.MaxTitle)
		if err != nil {
			writeInvalid(w, err.Error())
			return
		}
		p.Title, changed = &t, append(changed, "title")
	}
	if in.Summary != nil {
		sm, err := validText("summary", *in.Summary, 0, incident.MaxSummary)
		if err != nil {
			writeInvalid(w, err.Error())
			return
		}
		p.Summary, changed = &sm, append(changed, "summary")
	}
	if in.Severity != nil {
		sev, ok := validSeverity(*in.Severity)
		if !ok {
			writeInvalid(w, "severity: must be critical, high, medium or low")
			return
		}
		p.Severity, changed = &sev, append(changed, "severity="+string(sev))
	}
	if len(changed) == 0 {
		writeInvalid(w, "nothing to change: send title, severity or summary")
		return
	}
	out, err := s.opts.Incidents.UpdateIncident(r.Context(), m.ID, n, in.Version, p, sess.UserID, incidentAudit(sess, m, "incident.update", n, strings.Join(changed, ",")))
	if err != nil {
		s.incidentError(w, "update incident", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"incident": out})
}

// --- status, assignment, comments, groups ---

type statusInput struct {
	Note    string `json:"note"`
	Version int    `json:"version"`
}

func (s *Server) handleIncidentStatus(action string) func(http.ResponseWriter, *http.Request, postgres.Session) {
	return func(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
		m, n, ok := s.incidentMember(w, r, sess, actManageIncidents)
		if !ok {
			return
		}
		var in statusInput
		if !decode(w, r, &in) {
			return
		}
		if in.Version < 1 {
			writeInvalid(w, "version: required (from the incident you change)")
			return
		}
		note, err := validText("note", in.Note, 0, incident.MaxComment)
		if err != nil {
			writeInvalid(w, err.Error())
			return
		}
		out, err := s.opts.Incidents.ChangeStatus(r.Context(), m.ID, n, in.Version, action, note, sess.UserID, incidentAudit(sess, m, "incident."+action, n, ""))
		if err != nil {
			s.incidentError(w, action+" incident", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"incident": out})
	}
}

type assignInput struct {
	UserID string `json:"user_id"`
}

func (s *Server) handleAssign(set bool) func(http.ResponseWriter, *http.Request, postgres.Session) {
	return func(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
		m, n, ok := s.incidentMember(w, r, sess, actManageIncidents)
		if !ok {
			return
		}
		var in assignInput
		if !decode(w, r, &in) {
			return
		}
		var uid *string
		action := "incident.unassign"
		if set {
			if len(in.UserID) != 36 {
				writeInvalid(w, "user_id: not a member of this project")
				return
			}
			uid, action = &in.UserID, "incident.assign"
		}
		out, err := s.opts.Incidents.Assign(r.Context(), m.ID, n, uid, sess.UserID, incidentAudit(sess, m, action, n, in.UserID))
		if err != nil {
			s.incidentError(w, "assign incident", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"incident": out})
	}
}

type commentInput struct {
	Body string `json:"body"`
}

func (s *Server) handleAddComment(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, n, ok := s.incidentMember(w, r, sess, actManageIncidents)
	if !ok {
		return
	}
	var in commentInput
	if !decode(w, r, &in) {
		return
	}
	body, err := validText("body", in.Body, 1, incident.MaxComment)
	if err != nil {
		writeInvalid(w, err.Error())
		return
	}
	ev, err := s.opts.Incidents.AddComment(r.Context(), m.ID, n, body, sess.UserID)
	if err != nil {
		s.incidentError(w, "add comment", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"event": ev})
}

func (s *Server) handleDeleteComment(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, n, ok := s.incidentMember(w, r, sess, actManageIncidents)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusNotFound)
		return
	}
	// The author deletes their own; owners and admins delete any.
	moderate := m.Role == postgres.RoleOwner || m.Role == postgres.RoleAdmin
	if err := s.opts.Incidents.DeleteComment(r.Context(), m.ID, n, id, sess.UserID, moderate, incidentAudit(sess, m, "incident.comment_delete", n, strconv.FormatInt(id, 10))); err != nil {
		s.incidentError(w, "delete comment", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleIncidentGroup(link bool) func(http.ResponseWriter, *http.Request, postgres.Session) {
	return func(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
		m, n, ok := s.incidentMember(w, r, sess, actManageIncidents)
		if !ok {
			return
		}
		fp, err := fingerprint.ParseHex(r.PathValue("fingerprint"))
		if err != nil {
			writeError(w, http.StatusNotFound)
			return
		}
		hex := r.PathValue("fingerprint")
		var out incident.Incident
		if link {
			// Only a group of this project can be linked.
			if s.logs != nil {
				exists, err := s.logs.GroupExists(r.Context(), query.ScopeFor(m.ID), fp)
				if err != nil {
					writeError(w, http.StatusServiceUnavailable)
					return
				}
				if !exists {
					writeError(w, http.StatusNotFound)
					return
				}
			}
			out, err = s.opts.Incidents.LinkGroup(r.Context(), m.ID, n, fp, "", sess.UserID, incidentAudit(sess, m, "incident.link_group", n, hex))
		} else {
			out, err = s.opts.Incidents.UnlinkGroup(r.Context(), m.ID, n, fp, sess.UserID, incidentAudit(sess, m, "incident.unlink_group", n, hex))
		}
		if err != nil {
			s.incidentError(w, "incident group", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"incident": out})
	}
}
