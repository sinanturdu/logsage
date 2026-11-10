package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/alert"
	"github.com/sinanturdu/logsage/backend/internal/incident"
	"github.com/sinanturdu/logsage/backend/internal/storage/postgres"
)

// fakeIncidents is an in-memory IncidentStore with the store's rules.
type fakeIncidents struct {
	mu      sync.Mutex
	byNum   map[string]map[int]*incident.Incident // project id -> number -> incident
	events  map[string][]incident.Event           // incident id -> events, oldest first
	audits  []postgres.AuditEntry
	limit   bool
	nextEv  int64
	members map[string][]postgres.ProjectMember
}

func newFakeIncidents() *fakeIncidents {
	return &fakeIncidents{byNum: map[string]map[int]*incident.Incident{}, events: map[string][]incident.Event{}, members: map[string][]postgres.ProjectMember{}}
}

func (f *fakeIncidents) get(pid string, n int) (*incident.Incident, error) {
	in, ok := f.byNum[pid][n]
	if !ok {
		return nil, postgres.ErrNotFound
	}
	return in, nil
}

func (f *fakeIncidents) event(in *incident.Incident, t incident.EventType, actor string, body *string) incident.Event {
	f.nextEv++
	ev := incident.Event{ID: f.nextEv, Type: t, Actor: &actor, At: time.Now(), LastAt: time.Now(), Count: 1, Payload: json.RawMessage(`{}`), Body: body}
	f.events[in.ID] = append(f.events[in.ID], ev)
	return ev
}

func (f *fakeIncidents) CreateIncident(_ context.Context, pid string, n incident.New, actor string, a postgres.AuditEntry) (incident.Incident, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.limit {
		return incident.Incident{}, fmt.Errorf("incidents: %w", postgres.ErrLimit)
	}
	if f.byNum[pid] == nil {
		f.byNum[pid] = map[int]*incident.Incident{}
	}
	num := len(f.byNum[pid]) + 1
	in := &incident.Incident{ID: fmt.Sprintf("00000000-0000-0000-0000-%012d", num), Number: num, Title: n.Title, Severity: n.Severity, Status: incident.StatusOpen,
		Source: "manual", Summary: n.Summary, Services: n.Services, Version: 1, OpenedAt: time.Now(), LastActivityAt: time.Now()}
	f.byNum[pid][num] = in
	f.event(in, incident.EvOpened, actor, nil)
	f.audits = append(f.audits, a)
	return *in, nil
}
func (f *fakeIncidents) ListIncidents(_ context.Context, pid string, _ incident.Filter, _ *postgres.IncidentCursor, limit int) ([]incident.Incident, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []incident.Incident{}
	for i := 1; i <= len(f.byNum[pid]) && len(out) < limit; i++ {
		out = append(out, *f.byNum[pid][i])
	}
	return out, nil
}
func (f *fakeIncidents) IncidentSummary(_ context.Context, pid string) (incident.Summary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var s incident.Summary
	for _, in := range f.byNum[pid] {
		switch in.Status {
		case incident.StatusOpen:
			s.Open++
		case incident.StatusAcknowledged:
			s.Acknowledged++
		default:
			s.Resolved++
		}
	}
	return s, nil
}
func (f *fakeIncidents) Incident(_ context.Context, pid string, n int) (postgres.IncidentDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	in, err := f.get(pid, n)
	if err != nil {
		return postgres.IncidentDetail{}, err
	}
	return postgres.IncidentDetail{Incident: *in, Alerts: []incident.LinkedAlert{}, Groups: []incident.LinkedGroup{}}, nil
}
func (f *fakeIncidents) IncidentEvents(_ context.Context, id string, _ int64, _ int) ([]incident.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	evs := f.events[id]
	out := make([]incident.Event, 0, len(evs))
	for i := len(evs) - 1; i >= 0; i-- {
		out = append(out, evs[i])
	}
	return out, nil
}
func (f *fakeIncidents) change(pid string, n, version int, a postgres.AuditEntry, fn func(*incident.Incident) error) (incident.Incident, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	in, err := f.get(pid, n)
	if err != nil {
		return incident.Incident{}, err
	}
	if version > 0 && version != in.Version {
		return incident.Incident{}, alert.ErrStale
	}
	if err := fn(in); err != nil {
		return incident.Incident{}, err
	}
	in.Version++
	f.audits = append(f.audits, a)
	return *in, nil
}
func (f *fakeIncidents) UpdateIncident(_ context.Context, pid string, n, version int, p incident.Patch, actor string, a postgres.AuditEntry) (incident.Incident, error) {
	return f.change(pid, n, version, a, func(in *incident.Incident) error {
		if p.Title != nil {
			in.Title = *p.Title
		}
		if p.Summary != nil {
			in.Summary = *p.Summary
		}
		if p.Severity != nil {
			in.Severity = *p.Severity
		}
		return nil
	})
}
func (f *fakeIncidents) ChangeStatus(_ context.Context, pid string, n, version int, action, note, actor string, a postgres.AuditEntry) (incident.Incident, error) {
	return f.change(pid, n, version, a, func(in *incident.Incident) error {
		to, ok := incident.Transition(in.Status, action)
		if !ok {
			return fmt.Errorf("%s from %s: %w", action, in.Status, postgres.ErrTransition)
		}
		in.Status = to
		if note != "" {
			f.event(in, incident.EvComment, actor, &note)
		}
		return nil
	})
}
func (f *fakeIncidents) Assign(_ context.Context, pid string, n int, uid *string, actor string, a postgres.AuditEntry) (incident.Incident, error) {
	return f.change(pid, n, 0, a, func(in *incident.Incident) error {
		if uid != nil && *uid != "00000000-0000-0000-0000-00000000000a" {
			return postgres.ErrNotMember
		}
		in.Assignee = uid
		return nil
	})
}
func (f *fakeIncidents) AddComment(_ context.Context, pid string, n int, body, actor string) (incident.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	in, err := f.get(pid, n)
	if err != nil {
		return incident.Event{}, err
	}
	return f.event(in, incident.EvComment, actor, &body), nil
}
func (f *fakeIncidents) DeleteComment(_ context.Context, pid string, n int, id int64, actor string, moderate bool, a postgres.AuditEntry) error {
	_, err := f.change(pid, n, 0, a, func(in *incident.Incident) error {
		for i, ev := range f.events[in.ID] {
			if ev.ID == id && ev.Type == incident.EvComment && ev.Body != nil {
				if !moderate && *ev.Actor != actor {
					return postgres.ErrForbidden
				}
				f.events[in.ID][i].Body = nil
				return nil
			}
		}
		return postgres.ErrNotFound
	})
	return err
}
func (f *fakeIncidents) LinkGroup(_ context.Context, pid string, n int, fp uint64, _, actor string, a postgres.AuditEntry) (incident.Incident, error) {
	return f.change(pid, n, 0, a, func(*incident.Incident) error { return nil })
}
func (f *fakeIncidents) UnlinkGroup(_ context.Context, pid string, n int, fp uint64, actor string, a postgres.AuditEntry) (incident.Incident, error) {
	return f.change(pid, n, 0, a, func(*incident.Incident) error { return postgres.ErrNotFound })
}
func (f *fakeIncidents) ProjectMembers(_ context.Context, pid string) ([]postgres.ProjectMember, error) {
	return f.members[pid], nil
}

const ib = "/api/projects/shop/incidents"

func incidentFixture(t *testing.T) (*readFixture, *fakeIncidents) {
	store := newFakeIncidents()
	store.members["11111111-1111-1111-1111-111111111111"] = []postgres.ProjectMember{{UserID: "u-owner", Email: "owner@example.com", Role: postgres.RoleOwner}}
	return newReadFixture(t, 4, func(o *Options) { o.Incidents = store }), store
}

func TestIncidentRoleMatrix(t *testing.T) {
	f, store := incidentFixture(t)
	for _, c := range []struct {
		token string
		want  int
	}{{"tok-viewer", 403}, {"tok-outsider", 404}, {"", 401}, {"tok-admin", 201}, {"tok-owner", 201}} {
		status, body, _ := f.send(t, "POST", ib, c.token, `{"title":"Checkout down","severity":"critical"}`)
		if status != c.want {
			t.Errorf("create as %q: %d %s", c.token, status, body)
		}
	}
	for _, c := range []struct {
		path, token string
		want        int
	}{
		{ib, "tok-viewer", 200}, {ib + "/summary", "tok-viewer", 200}, {ib + "/1", "tok-viewer", 200}, {ib + "/1/events", "tok-viewer", 200},
		{"/api/projects/shop/members", "tok-viewer", 200},
		{ib, "tok-outsider", 404}, {ib + "/1", "tok-outsider", 404}, {"/api/projects/shop/members", "tok-outsider", 404},
		{ib + "/99", "tok-viewer", 404}, {ib + "/x", "tok-viewer", 404}, {ib + "/0", "tok-viewer", 404},
	} {
		if status, body, _ := f.get(t, c.path, c.token); status != c.want {
			t.Errorf("GET %s as %s: %d %s", c.path, c.token, status, body)
		}
	}
	// Every write is closed to viewers.
	for _, w := range []struct{ method, path, body string }{
		{"PATCH", ib + "/1", `{"title":"x","version":1}`},
		{"POST", ib + "/1/acknowledge", `{"version":1}`},
		{"POST", ib + "/1/resolve", `{"version":1}`},
		{"POST", ib + "/1/reopen", `{"version":1}`},
		{"POST", ib + "/1/assign", `{"user_id":"00000000-0000-0000-0000-00000000000a"}`},
		{"POST", ib + "/1/unassign", `{}`},
		{"POST", ib + "/1/comments", `{"body":"x"}`},
		{"DELETE", ib + "/1/comments/1", ``},
		{"POST", ib + "/1/groups/00000000000000ff", `{}`},
		{"DELETE", ib + "/1/groups/00000000000000ff", ``},
	} {
		if status, _, _ := f.send(t, w.method, w.path, "tok-viewer", w.body); status != 403 {
			t.Errorf("%s %s as viewer: %d", w.method, w.path, status)
		}
	}
	if len(store.audits) != 2 {
		t.Fatalf("audits %d", len(store.audits))
	}
}

func TestIncidentValidationAndLimits(t *testing.T) {
	f, store := incidentFixture(t)
	for _, body := range []string{
		`{"title":""}`, `{"title":"   "}`, `{"title":"` + strings.Repeat("x", 201) + `"}`, `{"title":"x","severity":"urgent"}`,
		`{"title":"x","summary":"` + strings.Repeat("x", 2001) + `"}`, `{"title":"x","services":[""]}`,
		`{"title":"x","services":[` + strings.TrimSuffix(strings.Repeat(`"s",`, 21), ",") + `]}`, `{"title":"x","assignee":"me"}`, `{"title":"x","extra":1}`,
	} {
		if status, out, _ := f.send(t, "POST", ib, "tok-owner", body); status != 400 {
			t.Errorf("%.60s: %d %s", body, status, out)
		}
	}
	for _, q := range []string{"?status=closed", "?severity=urgent", "?assignee=bob", "?from=yesterday", "?limit=0", "?limit=101", "?cursor=!!", "?q=" + strings.Repeat("x", 201)} {
		if status, _, _ := f.get(t, ib+q, "tok-viewer"); status != 400 {
			t.Errorf("list %s: %d", q, status)
		}
	}
	store.limit = true
	if status, out, _ := f.send(t, "POST", ib, "tok-owner", `{"title":"one too many"}`); status != 409 || !strings.Contains(out, "at most 1000 open incidents") {
		t.Fatalf("cap: %d %s", status, out)
	}
}

func TestIncidentVersionsTransitionsAndComments(t *testing.T) {
	f, store := incidentFixture(t)
	if status, _, _ := f.send(t, "POST", ib, "tok-owner", `{"title":"x"}`); status != 201 {
		t.Fatal(status)
	}
	if status, out, _ := f.send(t, "PATCH", ib+"/1", "tok-owner", `{"severity":"low"}`); status != 400 || !strings.Contains(out, "version") {
		t.Fatalf("version required: %d %s", status, out)
	}
	if status, out, _ := f.send(t, "PATCH", ib+"/1", "tok-owner", `{"severity":"low","version":7}`); status != 409 || !strings.Contains(out, "the incident changed") {
		t.Fatalf("stale: %d %s", status, out)
	}
	if status, out, _ := f.send(t, "PATCH", ib+"/1", "tok-admin", `{"severity":"low","version":1}`); status != 200 || !strings.Contains(out, `"version":2`) {
		t.Fatalf("patch: %d %s", status, out)
	}
	if status, _, _ := f.send(t, "POST", ib+"/1/acknowledge", "tok-owner", `{"version":2,"note":"on it"}`); status != 200 {
		t.Fatal(status)
	}
	if status, out, _ := f.send(t, "POST", ib+"/1/acknowledge", "tok-owner", `{"version":3}`); status != 409 || !strings.Contains(out, "acknowledge from acknowledged is not allowed") {
		t.Fatalf("twice: %d %s", status, out)
	}
	if status, out, _ := f.send(t, "POST", ib+"/1/assign", "tok-owner", `{"user_id":"00000000-0000-0000-0000-00000000000b"}`); status != 400 || !strings.Contains(out, "not a member") {
		t.Fatalf("non-member: %d %s", status, out)
	}
	// User text is stored and returned as plain text, unchanged.
	xss := `<img src=x onerror="alert(1)"><script>alert(2)</script>`
	b, _ := json.Marshal(map[string]string{"body": xss})
	status, out, _ := f.send(t, "POST", ib+"/1/comments", "tok-owner", string(b))
	var c struct {
		Event incident.Event `json:"event"`
	}
	if err := json.Unmarshal([]byte(out), &c); status != 201 || err != nil || c.Event.Body == nil || *c.Event.Body != xss {
		t.Fatalf("comment: %d %s", status, out)
	}
	if strings.Contains(out, "<script>") {
		t.Fatal("the JSON must escape markup")
	}
	if status, _, _ := f.send(t, "POST", ib+"/1/comments", "tok-owner", `{"body":"  "}`); status != 400 {
		t.Fatal("empty comment")
	}
	if status, _, _ := f.send(t, "POST", ib+"/1/comments", "tok-owner", `{"body":"`+strings.Repeat("x", 2001)+`"}`); status != 400 {
		t.Fatal("long comment")
	}
	if status, _, _ := f.send(t, "DELETE", fmt.Sprintf("%s/1/comments/%d", ib, c.Event.ID), "tok-admin", ``); status != 204 {
		t.Fatal("an admin deletes any comment")
	}
	if status, _, _ := f.send(t, "DELETE", fmt.Sprintf("%s/1/comments/%d", ib, c.Event.ID), "tok-owner", ``); status != 404 {
		t.Fatal("deleted twice")
	}
	if status, _, _ := f.send(t, "DELETE", ib+"/1/comments/abc", "tok-owner", ``); status != 404 {
		t.Fatal("bad id")
	}
	if status, _, _ := f.send(t, "POST", ib+"/1/groups/nothex", "tok-owner", `{}`); status != 404 {
		t.Fatal("bad fingerprint")
	}
	actions := []string{}
	for _, a := range store.audits {
		actions = append(actions, a.Action)
		if strings.Contains(a.Detail, "on it") || strings.Contains(a.Detail, "script") {
			t.Fatalf("user text in the audit: %+v", a)
		}
	}
	if got := strings.Join(actions, ","); got != "incident.create,incident.update,incident.acknowledge,incident.comment_delete" {
		t.Fatalf("audits %s", got)
	}
	// The detail returns the incident, links and timeline; suggestions are a list or null with a reason.
	_, out, _ = f.get(t, ib+"/1", "tok-viewer")
	for _, k := range []string{`"incident":`, `"alerts":[]`, `"groups":[]`, `"suggested_groups":[`, `"events":[`} {
		if !strings.Contains(out, k) {
			t.Errorf("detail lacks %s: %.300s", k, out)
		}
	}
	// The suggestion query is a valid groups query (the real parser runs here).
	if strings.Contains(out, "suggested_groups_error") {
		t.Fatalf("suggestions failed: %.400s", out)
	}
}

func TestMembersEndpoint(t *testing.T) {
	f, _ := incidentFixture(t)
	status, out, _ := f.get(t, "/api/projects/shop/members", "tok-viewer")
	if status != 200 || !strings.Contains(out, `"email":"owner@example.com"`) || !strings.Contains(out, `"role":"owner"`) {
		t.Fatalf("%d %s", status, out)
	}
	if status, _, _ := f.send(t, "POST", "/api/projects/shop/members", "tok-owner", `{}`); status != 404 && status != 405 {
		t.Fatalf("members are managed in the admin CLI only: %d", status)
	}
}
