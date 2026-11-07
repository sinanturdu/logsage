package auth

import (
	"container/list"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/keys"
)

// DefaultProjectID is the fixed id of the "default" project (migration 0001).
const DefaultProjectID = "00000000-0000-0000-0000-000000000001"

var (
	// ErrUnauthorized: missing, malformed, unknown, wrong-kind, revoked,
	// expired or disabled-project key. Callers answer 401 with a generic body.
	ErrUnauthorized = errors.New("missing or invalid API key")
	// ErrUnavailable: the key is not cached and the key store failed.
	// Callers answer 503; never 401 and never success.
	ErrUnavailable = errors.New("authentication temporarily unavailable")
)

// Principal is who a verified key belongs to. ProjectID decides the
// project for everything the request does.
type Principal struct {
	ProjectID string
	Kind      keys.Kind
	KeyID     string // empty for legacy static keys
	Legacy    bool
}

// Authenticator is what the ingest and query handlers depend on.
type Authenticator interface {
	Authenticate(ctx context.Context, key string, want keys.Kind) (Principal, error)
}

// KeyVerifier checks per-project keys (keys.Verifier in production).
type KeyVerifier interface {
	Verify(ctx context.Context, plaintext string, want keys.Kind) (keys.Identity, error)
}

// Options configures Service.
type Options struct {
	// Verifier checks database keys; nil means legacy static keys only.
	Verifier KeyVerifier
	// Legacy static keys, honored only when AllowLegacy is set. They map to
	// DefaultProjectID.
	AllowLegacy     bool
	LegacyIngestKey string
	LegacyQueryKey  string

	CacheTTL    time.Duration // positive results
	NegativeTTL time.Duration // rejected keys
	CacheSize   int           // max cached keys (LRU eviction)
	// VerifyTimeout bounds one store lookup; it is detached from the
	// request so one cancelled request does not fail the others waiting.
	VerifyTimeout time.Duration
}

// Service implements Authenticator.
type Service struct {
	opts   Options
	legacy []legacyKey
	now    func() time.Time

	mu       sync.Mutex
	entries  map[string]*list.Element // cache key -> element holding *entry
	lru      *list.List               // front = most recently used
	inflight map[string]*call         // single lookup per cache key
}

type legacyKey struct {
	hash [sha256.Size]byte
	kind keys.Kind
}

type entry struct {
	cacheKey  string
	principal Principal
	valid     bool
	expires   time.Time
}

type call struct {
	done      chan struct{}
	principal Principal
	err       error
}

// New validates opts and returns a Service.
func New(opts Options) (*Service, error) {
	if opts.Verifier == nil && !opts.AllowLegacy {
		return nil, errors.New("auth: no key source: set a database or allow legacy static keys")
	}
	if opts.CacheTTL <= 0 || opts.NegativeTTL <= 0 || opts.CacheSize <= 0 {
		return nil, errors.New("auth: cache TTLs and size must be positive")
	}
	if opts.VerifyTimeout <= 0 {
		opts.VerifyTimeout = 5 * time.Second
	}
	s := &Service{opts: opts, now: time.Now, entries: map[string]*list.Element{}, lru: list.New(), inflight: map[string]*call{}}
	if opts.AllowLegacy {
		if opts.LegacyIngestKey != "" && opts.LegacyIngestKey == opts.LegacyQueryKey {
			return nil, errors.New("auth: the legacy ingest and query keys must differ")
		}
		if opts.LegacyIngestKey != "" {
			s.legacy = append(s.legacy, legacyKey{sha256.Sum256([]byte(opts.LegacyIngestKey)), keys.KindIngest})
		}
		if opts.LegacyQueryKey != "" {
			s.legacy = append(s.legacy, legacyKey{sha256.Sum256([]byte(opts.LegacyQueryKey)), keys.KindQuery})
		}
	}
	return s, nil
}

// LogDeprecation warns once at startup when legacy static keys are active.
func (s *Service) LogDeprecation(log *slog.Logger) {
	if len(s.legacy) > 0 {
		log.Warn("legacy static API keys are enabled and map to the default project; they are deprecated, create per-project keys with cmd/admin and set ALLOW_LEGACY_STATIC_KEYS=false",
			"legacy_keys", len(s.legacy))
	}
}

// Authenticate verifies key for kind want.
func (s *Service) Authenticate(ctx context.Context, key string, want keys.Kind) (Principal, error) {
	if key == "" {
		return Principal{}, ErrUnauthorized
	}
	sum := sha256.Sum256([]byte(key))
	for _, l := range s.legacy { // constant time per configured key
		if subtle.ConstantTimeCompare(sum[:], l.hash[:]) == 1 {
			if l.kind != want {
				return Principal{}, ErrUnauthorized
			}
			return Principal{ProjectID: DefaultProjectID, Kind: l.kind, Legacy: true}, nil
		}
	}
	if s.opts.Verifier == nil {
		return Principal{}, ErrUnauthorized
	}

	ck := string(sum[:]) + "|" + string(want) // never the plaintext
	if p, valid, ok := s.lookup(ck); ok {
		if !valid {
			return Principal{}, ErrUnauthorized
		}
		return p, nil
	}
	return s.verifyOnce(ctx, ck, key, want)
}

// verifyOnce lets one caller per cache key query the store; concurrent
// callers for the same key wait for that result (no stampede).
func (s *Service) verifyOnce(ctx context.Context, ck, key string, want keys.Kind) (Principal, error) {
	s.mu.Lock()
	if c, ok := s.inflight[ck]; ok {
		s.mu.Unlock()
		select {
		case <-c.done:
			return c.principal, c.err
		case <-ctx.Done():
			return Principal{}, fmt.Errorf("%w: %v", ErrUnavailable, ctx.Err())
		}
	}
	c := &call{done: make(chan struct{})}
	s.inflight[ck] = c
	s.mu.Unlock()

	vctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.opts.VerifyTimeout)
	id, err := s.opts.Verifier.Verify(vctx, key, want)
	cancel()
	switch {
	case err == nil:
		c.principal = Principal{ProjectID: id.ProjectID, Kind: id.Kind, KeyID: id.KeyID}
		s.store(ck, c.principal, true, s.opts.CacheTTL)
	case errors.Is(err, keys.ErrInvalidKey):
		c.err = ErrUnauthorized
		s.store(ck, Principal{}, false, s.opts.NegativeTTL)
	default:
		// Not cached: the next request tries the store again.
		c.err = fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	s.mu.Lock()
	delete(s.inflight, ck)
	s.mu.Unlock()
	close(c.done)
	return c.principal, c.err
}

func (s *Service) lookup(ck string) (Principal, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	el, ok := s.entries[ck]
	if !ok {
		return Principal{}, false, false
	}
	e := el.Value.(*entry)
	if !s.now().Before(e.expires) {
		s.lru.Remove(el)
		delete(s.entries, ck)
		return Principal{}, false, false
	}
	s.lru.MoveToFront(el)
	return e.principal, e.valid, true
}

func (s *Service) store(ck string, p Principal, valid bool, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if el, ok := s.entries[ck]; ok {
		s.lru.Remove(el)
	}
	s.entries[ck] = s.lru.PushFront(&entry{cacheKey: ck, principal: p, valid: valid, expires: s.now().Add(ttl)})
	for s.lru.Len() > s.opts.CacheSize {
		old := s.lru.Back()
		s.lru.Remove(old)
		delete(s.entries, old.Value.(*entry).cacheKey)
	}
}

// CacheLen reports the number of cached keys (for tests and stats).
func (s *Service) CacheLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lru.Len()
}

// StaticKey is an Authenticator for one fixed key, for tests and tools.
type StaticKey struct {
	hash      [sha256.Size]byte
	principal Principal
}

// NewStaticKey returns an Authenticator that accepts key for kind and maps
// it to projectID.
func NewStaticKey(key string, kind keys.Kind, projectID string) *StaticKey {
	return &StaticKey{hash: sha256.Sum256([]byte(key)), principal: Principal{ProjectID: projectID, Kind: kind}}
}

// Authenticate implements Authenticator.
func (s *StaticKey) Authenticate(_ context.Context, key string, want keys.Kind) (Principal, error) {
	sum := sha256.Sum256([]byte(key))
	if key == "" || subtle.ConstantTimeCompare(sum[:], s.hash[:]) != 1 || s.principal.Kind != want {
		return Principal{}, ErrUnauthorized
	}
	return s.principal, nil
}

