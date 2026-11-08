package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/fingerprint"
)

// TestIntegrationGroupStatusAndRegression runs the group endpoints against
// PostgreSQL and ClickHouse: the status persists with an audit row, and a
// resolved group seen again after resolved_at is flagged as regressed.
// Timestamps are relative to the real clock, because resolved_at is the
// database's now().
func TestIntegrationGroupStatusAndRegression(t *testing.T) {
	e := newReadEnv(t)
	ctx := context.Background()
	start := time.Now().UTC().Truncate(time.Second)
	insert := func(project, id string, at time.Time, msg string) {
		t.Helper()
		fp, tpl := fingerprint.Compute("error", "payments", msg, nil)
		b, _ := json.Marshal(map[string]any{
			"project_id": project, "event_id": id, "timestamp": at.UTC().Format("2006-01-02 15:04:05.000"), "level": "error",
			"service": "payments", "message": msg, "fingerprint": fp, "template": tpl, "trace_id": "",
			"attributes": map[string]string{}, "ingested_at": at.UTC().Format("2006-01-02 15:04:05.000"),
		})
		if err := e.ch.InsertJSONEachRow(ctx, "logs", append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		insert(e.alphaID, fmt.Sprintf("g-%d", i), start.Add(-time.Duration(30-i)*time.Minute), fmt.Sprintf("payment %08x declined: gateway timeout after %dms", 0xb0000000+i, 5000+i))
		insert(e.bID, fmt.Sprintf("g-%d", i), start.Add(-time.Duration(30-i)*time.Minute), fmt.Sprintf("payment %08x declined: gateway timeout after %dms", 0xb0000000+i, 5000+i))
	}
	fp := fingerprint.Hex(fingerprint.Of("payments", "payment <id> declined: gateway timeout after <n>ms", ""))
	window := "from=" + start.Add(-time.Hour).Format(time.RFC3339) + "&to=" + start.Add(time.Hour).Format(time.RFC3339)

	groupOf := func(token string) map[string]any {
		t.Helper()
		body := e.get(t, token, "/api/projects/alpha/groups?"+window, 200)
		groups := body["groups"].([]any)
		for _, g := range groups {
			if m := g.(map[string]any); m["fingerprint"] == fp {
				return m
			}
		}
		t.Fatalf("group %s not listed: %v", fp, groups)
		return nil
	}
	g := groupOf(e.alice)
	if g["status"] != "open" || g["regressed"] != false || g["is_new"] != true || g["count"].(float64) != 3 {
		t.Fatalf("before: %v", g)
	}

	// Resolve, as the owner. Bob (not a member of alpha) gets 404 for both.
	r := e.h.do("POST", "/api/projects/alpha/groups/"+fp+"/status", `{"status":"resolved","note":"fixed in 1.4.2"}`, e.alice, nil)
	e.h.wantStatus(r, 200, "resolve")
	e.h.wantStatus(e.h.do("POST", "/api/projects/alpha/groups/"+fp+"/status", `{"status":"open"}`, e.bob, nil), 404, "bob")
	e.h.wantStatus(e.h.do("GET", "/api/projects/alpha/groups?"+window, "", e.bob, nil), 404, "bob list")

	g = groupOf(e.alice)
	if g["status"] != "resolved" || g["regressed"] != false || g["resolved_at"] == nil {
		t.Fatalf("after resolve: %v", g)
	}
	var audits int
	if err := e.h.pool.QueryRow(ctx, "SELECT count(*) FROM audit_log WHERE action = 'group.status' AND target = $1 AND detail = 'resolved'", "alpha/"+fp).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audit rows = %d (%v)", audits, err)
	}
	// Bob's project has the same group, still open: state is per project.
	if body := e.get(t, e.bob, "/api/projects/beta/groups?"+window, 200); !strings.Contains(fmt.Sprint(body["groups"]), "status:open") {
		t.Errorf("beta: %v", body["groups"])
	}

	// Seen again after resolved_at: regressed, still resolved.
	time.Sleep(1100 * time.Millisecond)
	insert(e.alphaID, "g-new", time.Now().UTC().Add(time.Second), "payment b00000ff declined: gateway timeout after 9000ms")
	g = groupOf(e.alice)
	if g["status"] != "resolved" || g["regressed"] != true || g["count"].(float64) != 4 {
		t.Fatalf("after a new row: %v", g)
	}
	detail := e.get(t, e.alice, "/api/projects/alpha/groups/"+fp+"?"+window, 200)
	if detail["note"] != "fixed in 1.4.2" || detail["regressed"] != true || detail["updated_by"] != "alice@example.com" || len(detail["samples"].([]any)) != 4 {
		t.Fatalf("detail: %v", detail)
	}
	// Counts in the list equal /logs for the same fingerprint filter.
	logs := e.get(t, e.alice, "/api/projects/alpha/logs?"+window+"&fingerprint="+fp, 200)
	if n := len(logs["events"].([]any)); n != 4 {
		t.Fatalf("/logs with the fingerprint filter: %d events, want 4", n)
	}
	for _, ev := range logs["events"].([]any) {
		if ev.(map[string]any)["fingerprint"] != fp {
			t.Fatalf("event without the group's fingerprint: %v", ev)
		}
	}
	// Reopening clears resolved_at and the flag.
	e.h.wantStatus(e.h.do("POST", "/api/projects/alpha/groups/"+fp+"/status", `{"status":"open"}`, e.alice, nil), 200, "reopen")
	if g = groupOf(e.alice); g["status"] != "open" || g["regressed"] != false || g["resolved_at"] != nil {
		t.Fatalf("reopened: %v", g)
	}
	// Legacy rows (fingerprint 0) of the shared dataset never form a group.
	body := e.get(t, e.alice, "/api/projects/alpha/groups?"+itRange, 200)
	if n := len(body["groups"].([]any)); n != 0 {
		t.Errorf("rows without a fingerprint formed %d groups", n)
	}
}
