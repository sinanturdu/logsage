package processor

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// GroupState is what the trimmer needs to know about one consumer group.
type GroupState struct {
	Name          string
	Pending       int64  // delivered, not yet acknowledged (XPENDING count)
	LowestPending string // smallest pending ID (XPENDING lower); empty when Pending == 0
	LastDelivered string // last-delivered-id from XINFO GROUPS
}

// TrimStore reads consumer-group state and trims the stream.
type TrimStore interface {
	GroupStates(ctx context.Context) ([]GroupState, error)
	TrimMinID(ctx context.Context, minID string) (int64, error)
}

// streamID is a parsed Redis stream entry ID (<ms>-<seq>).
type streamID struct{ ms, seq uint64 }

func parseStreamID(s string) (streamID, error) {
	msPart, seqPart, ok := strings.Cut(s, "-")
	if !ok {
		return streamID{}, fmt.Errorf("invalid stream ID %q", s)
	}
	ms, err1 := strconv.ParseUint(msPart, 10, 64)
	seq, err2 := strconv.ParseUint(seqPart, 10, 64)
	if err1 != nil || err2 != nil {
		return streamID{}, fmt.Errorf("invalid stream ID %q", s)
	}
	return streamID{ms, seq}, nil
}

func (a streamID) less(b streamID) bool {
	return a.ms < b.ms || (a.ms == b.ms && a.seq < b.seq)
}

func (a streamID) isZero() bool { return a.ms == 0 && a.seq == 0 }

// next returns the smallest ID greater than a.
func (a streamID) next() streamID {
	if a.seq == math.MaxUint64 {
		return streamID{a.ms + 1, 0}
	}
	return streamID{a.ms, a.seq + 1}
}

func (a streamID) String() string { return fmt.Sprintf("%d-%d", a.ms, a.seq) }

// SafeTrimBoundary returns the MINID for XTRIM such that every entry below it
// has been delivered to and acknowledged by EVERY consumer group:
//
//   - a group with pending entries needs everything from its lowest pending
//     ID on (entries below it were delivered in order and are acknowledged);
//   - a group with nothing pending has acknowledged everything up to its
//     last-delivered ID and needs everything after it (not delivered yet).
//
// The boundary is the minimum over all groups, so a slow or new group (for
// example a future alerting consumer) holds back trimming for everyone. When
// the boundary cannot be determined safely, ok is false and reason says why;
// the caller must not trim. Stream IDs only grow and a group's lowest pending
// ID only moves forward, so state read slightly earlier is still safe.
func SafeTrimBoundary(groups []GroupState) (minID string, ok bool, reason string) {
	if len(groups) == 0 {
		return "", false, "stream has no consumer groups"
	}
	var boundary streamID
	for i, g := range groups {
		var candidate streamID
		switch {
		case g.Pending < 0:
			return "", false, fmt.Sprintf("group %q: negative pending count %d", g.Name, g.Pending)
		case g.Pending > 0:
			id, err := parseStreamID(g.LowestPending)
			if err != nil {
				return "", false, fmt.Sprintf("group %q: %d pending but lowest pending ID unknown: %v", g.Name, g.Pending, err)
			}
			candidate = id
		default:
			id, err := parseStreamID(g.LastDelivered)
			if err != nil {
				return "", false, fmt.Sprintf("group %q: %v", g.Name, err)
			}
			if id.isZero() {
				return "", false, fmt.Sprintf("group %q has not read anything yet", g.Name)
			}
			candidate = id.next()
		}
		if i == 0 || candidate.less(boundary) {
			boundary = candidate
		}
	}
	return boundary.String(), true, ""
}

// TrimOnce computes the safe boundary and trims below it. It never trims when
// the group state cannot be read or understood.
func (p *Processor) TrimOnce(ctx context.Context, ts TrimStore) (int64, error) {
	groups, err := ts.GroupStates(ctx)
	if err != nil {
		return 0, fmt.Errorf("read consumer groups: %w", err)
	}
	minID, ok, reason := SafeTrimBoundary(groups)
	if !ok {
		return 0, fmt.Errorf("no safe trim boundary: %s", reason)
	}
	n, err := ts.TrimMinID(ctx, minID)
	if err != nil {
		return 0, fmt.Errorf("xtrim minid %s: %w", minID, err)
	}
	p.trimmed.Add(n)
	return n, nil
}

// RunTrimmer calls TrimOnce every interval until ctx ends. Failures are
// logged at warn level and leave the stream untouched.
func (p *Processor) RunTrimmer(ctx context.Context, ts TrimStore, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := p.TrimOnce(ctx, ts)
			if err != nil {
				if ctx.Err() == nil {
					p.log.Warn("stream not trimmed", "err", err)
				}
				continue
			}
			if n > 0 {
				p.log.Debug("stream trimmed", "removed", n)
			}
		}
	}
}
