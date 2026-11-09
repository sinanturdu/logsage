package alert

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/fingerprint"
	"github.com/sinanturdu/logsage/backend/internal/query"
)

// Querier runs the alert queries (query.Service in production).
type Querier interface {
	AlertCounts(ctx context.Context, scope query.Scope, w query.AlertWindow) (total, errs uint64, err error)
	NewGroupCount(ctx context.Context, scope query.Scope, w query.AlertWindow) (uint64, error)
	NewGroups(ctx context.Context, scope query.Scope, w query.AlertWindow, minEvents uint64) ([]query.AlertGroup, error)
	GroupActivity(ctx context.Context, scope query.Scope, w query.AlertWindow, fps []uint64) ([]query.AlertGroup, error)
}

// ResolvedGroup is a group whose status is resolved (from PostgreSQL).
type ResolvedGroup struct {
	Fingerprint uint64
	ResolvedAt  time.Time
}

// ResolvedSource lists a project's resolved groups (the regression rule).
type ResolvedSource interface {
	ResolvedGroups(ctx context.Context, projectID string) ([]ResolvedGroup, error)
}

// Window is the evaluated event-time range [From, To).
type Window struct{ From, To time.Time }

// WindowAt is the window a rule looks at when evaluated at now: it ends
// lateness before now, so logs still on their way do not look missing.
func WindowAt(r Rule, now time.Time, lateness time.Duration) Window {
	end := now.Add(-lateness).Truncate(time.Millisecond)
	return Window{From: end.Add(-r.EffectiveWindow()), To: end}
}

// RuleKey is the dedup key of rules with one state.
const RuleKey = "rule"

// GroupKey is the dedup key of a new_group state.
func GroupKey(fp uint64) string { return "fp:" + fingerprint.Hex(fp) }

// RegressionKey is the dedup key of a regression: one per resolution.
func RegressionKey(fp uint64, resolvedAt time.Time) string {
	return GroupKey(fp) + ":" + strconv.FormatInt(resolvedAt.UnixMilli(), 10)
}

func band(v uint64, percent float64) uint64 { return uint64(math.Floor(float64(v) * percent / 100)) }

// Observe runs the rule's queries for one window and turns the result into
// observations. A query error is returned as is: the caller changes no
// state then.
func Observe(ctx context.Context, r Rule, q Querier, resolved ResolvedSource, win Window) ([]Observation, error) {
	scope := query.ScopeFor(r.ProjectID)
	w := query.AlertWindow{From: win.From, To: win.To, Services: r.Params.Services, Levels: r.Params.Levels}
	h := r.Hysteresis()
	p := r.Params
	switch r.Type {
	case ErrorRate:
		total, errs, err := q.AlertCounts(ctx, scope, w)
		if err != nil {
			return nil, err
		}
		share := 0.0
		if total > 0 {
			share = float64(errs) / float64(total) * 100
		}
		share = math.Round(share*100) / 100
		enough := total >= p.MinLogs
		return []Observation{{Key: RuleKey, Value: share, Threshold: p.ThresholdPercent,
			Fire:  enough && share >= p.ThresholdPercent,
			Clear: !enough || share < p.ThresholdPercent*(1-h/100)}}, nil
	case LogVolume:
		n, _, err := q.AlertCounts(ctx, scope, w)
		if err != nil {
			return nil, err
		}
		o := Observation{Key: RuleKey, Value: float64(n), Threshold: float64(p.Count)}
		if p.Op == "below" {
			o.Fire, o.Clear = n < p.Count, n >= p.Count+band(p.Count, h)
		} else {
			o.Fire, o.Clear = n > p.Count, n <= p.Count-min(p.Count, band(p.Count, h))
		}
		return []Observation{o}, nil
	case NewGroupsPerHour:
		n, err := q.NewGroupCount(ctx, scope, w)
		if err != nil {
			return nil, err
		}
		return []Observation{{Key: RuleKey, Value: float64(n), Threshold: float64(p.MaxGroups),
			Fire: n > p.MaxGroups, Clear: n <= p.MaxGroups-min(p.MaxGroups, band(p.MaxGroups, h))}}, nil
	case NewGroup:
		groups, err := q.NewGroups(ctx, scope, w, p.MinEvents)
		if err != nil {
			return nil, err
		}
		out := make([]Observation, 0, len(groups))
		for _, g := range groups {
			out = append(out, Observation{Key: GroupKey(g.Fingerprint), Value: float64(g.Count), Threshold: float64(p.MinEvents), Fire: true,
				Fingerprint: g.Fingerprint, Template: g.Title, Sample: g.Sample})
		}
		return out, nil
	case Regression:
		rs, err := resolved.ResolvedGroups(ctx, r.ProjectID)
		if err != nil {
			return nil, err
		}
		if len(rs) == 0 {
			return nil, nil
		}
		at := make(map[uint64]time.Time, len(rs))
		fps := make([]uint64, 0, len(rs))
		for _, g := range rs {
			at[g.Fingerprint] = g.ResolvedAt
			fps = append(fps, g.Fingerprint)
		}
		groups, err := q.GroupActivity(ctx, scope, w, fps)
		if err != nil {
			return nil, err
		}
		var out []Observation
		for _, g := range groups {
			res, ok := at[g.Fingerprint]
			if !ok || !g.Last.After(res) {
				continue
			}
			out = append(out, Observation{Key: RegressionKey(g.Fingerprint, res), Value: float64(g.Count), Fire: true,
				Fingerprint: g.Fingerprint, Template: g.Title, Sample: g.Sample})
		}
		return out, nil
	}
	return nil, fmt.Errorf("alert: unknown rule type %q", r.Type)
}
