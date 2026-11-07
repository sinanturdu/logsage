package api

import (
	"container/list"
	"sync"
	"time"
)

// Limiter throttles failed logins per key (client IP, email hash). After
// MaxFailures free failures each further failure blocks the key for
// Base, 2xBase, 4xBase ... up to Max. It is in memory, bounded (LRU) and
// per instance: N instances allow N times the attempts (ADR 0013).
type Limiter struct {
	mu          sync.Mutex
	maxFailures int
	base, max   time.Duration
	size        int
	entries     map[string]*list.Element
	lru         *list.List // front = most recently used
}

type limEntry struct {
	key          string
	failures     int
	blockedUntil time.Time
	last         time.Time
}

// NewLimiter returns a limiter tracking at most size keys.
func NewLimiter(maxFailures int, base, max time.Duration, size int) *Limiter {
	if size < 1 {
		size = 1
	}
	return &Limiter{maxFailures: maxFailures, base: base, max: max, size: size,
		entries: make(map[string]*list.Element), lru: list.New()}
}

// get returns the live entry for key, forgetting failures older than max
// (the counter decays after a quiet period).
func (l *Limiter) get(key string, now time.Time) *limEntry {
	el, ok := l.entries[key]
	if !ok {
		return nil
	}
	e := el.Value.(*limEntry)
	if now.After(e.blockedUntil) && now.Sub(e.last) > l.max {
		l.lru.Remove(el)
		delete(l.entries, key)
		return nil
	}
	return e
}

// Check returns how long the caller must wait when any key is blocked.
func (l *Limiter) Check(now time.Time, keys ...string) (wait time.Duration, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		if e := l.get(k, now); e != nil && now.Before(e.blockedUntil) {
			if d := e.blockedUntil.Sub(now); d > wait {
				wait = d
			}
		}
	}
	return wait, wait == 0
}

// Fail records a failure for every key.
func (l *Limiter) Fail(now time.Time, keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		e := l.get(k, now)
		if e == nil {
			e = &limEntry{key: k}
			l.entries[k] = l.lru.PushFront(e)
			for l.lru.Len() > l.size {
				old := l.lru.Back()
				l.lru.Remove(old)
				delete(l.entries, old.Value.(*limEntry).key)
			}
		} else {
			l.lru.MoveToFront(l.entries[k])
		}
		e.failures++
		e.last = now
		if over := e.failures - l.maxFailures; over > 0 {
			d := l.base
			for i := 1; i < over && d < l.max; i++ {
				d *= 2
			}
			if d > l.max {
				d = l.max
			}
			e.blockedUntil = now.Add(d)
		}
	}
}

// Reset forgets a key (after a successful login for that email). Client
// IP keys are not reset, so logging in to one's own account does not
// clear an IP's failures against other accounts.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if el, ok := l.entries[key]; ok {
		l.lru.Remove(el)
		delete(l.entries, key)
	}
}

// Len returns the number of tracked keys.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lru.Len()
}
