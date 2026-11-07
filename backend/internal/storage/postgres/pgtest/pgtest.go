// Package pgtest gives integration tests a fresh, migrated PostgreSQL
// database on the server at POSTGRES_TEST_URL. Tests skip when it is unset.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sinanturdu/logsage/backend/internal/config"
	"github.com/sinanturdu/logsage/backend/internal/storage/postgres"
	"github.com/sinanturdu/logsage/backend/migrations"
)

// New creates a database named logsage_it_<random>, applies the
// migrations, and drops it when the test ends. It returns the pool, a
// store and the database URL.
func New(t testing.TB) (*pgxpool.Pool, *postgres.Store, string) {
	t.Helper()
	admin := os.Getenv("POSTGRES_TEST_URL")
	if admin == "" {
		t.Skip("POSTGRES_TEST_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	adminPool, err := postgres.Open(ctx, config.PostgresConfig{URL: admin, MaxConns: 2, ConnectTimeout: 10 * time.Second})
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
	pool, err := postgres.Open(ctx, config.PostgresConfig{URL: u.String(), MaxConns: 10, ConnectTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = adminPool.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		adminPool.Close()
	})
	if _, err := postgres.Migrate(ctx, pool, migrations.Postgres()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool, postgres.NewStore(pool, 10*time.Second), u.String()
}
