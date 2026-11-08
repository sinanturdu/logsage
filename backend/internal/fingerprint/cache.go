package fingerprint

import (
	"strings"
	"sync"
)

// DefaultCacheEntries is the processor's cache size (ADR 0022).
const DefaultCacheEntries = 8192

// Cache memoizes Normalize by its exact input (the source line cut to
// MaxInput), so repeated messages are normalized once. It never changes a
// result: a hit returns what Normalize returned for the same input.
//
// Eviction is by generation: entries go into the current map; when it holds
// half of maxEntries it becomes the old map and the previous old map is
// dropped. A hit in the old map moves the entry back. At most maxEntries
// entries are held. Memory cap: each entry holds a key and a template of at
// most MaxInput (512) bytes each, plus map overhead, so about 1.1 KiB per
// entry in the worst case: about 9 MiB for 8192 entries (typical log lines
// need a tenth of that).
//
// A Cache is safe for concurrent use. A nil *Cache computes without caching.
type Cache struct {
	mu       sync.Mutex
	half     int
	cur, old map[string]string
}

// NewCache returns a cache holding at most maxEntries templates (at least 2).
func NewCache(maxEntries int) *Cache {
	half := max(maxEntries/2, 1)
	return &Cache{half: half, cur: make(map[string]string, half)}
}

// Normalize returns Normalize(message), from the cache when it can.
func (c *Cache) Normalize(message string) string {
	if c == nil {
		return Normalize(message)
	}
	key := inputLine(message)
	c.mu.Lock()
	if t, ok := c.cur[key]; ok {
		c.mu.Unlock()
		return t
	}
	if t, ok := c.old[key]; ok {
		c.putLocked(key, t)
		c.mu.Unlock()
		return t
	}
	c.mu.Unlock()
	t := normalizeLine(key)
	c.mu.Lock()
	c.putLocked(strings.Clone(key), t) // the key must not pin the whole message
	c.mu.Unlock()
	return t
}

func (c *Cache) putLocked(key, tpl string) {
	if len(c.cur) >= c.half {
		c.old, c.cur = c.cur, make(map[string]string, c.half)
	}
	c.cur[key] = tpl
}

// Len returns the number of cached templates.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.cur) + len(c.old)
}

// Compute is Compute with the template taken from the cache.
func (c *Cache) Compute(level, service, message string, attrs map[string]string) (uint64, string) {
	if !Groupable(level) {
		return 0, ""
	}
	tpl := c.Normalize(message)
	return Of(service, tpl, ErrorType(attrs)), tpl
}
