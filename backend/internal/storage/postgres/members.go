package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sinanturdu/logsage/backend/internal/keys"
)

// Role is a project member's role.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleViewer Role = "viewer"
)

// ParseRole accepts owner, admin or viewer.
func ParseRole(s string) (Role, error) {
	switch r := Role(s); r {
	case RoleOwner, RoleAdmin, RoleViewer:
		return r, nil
	}
	return "", fmt.Errorf("invalid role %q: use owner, admin or viewer", s)
}

// Membership is a project with the role a user has in it.
type Membership struct {
	Project
	Role Role
}

// CreateProjectWithOwner creates a project and makes userID its owner in
// one transaction.
func (s *Store) CreateProjectWithOwner(ctx context.Context, name, slug, userID string) (Project, error) {
	if err := ValidateSlug(slug); err != nil {
		return Project{}, err
	}
	if err := ValidateName(name); err != nil {
		return Project{}, err
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	var p Project
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		p, err = scanProject(tx.QueryRow(ctx,
			"INSERT INTO projects (name, slug, created_by) VALUES ($1, $2, $3) RETURNING "+projectCols,
			strings.TrimSpace(name), slug, userID))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'owner')", p.ID, userID)
		return err
	})
	if uniqueViolation(err, "projects_slug_key") {
		return Project{}, fmt.Errorf("project %q: %w", slug, ErrConflict)
	}
	if err != nil {
		return Project{}, fmt.Errorf("postgres: create project: %w", err)
	}
	return p, nil
}

const membershipCols = "p.id::text, p.name, p.slug, p.created_at, p.disabled_at, m.role"

func scanMembership(row pgx.Row) (Membership, error) {
	var m Membership
	var role string
	err := row.Scan(&m.ID, &m.Name, &m.Slug, &m.CreatedAt, &m.DisabledAt, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return Membership{}, ErrNotFound
	}
	m.Role = Role(role)
	return m, err
}

// ProjectsForUser lists the projects userID is a member of, by slug.
func (s *Store) ProjectsForUser(ctx context.Context, userID string) ([]Membership, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	rows, err := s.pool.Query(ctx, "SELECT "+membershipCols+` FROM project_members m JOIN projects p ON p.id = m.project_id
		WHERE m.user_id = $1 ORDER BY p.slug`, userID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list user projects: %w", err)
	}
	defer rows.Close()
	out := []Membership{}
	for rows.Next() {
		m, err := scanMembership(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan membership: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MembershipFor returns userID's membership in the project with slug.
// A missing project and a non-member both give ErrNotFound.
func (s *Store) MembershipFor(ctx context.Context, userID, slug string) (Membership, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	m, err := scanMembership(s.pool.QueryRow(ctx, "SELECT "+membershipCols+` FROM project_members m JOIN projects p ON p.id = m.project_id
		WHERE m.user_id = $1 AND p.slug = $2`, userID, slug))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Membership{}, fmt.Errorf("postgres: find membership: %w", err)
	}
	return m, err
}

// Member is a row of project_members joined with the user.
type Member struct {
	Email        string
	Role         Role
	CreatedAt    time.Time
	UserDisabled bool
}

// AddMember adds the user with email to the project, or changes their
// role if already a member.
func (s *Store) AddMember(ctx context.Context, slug, email string, role Role) (Member, error) {
	if _, err := ParseRole(string(role)); err != nil {
		return Member{}, err
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	var m Member
	var r string
	err := s.pool.QueryRow(ctx, `
		WITH ins AS (
			INSERT INTO project_members (project_id, user_id, role)
			SELECT p.id, u.id, $3 FROM projects p, users u WHERE p.slug = $1 AND lower(u.email) = lower($2)
			ON CONFLICT (project_id, user_id) DO UPDATE SET role = EXCLUDED.role
			RETURNING user_id, role, created_at
		)
		SELECT u.email, ins.role, ins.created_at, u.disabled_at IS NOT NULL FROM ins JOIN users u ON u.id = ins.user_id`,
		slug, strings.TrimSpace(email), string(role)).Scan(&m.Email, &r, &m.CreatedAt, &m.UserDisabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return Member{}, fmt.Errorf("project %q or user: %w", slug, ErrNotFound)
	}
	if err != nil {
		return Member{}, fmt.Errorf("postgres: add member: %w", err)
	}
	m.Role = Role(r)
	return m, nil
}

// ListMembers lists the members of a project by email.
func (s *Store) ListMembers(ctx context.Context, slug string) ([]Member, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	var exists bool
	if err := s.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM projects WHERE slug = $1)", slug).Scan(&exists); err != nil {
		return nil, fmt.Errorf("postgres: find project: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("project %q: %w", slug, ErrNotFound)
	}
	rows, err := s.pool.Query(ctx, `SELECT u.email, m.role, m.created_at, u.disabled_at IS NOT NULL
		FROM project_members m JOIN projects p ON p.id = m.project_id JOIN users u ON u.id = m.user_id
		WHERE p.slug = $1 ORDER BY lower(u.email)`, slug)
	if err != nil {
		return nil, fmt.Errorf("postgres: list members: %w", err)
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		var r string
		if err := rows.Scan(&m.Email, &r, &m.CreatedAt, &m.UserDisabled); err != nil {
			return nil, fmt.Errorf("postgres: scan member: %w", err)
		}
		m.Role = Role(r)
		out = append(out, m)
	}
	return out, rows.Err()
}

// maxPrefixAttempts bounds retries when a random 40-bit prefix collides.
const maxPrefixAttempts = 5

// IssueKey generates a key, stores its prefix and hash, and returns the
// plaintext once. Shared by the admin CLI and the management API.
func (s *Store) IssueKey(ctx context.Context, slug string, kind keys.Kind, name string, expiresAt *time.Time) (keys.Generated, KeyInfo, error) {
	for attempt := 1; ; attempt++ {
		g, err := keys.Generate(kind)
		if err != nil {
			return keys.Generated{}, KeyInfo{}, err
		}
		info, err := s.CreateKey(ctx, slug, kind, name, g.Prefix, g.Hash[:], expiresAt)
		if errors.Is(err, ErrPrefixTaken) && attempt < maxPrefixAttempts {
			continue
		}
		if err != nil {
			return keys.Generated{}, KeyInfo{}, err
		}
		return g, info, nil
	}
}

// RevokeProjectKey revokes the key with prefix only if it belongs to the
// project with slug; otherwise ErrNotFound.
func (s *Store) RevokeProjectKey(ctx context.Context, slug, prefix string) (KeyInfo, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	k, err := scanKey(s.pool.QueryRow(ctx, `
		WITH k AS (
			UPDATE api_keys SET revoked_at = coalesce(revoked_at, now())
			WHERE key_prefix = $2 AND project_id = (SELECT id FROM projects WHERE slug = $1)
			RETURNING *
		)
		SELECT `+keyCols+` FROM k JOIN projects p ON p.id = k.project_id`, slug, prefix))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return KeyInfo{}, fmt.Errorf("postgres: revoke key: %w", err)
	}
	return k, err
}

// AuditEntry is one audit_log row. Detail must never hold a secret.
type AuditEntry struct {
	UserID string // empty: no user (for example a failed login)
	Action string // e.g. "login.success", "key.create"
	Target string
	Detail string
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// Audit appends an audit_log row.
func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	var uid *string
	if e.UserID != "" {
		uid = &e.UserID
	}
	_, err := s.pool.Exec(ctx, "INSERT INTO audit_log (user_id, action, target, detail) VALUES ($1, $2, $3, $4)",
		uid, e.Action, clip(e.Target, 300), clip(e.Detail, 500))
	if err != nil {
		return fmt.Errorf("postgres: audit: %w", err)
	}
	return nil
}
