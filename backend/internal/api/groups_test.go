package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/fingerprint"
	"github.com/sinanturdu/logsage/backend/internal/storage/clickhouse"
	"github.com/sinanturdu/logsage/backend/internal/storage/postgres"
)

// Group status fakes on readStore (ADR 0022).

func (s *readStore) GroupStates(_ context.Context, projectID string) ([]postgres.GroupState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []postgres.GroupState
	for k, g := range s.groups {
		if strings.HasPrefix(k, projectID+"/") {
			out = append(out, g)
		}
	}
	return out, nil
}

func (s *readStore) GroupState(_ context.Context, projectID string, fp uint64) (postgres.GroupState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[projectID+"/"+fingerprint.Hex(fp)]
	if !ok {
		return postgres.GroupState{}, postgres.ErrNotFound
	}
	return g, nil
}

func (s *readStore) SetGroupStatus(_ context.Context, projectID string, fp uint64, status, note, userID string, audit postgres.AuditEntry) (postgres.GroupState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.groups == nil {
		s.groups = map[string]postgres.GroupState{}
	}
	g := postgres.GroupState{Fingerprint: fp, Status: status, Note: note, UpdatedBy: userID + "@example.com", UpdatedAt: time.Now()}
	if status == "resolved" {
		at := time.Date(2026, 11, 7, 2, 5, 0, 0, time.UTC)
		g.ResolvedAt = &at
	}
	s.groups[projectID+"/"+fingerprint.Hex(fp)] = g
	s.audits = append(s.audits, audit)
	return g, nil
}

const (
	fpA     = uint64(0x00000000000000aa)
	fpB     = uint64(0x00000000000000bb)
	fpAHex  = "00000000000000aa"
	unknown = "00000000000000cc"
)

// groupCH answers the group statements with two groups (aa, bb) last seen
// at 02:10, honoring the include and exclude lists; group_seen knows aa and bb.
func groupCH(sql string, params map[string]string) []byte {
	switch {
	case strings.Contains(sql, ") AS g"):
		var lines []string
		for _, fp := range []uint64{fpA, fpB} {
			n := strconv.FormatUint(fp, 10)
			if inc, ok := params["include"]; ok && !strings.Contains(inc, n) {
				continue
			}
			if exc, ok := params["exclude"]; ok && strings.Contains(exc, n) {
				continue
			}
			lines = append(lines, `{"fingerprint":"`+n+`","count":"7","title":"payment <id> declined","sample":"payment 1 declined","error_type":"","levels":["error"],"services":["payments"],"first_seen":"2026-11-07 02:00:00.000","last_seen":"2026-11-07 02:10:00.000"}`)
		}
		return []byte(strings.Join(lines, "\n"))
	case strings.Contains(sql, "count() AS n"):
		if params["fingerprint"] == strconv.FormatUint(fpA, 10) || params["fingerprint"] == strconv.FormatUint(fpB, 10) {
			return []byte(`{"first_seen":"2026-11-07 02:00:00.000","last_seen":"2026-11-07 02:10:00.000","n":"1"}`)
		}
		return []byte(`{"first_seen":"1970-01-01 00:00:00.000","last_seen":"1970-01-01 00:00:00.000","n":"0"}`)
	}
	return nil // sparklines, counts, series and samples: empty
}

func (f *readFixture) post(t *testing.T, path, token, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", f.srv.URL)
	if token != "" {
		req.AddCookie(&http.Cookie{Name: cookieName(false), Value: f.tokens[token]})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func groupFixture(t *testing.T) *readFixture {
	f := newReadFixture(t, 4)
	f.ch.set(func(c *chStore) { c.respond = groupCH })
	return f
}

func TestGroupEndpointsRoleMatrix(t *testing.T) {
	f := groupFixture(t)
	for _, path := range []string{"/groups?" + fromTo, "/groups/" + fpAHex + "?" + fromTo} {
		for _, tt := range []struct {
			token, slug string
			want        int
		}{
			{"tok-owner", "shop", 200}, {"tok-admin", "shop", 200}, {"tok-viewer", "shop", 200},
			{"tok-outsider", "shop", 404}, {"tok-owner", "nope", 404}, {"", "shop", 401},
		} {
			calls := f.ch.calls()
			status, body, _ := f.get(t, "/api/projects/"+tt.slug+path, tt.token)
			if status != tt.want {
				t.Errorf("GET %s %s as %s: %d %s, want %d", tt.slug, path, tt.token, status, body, tt.want)
			}
			if tt.want != 200 && f.ch.calls() != calls {
				t.Errorf("GET %s as %s: ClickHouse was queried", path, tt.token)
			}
		}
	}
	for _, tt := range []struct {
		token, slug string
		want        int
	}{
		{"tok-owner", "shop", 200}, {"tok-admin", "shop", 200},
		{"tok-viewer", "shop", 403}, // viewers cannot change a status
		{"tok-outsider", "shop", 404}, {"", "shop", 401},
	} {
		status, body := f.post(t, "/api/projects/"+tt.slug+"/groups/"+fpAHex+"/status", tt.token, `{"status":"ignored"}`)
		if status != tt.want {
			t.Errorf("POST status as %s: %d %s, want %d", tt.token, status, body, tt.want)
		}
	}
}

func TestGroupListAppliesStatus(t *testing.T) {
	f := groupFixture(t)
	if status, body := f.post(t, "/api/projects/shop/groups/"+fpAHex+"/status", "tok-owner", `{"status":"resolved","note":"fixed in 1.4.2"}`); status != 200 || !strings.Contains(body, `"resolved_at":"2026-11-07T02:05:00.000Z"`) {
		t.Fatalf("resolve: %d %s", status, body)
	}
	list := func(q string) GroupsResponse {
		t.Helper()
		status, body, _ := f.get(t, "/api/projects/shop/groups?"+fromTo+q, "tok-viewer")
		if status != 200 {
			t.Fatalf("list%s: %d %s", q, status, body)
		}
		var r GroupsResponse
		if err := json.Unmarshal([]byte(body), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	all := list("")
	if len(all.Groups) != 2 || all.Groups[0].Status != "resolved" || !all.Groups[0].Regressed || all.Groups[1].Status != "open" || all.Groups[1].Regressed {
		t.Fatalf("all groups: %+v", all.Groups)
	}
	if r := list("&status=resolved"); len(r.Groups) != 1 || r.Groups[0].Fingerprint != fpAHex {
		t.Errorf("status=resolved: %+v", r.Groups)
	}
	if r := list("&status=open"); len(r.Groups) != 1 || r.Groups[0].Fingerprint != "00000000000000bb" {
		t.Errorf("status=open: %+v", r.Groups)
	}
	calls := f.ch.calls()
	if r := list("&status=ignored"); len(r.Groups) != 0 || f.ch.calls() != calls {
		t.Errorf("status=ignored with no ignored groups must not query: %+v", r.Groups)
	}
	// Detail carries the stored state.
	status, body, _ := f.get(t, "/api/projects/shop/groups/"+fpAHex+"?"+fromTo, "tok-viewer")
	if status != 200 || !strings.Contains(body, `"note":"fixed in 1.4.2"`) || !strings.Contains(body, `"status":"resolved"`) || !strings.Contains(body, `"regressed":true`) {
		t.Errorf("detail: %d %s", status, body)
	}
}

func TestGroupStatusValidationAndAudit(t *testing.T) {
	f := groupFixture(t)
	st := f.store()
	for body, want := range map[string]string{
		`{"status":"deleted"}`: "status: must be open, ignored or resolved",
		`{"status":"open","note":"` + strings.Repeat("n", 501) + `"}`: "note: at most 500 characters",
		`{"status":"open","extra":1}`:                                 "",
		`not json`:                                                    "",
	} {
		status, resp := f.post(t, "/api/projects/shop/groups/"+fpAHex+"/status", "tok-owner", body)
		if status != 400 || (want != "" && !strings.Contains(resp, want)) {
			t.Errorf("body %.40q: %d %s, want 400 %q", body, status, resp, want)
		}
	}
	for _, fp := range []string{"zz", "0000000000000000", "' OR 1=1 --xxxxx"} {
		if status, resp := f.post(t, "/api/projects/shop/groups/"+fp+"/status", "tok-owner", `{"status":"open"}`); status != 400 && status != 404 {
			t.Errorf("fingerprint %q: %d %s", fp, status, resp)
		}
	}
	if status, _ := f.post(t, "/api/projects/shop/groups/"+unknown+"/status", "tok-owner", `{"status":"open"}`); status != 404 {
		t.Errorf("a group the project never saw: %d, want 404", status)
	}
	if len(st.audits) != 0 {
		t.Fatalf("refused changes wrote audits: %+v", st.audits)
	}
	if status, body := f.post(t, "/api/projects/shop/groups/"+fpAHex+"/status", "tok-admin", `{"status":"ignored","note":"  noisy  "}`); status != 200 || !strings.Contains(body, `"note":"noisy"`) {
		t.Fatalf("admin ignore: %d %s", status, body)
	}
	if len(st.audits) != 1 || st.audits[0].Action != "group.status" || st.audits[0].Target != "shop/"+fpAHex || st.audits[0].Detail != "ignored" || st.audits[0].UserID != "u-admin" {
		t.Errorf("audit = %+v", st.audits)
	}
}

func TestGroupReadValidation(t *testing.T) {
	f := groupFixture(t)
	for path, want := range map[string]string{
		"/groups?" + fromTo + "&sort=random":        "sort:",
		"/groups?" + fromTo + "&limit=101":          "limit:",
		"/groups?" + fromTo + "&status=gone":        "status:",
		"/groups?" + fromTo + "&cursor=abc":         "cursor:",
		"/groups?" + fromTo + "&project_id=x":       "unknown parameter",
		"/groups":                                   "from",
		"/groups/zzzz?" + fromTo:                    "fingerprint: must be 16 hexadecimal characters",
		"/groups/" + fpAHex + "?" + fromTo + "&q=x": "unknown parameter",
		"/logs?" + fromTo + "&fingerprint=12":       "fingerprint: must be 16 hexadecimal characters",
	} {
		status, body, _ := f.get(t, "/api/projects/shop"+path, "tok-owner")
		if status != 400 || !strings.Contains(body, want) {
			t.Errorf("%s: %d %s, want 400 %q", path, status, body, want)
		}
	}
	if status, body, _ := f.get(t, "/api/projects/shop/groups/"+unknown+"?"+fromTo, "tok-owner"); status != 404 || !strings.Contains(body, "not found") {
		t.Errorf("unknown group detail: %d %s", status, body)
	}
	// The fingerprint filter reaches ClickHouse as a number parameter.
	f.ch.set(func(c *chStore) { c.params = nil })
	if status, _, _ := f.get(t, "/api/projects/shop/logs?"+fromTo+"&fingerprint="+fpAHex, "tok-owner"); status != 200 {
		t.Fatal(status)
	}
	if p := f.ch.recorded(); len(p) != 1 || p[0]["fingerprint"] != "170" || p[0]["project_id"] != shopID {
		t.Errorf("params = %v", p)
	}
}

func TestGroupReadsHideStorageErrors(t *testing.T) {
	f := groupFixture(t)
	f.ch.set(func(c *chStore) {
		c.err = &clickhouse.Error{Status: 500, Code: "60", Message: "DB::Exception: Table logsage.secret_table does not exist"}
	})
	for _, path := range []string{"/groups?" + fromTo, "/groups/" + fpAHex + "?" + fromTo} {
		status, body, _ := f.get(t, "/api/projects/shop"+path, "tok-owner")
		if status < 500 || strings.Contains(body, "DB::") || strings.Contains(body, "secret_table") {
			t.Errorf("%s: %d %s", path, status, body)
		}
	}
	status, body := f.post(t, "/api/projects/shop/groups/"+fpAHex+"/status", "tok-owner", `{"status":"open"}`)
	if status < 500 || strings.Contains(body, "DB::") {
		t.Errorf("status endpoint: %d %s", status, body)
	}
}

func TestGroupReadsShareTheQueryCap(t *testing.T) {
	f := newReadFixture(t, 1)
	block, started := make(chan struct{}), make(chan struct{}, 4)
	f.ch.set(func(c *chStore) { c.block, c.started, c.respond = block, started, groupCH })
	done := make(chan int)
	go func() {
		status, _, _ := f.get(t, "/api/projects/shop/groups?"+fromTo, "tok-owner")
		done <- status
	}()
	<-started
	if status, _, h := f.get(t, "/api/projects/shop/groups?"+fromTo, "tok-owner"); status != 429 || h.Get("Retry-After") == "" {
		t.Errorf("second concurrent read: %d, want 429 with Retry-After", status)
	}
	close(block)
	if s := <-done; s != 200 {
		t.Errorf("first read: %d", s)
	}
}

// store returns the fixture's fake PostgreSQL store.
func (f *readFixture) store() *readStore { return f.st }
