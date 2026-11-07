package password

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testParams keeps unit tests fast; production defaults are benchmarked
// separately (BenchmarkDefaultParams).
func testParams() Params {
	return Params{MemoryKiB: MinMemoryKiB, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}
}

func newTestHasher(t *testing.T, p Params, n int) *Hasher {
	t.Helper()
	h, err := NewHasher(p, n)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestCheckPolicy(t *testing.T) {
	tests := []struct {
		pw, email string
		ok        bool
	}{
		{"correct horse battery", "a@b.c", true},
		{strings.Repeat("x", 12), "", true},
		{strings.Repeat("x", 11), "", false},
		{strings.Repeat("ü", 12), "", true}, // characters, not bytes
		{strings.Repeat("x", 128), "", true},
		{strings.Repeat("x", 129), "", false},
		{"alice@example.com", "alice@example.com", false},
		{"ALICE@example.com ", "alice@example.com", false},
		{"no rules here 1", "", true}, // no composition rules
		{"\xff\xfe invalid utf8!!", "", false},
	}
	for _, tt := range tests {
		err := CheckPolicy(tt.pw, tt.email)
		if (err == nil) != tt.ok {
			t.Errorf("CheckPolicy(%d chars) = %v, want ok=%v", len(tt.pw), err, tt.ok)
		}
		if err != nil {
			if !errors.Is(err, ErrPolicy) {
				t.Errorf("error does not wrap ErrPolicy: %v", err)
			}
			if strings.Contains(err.Error(), tt.pw) && tt.pw != "" {
				t.Errorf("policy error echoes the password: %v", err)
			}
		}
	}
}

func TestHashAndVerify(t *testing.T) {
	h := newTestHasher(t, testParams(), 2)
	ctx := context.Background()
	pw := "correct horse battery staple"
	a, err := h.Hash(ctx, pw)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := h.Hash(ctx, pw)
	if a == b {
		t.Error("two hashes of the same password are equal: salt not random")
	}
	if !strings.HasPrefix(a, "$argon2id$v=19$m=19456,t=1,p=1$") || strings.Contains(a, pw) {
		t.Errorf("PHC = %q", a)
	}
	ok, rehash, err := h.Verify(ctx, pw, a)
	if !ok || rehash || err != nil {
		t.Fatalf("Verify(right) = %v %v %v", ok, rehash, err)
	}
	if ok, _, _ := h.Verify(ctx, pw+"x", a); ok {
		t.Error("wrong password verified")
	}
	if ok, _, _ := h.Verify(ctx, "", a); ok {
		t.Error("empty password verified")
	}
}

func TestRehashWhenParamsChange(t *testing.T) {
	old := newTestHasher(t, testParams(), 1)
	stronger := testParams()
	stronger.Iterations = 2
	cur := newTestHasher(t, stronger, 1)
	ctx := context.Background()
	phc, _ := old.Hash(ctx, "a long enough password")
	ok, rehash, err := cur.Verify(ctx, "a long enough password", phc)
	if !ok || !rehash || err != nil {
		t.Fatalf("old hash with new params: ok=%v rehash=%v err=%v, want ok and rehash", ok, rehash, err)
	}
	newPHC, _ := cur.Hash(ctx, "a long enough password")
	if _, rehash, _ := cur.Verify(ctx, "a long enough password", newPHC); rehash {
		t.Error("fresh hash flagged for rehash")
	}
}

func TestParse(t *testing.T) {
	h := newTestHasher(t, testParams(), 1)
	good, _ := h.Hash(context.Background(), "a long enough password")
	p, salt, key, err := Parse(good)
	if err != nil || p.MemoryKiB != MinMemoryKiB || p.Iterations != 1 || p.Parallelism != 1 || len(salt) != 16 || len(key) != 32 {
		t.Fatalf("Parse(good) = %+v %d %d %v", p, len(salt), len(key), err)
	}
	bad := []string{
		"", "plaintext", "$argon2i$v=19$m=19456,t=1,p=1$c2FsdHNhbHRzYWx0$aGFzaA",
		"$argon2id$v=16$m=19456,t=1,p=1$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=0,t=1,p=1$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=19456,t=1$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=19456,t=1,p=1,x=2$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=19456,t=1,p=1$!!!$aGFzaGhhc2hoYXNoaGFzaA",
		strings.Replace(good, "$argon2id", "$bcrypt", 1),
	}
	for _, b := range bad {
		if _, _, _, err := Parse(b); !errors.Is(err, ErrMalformed) {
			t.Errorf("Parse(%q) = %v, want ErrMalformed", b, err)
		}
		if ok, _, err := h.Verify(context.Background(), "x", b); ok || err == nil {
			t.Errorf("Verify against malformed %q = %v, %v", b, ok, err)
		}
	}
}

func TestParamsValidate(t *testing.T) {
	if err := DefaultParams().Validate(); err != nil {
		t.Fatal(err)
	}
	for i, p := range []Params{
		{MemoryKiB: 8 * 1024, Iterations: 3, Parallelism: 1, SaltLen: 16, KeyLen: 32},
		{MemoryKiB: 65536, Iterations: 0, Parallelism: 1, SaltLen: 16, KeyLen: 32},
		{MemoryKiB: 65536, Iterations: 3, Parallelism: 0, SaltLen: 16, KeyLen: 32},
		{MemoryKiB: 65536, Iterations: 3, Parallelism: 1, SaltLen: 8, KeyLen: 32},
	} {
		if p.Validate() == nil {
			t.Errorf("params %d accepted: %+v", i, p)
		}
	}
	if _, err := NewHasher(testParams(), 0); err == nil {
		t.Error("zero concurrency accepted")
	}
}

func TestSemaphoreBoundsConcurrency(t *testing.T) {
	h := newTestHasher(t, testParams(), 2)
	var cur, peak atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = h.acquire(context.Background())
			n := cur.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			cur.Add(-1)
			h.release()
		}()
	}
	wg.Wait()
	if peak.Load() > 2 {
		t.Fatalf("peak concurrent hashes = %d, want <= 2", peak.Load())
	}
	// A waiting caller gives up with its context.
	h.sem <- struct{}{}
	h.sem <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := h.Hash(ctx, "a long enough password"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Hash with a full semaphore = %v, want deadline exceeded", err)
	}
}

func TestDummyTakesSimilarTime(t *testing.T) {
	h := newTestHasher(t, testParams(), 1)
	ctx := context.Background()
	phc, _ := h.Hash(ctx, "a long enough password")
	measure := func(f func()) time.Duration {
		start := time.Now()
		for i := 0; i < 5; i++ {
			f()
		}
		return time.Since(start)
	}
	real := measure(func() { _, _, _ = h.Verify(ctx, "wrong password!!", phc) })
	dummy := measure(func() { h.VerifyDummy(ctx, "wrong password!!") })
	ratio := float64(dummy) / float64(real)
	if ratio < 0.25 || ratio > 4 { // loose: the suite runs packages in parallel
		t.Errorf("dummy/real verify time ratio = %.2f, want about 1 (real %v, dummy %v)", ratio, real, dummy)
	}
}

// BenchmarkDefaultParams measures one hash with the production defaults.
func BenchmarkDefaultParams(b *testing.B) {
	for _, p := range []Params{
		{MemoryKiB: 19 * 1024, Iterations: 2, Parallelism: 1, SaltLen: 16, KeyLen: 32},
		{MemoryKiB: 46 * 1024, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32},
		{MemoryKiB: 64 * 1024, Iterations: 2, Parallelism: 2, SaltLen: 16, KeyLen: 32},
		DefaultParams(),
		{MemoryKiB: 64 * 1024, Iterations: 4, Parallelism: 2, SaltLen: 16, KeyLen: 32},
	} {
		h, _ := NewHasher(p, 1)
		b.Run(fmt.Sprintf("m=%dMiB_t=%d_p=%d", p.MemoryKiB/1024, p.Iterations, p.Parallelism), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_, _ = h.Hash(context.Background(), "a long enough password")
			}
		})
	}
}
