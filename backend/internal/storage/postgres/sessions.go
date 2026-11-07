package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// MaxUserAgent is the stored user agent length in bytes.
const MaxUserAgent = 256

// TruncateUserAgent cuts ua to MaxUserAgent bytes on a rune boundary and
// drops invalid UTF-8.
func TruncateUserAgent(ua string) string {
	ua = string([]rune(ua)) // replaces invalid bytes with U+FFFD
	if len(ua) <= MaxUserAgent {
		return ua
	}
	cut := MaxUserAgent
	for cut > 0 && !utf8.RuneStart(ua[cut]) {
		cut--
	}
	return ua[:cut]
}

// NewSession is a session to insert. TokenHash is SHA-256 of the cookie
// token; the token itself never reaches this layer.
type NewSession struct {
	UserID            string
	TokenHash         []byte
	UserAgent         string
	CreatedAt         time.Time
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
}

// Session is a session joined with its user.
type Session struct {
	ID                string
	UserID            string
	Email             string
	UserDisabled      bool
	CreatedAt         time.Time
	LastSeenAt        time.Time
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
	RevokedAt         *time.Time
}

// Valid reports whether the session can be used at now.
func (s Session) Valid(now time.Time) bool {
	return s.RevokedAt == nil && !s.UserDisabled && now.Before(s.IdleExpiresAt) && now.Before(s.AbsoluteExpiresAt)
}

// CreateSession inserts a session and returns its id.
func (s *Store) CreateSession(ctx context.Context, n NewSession) (string, error) {
	if len(n.TokenHash) != 32 {
		return "", errors.New("postgres: token hash must be 32 bytes")
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	var id string
	err := s.pool.QueryRow(ctx, `INSERT INTO sessions
		(user_id, token_hash, created_at, last_seen_at, idle_expires_at, absolute_expires_at, user_agent)
		VALUES ($1, $2, $3, $3, $4, $5, $6) RETURNING id::text`,
		n.UserID, n.TokenHash, n.CreatedAt, n.IdleExpiresAt, n.AbsoluteExpiresAt, TruncateUserAgent(n.UserAgent)).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("postgres: create session: %w", err)
	}
	return id, nil
}

// LookupSession finds a session by token hash (revoked and expired ones
// included; the caller checks Valid).
func (s *Store) LookupSession(ctx context.Context, tokenHash []byte) (Session, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	var x Session
	err := s.pool.QueryRow(ctx, `
		SELECT s.id::text, u.id::text, u.email, u.disabled_at IS NOT NULL, s.created_at, s.last_seen_at,
		       s.idle_expires_at, s.absolute_expires_at, s.revoked_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1`, tokenHash).
		Scan(&x.ID, &x.UserID, &x.Email, &x.UserDisabled, &x.CreatedAt, &x.LastSeenAt, &x.IdleExpiresAt, &x.AbsoluteExpiresAt, &x.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("postgres: look up session: %w", err)
	}
	return x, nil
}

// TouchSession records activity and slides the idle expiry, never past
// the absolute expiry.
func (s *Store) TouchSession(ctx context.Context, id string, now, idleExpiresAt time.Time) error {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	_, err := s.pool.Exec(ctx, `UPDATE sessions SET last_seen_at = $2, idle_expires_at = least($3, absolute_expires_at)
		WHERE id = $1 AND revoked_at IS NULL`, id, now, idleExpiresAt)
	if err != nil {
		return fmt.Errorf("postgres: touch session: %w", err)
	}
	return nil
}

// RevokeSession revokes one session.
func (s *Store) RevokeSession(ctx context.Context, id string) error {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	if _, err := s.pool.Exec(ctx, "UPDATE sessions SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL", id); err != nil {
		return fmt.Errorf("postgres: revoke session: %w", err)
	}
	return nil
}

// RevokeUserSessions revokes every active session of the user except
// exceptID (empty: all).
func (s *Store) RevokeUserSessions(ctx context.Context, userID, exceptID string) error {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	return revokeUserSessions(ctx, s.pool, userID, exceptID)
}

// execer is satisfied by *pgxpool.Pool and pgx.Tx.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func revokeUserSessions(ctx context.Context, db execer, userID, exceptID string) error {
	_, err := db.Exec(ctx, `UPDATE sessions SET revoked_at = now()
		WHERE user_id = $1 AND revoked_at IS NULL AND ($2 = '' OR id::text <> $2)`, userID, exceptID)
	if err != nil {
		return fmt.Errorf("postgres: revoke user sessions: %w", err)
	}
	return nil
}
