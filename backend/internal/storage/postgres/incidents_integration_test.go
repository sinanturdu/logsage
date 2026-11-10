package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/sinanturdu/logsage/backend/internal/alert"
	"github.com/sinanturdu/logsage/backend/internal/incident"
)

func manual(title string) incident.New {
	return incident.New{Title: title, Severity: incident.High, Services: []string{"payments"}}
}

func TestParallelCreatesGetDistinctNumbers(t *testing.T) {
	f, count := newAlertFixture(t)
	ctx := context.Background()
	const n = 12
	var wg sync.WaitGroup
	nums := make([]int, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			in, err := f.st.CreateIncident(ctx, f.project.ID, manual(fmt.Sprintf("i%d", i)), f.user, AuditEntry{UserID: f.user, Action: "incident.create", Target: "shop"})
			nums[i], errs[i] = in.Number, err
		}(i)
	}
	close(start)
	wg.Wait()
	seen := map[int]bool{}
	for i := range nums {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if seen[nums[i]] {
			t.Fatalf("number %d twice: %v", nums[i], nums)
		}
		seen[nums[i]] = true
	}
	for i := 1; i <= n; i++ {
		if !seen[i] {
			t.Fatalf("gap at %d: %v", i, nums)
		}
	}
	if c := count("SELECT count(*) FROM audit_log WHERE action = 'incident.create'"); c != n {
		t.Fatalf("audit rows %d", c)
	}
	// Numbers are per project.
	other, err := f.st.CreateProjectWithOwner(ctx, "Other", "other", f.user)
	if err != nil {
		t.Fatal(err)
	}
	in, err := f.st.CreateIncident(ctx, other.ID, manual("first"), f.user, AuditEntry{UserID: f.user, Action: "incident.create", Target: "other"})
	if err != nil || in.Number != 1 {
		t.Fatalf("other project: %+v %v", in, err)
	}
}

func TestIncidentWorkflowAuditAndVersions(t *testing.T) {
	f, count := newAlertFixture(t)
	ctx := context.Background()
	audit := func(action string) AuditEntry { return AuditEntry{UserID: f.user, Action: action, Target: "shop"} }
	in, err := f.st.CreateIncident(ctx, f.project.ID, manual("Checkout errors"), f.user, audit("incident.create"))
	if err != nil {
		t.Fatal(err)
	}
	// A stale version is refused.
	sev := incident.Critical
	if _, err := f.st.UpdateIncident(ctx, f.project.ID, in.Number, in.Version+1, incident.Patch{Severity: &sev}, f.user, audit("incident.update")); !errors.Is(err, alert.ErrStale) {
		t.Fatalf("stale: %v", err)
	}
	up, err := f.st.UpdateIncident(ctx, f.project.ID, in.Number, in.Version, incident.Patch{Severity: &sev}, f.user, audit("incident.update"))
	if err != nil || up.Severity != incident.Critical || up.Version != in.Version+1 {
		t.Fatalf("update: %+v %v", up, err)
	}
	ack, err := f.st.ChangeStatus(ctx, f.project.ID, in.Number, up.Version, "acknowledge", "looking", f.user, audit("incident.acknowledge"))
	if err != nil || ack.Status != incident.StatusAcknowledged || ack.AcknowledgedBy == nil || *ack.AcknowledgedBy != f.user {
		t.Fatalf("ack: %+v %v", ack, err)
	}
	if _, err := f.st.ChangeStatus(ctx, f.project.ID, in.Number, ack.Version, "acknowledge", "", f.user, audit("incident.acknowledge")); !errors.Is(err, ErrTransition) {
		t.Fatalf("ack twice: %v", err)
	}
	res, err := f.st.ChangeStatus(ctx, f.project.ID, in.Number, ack.Version, "resolve", "", f.user, audit("incident.resolve"))
	if err != nil || res.Status != incident.StatusResolved || res.ResolvedAt == nil {
		t.Fatalf("resolve: %+v %v", res, err)
	}
	re, err := f.st.ChangeStatus(ctx, f.project.ID, in.Number, res.Version, "reopen", "", f.user, audit("incident.reopen"))
	if err != nil || re.Status != incident.StatusOpen || re.ResolvedAt != nil {
		t.Fatalf("reopen: %+v %v", re, err)
	}
	evs, err := f.st.IncidentEvents(ctx, in.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for i := len(evs) - 1; i >= 0; i-- {
		types = append(types, string(evs[i].Type))
	}
	if got := strings.Join(types, ","); got != "opened,severity_changed,status_changed,comment,status_changed,reopened" {
		t.Fatalf("timeline %s", got)
	}
	for _, a := range []string{"incident.update", "incident.acknowledge", "incident.resolve", "incident.reopen"} {
		if count(fmt.Sprintf("SELECT count(*) FROM audit_log WHERE action = '%s'", a)) != 1 {
			t.Errorf("audit %s", a)
		}
	}
	if count("SELECT count(*) FROM audit_log WHERE detail LIKE '%looking%' OR target LIKE '%looking%'") != 0 {
		t.Fatal("a note must never reach the audit log")
	}
}

func TestCommentsAssignmentAndGroups(t *testing.T) {
	f, count := newAlertFixture(t)
	ctx := context.Background()
	in, err := f.st.CreateIncident(ctx, f.project.ID, manual("x"), f.user, AuditEntry{UserID: f.user, Action: "incident.create", Target: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	xss := `<img src=x onerror=alert(1)>`
	c, err := f.st.AddComment(ctx, f.project.ID, in.Number, xss, f.user)
	if err != nil || c.Body == nil || *c.Body != xss || c.Type != incident.EvComment {
		t.Fatalf("comment %+v %v", c, err)
	}
	other, err := f.st.CreateUser(ctx, "viewer@example.com", fakePHC)
	if err != nil {
		t.Fatal(err)
	}
	// Only the author or a manager deletes; the text goes, a marker stays.
	if err := f.st.DeleteComment(ctx, f.project.ID, in.Number, c.ID, other.ID, false, AuditEntry{UserID: other.ID, Action: "incident.comment_delete", Target: "shop"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("not the author: %v", err)
	}
	if err := f.st.DeleteComment(ctx, f.project.ID, in.Number, c.ID, f.user, false, AuditEntry{UserID: f.user, Action: "incident.comment_delete", Target: "shop"}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.DeleteComment(ctx, f.project.ID, in.Number, c.ID, f.user, true, AuditEntry{UserID: f.user, Action: "incident.comment_delete", Target: "shop"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("twice: %v", err)
	}
	if count("SELECT count(*) FROM incident_events WHERE body IS NOT NULL") != 0 || count("SELECT count(*) FROM incident_events WHERE type = 'comment_deleted'") != 1 {
		t.Fatal("comment text must be gone with a marker left")
	}
	// The assignee must be a member of the project.
	if _, err := f.st.Assign(ctx, f.project.ID, in.Number, &other.ID, f.user, AuditEntry{UserID: f.user, Action: "incident.assign", Target: "shop"}); !errors.Is(err, ErrNotMember) {
		t.Fatalf("non-member: %v", err)
	}
	got, err := f.st.Assign(ctx, f.project.ID, in.Number, &f.user, f.user, AuditEntry{UserID: f.user, Action: "incident.assign", Target: "shop"})
	if err != nil || got.Assignee == nil || got.AssigneeEmail != "owner@example.com" {
		t.Fatalf("assign: %+v %v", got, err)
	}
	if got, err = f.st.Assign(ctx, f.project.ID, in.Number, nil, f.user, AuditEntry{UserID: f.user, Action: "incident.unassign", Target: "shop"}); err != nil || got.Assignee != nil {
		t.Fatalf("unassign: %+v %v", got, err)
	}
	// Groups: linked once, unlinked, capped.
	if _, err := f.st.LinkGroup(ctx, f.project.ID, in.Number, 0xff, "db timeout <n>", f.user, AuditEntry{UserID: f.user, Action: "incident.link_group", Target: "shop"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.LinkGroup(ctx, f.project.ID, in.Number, 0xff, "", f.user, AuditEntry{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("twice: %v", err)
	}
	d, err := f.st.Incident(ctx, f.project.ID, in.Number)
	if err != nil || len(d.Groups) != 1 || d.Groups[0].Fingerprint != "00000000000000ff" || d.Groups[0].Source != "manual" {
		t.Fatalf("detail %+v %v", d.Groups, err)
	}
	if _, err := f.st.UnlinkGroup(ctx, f.project.ID, in.Number, 0xff, f.user, AuditEntry{UserID: f.user, Action: "incident.unlink_group", Target: "shop"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < incident.MaxGroups; i++ {
		if _, err := f.st.LinkGroup(ctx, f.project.ID, in.Number, uint64(1000+i), "", f.user, AuditEntry{}); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := f.st.LinkGroup(ctx, f.project.ID, in.Number, 9999, "", f.user, AuditEntry{}); !errors.Is(err, ErrLimit) {
		t.Fatalf("group cap: %v", err)
	}
	for _, a := range []string{"incident.comment_delete", "incident.assign", "incident.unassign", "incident.link_group", "incident.unlink_group"} {
		if count(fmt.Sprintf("SELECT count(*) FROM audit_log WHERE action = '%s'", a)) < 1 {
			t.Errorf("audit %s", a)
		}
	}
	ms, err := f.st.ProjectMembers(ctx, f.project.ID)
	if err != nil || len(ms) != 1 || ms[0].Email != "owner@example.com" || ms[0].Role != RoleOwner {
		t.Fatalf("members %+v %v", ms, err)
	}
}

func TestIncidentCapsListAndSummary(t *testing.T) {
	f, count := newAlertFixture(t)
	ctx := context.Background()
	pool := f.st.pool
	// 1000 open incidents straight in SQL, then a manual create is refused.
	if _, err := pool.Exec(ctx, `INSERT INTO incidents (project_id, number, title, severity, status, source, opened_at, last_activity_at)
		SELECT $1, g, 'bulk', 'low', 'open', 'manual', now(), now() - make_interval(secs => g) FROM generate_series(1, 1000) g`, f.project.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO incident_counters (project_id, last) VALUES ($1, 1000)", f.project.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.CreateIncident(ctx, f.project.ID, manual("one more"), f.user, AuditEntry{}); !errors.Is(err, ErrLimit) {
		t.Fatalf("cap: %v", err)
	}
	if count("SELECT count(*) FROM incidents") != 1000 {
		t.Fatal("nothing must be written at the cap")
	}
	page, err := f.st.ListIncidents(ctx, f.project.ID, incident.Filter{}, nil, 3)
	if err != nil || len(page) != 3 || page[0].Number != 1 {
		t.Fatalf("page %v %v", len(page), err)
	}
	next, err := f.st.ListIncidents(ctx, f.project.ID, incident.Filter{}, &IncidentCursor{At: page[2].LastActivityAt, ID: page[2].ID}, 3)
	if err != nil || len(next) != 3 || next[0].Number != 4 {
		t.Fatalf("next %+v %v", next, err)
	}
	if got, _ := f.st.ListIncidents(ctx, f.project.ID, incident.Filter{Q: "BULK", Severity: []incident.Severity{incident.High}}, nil, 10); len(got) != 0 {
		t.Fatal("severity filter")
	}
	if got, _ := f.st.ListIncidents(ctx, f.project.ID, incident.Filter{Q: "BULK", Status: []incident.Status{incident.StatusOpen}, Assignee: "none"}, nil, 10); len(got) != 10 {
		t.Fatal("q, status and assignee filters")
	}
	sum, err := f.st.IncidentSummary(ctx, f.project.ID)
	if err != nil || sum.Open != 1000 {
		t.Fatalf("%+v %v", sum, err)
	}
	// The event cap: past MaxEvents only the counter grows.
	in := page[0]
	if _, err := pool.Exec(ctx, "UPDATE incidents SET event_count = $2 WHERE id::text = $1", in.ID, incident.MaxEvents); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.AddComment(ctx, f.project.ID, in.Number, "past the cap", f.user); !errors.Is(err, ErrLimit) {
		t.Fatalf("event cap: %v", err)
	}
	if d, _ := f.st.Incident(ctx, f.project.ID, in.Number); d.Incident.EventsDropped != 0 {
		t.Fatal("a refused comment is rolled back, not counted")
	}
}
