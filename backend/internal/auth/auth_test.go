package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/keys"
)

// fakeVerifier answers from a map and counts calls; block makes calls wait.
type fakeVerifier struct {
	mu    sync.Mutex
	ids   map[string]keys.Identity // key -> identity (kind checked against want)
	err   error                    // returned for every call when set
	calls atomic.Int64
	block chan struct{}
}

func (f *fakeVerifier) Verify(_ context.Context, key string, want keys.Kind) (keys.Identity, error) {
	f.calls.Add(1)
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return keys.Identity{}, f.err
	}
	id, ok := f.ids[key]
	if !ok || (want != "" && id.Kind != want) {
		return keys.Identity{}, keys.ErrInvalidKey
	}
	return id, nil
}

func (f *fakeVerifier) set(key string, id *keys.Identity, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id == nil {
		delete(f.ids, key)
	} else {
		f.ids[key] = *id
	}
	f.err = err
}

const (
	ingKey = "lsk_ing_aaaaaaaa_good-ingest-secret"
	qryKey = "lsk_qry_bbbbbbbb_good-query-secret"
	projA  = "11111111-1111-1111-1111-111111111111"
)

func newSvc(t *testing.T, v *fakeVerifier, mutate func(*Options)) (*Service, *time.Time) {
	t.Helper()
	o := Options{Verifier: v, CacheTTL: 30 * time.Second, NegativeTTL: 5 * time.Second, CacheSize: 100}
	if mutate != nil {
		mutate(&o)
	}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	return s, &clock
}

func goodVerifier() *fakeVerifier {
	return &fakeVerifier{ids: map[string]keys.Identity{
		ingKey: {KeyID: "k1", ProjectID: projA, ProjectSlug: "a", Kind: keys.KindIngest},
		qryKey: {KeyID: "k2", ProjectID: projA, ProjectSlug: "a", Kind: keys.KindQuery},
	}}
}

func TestAuthenticateOutcomes(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		key     string
		want    keys.Kind
		verr    error
		wantErr error
		wantPID string
	}{
		{name: "valid ingest key", key: ingKey, want: keys.KindIngest, wantPID: projA},
		{name: "valid query key", key: qryKey, want: keys.KindQuery, wantPID: projA},
		{name: "ingest key on query", key: ingKey, want: keys.KindQuery, wantErr: ErrUnauthorized},
		{name: "query key on ingest", key: qryKey, want: keys.KindIngest, wantErr: ErrUnauthorized},
		{name: "unknown key", key: "lsk_ing_cccccccc_unknown", want: keys.KindIngest, wantErr: ErrUnauthorized},
		{name: "empty key", key: "", want: keys.KindIngest, wantErr: ErrUnauthorized},
		{name: "revoked/expired/disabled all come back as ErrInvalidKey", key: ingKey, want: keys.KindIngest, verr: keys.ErrInvalidKey, wantErr: ErrUnauthorized},
		{name: "store error is 503, not 401", key: ingKey, want: keys.KindIngest, verr: errors.New("dial tcp 10.0.0.5:5432: refused"), wantErr: ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := goodVerifier()
			v.err = tt.verr
			s, _ := newSvc(t, v, nil)
			p, err := s.Authenticate(ctx, tt.key, tt.want)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				if errors.Is(err, ErrUnauthorized) && errors.Is(err, ErrUnavailable) {
					t.Fatal("error is both 401 and 503")
				}
				if tt.key != "" && strings.Contains(err.Error(), tt.key) {
					t.Fatalf("error leaks the key: %v", err)
				}
				return
			}
			if err != nil || p.ProjectID != tt.wantPID || p.Kind != tt.want {
				t.Fatalf("principal = %+v, %v", p, err)
			}
		})
	}
}

func TestCachePositiveAndExpiry(t *testing.T) {
	v := goodVerifier()
	s, clock := newSvc(t, v, nil)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if _, err := s.Authenticate(ctx, ingKey, keys.KindIngest); err != nil {
			t.Fatal(err)
		}
	}
	if v.calls.Load() != 1 {
		t.Fatalf("store calls = %d, want 1 (cache hits)", v.calls.Load())
	}
	// The key is revoked in the store: still valid until the TTL ends.
	v.set(ingKey, nil, nil)
	*clock = clock.Add(29 * time.Second)
	if _, err := s.Authenticate(ctx, ingKey, keys.KindIngest); err != nil {
		t.Fatalf("within TTL: %v, want still valid (documented trade-off)", err)
	}
	*clock = clock.Add(2 * time.Second)
	if _, err := s.Authenticate(ctx, ingKey, keys.KindIngest); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("after TTL: %v, want 401", err)
	}
	if v.calls.Load() != 2 {
		t.Errorf("store calls = %d, want 2", v.calls.Load())
	}
}

func TestNegativeCache(t *testing.T) {
	v := goodVerifier()
	s, clock := newSvc(t, v, nil)
	ctx := context.Background()
	garbage := "lsk_ing_zzzzzzzz_garbage"
	for i := 0; i < 50; i++ {
		if _, err := s.Authenticate(ctx, garbage, keys.KindIngest); !errors.Is(err, ErrUnauthorized) {
			t.Fatal(err)
		}
	}
	if v.calls.Load() != 1 {
		t.Fatalf("store calls for a garbage key = %d, want 1 (negative cache)", v.calls.Load())
	}
	*clock = clock.Add(6 * time.Second) // negative TTL 5s
	_, _ = s.Authenticate(ctx, garbage, keys.KindIngest)
	if v.calls.Load() != 2 {
		t.Errorf("after negative TTL: calls = %d, want 2", v.calls.Load())
	}
	// A key that becomes valid is picked up after the negative TTL.
	v.set(garbage, &keys.Identity{ProjectID: projA, Kind: keys.KindIngest}, nil)
	*clock = clock.Add(6 * time.Second)
	if _, err := s.Authenticate(ctx, garbage, keys.KindIngest); err != nil {
		t.Errorf("new key after negative TTL: %v", err)
	}
}

func TestCachedKeySurvivesStoreErrorUntilTTL(t *testing.T) {
	v := goodVerifier()
	s, clock := newSvc(t, v, nil)
	ctx := context.Background()
	if _, err := s.Authenticate(ctx, ingKey, keys.KindIngest); err != nil {
		t.Fatal(err)
	}
	v.set(ingKey, &keys.Identity{ProjectID: projA, Kind: keys.KindIngest}, errors.New("postgres down"))
	*clock = clock.Add(20 * time.Second)
	if _, err := s.Authenticate(ctx, ingKey, keys.KindIngest); err != nil {
		t.Fatalf("cached key during outage: %v, want valid", err)
	}
	// Uncached key during the outage: 503.
	if _, err := s.Authenticate(ctx, qryKey, keys.KindQuery); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("uncached key during outage: %v, want ErrUnavailable", err)
	}
	*clock = clock.Add(11 * time.Second) // TTL over
	if _, err := s.Authenticate(ctx, ingKey, keys.KindIngest); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expired entry during outage: %v, want ErrUnavailable", err)
	}
	// Errors are not cached: once the store is back, the key works again.
	v.set(ingKey, &keys.Identity{ProjectID: projA, Kind: keys.KindIngest}, nil)
	if _, err := s.Authenticate(ctx, ingKey, keys.KindIngest); err != nil {
		t.Fatalf("after recovery: %v", err)
	}
}

func TestNoStampede(t *testing.T) {
	v := goodVerifier()
	v.block = make(chan struct{})
	s, _ := newSvc(t, v, nil)
	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Authenticate(context.Background(), ingKey, keys.KindIngest)
			errs <- err
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for v.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // let the others queue behind the first
	close(v.block)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if v.calls.Load() != 1 {
		t.Fatalf("store calls for %d concurrent requests = %d, want 1", n, v.calls.Load())
	}
}

func TestCacheIsBoundedAndKeyedByHash(t *testing.T) {
	v := goodVerifier()
	s, _ := newSvc(t, v, func(o *Options) { o.CacheSize = 3 })
	for i := 0; i < 10; i++ {
		_, _ = s.Authenticate(context.Background(), "lsk_ing_garbage"+string(rune('a'+i)), keys.KindIngest)
	}
	if s.CacheLen() != 3 {
		t.Fatalf("cache size = %d, want 3", s.CacheLen())
	}
	_, _ = s.Authenticate(context.Background(), ingKey, keys.KindIngest)
	s.mu.Lock()
	defer s.mu.Unlock()
	for ck := range s.entries {
		if strings.Contains(ck, "lsk_") || strings.Contains(ck, "secret") {
			t.Fatalf("cache key contains plaintext: %q", ck)
		}
	}
}

func TestLegacyKeys(t *testing.T) {
	ctx := context.Background()
	v := goodVerifier()
	on, _ := newSvc(t, v, func(o *Options) {
		o.AllowLegacy, o.LegacyIngestKey, o.LegacyQueryKey = true, "legacy-ingest", "legacy-query"
	})
	p, err := on.Authenticate(ctx, "legacy-ingest", keys.KindIngest)
	if err != nil || p.ProjectID != DefaultProjectID || !p.Legacy {
		t.Fatalf("legacy ingest: %+v, %v", p, err)
	}
	if _, err := on.Authenticate(ctx, "legacy-ingest", keys.KindQuery); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("legacy ingest key on query: %v", err)
	}
	if p, err := on.Authenticate(ctx, "legacy-query", keys.KindQuery); err != nil || p.ProjectID != DefaultProjectID {
		t.Fatalf("legacy query: %+v, %v", p, err)
	}
	if v.calls.Load() != 0 {
		t.Error("legacy keys should not hit the store")
	}

	off, _ := newSvc(t, v, func(o *Options) {
		o.AllowLegacy, o.LegacyIngestKey = false, "legacy-ingest"
	})
	if _, err := off.Authenticate(ctx, "legacy-ingest", keys.KindIngest); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("legacy key with flag off: %v, want 401", err)
	}

	if _, err := New(Options{AllowLegacy: true, LegacyIngestKey: "same", LegacyQueryKey: "same", CacheTTL: time.Second, NegativeTTL: time.Second, CacheSize: 1}); err == nil {
		t.Error("equal legacy keys accepted")
	}
	if _, err := New(Options{CacheTTL: time.Second, NegativeTTL: time.Second, CacheSize: 1}); err == nil {
		t.Error("no key source accepted")
	}
}

func TestStaticKey(t *testing.T) {
	a := NewStaticKey("k", keys.KindIngest, projA)
	if p, err := a.Authenticate(context.Background(), "k", keys.KindIngest); err != nil || p.ProjectID != projA {
		t.Fatalf("%+v %v", p, err)
	}
	for _, c := range []struct {
		key  string
		kind keys.Kind
	}{{"k", keys.KindQuery}, {"x", keys.KindIngest}, {"", keys.KindIngest}} {
		if _, err := a.Authenticate(context.Background(), c.key, c.kind); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("%q/%s: %v", c.key, c.kind, err)
		}
	}
}
