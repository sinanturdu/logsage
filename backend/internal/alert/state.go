package alert

import (
	"slices"
	"time"
)

// StateName is where a rule (or one group of a group rule) stands.
type StateName string

const (
	StateOK       StateName = "ok"
	StatePending  StateName = "pending"
	StateFiring   StateName = "firing"
	StateResolved StateName = "resolved"
)

// State is the stored state of one dedup key of a rule.
type State struct {
	DedupKey       string
	State          StateName
	Since          time.Time  // when State was entered
	ClearSince     *time.Time // while firing: since when the condition has been clear
	LastValue      float64
	FiredAt        *time.Time
	LastNotifiedAt *time.Time
	NotifyCount    int
	Fingerprint    uint64
	Template       string
	// Version is the row version for the optimistic check; 0 for a new row.
	Version int
}

// Observation is what one evaluation measured for one dedup key.
type Observation struct {
	Key       string
	Value     float64
	Threshold float64
	// Fire: the condition holds. Clear: the value is back past the
	// hysteresis band. Neither: inside the band, nothing changes.
	Fire, Clear bool
	Fingerprint uint64
	Template    string
	Sample      string // a raw message, only sent to channels that opted in
}

// Transition is one state change, recorded as an alert_events row.
type Transition struct {
	Key         string
	From, To    StateName
	Value       float64
	Threshold   float64
	Fingerprint uint64
	Template    string
	Reason      string
}

// Event names of notifications.
const (
	EventFiring   = "alert.firing"
	EventResolved = "alert.resolved"
	EventTest     = "alert.test"
)

// Notice asks for a notification on every channel of the rule.
type Notice struct {
	Key         string
	Event       string
	Value       float64
	Threshold   float64
	FiredAt     time.Time
	ResolvedAt  *time.Time
	Repeat      int // 0 for the first firing notification, then 1, 2, ...
	Fingerprint uint64
	Template    string
	Sample      string
	// Transition is the index of the transition this notice belongs to in
	// Outcome.Transitions, or -1 for a repeat (no state change).
	Transition int
}

// Outcome is the result of one step: states to write, transitions to record
// and notifications to send. All of it is written in one transaction.
type Outcome struct {
	States      []State
	Transitions []Transition
	Notices     []Notice
	// The heartbeat: EvaluatedAt is set after a successful evaluation;
	// EvalError is the category of a failed one (EvalQueryTimeout and so on).
	EvaluatedAt time.Time
	EvalError   string
}

// Step advances every dedup key of a rule from its previous states and this
// evaluation's observations, at time now. It is pure: the same input gives
// the same output. Keys of group rules that are no longer observed count as
// clear, so their states resolve once the clear_for time has passed.
func Step(r Rule, prev map[string]State, obs []Observation, now time.Time) Outcome {
	byKey := make(map[string]Observation, len(obs))
	keys := make([]string, 0, len(obs)+len(prev))
	for _, o := range obs {
		if _, dup := byKey[o.Key]; !dup {
			keys = append(keys, o.Key)
		}
		byKey[o.Key] = o
	}
	for k, s := range prev {
		if _, seen := byKey[k]; seen || (s.State != StatePending && s.State != StateFiring) {
			continue
		}
		keys = append(keys, k)
		byKey[k] = Observation{Key: k, Clear: true, Value: s.LastValue, Fingerprint: s.Fingerprint, Template: s.Template}
	}
	slices.Sort(keys)

	var out Outcome
	for _, k := range keys {
		o := byKey[k]
		cur, ok := prev[k]
		if !ok {
			cur = State{DedupKey: k, State: StateOK, Since: now, Fingerprint: o.Fingerprint, Template: o.Template}
		}
		next, changed := cur, false
		transition := func(to StateName, reason string) int {
			out.Transitions = append(out.Transitions, Transition{Key: k, From: cur.State, To: to, Value: o.Value, Threshold: o.Threshold,
				Fingerprint: o.Fingerprint, Template: o.Template, Reason: reason})
			next.State, next.Since, changed = to, now, true
			return len(out.Transitions) - 1
		}
		fire := func() {
			i := transition(StateFiring, "")
			at := now
			next.FiredAt, next.LastNotifiedAt, next.NotifyCount, next.ClearSince = &at, &at, 1, nil
			out.Notices = append(out.Notices, Notice{Key: k, Event: EventFiring, Value: o.Value, Threshold: o.Threshold, FiredAt: now,
				Fingerprint: o.Fingerprint, Template: o.Template, Sample: o.Sample, Transition: i})
		}
		switch cur.State {
		case StateOK, StateResolved:
			// A group key (a new group, or one regression) fires once: after it
			// resolved it never fires again.
			if o.Fire && !(r.IsGroupRule() && cur.State == StateResolved) {
				if r.For == 0 {
					fire()
				} else {
					transition(StatePending, "")
				}
			}
		case StatePending:
			switch {
			case o.Fire && now.Sub(cur.Since) >= r.For:
				fire()
			case !o.Fire:
				transition(StateOK, "condition no longer holds")
			}
		case StateFiring:
			if o.Clear {
				if cur.ClearSince == nil {
					at := now
					next.ClearSince, changed = &at, true
				}
				if now.Sub(*next.ClearSince) >= r.ClearFor {
					i := transition(StateResolved, "")
					at := now
					next.ClearSince = nil
					fired := now
					if cur.FiredAt != nil {
						fired = *cur.FiredAt
					}
					out.Notices = append(out.Notices, Notice{Key: k, Event: EventResolved, Value: o.Value, Threshold: o.Threshold, FiredAt: fired,
						ResolvedAt: &at, Repeat: cur.NotifyCount, Fingerprint: o.Fingerprint, Template: o.Template, Transition: i})
				}
			} else {
				if cur.ClearSince != nil {
					next.ClearSince, changed = nil, true
				}
				if r.Repeat > 0 && cur.LastNotifiedAt != nil && now.Sub(*cur.LastNotifiedAt) >= r.Repeat {
					at := now
					next.LastNotifiedAt, next.NotifyCount, changed = &at, cur.NotifyCount+1, true
					fired := now
					if cur.FiredAt != nil {
						fired = *cur.FiredAt
					}
					out.Notices = append(out.Notices, Notice{Key: k, Event: EventFiring, Value: o.Value, Threshold: o.Threshold, FiredAt: fired,
						Repeat: cur.NotifyCount, Fingerprint: o.Fingerprint, Template: o.Template, Sample: o.Sample, Transition: -1})
				}
			}
		}
		if next.LastValue != o.Value && (changed || next.State == StateFiring || next.State == StatePending) {
			next.LastValue, changed = o.Value, true
		}
		if changed {
			out.States = append(out.States, next)
		}
	}
	return out
}
