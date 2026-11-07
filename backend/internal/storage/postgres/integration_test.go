package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sinanturdu/logsage/backend/internal/config"
	"github.com/sinanturdu/logsage/backend/internal/keys"
	"github.com/sinanturdu/logsage/backend/migrations"
)

// tempDB creates a fresh database on the server at POSTGRES_TEST_URL and
// drops it after the test. It skips when POSTGRES_TEST_URL is not set.
func tempDB(t *testing.T) (*pgxpool.Pool, config.PostgresConfig) {
	t.Helper()
	admin := os.Getenv("POSTGRES_TEST_URL")
	if admin == "" {
		t.Skip("POSTGRES_TEST_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	adminCfg := config.PostgresConfig{URL: admin, MaxConns: 2, ConnectTimeout: 10 * time.Second, QueryTimeout: 30 * time.Second}
	adminPool, err := Open(ctx, adminCfg)
	if err != nil {
		t.Fatalf("connect to POSTGRES_TEST_URL: %v", err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "logsage_it_" + hex.EncodeToString(b)
	if _, err := adminPool.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		adminPool.Close()
		t.Fatalf("create test database: %v", err)
	}
	u, _ := url.Parse(admin)
	u.Path = "/" + name
	cfg := config.PostgresConfig{URL: u.String(), MaxConns: 10, ConnectTimeout: 10 * time.Second, QueryTimeout: 30 * time.Second}
	pool, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = adminPool.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		adminPool.Close()
	})
	return pool, cfg
}

func migrated(t *testing.T) (*pgxpool.Pool, *Store) {
	t.Helper()
	pool, _ := tempDB(t)
	if _, err := Migrate(context.Background(), pool, migrations.Postgres()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return pool, NewStore(pool, 10*time.Second)
}

func embedded0001(t *testing.T) []byte {
	t.Helper()
	b, err := fs.ReadFile(migrations.Postgres(), "0001_projects_and_api_keys.sql")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestIntegrationMigrateAppliesAndIsIdempotent(t *testing.T) {
	pool, _ := tempDB(t)
	ctx := context.Background()
	res, err := Migrate(ctx, pool, migrations.Postgres())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 1 || res.Applied[0] != "0001_projects_and_api_keys" || res.Current != 1 {
		t.Fatalf("first run = %+v", res)
	}
	res, err = Migrate(ctx, pool, migrations.Postgres())
	if err != nil || len(res.Applied) != 0 || res.Current != 1 {
		t.Fatalf("second run = %+v, %v; want nothing applied", res, err)
	}
	var id, slug string
	if err := pool.QueryRow(ctx, "SELECT id::text, slug FROM projects WHERE slug = 'default'").Scan(&id, &slug); err != nil || id != DefaultProjectID {
		t.Fatalf("default project = %s %s, %v", id, slug, err)
	}
	var n int
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&n)
	if n != 1 {
		t.Errorf("schema_migrations rows = %d, want 1", n)
	}
}

func TestIntegrationMigrateDetectsChanges(t *testing.T) {
	ctx := context.Background()
	orig := embedded0001(t)
	crlf := []byte(strings.ReplaceAll(strings.ReplaceAll(string(orig), "\r\n", "\n"), "\n", "\r\n"))

	tests := []struct {
		name    string
		second  fstest.MapFS
		setup   func(t *testing.T, pool *pgxpool.Pool)
		wantErr string // "" = must succeed
	}{
		{
			name:   "same file with CRLF line endings is not a change",
			second: fstest.MapFS{"0001_projects_and_api_keys.sql": {Data: crlf}},
		},
		{
			name:    "edited applied migration fails",
			second:  fstest.MapFS{"0001_projects_and_api_keys.sql": {Data: append(append([]byte{}, orig...), []byte("\n-- tweak\n")...)}},
			wantErr: "was modified after it was applied",
		},
		{
			name:    "migration unknown to this binary fails",
			second:  fstest.MapFS{"0001_projects_and_api_keys.sql": {Data: orig}},
			setup:   func(t *testing.T, pool *pgxpool.Pool) { exec(t, pool, "INSERT INTO schema_migrations VALUES (7, '0007_future', 'x')") },
			wantErr: "does not know",
		},
		{
			name: "pending migration older than the newest applied fails",
			second: fstest.MapFS{
				"0001_projects_and_api_keys.sql": {Data: orig},
				"0002_late.sql":                  {Data: []byte("SELECT 1;")},
				"0003_applied.sql":               {Data: []byte("SELECT 3;")},
			},
			setup: func(t *testing.T, pool *pgxpool.Pool) {
				ms, _ := LoadMigrations(fstest.MapFS{"0003_applied.sql": {Data: []byte("SELECT 3;")}})
				exec(t, pool, "INSERT INTO schema_migrations VALUES (3, '0003_applied', '"+ms[0].Checksum+"')")
			},
			wantErr: "older than the newest applied",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool, _ := tempDB(t)
			if _, err := Migrate(ctx, pool, migrations.Postgres()); err != nil {
				t.Fatal(err)
			}
			if tt.setup != nil {
				tt.setup(t, pool)
			}
			_, err := Migrate(ctx, pool, tt.second)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Migrate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Migrate error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestIntegrationMigrateFailureRollsBack(t *testing.T) {
	pool, _ := tempDB(t)
	ctx := context.Background()
	fsys := fstest.MapFS{
		"0001_projects_and_api_keys.sql": {Data: embedded0001(t)},
		"0002_broken.sql":                {Data: []byte("CREATE TABLE half_done (x int);\nSELECT * FROM no_such_table;")},
	}
	if _, err := Migrate(ctx, pool, fsys); err == nil || !strings.Contains(err.Error(), "apply 0002_broken") {
		t.Fatalf("Migrate error = %v, want failure in 0002", err)
	}
	var exists bool
	_ = pool.QueryRow(ctx, "SELECT to_regclass('half_done') IS NOT NULL").Scan(&exists)
	var versions int
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&versions)
	if exists || versions != 1 {
		t.Fatalf("after failed 0002: half_done exists=%v, schema_migrations=%d; want rollback and only 0001 recorded", exists, versions)
	}
}

func TestIntegrationMigrateConcurrentRunners(t *testing.T) {
	pool, _ := tempDB(t)
	ctx := context.Background()
	const runners = 8
	var wg sync.WaitGroup
	results := make([]MigrationResult, runners)
	errs := make([]error, runners)
	start := make(chan struct{})
	for i := 0; i < runners; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = Migrate(ctx, pool, migrations.Postgres())
		}(i)
	}
	close(start)
	wg.Wait()
	appliedBy := 0
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("runner %d: %v", i, errs[i])
		}
		if len(results[i].Applied) > 0 {
			appliedBy++
		}
		if results[i].Current != 1 {
			t.Errorf("runner %d ended at version %d", i, results[i].Current)
		}
	}
	if appliedBy != 1 {
		t.Errorf("%d runners applied migrations, want exactly 1", appliedBy)
	}
	var projects int
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM projects").Scan(&projects)
	if projects != 1 {
		t.Errorf("projects = %d, want only the default project", projects)
	}
}

func TestIntegrationProjectAndKeyFlows(t *testing.T) {
	pool, st := migrated(t)
	ctx := context.Background()

	p, err := st.CreateProject(ctx, "Demo app", "demo")
	if err != nil || p.Slug != "demo" || p.ID == "" {
		t.Fatalf("CreateProject = %+v, %v", p, err)
	}
	if _, err := st.CreateProject(ctx, "Again", "demo"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate slug: %v, want ErrConflict", err)
	}
	if _, err := st.CreateProject(ctx, "Bad", "Bad_Slug"); err == nil {
		t.Fatal("invalid slug accepted")
	}
	ps, _ := st.ListProjects(ctx)
	if len(ps) != 2 || ps[0].Slug != "default" || ps[1].Slug != "demo" {
		t.Fatalf("ListProjects = %+v", ps)
	}

	g, _ := keys.Generate(keys.KindIngest)
	k, err := st.CreateKey(ctx, "demo", keys.KindIngest, "ci pipeline", g.Prefix, g.Hash[:], nil)
	if err != nil || k.Prefix != g.Prefix || k.Kind != keys.KindIngest || k.ProjectSlug != "demo" {
		t.Fatalf("CreateKey = %+v, %v", k, err)
	}
	if _, err := st.CreateKey(ctx, "demo", keys.KindQuery, "dup", g.Prefix, g.Hash[:], nil); !errors.Is(err, ErrPrefixTaken) {
		t.Fatalf("duplicate prefix: %v, want ErrPrefixTaken", err)
	}
	if _, err := st.CreateKey(ctx, "nope", keys.KindIngest, "x", "abcdefgh", g.Hash[:], nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown project: %v", err)
	}
	ks, err := st.ListKeys(ctx, "demo")
	if err != nil || len(ks) != 1 || ks[0].Status(time.Now()) != "active" {
		t.Fatalf("ListKeys = %+v, %v", ks, err)
	}
	r, err := st.RevokeKey(ctx, g.Prefix)
	if err != nil || r.RevokedAt == nil || r.Status(time.Now()) != "revoked" {
		t.Fatalf("RevokeKey = %+v, %v", r, err)
	}
	if _, err := st.RevokeKey(ctx, "zzzzzzzz"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoke unknown: %v", err)
	}
	d, err := st.DisableProject(ctx, "demo")
	if err != nil || d.DisabledAt == nil {
		t.Fatalf("DisableProject = %+v, %v", d, err)
	}
	g2, _ := keys.Generate(keys.KindQuery)
	if _, err := st.CreateKey(ctx, "demo", keys.KindQuery, "late", g2.Prefix, g2.Hash[:], nil); !errors.Is(err, ErrProjectDisabled) {
		t.Fatalf("key for disabled project: %v", err)
	}

	// Deleting a project removes its keys (ON DELETE CASCADE).
	exec(t, pool, "DELETE FROM projects WHERE slug = 'demo'")
	var left int
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM api_keys").Scan(&left)
	if left != 0 {
		t.Errorf("api_keys left after deleting the project: %d", left)
	}
}

func TestIntegrationVerify(t *testing.T) {
	pool, st := migrated(t)
	ctx := context.Background()
	if _, err := st.CreateProject(ctx, "Live", "live"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateProject(ctx, "Off", "off"); err != nil {
		t.Fatal(err)
	}
	past, future := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	mk := func(project string, kind keys.Kind, expires *time.Time) keys.Generated {
		t.Helper()
		g, _ := keys.Generate(kind)
		if _, err := st.CreateKey(ctx, project, kind, "k", g.Prefix, g.Hash[:], expires); err != nil {
			t.Fatal(err)
		}
		return g
	}
	good := mk("live", keys.KindIngest, &future)
	query := mk("live", keys.KindQuery, nil)
	revoked := mk("live", keys.KindIngest, nil)
	expired := mk("live", keys.KindIngest, &past)
	disabled := mk("off", keys.KindIngest, nil)
	if _, err := st.RevokeKey(ctx, revoked.Prefix); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DisableProject(ctx, "off"); err != nil {
		t.Fatal(err)
	}
	unknown, _ := keys.Generate(keys.KindIngest)
	forged := good.Plaintext[:len(good.Plaintext)-4] + "AAAA"

	v := keys.NewVerifier(st, nil)
	id, err := v.Verify(ctx, good.Plaintext, keys.KindIngest)
	if err != nil || id.ProjectSlug != "live" || id.Kind != keys.KindIngest || id.ProjectID == "" {
		t.Fatalf("good key: %+v, %v", id, err)
	}
	if id, err := v.Verify(ctx, query.Plaintext, keys.KindQuery); err != nil || id.Kind != keys.KindQuery {
		t.Fatalf("query key: %+v, %v", id, err)
	}
	rejects := map[string]struct {
		key  string
		want keys.Kind
	}{
		"revoked":           {revoked.Plaintext, keys.KindIngest},
		"expired":           {expired.Plaintext, keys.KindIngest},
		"disabled project":  {disabled.Plaintext, keys.KindIngest},
		"wrong kind":        {query.Plaintext, keys.KindIngest},
		"malformed":         {"lsk_ing_short", keys.KindIngest},
		"unknown prefix":    {unknown.Plaintext, keys.KindIngest},
		"wrong secret":      {forged, keys.KindIngest},
		"garbage":           {"hello world", ""},
		"ingest key, query": {good.Plaintext, keys.KindQuery},
	}
	for name, r := range rejects {
		if _, err := v.Verify(ctx, r.key, r.want); !errors.Is(err, keys.ErrInvalidKey) || err.Error() != "invalid API key" {
			t.Errorf("%s: err = %v, want the generic ErrInvalidKey", name, err)
		}
	}

	assertNoPlaintext(t, pool, good, query, revoked, expired, disabled)
}

func TestIntegrationTouchThrottle(t *testing.T) {
	pool, st := migrated(t)
	ctx := context.Background()
	g, _ := keys.Generate(keys.KindIngest)
	k, err := st.CreateKey(ctx, "default", keys.KindIngest, "touch", g.Prefix, g.Hash[:], nil)
	if err != nil {
		t.Fatal(err)
	}
	lastUsed := func() *time.Time {
		var t0 *time.Time
		_ = pool.QueryRow(ctx, "SELECT last_used_at FROM api_keys WHERE id = $1", k.ID).Scan(&t0)
		return t0
	}
	if lastUsed() != nil {
		t.Fatal("last_used_at set before use")
	}
	if err := st.TouchKey(ctx, k.ID); err != nil {
		t.Fatal(err)
	}
	first := lastUsed()
	if first == nil {
		t.Fatal("first touch did not write")
	}
	time.Sleep(20 * time.Millisecond)
	if err := st.TouchKey(ctx, k.ID); err != nil {
		t.Fatal(err)
	}
	if second := lastUsed(); !second.Equal(*first) {
		t.Fatalf("second touch within a minute wrote %v (was %v)", second, first)
	}
	exec(t, pool, "UPDATE api_keys SET last_used_at = now() - interval '61 seconds' WHERE id = '"+k.ID+"'")
	old := lastUsed()
	if err := st.TouchKey(ctx, k.ID); err != nil {
		t.Fatal(err)
	}
	if third := lastUsed(); !third.After(*old) {
		t.Fatalf("touch after a minute did not write (%v, was %v)", third, old)
	}

	// Through the verifier.
	v := keys.NewVerifier(st, nil)
	exec(t, pool, "UPDATE api_keys SET last_used_at = NULL WHERE id = '"+k.ID+"'")
	for i := 0; i < 20; i++ {
		if _, err := v.Verify(ctx, g.Plaintext, keys.KindIngest); err != nil {
			t.Fatal(err)
		}
	}
	// The once-per-minute cache in the verifier is covered by the keys unit
	// tests; here the end result is that the key is recorded as used.
	if lastUsed() == nil {
		t.Error("verification did not record last_used_at")
	}
}

// assertNoPlaintext dumps every row of every table in the database as text
// and checks that no key or secret part appears anywhere.
func assertNoPlaintext(t *testing.T, pool *pgxpool.Pool, gs ...keys.Generated) {
	t.Helper()
	ctx := context.Background()
	rows, err := pool.Query(ctx, "SELECT tablename FROM pg_tables WHERE schemaname = 'public'")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	var dump strings.Builder
	for _, tbl := range tables {
		rs, err := pool.Query(ctx, "SELECT t::text FROM "+tbl+" t")
		if err != nil {
			t.Fatal(err)
		}
		for rs.Next() {
			var line string
			_ = rs.Scan(&line)
			dump.WriteString(line + "\n")
		}
		rs.Close()
	}
	all := dump.String()
	if !strings.Contains(all, "api_keys") && len(tables) < 3 {
		t.Fatalf("unexpected tables %v", tables)
	}
	for _, g := range gs {
		secret := strings.SplitN(g.Plaintext, "_", 4)[3]
		if strings.Contains(all, g.Plaintext) || strings.Contains(all, secret) {
			t.Fatalf("plaintext key or secret found in the database dump")
		}
		if !strings.Contains(all, g.Prefix) {
			t.Fatalf("prefix %s missing from the dump; the dump did not cover api_keys", g.Prefix)
		}
	}
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}
