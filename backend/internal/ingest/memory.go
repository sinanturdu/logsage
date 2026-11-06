package ingest

import (
	"context"
	"sync"
)

// MemoryPublisher keeps published events in memory. It is meant for tests and
// local runs until the Redis Streams publisher exists.
type MemoryPublisher struct {
	mu     sync.Mutex
	max    int
	events []Event
}

// NewMemoryPublisher returns a publisher that keeps at most max events,
// dropping the oldest first. max <= 0 means unbounded.
func NewMemoryPublisher(max int) *MemoryPublisher {
	return &MemoryPublisher{max: max}
}

// Publish stores a copy of events.
func (p *MemoryPublisher) Publish(ctx context.Context, events []Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, events...)
	if p.max > 0 && len(p.events) > p.max {
		p.events = append([]Event(nil), p.events[len(p.events)-p.max:]...)
	}
	return nil
}

// Events returns a copy of the stored events, oldest first.
func (p *MemoryPublisher) Events() []Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Event(nil), p.events...)
}
