package fingerprint

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
)

// TestCacheEqualsUncached: hits, misses and evictions all return exactly
// what Normalize returns, for every corpus message and random variants.
func TestCacheEqualsUncached(t *testing.T) {
	inputs := append([]string{}, corpus...)
	r := rand.New(rand.NewPCG(9, 9))
	for i := 0; i < 500; i++ {
		inputs = append(inputs, fmt.Sprintf("payment %x declined after %dms", r.Uint32(), r.IntN(9000)),
			corpus[r.IntN(len(corpus))]+strings.Repeat(" x", r.IntN(3)))
	}
	for _, size := range []int{2, 16, DefaultCacheEntries} {
		c := NewCache(size)
		for round := 0; round < 3; round++ {
			for _, i := range r.Perm(len(inputs)) {
				m := inputs[i]
				if got, want := c.Normalize(m), Normalize(m); got != want {
					t.Fatalf("size %d: cache %q, uncached %q for %q", size, got, want, m)
				}
				fp, tpl := c.Compute("error", "api", m, map[string]string{"error.type": "E"})
				wfp, wtpl := Compute("error", "api", m, map[string]string{"error.type": "E"})
				if fp != wfp || tpl != wtpl {
					t.Fatalf("Compute differs for %q", m)
				}
			}
			if c.Len() > size {
				t.Fatalf("size %d holds %d entries", size, c.Len())
			}
		}
	}
	var nilCache *Cache
	if fp, tpl := nilCache.Compute("info", "api", "x", nil); fp != 0 || tpl != "" || nilCache.Normalize("a 1") != "a <n>" {
		t.Fatal("nil cache")
	}
}

func TestCacheConcurrent(t *testing.T) {
	c := NewCache(64)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				m := corpus[(i*7+g)%len(corpus)]
				if got := c.Normalize(m); got != Normalize(m) {
					t.Errorf("concurrent: %q", m)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

func BenchmarkCacheHit(b *testing.B) {
	c := NewCache(DefaultCacheEntries)
	for _, m := range corpus {
		c.Normalize(m)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Normalize(corpus[i%len(corpus)])
	}
}
