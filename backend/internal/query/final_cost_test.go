package query

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/storage/clickhouse"
	"github.com/sinanturdu/logsage/backend/migrations"
)

// TestFinalCost measures what FROM logs FINAL costs for the three dashboard
// queries on 100k rows (1% redelivered in separate parts). It only runs with
// LOGSAGE_FINAL_COST=1 and CLICKHOUSE_TEST_URL set, and reports numbers
// instead of asserting them (ADR 0014).
func TestFinalCost(t *testing.T) {
	if os.Getenv("LOGSAGE_FINAL_COST") != "1" {
		t.Skip("set LOGSAGE_FINAL_COST=1 to measure FINAL")
	}
	chURL := os.Getenv("CLICKHOUSE_TEST_URL")
	if chURL == "" {
		t.Skip("CLICKHOUSE_TEST_URL not set")
	}
	user := os.Getenv("CLICKHOUSE_TEST_USER")
	if user == "" {
		user = "default"
	}
	db := "logsage_final_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	ch, err := clickhouse.New(clickhouse.Options{URL: chURL, Database: db, User: user, Password: os.Getenv("CLICKHOUSE_TEST_PASSWORD"), Timeout: 60 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := ch.Migrate(ctx, migrations.ClickHouse(), clickhouse.MigrationData{Database: db, TTLDays: 3650}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ch.Exec(context.Background(), "DROP DATABASE IF EXISTS "+db) })

	const rows = 100_000
	start := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	services := []string{"checkout", "payments", "search", "billing", "api", "auth"}
	levels := []string{"debug", "info", "info", "info", "info", "info", "warn", "error"}
	line := func(i int) string {
		ts := start.Add(time.Duration(i) * 864 * time.Millisecond) // 100k rows over 24h
		return fmt.Sprintf(`{"project_id":"p1","event_id":"%d-0","timestamp":"%s","level":"%s","service":"%s","message":"request %d done","fingerprint":0,"trace_id":"%032x","attributes":{"k":"v"},"ingested_at":"2026-11-08 00:00:00.000"}`+"\n",
			i, ts.Format(chTime), levels[i%len(levels)], services[i%len(services)], i, i/4)
	}
	// Ten inserts of 10k rows, then 1% redelivered rows in a further insert.
	for part := 0; part < 10; part++ {
		var b strings.Builder
		for i := part * 10_000; i < (part+1)*10_000; i++ {
			b.WriteString(line(i))
		}
		if err := ch.InsertJSONEachRow(ctx, "logs", []byte(b.String())); err != nil {
			t.Fatal(err)
		}
	}
	var dup strings.Builder
	for i := 0; i < rows; i += 100 {
		dup.WriteString(line(i))
	}
	if err := ch.InsertJSONEachRow(ctx, "logs", []byte(dup.String())); err != nil {
		t.Fatal(err)
	}

	scope := ScopeFor("p1")
	f := Filter{From: start, To: start.Add(24 * time.Hour), Limit: 100}
	fErr := f
	fErr.Levels = []string{"error", "warn"}
	fErr.Services = []string{"payments", "checkout"}
	stmts := map[string]Statement{}
	add := func(name string, st Statement, err error) {
		if err != nil {
			t.Fatal(err)
		}
		stmts[name] = st
	}
	st, err := Build(scope, f)
	add("logs page (24h, limit 100)", st, err)
	st, err = Build(scope, fErr)
	add("logs page, level+service list", st, err)
	st, err = BuildHistogram(scope, f, AutoBucket(f.From, f.To))
	add("histogram 24h (15m buckets)", st, err)
	st, err = BuildLevelFacets(scope, f)
	add("facets: levels", st, err)
	st, err = BuildServiceFacets(scope, f, ServiceFacetLimit)
	add("facets: services", st, err)

	median := func(sql string, params map[string]string) time.Duration {
		var ds []time.Duration
		for i := 0; i < 7; i++ {
			t0 := time.Now()
			if _, err := ch.QueryWithParams(ctx, sql, params, map[string]string{"readonly": "1"}); err != nil {
				t.Fatal(err)
			}
			ds = append(ds, time.Since(t0))
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		return ds[len(ds)/2]
	}
	names := make([]string, 0, len(stmts))
	for n := range stmts {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, st := range stmts { // warm caches so the first statement is not penalized
		median(st.SQL, st.Params)
	}
	t.Logf("%d rows + %d redelivered, median of 7 runs over HTTP:", rows, rows/100)
	for _, n := range names {
		st := stmts[n]
		withFinal := median(st.SQL, st.Params)
		without := median(strings.Replace(st.SQL, "FROM logs FINAL", "FROM logs", 1), st.Params)
		t.Logf("  %-32s FINAL %6.1f ms   without %6.1f ms", n, ms(withFinal), ms(without))
	}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
