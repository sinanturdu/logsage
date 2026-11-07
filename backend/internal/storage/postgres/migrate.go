// Package postgres holds the PostgreSQL access for LogSage: connection,
// schema migrations, projects and API keys.
package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationLockKey is the pg_advisory_lock key that serializes migration
// runners ("logsage" as ASCII bytes).
const migrationLockKey int64 = 0x6c6f6773616765

// Migration is one embedded SQL file.
type Migration struct {
	Version  int
	Name     string // file name without .sql
	SQL      string
	Checksum string // hex SHA-256 of SQL with line endings normalized to \n
}

var migrationFile = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

// LoadMigrations reads NNNN_name.sql files from fsys in version order.
// Line endings are normalized before hashing so a checkout with CRLF (Windows)
// and one with LF (Linux CI) produce the same checksum.
func LoadMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("postgres: list migrations: %w", err)
	}
	var out []Migration
	seen := map[int]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		m := migrationFile.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("postgres: migration file %q does not match NNNN_name.sql", e.Name())
		}
		v, _ := strconv.Atoi(m[1])
		if v == 0 {
			return nil, fmt.Errorf("postgres: migration %q: versions start at 0001", e.Name())
		}
		if prev, dup := seen[v]; dup {
			return nil, fmt.Errorf("postgres: migrations %q and %q share version %d", prev, e.Name(), v)
		}
		seen[v] = e.Name()
		raw, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("postgres: read migration %s: %w", e.Name(), err)
		}
		sql := strings.ReplaceAll(string(raw), "\r\n", "\n")
		if strings.TrimSpace(sql) == "" {
			return nil, fmt.Errorf("postgres: migration %s is empty", e.Name())
		}
		sum := sha256.Sum256([]byte(sql))
		out = append(out, Migration{Version: v, Name: strings.TrimSuffix(e.Name(), ".sql"), SQL: sql, Checksum: hex.EncodeToString(sum[:])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	if len(out) == 0 {
		return nil, errors.New("postgres: no migrations found")
	}
	return out, nil
}

// MigrationResult reports what Migrate did.
type MigrationResult struct {
	Applied []string // names applied by this call, in order
	Current int      // highest applied version afterwards
}

// Migrate applies pending migrations from fsys. It holds a session advisory
// lock for the whole run, so concurrent runners wait for each other and the
// second one finds nothing to do. Each migration runs in its own transaction
// together with its schema_migrations row. It fails without changing anything
// when an applied migration's checksum differs from the file (the file was
// edited after it was applied), when the database has a migration this binary
// does not know, or when a pending migration is older than the newest applied.
func Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) (MigrationResult, error) {
	migrations, err := LoadMigrations(fsys)
	if err != nil {
		return MigrationResult{}, err
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return MigrationResult{}, fmt.Errorf("postgres: acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		return MigrationResult{}, fmt.Errorf("postgres: take migration lock: %w", err)
	}
	defer func() {
		// Unlock even if ctx is done; a dropped connection releases it too.
		uctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(uctx, "SELECT pg_advisory_unlock($1)", migrationLockKey)
	}()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    integer     PRIMARY KEY,
		name       text        NOT NULL,
		checksum   text        NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return MigrationResult{}, fmt.Errorf("postgres: create schema_migrations: %w", err)
	}

	rows, err := conn.Query(ctx, "SELECT version, name, checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return MigrationResult{}, fmt.Errorf("postgres: read schema_migrations: %w", err)
	}
	type applied struct{ name, checksum string }
	done := map[int]applied{}
	maxApplied := 0
	for rows.Next() {
		var v int
		var a applied
		if err := rows.Scan(&v, &a.name, &a.checksum); err != nil {
			rows.Close()
			return MigrationResult{}, fmt.Errorf("postgres: scan schema_migrations: %w", err)
		}
		done[v] = a
		if v > maxApplied {
			maxApplied = v
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return MigrationResult{}, fmt.Errorf("postgres: read schema_migrations: %w", err)
	}

	known := map[int]Migration{}
	for _, m := range migrations {
		known[m.Version] = m
	}
	for v, a := range done {
		m, ok := known[v]
		if !ok {
			return MigrationResult{}, fmt.Errorf("postgres: database has migration %04d (%s) that this binary does not know; upgrade the binary", v, a.name)
		}
		if m.Checksum != a.checksum {
			return MigrationResult{}, fmt.Errorf("postgres: migration %s was modified after it was applied (checksum %.12s, applied %.12s); add a new migration instead of editing it", m.Name, m.Checksum, a.checksum)
		}
	}

	res := MigrationResult{Current: maxApplied}
	for _, m := range migrations {
		if _, ok := done[m.Version]; ok {
			continue
		}
		if m.Version < maxApplied {
			return res, fmt.Errorf("postgres: pending migration %s is older than the newest applied %04d", m.Name, maxApplied)
		}
		if err := applyOne(ctx, conn, m); err != nil {
			return res, err
		}
		res.Applied = append(res.Applied, m.Name)
		res.Current = m.Version
	}
	return res, nil
}

func applyOne(ctx context.Context, conn *pgxpool.Conn, m Migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin %s: %w", m.Name, err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Exec without arguments uses the simple protocol, so a file may hold
	// several statements.
	if _, err := tx.Exec(ctx, m.SQL); err != nil {
		return fmt.Errorf("postgres: apply %s: %w", m.Name, err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)", m.Version, m.Name, m.Checksum); err != nil {
		return fmt.Errorf("postgres: record %s: %w", m.Name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit %s: %w", m.Name, err)
	}
	return nil
}
