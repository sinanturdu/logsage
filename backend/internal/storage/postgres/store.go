package postgres

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sinanturdu/logsage/backend/internal/config"
	"github.com/sinanturdu/logsage/backend/internal/keys"
)

// DefaultProjectID is the fixed id of the "default" project created by
// migration 0001 (v0.1 ClickHouse rows carry project_id = 'default').
const DefaultProjectID = "00000000-0000-0000-0000-000000000001"

var (
	ErrNotFound        = errors.New("not found")
	ErrConflict        = errors.New("already exists")
	ErrProjectDisabled = errors.New("project is disabled")
	// ErrPrefixTaken means a generated key prefix collided; generate again.
	ErrPrefixTaken = errors.New("key prefix already in use")
)

// Open creates a connection pool from cfg and checks it with a ping.
// Errors never contain the password (pgx redacts it in parse errors).
func Open(ctx context.Context, cfg config.PostgresConfig) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("postgres: invalid database URL %s", cfg.Redacted())
	}
	pc.MaxConns = cfg.MaxConns
	pc.ConnConfig.ConnectTimeout = cfg.ConnectTimeout
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("postgres: create pool for %s: %w", cfg.Redacted(), err)
	}
	pctx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := pool.Ping(pctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: connect to %s: %w", cfg.Redacted(), err)
	}
	return pool, nil
}

var slugPattern = regexp.MustCompile(`^[a-z0-9-]{3,40}$`)

// ValidateSlug enforces the project slug rule (same as the SQL CHECK):
// 3-40 lowercase letters, digits or dashes.
func ValidateSlug(s string) error {
	if !slugPattern.MatchString(s) {
		return fmt.Errorf("invalid slug %q: use 3-40 lowercase letters, digits or dashes", s)
	}
	return nil
}

// ValidateName checks a project or key display name.
func ValidateName(s string) error {
	if n := len(strings.TrimSpace(s)); n < 1 || n > 200 {
		return errors.New("name must be 1-200 characters")
	}
	return nil
}

// Store reads and writes projects and API keys.
type Store struct {
	pool    *pgxpool.Pool
	timeout time.Duration
}

// NewStore returns a Store; every call is bounded by timeout.
func NewStore(pool *pgxpool.Pool, timeout time.Duration) *Store {
	return &Store{pool: pool, timeout: timeout}
}

func (s *Store) ctx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.timeout)
}

// Project is a row of projects.
type Project struct {
	ID         string
	Name       string
	Slug       string
	CreatedAt  time.Time
	DisabledAt *time.Time
}

const projectCols = "id::text, name, slug, created_at, disabled_at"

func scanProject(row pgx.Row) (Project, error) {
	var p Project
	err := row.Scan(&p.ID, &p.Name, &p.Slug, &p.CreatedAt, &p.DisabledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	return p, err
}

func uniqueViolation(err error, constraint string) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505" && (constraint == "" || pe.ConstraintName == constraint)
}

// CreateProject inserts a project.
func (s *Store) CreateProject(ctx context.Context, name, slug string) (Project, error) {
	if err := ValidateSlug(slug); err != nil {
		return Project{}, err
	}
	if err := ValidateName(name); err != nil {
		return Project{}, err
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	p, err := scanProject(s.pool.QueryRow(ctx,
		"INSERT INTO projects (name, slug) VALUES ($1, $2) RETURNING "+projectCols, strings.TrimSpace(name), slug))
	if uniqueViolation(err, "") {
		return Project{}, fmt.Errorf("project %q: %w", slug, ErrConflict)
	}
	if err != nil {
		return Project{}, fmt.Errorf("postgres: create project: %w", err)
	}
	return p, nil
}

// ListProjects returns all projects ordered by slug.
func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	rows, err := s.pool.Query(ctx, "SELECT "+projectCols+" FROM projects ORDER BY slug")
	if err != nil {
		return nil, fmt.Errorf("postgres: list projects: %w", err)
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan project: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DisableProject sets disabled_at (once). Keys of a disabled project stop
// verifying immediately.
func (s *Store) DisableProject(ctx context.Context, slug string) (Project, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	p, err := scanProject(s.pool.QueryRow(ctx,
		"UPDATE projects SET disabled_at = coalesce(disabled_at, now()) WHERE slug = $1 RETURNING "+projectCols, slug))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Project{}, fmt.Errorf("postgres: disable project: %w", err)
	}
	return p, err
}

// KeyInfo is an API key without its hash.
type KeyInfo struct {
	ID              string
	ProjectSlug     string
	Kind            keys.Kind
	Name            string
	Prefix          string
	CreatedAt       time.Time
	ExpiresAt       *time.Time
	RevokedAt       *time.Time
	LastUsedAt      *time.Time
	ProjectDisabled bool
}

// Status is "active", "revoked", "expired" or "project-disabled".
func (k KeyInfo) Status(now time.Time) string {
	switch {
	case k.RevokedAt != nil:
		return "revoked"
	case k.ExpiresAt != nil && !now.Before(*k.ExpiresAt):
		return "expired"
	case k.ProjectDisabled:
		return "project-disabled"
	}
	return "active"
}

// keyCols never selects key_hash.
const keyCols = `k.id::text, p.slug, k.kind, k.name, k.key_prefix, k.created_at, k.expires_at, k.revoked_at, k.last_used_at, p.disabled_at IS NOT NULL`

func scanKey(row pgx.Row) (KeyInfo, error) {
	var k KeyInfo
	var kind string
	err := row.Scan(&k.ID, &k.ProjectSlug, &kind, &k.Name, &k.Prefix, &k.CreatedAt, &k.ExpiresAt, &k.RevokedAt, &k.LastUsedAt, &k.ProjectDisabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return KeyInfo{}, ErrNotFound
	}
	k.Kind = keys.Kind(kind)
	return k, err
}

// CreateKey stores a generated key's prefix and hash for the project with
// slug. The plaintext never reaches this layer.
func (s *Store) CreateKey(ctx context.Context, slug string, kind keys.Kind, name, prefix string, hash []byte, expiresAt *time.Time) (KeyInfo, error) {
	if err := ValidateName(name); err != nil {
		return KeyInfo{}, err
	}
	if len(hash) != 32 {
		return KeyInfo{}, errors.New("postgres: key hash must be 32 bytes")
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	var projectID string
	var disabled *time.Time
	err := s.pool.QueryRow(ctx, "SELECT id::text, disabled_at FROM projects WHERE slug = $1", slug).Scan(&projectID, &disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return KeyInfo{}, fmt.Errorf("project %q: %w", slug, ErrNotFound)
	}
	if err != nil {
		return KeyInfo{}, fmt.Errorf("postgres: find project: %w", err)
	}
	if disabled != nil {
		return KeyInfo{}, fmt.Errorf("project %q: %w", slug, ErrProjectDisabled)
	}
	k, err := scanKey(s.pool.QueryRow(ctx, `
		WITH k AS (
			INSERT INTO api_keys (project_id, kind, name, key_prefix, key_hash, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING *
		)
		SELECT `+keyCols+` FROM k JOIN projects p ON p.id = k.project_id`,
		projectID, string(kind), strings.TrimSpace(name), prefix, hash, expiresAt))
	if uniqueViolation(err, "api_keys_key_prefix_key") {
		return KeyInfo{}, ErrPrefixTaken
	}
	if err != nil {
		return KeyInfo{}, fmt.Errorf("postgres: create key: %w", err)
	}
	return k, nil
}

// ListKeys returns the keys of a project (never the hashes), newest first.
func (s *Store) ListKeys(ctx context.Context, slug string) ([]KeyInfo, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	var exists bool
	if err := s.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM projects WHERE slug = $1)", slug).Scan(&exists); err != nil {
		return nil, fmt.Errorf("postgres: find project: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("project %q: %w", slug, ErrNotFound)
	}
	rows, err := s.pool.Query(ctx, "SELECT "+keyCols+` FROM api_keys k JOIN projects p ON p.id = k.project_id
		WHERE p.slug = $1 ORDER BY k.created_at DESC, k.key_prefix`, slug)
	if err != nil {
		return nil, fmt.Errorf("postgres: list keys: %w", err)
	}
	defer rows.Close()
	var out []KeyInfo
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan key: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// RevokeKey sets revoked_at (once) for the key with prefix.
func (s *Store) RevokeKey(ctx context.Context, prefix string) (KeyInfo, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	k, err := scanKey(s.pool.QueryRow(ctx, `
		WITH k AS (
			UPDATE api_keys SET revoked_at = coalesce(revoked_at, now()) WHERE key_prefix = $1 RETURNING *
		)
		SELECT `+keyCols+` FROM k JOIN projects p ON p.id = k.project_id`, prefix))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return KeyInfo{}, fmt.Errorf("postgres: revoke key: %w", err)
	}
	return k, err
}

// LookupKey implements keys.Store.
func (s *Store) LookupKey(ctx context.Context, prefix string) (keys.Record, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	var r keys.Record
	var kind string
	err := s.pool.QueryRow(ctx, `
		SELECT k.id::text, p.id::text, p.slug, k.kind, k.key_hash, k.expires_at, k.revoked_at, p.disabled_at
		FROM api_keys k JOIN projects p ON p.id = k.project_id
		WHERE k.key_prefix = $1`, prefix).
		Scan(&r.ID, &r.ProjectID, &r.ProjectSlug, &kind, &r.Hash, &r.ExpiresAt, &r.RevokedAt, &r.ProjectDisabledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return keys.Record{}, keys.ErrNotFound
	}
	if err != nil {
		return keys.Record{}, fmt.Errorf("postgres: look up key: %w", err)
	}
	r.Kind = keys.Kind(kind)
	return r, nil
}

// TouchKey implements keys.Store: last_used_at is written at most once per
// keys.TouchInterval, also across processes.
func (s *Store) TouchKey(ctx context.Context, id string) error {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	_, err := s.pool.Exec(ctx, `UPDATE api_keys SET last_used_at = now()
		WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - make_interval(secs => $2))`,
		id, keys.TouchInterval.Seconds())
	if err != nil {
		return fmt.Errorf("postgres: touch key: %w", err)
	}
	return nil
}

var _ keys.Store = (*Store)(nil)
