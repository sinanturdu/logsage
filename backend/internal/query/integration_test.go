package query

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/storage/clickhouse"
	"github.com/sinanturdu/logsage/backend/migrations"
)

// integrationHandler creates a fresh database with the logs table, inserts
// the fixture rows and returns a Handler on the real ClickHouse client.
// Skipped unless CLICKHOUSE_TEST_URL is set.
func integrationHandler(t *testing.T) (*Handler, *clickhouse.Client) {
	t.Helper()
	chURL := os.Getenv("CLICKHOUSE_TEST_URL")
	if chURL == "" {
		t.Skip("CLICKHOUSE_TEST_URL not set; skipping integration test")
	}
	user := os.Getenv("CLICKHOUSE_TEST_USER")
	if user == "" {
		user = "default"
	}
	db := "logsage_qit_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	ch, err := clickhouse.New(clickhouse.Options{URL: chURL, Database: db, User: user, Password: os.Getenv("CLICKHOUSE_TEST_PASSWORD"), Timeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := ch.Migrate(ctx, migrations.ClickHouse(), clickhouse.MigrationData{Database: db, TTLDays: 30}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = ch.Exec(context.Background(), "DROP DATABASE IF EXISTS "+db) })

	row := func(project, id, ts, level, service, message, trace string) string {
		b, _ := json.Marshal(map[string]any{
			"project_id": project, "event_id": id, "timestamp": ts, "level": level, "service": service,
			"message": message, "fingerprint": 0, "trace_id": trace, "attributes": map[string]string{"env": "it"},
			"ingested_at": "2026-11-06 12:00:00.000",
		})
		return string(b) + "\n"
	}
	first := row("default", "1000-0", "2026-11-06 10:00:00.000", "error", "checkout", "Payment gateway TIMEOUT", "t1") +
		row("default", "1000-1", "2026-11-06 10:00:00.000", "info", "checkout", "order created", "t1") +
		row("default", "1000-2", "2026-11-06 10:00:00.000", "warn", "search", "slow query", "t2") +
		row("default", "1001-0", "2026-11-06 10:00:01.000", "debug", "search", "cache miss", "") +
		row("default", "1002-0", "2026-11-06 10:00:02.000", "fatal", "billing", `disk full at C:\data\tmp`, "") +
		row("default", "1004-0", "2026-11-06 10:00:04.000", "info", "api", "Ünïcödé Straße", "") +
		// Another project, including the same event_id and matching words: must never be returned.
		row("other", "1000-0", "2026-11-06 10:00:00.000", "error", "checkout", "payment gateway timeout LEAK", "t1") +
		row("other", "2000-0", "2026-11-06 10:00:05.000", "info", "api", "other project", "")
	for i := 0; i < 5; i++ { // five rows with an identical timestamp
		first += row("default", "1003-"+strconv.Itoa(i), "2026-11-06 10:00:03.000", "info", "api", "tick "+strconv.Itoa(i), "")
	}
	if err := ch.InsertJSONEachRow(ctx, "logs", []byte(first)); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// A redelivered row in a separate insert (separate part): FINAL must collapse it.
	dup := row("default", "1001-0", "2026-11-06 10:00:01.000", "debug", "search", "cache miss", "")
	if err := ch.InsertJSONEachRow(ctx, "logs", []byte(dup)); err != nil {
		t.Fatalf("insert duplicate: %v", err)
	}
	out, err := ch.Query(ctx, "SELECT count() FROM logs FORMAT TabSeparated")
	if err != nil || strings.TrimSpace(string(out)) != "14" {
		t.Fatalf("raw row count = %q (err %v), want 14: 11 default + 2 other + 1 duplicate", out, err)
	}

	h, err := NewHandler(ch, testKey, testOpts(), newTestSlog(t))
	if err != nil {
		t.Fatal(err)
	}
	return h, ch
}

func searchIDs(t *testing.T, h *Handler, query string) ([]string, *string) {
	t.Helper()
	rec := do(h, http.MethodGet, query, testKey)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET ?%s: %d %s", query, rec.Code, rec.Body.String())
	}
	var resp Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(resp.Events))
	for i, e := range resp.Events {
		ids[i] = e.EventID
	}
	return ids, resp.NextCursor
}

const itRange = "from=2026-11-06T09:00:00Z&to=2026-11-06T11:00:00Z"

func TestIntegrationSearch(t *testing.T) {
	h, ch := integrationHandler(t)
	all := "1004-0,1003-4,1003-3,1003-2,1003-1,1003-0,1002-0,1001-0,1000-2,1000-1,1000-0"

	tests := []struct {
		name  string
		query string
		want  string // comma separated event IDs in order
	}{
		{name: "all, newest first, deduplicated, own project only", query: itRange, want: all},
		{name: "level filter", query: itRange + "&level=error,fatal", want: "1002-0,1000-0"},
		{name: "service filter", query: itRange + "&service=checkout", want: "1000-1,1000-0"},
		{name: "text search is case-insensitive", query: itRange + "&q=timeout", want: "1000-0"},
		{name: "text search unicode case-insensitive", query: itRange + "&q=" + url.QueryEscape("ÜNÏCÖDÉ"), want: "1004-0"},
		{name: "text search with backslash stays literal", query: itRange + "&q=" + url.QueryEscape(`C:\data`), want: "1002-0"},
		{name: "text search with tab-like sequence", query: itRange + "&q=" + url.QueryEscape(`\t`), want: "1002-0"},
		{name: "trace id", query: itRange + "&trace_id=t1", want: "1000-1,1000-0"},
		{name: "combined filters", query: itRange + "&service=search&level=warn,debug&q=QUERY", want: "1000-2"},
		{name: "from inclusive, to exclusive", query: "from=2026-11-06T10:00:01Z&to=2026-11-06T10:00:03Z", want: "1002-0,1001-0"},
		{name: "no match", query: itRange + "&service=nope", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids, next := searchIDs(t, h, tt.query)
			if got := strings.Join(ids, ","); got != tt.want {
				t.Errorf("ids = %s, want %s", got, tt.want)
			}
			if next != nil {
				t.Errorf("next_cursor = %v, want null", *next)
			}
		})
	}

	t.Run("injection attempts return normally and change nothing", func(t *testing.T) {
		for _, v := range injection[:len(injection)-1] {
			for _, p := range []string{"q", "service", "trace_id"} {
				ids, _ := searchIDs(t, h, itRange+"&"+p+"="+url.QueryEscape(v))
				if len(ids) != 0 {
					t.Errorf("%s=%q returned %v, want no rows", p, v, ids)
				}
			}
		}
		out, err := ch.Query(context.Background(), "SELECT count() FROM logs FORMAT TabSeparated")
		if err != nil || strings.TrimSpace(string(out)) != "14" {
			t.Errorf("row count after injection attempts = %q (err %v), want 14", out, err)
		}
	})
}

func TestIntegrationPagination(t *testing.T) {
	h, _ := integrationHandler(t)
	for _, limit := range []int{1, 2, 3, 4} {
		t.Run("limit "+strconv.Itoa(limit), func(t *testing.T) {
			var got []string
			cursor := ""
			for page := 0; page < 20; page++ {
				q := itRange + "&limit=" + strconv.Itoa(limit)
				if cursor != "" {
					q += "&cursor=" + cursor
				}
				ids, next := searchIDs(t, h, q)
				if next != nil && len(ids) != limit {
					t.Fatalf("page %d has %d rows and a next cursor", page, len(ids))
				}
				got = append(got, ids...)
				if next == nil {
					break
				}
				cursor = *next
			}
			want := "1004-0,1003-4,1003-3,1003-2,1003-1,1003-0,1002-0,1001-0,1000-2,1000-1,1000-0"
			if strings.Join(got, ",") != want {
				t.Errorf("pages = %v, want every row exactly once: %s", got, want)
			}
		})
	}
}

func TestIntegrationReadOnly(t *testing.T) {
	_, ch := integrationHandler(t)
	_, err := ch.QueryWithParams(context.Background(), "CREATE TABLE ro_probe (x UInt8) ENGINE = Memory", nil,
		map[string]string{"readonly": "1"})
	if err == nil {
		t.Fatal("write succeeded with readonly=1")
	}
}
