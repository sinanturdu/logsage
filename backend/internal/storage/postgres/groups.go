package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// MaxGroupNote is the longest note on a group status, in characters.
const MaxGroupNote = 500

// GroupState is the user-set state of an error group (ADR 0022). A group
// without a row is open.
type GroupState struct {
	Fingerprint uint64
	Status      string // open, ignored or resolved
	ResolvedAt  *time.Time
	Note        string
	UpdatedBy   string // the email of who changed it last, empty when unknown
	UpdatedAt   time.Time
}

// The fingerprint is a uint64; PostgreSQL stores its bits in a bigint.
func fpToDB(fp uint64) int64  { return int64(fp) }
func fpFromDB(v int64) uint64 { return uint64(v) }

const groupCols = `g.fingerprint, g.status, g.resolved_at, g.note, coalesce(u.email, ''), g.updated_at`

func scanGroup(row pgx.Row) (GroupState, error) {
	var g GroupState
	var fp int64
	if err := row.Scan(&fp, &g.Status, &g.ResolvedAt, &g.Note, &g.UpdatedBy, &g.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return GroupState{}, ErrNotFound
		}
		return GroupState{}, err
	}
	g.Fingerprint = fpFromDB(fp)
	return g, nil
}

// GroupStates returns every group of the project with a stored status.
func (s *Store) GroupStates(ctx context.Context, projectID string) ([]GroupState, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	rows, err := s.pool.Query(ctx, `SELECT `+groupCols+` FROM error_groups g LEFT JOIN users u ON u.id = g.updated_by
		WHERE g.project_id = $1 ORDER BY g.fingerprint LIMIT 100000`, projectID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list group states: %w", err)
	}
	defer rows.Close()
	var out []GroupState
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan group state: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list group states: %w", err)
	}
	return out, nil
}

// GroupState returns one group's stored state, or ErrNotFound (open).
func (s *Store) GroupState(ctx context.Context, projectID string, fp uint64) (GroupState, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	g, err := scanGroup(s.pool.QueryRow(ctx, `SELECT `+groupCols+` FROM error_groups g LEFT JOIN users u ON u.id = g.updated_by
		WHERE g.project_id = $1 AND g.fingerprint = $2`, projectID, fpToDB(fp)))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return GroupState{}, fmt.Errorf("postgres: group state: %w", err)
	}
	return g, err
}

// SetGroupStatus stores a group's status and note and writes the audit row
// in the same transaction. resolved_at is set when the status becomes
// resolved (kept if it already was) and cleared otherwise.
func (s *Store) SetGroupStatus(ctx context.Context, projectID string, fp uint64, status, note, userID string, audit AuditEntry) (GroupState, error) {
	if fp == 0 {
		return GroupState{}, errors.New("postgres: fingerprint 0 is not a group")
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return GroupState{}, fmt.Errorf("postgres: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var uid *string
	if userID != "" {
		uid = &userID
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO error_groups AS g (project_id, fingerprint, status, resolved_at, note, updated_by, updated_at)
		VALUES ($1, $2, $3, CASE WHEN $3 = 'resolved' THEN now() END, $4, $5, now())
		ON CONFLICT (project_id, fingerprint) DO UPDATE SET
			status = excluded.status,
			resolved_at = CASE WHEN excluded.status = 'resolved' THEN coalesce(CASE WHEN g.status = 'resolved' THEN g.resolved_at END, now()) END,
			note = excluded.note,
			updated_by = excluded.updated_by,
			updated_at = now()`,
		projectID, fpToDB(fp), status, clip(note, MaxGroupNote), uid)
	if err != nil {
		return GroupState{}, fmt.Errorf("postgres: set group status: %w", err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO audit_log (user_id, action, target, detail) VALUES ($1, $2, $3, $4)",
		uid, audit.Action, clip(audit.Target, 300), clip(audit.Detail, 500)); err != nil {
		return GroupState{}, fmt.Errorf("postgres: audit group status: %w", err)
	}
	g, err := scanGroup(tx.QueryRow(ctx, `SELECT `+groupCols+` FROM error_groups g LEFT JOIN users u ON u.id = g.updated_by
		WHERE g.project_id = $1 AND g.fingerprint = $2`, projectID, fpToDB(fp)))
	if err != nil {
		return GroupState{}, fmt.Errorf("postgres: read group status: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return GroupState{}, fmt.Errorf("postgres: commit group status: %w", err)
	}
	return g, nil
}
