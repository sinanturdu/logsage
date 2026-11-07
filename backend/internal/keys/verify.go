package keys

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"
)

// ErrInvalidKey is the only error callers get for a key that must be
// rejected: malformed, unknown prefix, wrong secret, wrong kind, revoked,
// expired or belonging to a disabled project. It never carries the key.
var ErrInvalidKey = errors.New("invalid API key")

// ErrNotFound is returned by Store.LookupKey for an unknown prefix.
var ErrNotFound = errors.New("key not found")

// Record is what the store knows about a key.
type Record struct {
	ID                string
	ProjectID         string
	ProjectSlug       string
	Kind              Kind
	Hash              []byte
	ExpiresAt         *time.Time
	RevokedAt         *time.Time
	ProjectDisabledAt *time.Time
}

// Store is the key storage the verifier needs (PostgreSQL in production).
type Store interface {
	LookupKey(ctx context.Context, prefix string) (Record, error)
	// TouchKey sets last_used_at to now unless it was set less than a
	// minute ago (the store re-checks this, for several processes).
	TouchKey(ctx context.Context, id string) error
}

// Identity is a verified key's owner.
type Identity struct {
	KeyID       string
	ProjectID   string
	ProjectSlug string
	Kind        Kind
}

// TouchInterval is the minimum time between last_used_at writes per key.
const TouchInterval = time.Minute

// Verifier checks keys against a Store.
type Verifier struct {
	store Store
	log   *slog.Logger
	now   func() time.Time

	mu        sync.Mutex
	lastTouch map[string]time.Time // key id -> last TouchKey call by this process
}

// NewVerifier returns a Verifier. log may be nil.
func NewVerifier(store Store, log *slog.Logger) *Verifier {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Verifier{store: store, log: log, now: time.Now, lastTouch: map[string]time.Time{}}
}

// dummyHash is compared against when the prefix is unknown, so a miss does a
// similar amount of work as a hit.
var dummyHash = make([]byte, 32)

// Verify checks plaintext. want restricts the kind ("" accepts either kind).
// Every rejection returns ErrInvalidKey; a storage failure returns a
// different error (callers should answer 503, not 401). Errors never include
// the key.
func (v *Verifier) Verify(ctx context.Context, plaintext string, want Kind) (Identity, error) {
	kind, prefix, err := Parse(plaintext)
	if err != nil {
		return Identity{}, ErrInvalidKey
	}
	got := Hash(plaintext)

	rec, err := v.store.LookupKey(ctx, prefix)
	if errors.Is(err, ErrNotFound) {
		subtle.ConstantTimeCompare(got[:], dummyHash)
		return Identity{}, ErrInvalidKey
	}
	if err != nil {
		return Identity{}, fmt.Errorf("keys: look up key: %w", err)
	}

	match := subtle.ConstantTimeCompare(got[:], rec.Hash) == 1
	now := v.now()
	switch {
	case !match,
		rec.Kind != kind,
		want != "" && kind != want,
		rec.RevokedAt != nil,
		rec.ExpiresAt != nil && !now.Before(*rec.ExpiresAt),
		rec.ProjectDisabledAt != nil:
		return Identity{}, ErrInvalidKey
	}

	v.touch(ctx, rec.ID, now)
	return Identity{KeyID: rec.ID, ProjectID: rec.ProjectID, ProjectSlug: rec.ProjectSlug, Kind: rec.Kind}, nil
}

// touch records usage at most once per TouchInterval per key in this
// process; failures are logged and never fail verification.
func (v *Verifier) touch(ctx context.Context, id string, now time.Time) {
	v.mu.Lock()
	last, seen := v.lastTouch[id]
	if seen && now.Sub(last) < TouchInterval {
		v.mu.Unlock()
		return
	}
	v.lastTouch[id] = now
	v.mu.Unlock()
	if err := v.store.TouchKey(ctx, id); err != nil {
		v.log.Warn("could not update api key last_used_at", "key_id", id, "err", err)
	}
}
