package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/fingerprint"
	"github.com/sinanturdu/logsage/backend/internal/storage/clickhouse"
	"github.com/sinanturdu/logsage/backend/migrations"
)

const (
	projA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	projB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

// groupsService loads two projects with identical data (so their groups must
// stay separate), a redelivered duplicate, and legacy rows without a
// fingerprint. Templates and fingerprints come from internal/fingerprint,
// as the processor writes them.
func groupsService(t *testing.T) (*Service, *clickhouse.Client) {
	t.Helper()
	chURL := os.Getenv("CLICKHOUSE_TEST_URL")
	if chURL == "" {
		t.Skip("CLICKHOUSE_TEST_URL not set; skipping integration test")
	}
	user := os.Getenv("CLICKHOUSE_TEST_USER")
	if user == "" {
		user = "default"
	}
	db := "logsage_git_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	ch, err := clickhouse.New(clickhouse.Options{URL: chURL, Database: db, User: user, Password: os.Getenv("CLICKHOUSE_TEST_PASSWORD"), Timeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := ch.Migrate(ctx, migrations.ClickHouse(), clickhouse.MigrationData{Database: db, TTLDays: 30}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = ch.Exec(context.Background(), "DROP DATABASE IF EXISTS "+db) })

	row := func(project, id string, ts time.Time, level, service, message string, legacy bool) string {
		fp, tpl := fingerprint.Compute(level, service, message, nil)
		if legacy {
			fp, tpl = 0, ""
		}
		b, _ := json.Marshal(map[string]any{
			"project_id": project, "event_id": id, "timestamp": ts.UTC().Format(chTime), "level": level, "service": service,
			"message": message, "fingerprint": fp, "template": tpl, "trace_id": "", "attributes": map[string]string{},
			"ingested_at": ts.UTC().Format(chTime),
		})
		return string(b) + "\n"
	}
	base := day.Add(10 * time.Hour)
	var rows strings.Builder
	for _, p := range []string{projA, projB} {
		// Group "timeout": 30 rows over two hours, first seen at 10:00.
		for i := 0; i < 30; i++ {
			rows.WriteString(row(p, fmt.Sprintf("t-%d", i), base.Add(time.Duration(i)*4*time.Minute), []string{"error", "warn"}[i%2], "payments", fmt.Sprintf("payment %08x declined: gateway timeout after %dms", 0xa0000000+i, 5000+i), false))
		}
		// Group "disk": 5 rows from 11:00.
		for i := 0; i < 5; i++ {
			rows.WriteString(row(p, fmt.Sprintf("d-%d", i), base.Add(time.Hour+time.Duration(i)*time.Minute), "error", "exports", fmt.Sprintf("failed to write /var/data/exports/%d.json: disk quota exceeded", 100+i), false))
		}
		// Group "panic": 1 row at 11:30.
		rows.WriteString(row(p, "p-0", base.Add(90*time.Minute), "fatal", "api", "panic: runtime error: index out of range [5] with length 3", false))
		// Not grouped: info, and a legacy error row from before v0.3.
		rows.WriteString(row(p, "i-0", base.Add(time.Minute), "info", "payments", "payment accepted", false))
		rows.WriteString(row(p, "l-0", base.Add(2*time.Minute), "error", "payments", "payment 00000001 declined: gateway timeout after 1ms", true))
	}
	if err := ch.InsertJSONEachRow(ctx, "logs", []byte(rows.String())); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Redelivered duplicates in a separate part: FINAL must collapse them.
	dup := row(projA, "t-0", base, "error", "payments", "payment a0000000 declined: gateway timeout after 5000ms", false) +
		row(projA, "d-0", base.Add(time.Hour), "error", "exports", "failed to write /var/data/exports/100.json: disk quota exceeded", false)
	if err := ch.InsertJSONEachRow(ctx, "logs", []byte(dup)); err != nil {
		t.Fatalf("insert duplicates: %v", err)
	}
	svc, err := NewService(ch, testOpts())
	if err != nil {
		t.Fatal(err)
	}
	return svc, ch
}

func groupWindow(from, to time.Duration) GroupQuery {
	q := GroupQuery{Filter: Filter{From: day.Add(from), To: day.Add(to)}, Sort: SortCount}
	q.Limit = 50
	return q
}

func TestIntegrationGroups(t *testing.T) {
	svc, _ := groupsService(t)
	ctx := context.Background()
	scopeA := ScopeFor(projA)

	list, err := svc.Groups(ctx, scopeA, groupWindow(9*time.Hour, 13*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Groups) != 3 {
		t.Fatalf("groups = %d, want 3 (timeout, disk, panic; info and legacy rows are not grouped): %+v", len(list.Groups), list.Groups)
	}
	timeout, disk, panicG := list.Groups[0], list.Groups[1], list.Groups[2]
	if timeout.Count != 30 || disk.Count != 5 || panicG.Count != 1 {
		t.Fatalf("counts %d/%d/%d, want 30/5/1 (duplicates must not count)", timeout.Count, disk.Count, panicG.Count)
	}
	if timeout.Title != "payment <id> declined: gateway timeout after <n>ms" || strings.Join(timeout.Levels, ",") != "error,warn" || strings.Join(timeout.Services, ",") != "payments" {
		t.Errorf("timeout group = %+v", timeout)
	}
	if !timeout.IsNew || timeout.FirstSeen != "2026-11-06T10:00:00.000Z" || timeout.LastSeen != "2026-11-06T11:56:00.000Z" {
		t.Errorf("first/last seen: %+v", timeout)
	}
	for _, g := range list.Groups {
		var sum uint64
		for _, v := range g.Sparkline {
			sum += v
		}
		if len(g.Sparkline) != SparkBuckets || sum != g.Count {
			t.Errorf("%s: sparkline %v sums to %d, count %d", g.Title, g.Sparkline, sum, g.Count)
		}
	}

	// Group counts equal /logs totals for the same fingerprint filter.
	for _, g := range list.Groups {
		fp, _ := fingerprint.ParseHex(g.Fingerprint)
		f := Filter{From: day.Add(9 * time.Hour), To: day.Add(13 * time.Hour), Fingerprint: fp, Limit: 1000}
		events, _, err := svc.Search(ctx, scopeA, f)
		if err != nil {
			t.Fatal(err)
		}
		facets, err := svc.Facets(ctx, scopeA, f)
		if err != nil {
			t.Fatal(err)
		}
		if uint64(len(events)) != g.Count || facets.Total != g.Count {
			t.Errorf("%s: group count %d, /logs %d, facets %d", g.Title, g.Count, len(events), facets.Total)
		}
		for _, e := range events {
			if e.Fingerprint != g.Fingerprint {
				t.Fatalf("event %s fingerprint %q, want %q", e.EventID, e.Fingerprint, g.Fingerprint)
			}
		}
	}

	// Two projects with identical data: same fingerprints, separate counts.
	listB, err := svc.Groups(ctx, ScopeFor(projB), groupWindow(9*time.Hour, 13*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(listB.Groups) != 3 || listB.Groups[0].Count != 30 || listB.Groups[0].Fingerprint != timeout.Fingerprint {
		t.Errorf("project B groups: %+v", listB.Groups)
	}

	// is_new: false when the window starts after first seen; first/last seen stay all-time.
	late, err := svc.Groups(ctx, scopeA, groupWindow(11*time.Hour, 13*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range late.Groups {
		if g.Fingerprint == timeout.Fingerprint && (g.IsNew || g.FirstSeen != timeout.FirstSeen || g.Count != 15) {
			t.Errorf("late window, timeout: %+v", g)
		}
		if g.Fingerprint == disk.Fingerprint && !g.IsNew {
			t.Errorf("late window, disk is first seen at 11:00: %+v", g)
		}
	}

	// q matches the template; level and service filter.
	for query, want := range map[string]int{"QUOTA": 1, "<id> declined": 1, "nothing": 0} {
		q := groupWindow(9*time.Hour, 13*time.Hour)
		q.Text = query
		l, err := svc.Groups(ctx, scopeA, q)
		if err != nil || len(l.Groups) != want {
			t.Errorf("q=%q: %d groups (%v), want %d", query, len(l.Groups), err, want)
		}
	}
	q := groupWindow(9*time.Hour, 13*time.Hour)
	q.Levels = []string{"warn"}
	if l, _ := svc.Groups(ctx, scopeA, q); len(l.Groups) != 1 || l.Groups[0].Count != 15 {
		t.Errorf("level=warn: %+v", l.Groups)
	}

	// Include / Exclude (status filtering).
	q = groupWindow(9*time.Hour, 13*time.Hour)
	q.Include = []uint64{}
	if l, _ := svc.Groups(ctx, scopeA, q); len(l.Groups) != 0 {
		t.Error("empty include must return nothing")
	}
	fpDisk, _ := fingerprint.ParseHex(disk.Fingerprint)
	q.Include, q.Exclude = nil, []uint64{fpDisk}
	if l, _ := svc.Groups(ctx, scopeA, q); len(l.Groups) != 2 {
		t.Errorf("exclude: %d groups", len(l.Groups))
	}

	// Sorts.
	for sortBy, first := range map[string]string{SortLastSeen: timeout.Fingerprint, SortFirstSeen: panicG.Fingerprint} {
		q := groupWindow(9*time.Hour, 13*time.Hour)
		q.Sort = sortBy
		l, _ := svc.Groups(ctx, scopeA, q)
		if len(l.Groups) == 0 || l.Groups[0].Fingerprint != first {
			t.Errorf("sort %s: first %+v", sortBy, l.Groups)
		}
	}
}

func TestIntegrationGroupPagination(t *testing.T) {
	svc, _ := groupsService(t)
	ctx := context.Background()
	raw := "from=2026-11-06T09:00:00Z&to=2026-11-06T13:00:00Z&limit=1"
	var seen []string
	cursor := ""
	for page := 0; page < 5; page++ {
		q, err := svc.ParseGroups(raw + cursor)
		if err != nil {
			t.Fatal(err)
		}
		l, err := svc.Groups(ctx, ScopeFor(projA), q)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range l.Groups {
			seen = append(seen, g.Title)
		}
		if l.NextCursor == nil {
			break
		}
		cursor = "&cursor=" + *l.NextCursor
	}
	if len(seen) != 3 || seen[0] == seen[1] || seen[1] == seen[2] {
		t.Fatalf("pages = %v, want 3 distinct groups", seen)
	}
}

func TestIntegrationGroupDetail(t *testing.T) {
	svc, _ := groupsService(t)
	ctx := context.Background()
	scopeA := ScopeFor(projA)
	fp := fingerprint.Of("payments", "payment <id> declined: gateway timeout after <n>ms", "")
	w := Filter{From: day.Add(9 * time.Hour), To: day.Add(13 * time.Hour)}
	d, err := svc.GroupDetail(ctx, scopeA, w, fp)
	if err != nil {
		t.Fatal(err)
	}
	var byLevel uint64
	for _, c := range d.Counts {
		byLevel += c.Count
		if c.Service != "payments" {
			t.Errorf("counts: %+v", c)
		}
	}
	var series uint64
	for _, b := range d.Series.Buckets {
		series += b.Debug + b.Info + b.Warn + b.Error + b.Fatal
	}
	if d.Count != 30 || byLevel != 30 || series != 30 || len(d.Samples) != GroupSamples {
		t.Fatalf("detail count %d, by service/level %d, series %d, samples %d", d.Count, byLevel, series, len(d.Samples))
	}
	if d.Samples[0].Timestamp != "2026-11-06T11:56:00.000Z" {
		t.Errorf("samples must be newest first: %+v", d.Samples[0])
	}
	// Seen, but not in this window: count 0, all-time seen, title of the last row.
	d, err = svc.GroupDetail(ctx, scopeA, Filter{From: day.Add(20 * time.Hour), To: day.Add(21 * time.Hour)}, fp)
	if err != nil || d.Count != 0 || d.LastSeen != "2026-11-06T11:56:00.000Z" || d.Title != "payment <id> declined: gateway timeout after <n>ms" || len(d.Samples) != 0 {
		t.Fatalf("outside the window: %v %+v", err, d.Group)
	}
	// Never seen, or another project's group: not found.
	if _, err := svc.GroupDetail(ctx, scopeA, w, 12345); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("unknown fingerprint: %v", err)
	}
	if _, err := svc.GroupDetail(ctx, ScopeFor("cccccccc-cccc-4ccc-8ccc-cccccccccccc"), w, fp); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("another project: %v", err)
	}
}
