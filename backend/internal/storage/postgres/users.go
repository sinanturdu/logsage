package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

// ErrUserDisabled means the user exists but is disabled.
var ErrUserDisabled = errors.New("user is disabled")

// ValidateEmail does a light syntax check (same bounds as the SQL CHECK);
// there is no email verification yet (ADR 0013).
func ValidateEmail(email string) error {
	if n := len(email); n < 3 || n > 254 || email != strings.TrimSpace(email) {
		return errors.New("invalid email address")
	}
	at := strings.LastIndexByte(email, '@')
	if at < 1 || at == len(email)-1 {
		return errors.New("invalid email address")
	}
	for _, r := range email {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return errors.New("invalid email address")
		}
	}
	return nil
}

// User is a row of users. PasswordHash is only filled by UserByEmail and
// must never leave the server.
type User struct {
	ID           string
	Email        string
	PasswordHash string
	CreatedAt    time.Time
	DisabledAt   *time.Time
}

const userCols = "id::text, email, password_hash, created_at, disabled_at"

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.CreatedAt, &u.DisabledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

// CreateUser inserts a user; emails are unique case-insensitively.
func (s *Store) CreateUser(ctx context.Context, email, passwordHash string) (User, error) {
	email = strings.TrimSpace(email)
	if err := ValidateEmail(email); err != nil {
		return User{}, err
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	u, err := scanUser(s.pool.QueryRow(ctx,
		"INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING "+userCols, email, passwordHash))
	if uniqueViolation(err, "users_email_lower_key") {
		return User{}, fmt.Errorf("user: %w", ErrConflict)
	}
	if err != nil {
		return User{}, fmt.Errorf("postgres: create user: %w", err)
	}
	u.PasswordHash = ""
	return u, nil
}

// UserByEmail returns the user including the password hash (for login).
func (s *Store) UserByEmail(ctx context.Context, email string) (User, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	u, err := scanUser(s.pool.QueryRow(ctx, "SELECT "+userCols+" FROM users WHERE lower(email) = lower($1)", strings.TrimSpace(email)))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return User{}, fmt.Errorf("postgres: find user: %w", err)
	}
	return u, err
}

// UserByID returns the user including the password hash.
func (s *Store) UserByID(ctx context.Context, id string) (User, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	u, err := scanUser(s.pool.QueryRow(ctx, "SELECT "+userCols+" FROM users WHERE id = $1", id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return User{}, fmt.Errorf("postgres: find user: %w", err)
	}
	return u, err
}

// UpdatePasswordHash replaces the hash without touching sessions (used for
// rehash on login).
func (s *Store) UpdatePasswordHash(ctx context.Context, userID, oldHash, newHash string) error {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	// Compare-and-set: a concurrent password change wins over a rehash.
	_, err := s.pool.Exec(ctx, "UPDATE users SET password_hash = $3 WHERE id = $1 AND password_hash = $2", userID, oldHash, newHash)
	if err != nil {
		return fmt.Errorf("postgres: update password hash: %w", err)
	}
	return nil
}

// SetPassword replaces the hash and revokes every active session of the
// user except keepSessionID (empty: revoke all), in one transaction.
func (s *Store) SetPassword(ctx context.Context, userID, newHash, keepSessionID string) error {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "UPDATE users SET password_hash = $2 WHERE id = $1", userID, newHash)
		if err != nil {
			return fmt.Errorf("postgres: set password: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return revokeUserSessions(ctx, tx, userID, keepSessionID)
	})
}

// DisableUser sets disabled_at (once) and revokes all sessions.
func (s *Store) DisableUser(ctx context.Context, email string) (User, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	var u User
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		u, err = scanUser(tx.QueryRow(ctx,
			"UPDATE users SET disabled_at = coalesce(disabled_at, now()) WHERE lower(email) = lower($1) RETURNING "+userCols,
			strings.TrimSpace(email)))
		if err != nil {
			return err
		}
		return revokeUserSessions(ctx, tx, u.ID, "")
	})
	if err != nil && !errors.Is(err, ErrNotFound) {
		return User{}, fmt.Errorf("postgres: disable user: %w", err)
	}
	u.PasswordHash = ""
	return u, err
}
