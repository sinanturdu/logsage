package alert

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)

// run feeds a rule one observation per minute and keeps the states, like the
// evaluator does: a fake clock, no sleeping.
type run struct {
	t      *testing.T
	rule   Rule
	now    time.Time
	states map[string]State
	trans  []Transition
	notes  []Notice
}

func newRun(t *testing.T, r Rule) *run {
	return &run{t: t, rule: r, now: t0, states: map[string]State{}}
}

func (x *run) step(obs ...Observation) Outcome {
	out := Step(x.rule, x.states, obs, x.now)
	for _, s := range out.States {
		s.Version++
		x.states[s.DedupKey] = s
	}
	x.trans = append(x.trans, out.Transitions...)
	x.notes = append(x.notes, out.Notices...)
	x.now = x.now.Add(time.Minute)
	return out
}

func (x *run) state(key string) StateName { return x.states[key].State }

var (
	fire   = Observation{Key: RuleKey, Value: 12, Threshold: 5, Fire: true}
	inBand = Observation{Key: RuleKey, Value: 4.8, Threshold: 5} // below the threshold, inside the band
	clear  = Observation{Key: RuleKey, Value: 1, Threshold: 5, Clear: true}
)

func TestFiresAtOnceWithoutFor(t *testing.T) {
	x := newRun(t, Rule{})
	out := x.step(fire)
	if x.state(RuleKey) != StateFiring || len(out.Notices) != 1 || out.Notices[0].Event != EventFiring || out.Notices[0].Repeat != 0 {
		t.Fatalf("outcome %+v", out)
	}
	if out.Transitions[0].From != StateOK || out.Transitions[0].To != StateFiring || out.Notices[0].Transition != 0 {
		t.Fatalf("transition %+v", out.Transitions)
	}
	// Still firing: no new notification without repeat.
	if out := x.step(fire); len(out.Notices) != 0 || len(out.Transitions) != 0 {
		t.Fatalf("second step %+v", out)
	}
}

func TestPendingBecomesFiringAfterFor(t *testing.T) {
	x := newRun(t, Rule{For: 3 * time.Minute})
	x.step(fire) // 10:00 pending
	if x.state(RuleKey) != StatePending || len(x.notes) != 0 {
		t.Fatalf("not pending: %v", x.states)
	}
	x.step(fire) // 10:01
	x.step(fire) // 10:02
	if x.state(RuleKey) != StatePending || len(x.notes) != 0 {
		t.Fatal("fired before the for duration")
	}
	x.step(fire) // 10:03: held for 3 minutes
	if x.state(RuleKey) != StateFiring || len(x.notes) != 1 {
		t.Fatalf("did not fire after for: %v", x.states)
	}
}

func TestPendingDropsWithoutNotification(t *testing.T) {
	x := newRun(t, Rule{For: 5 * time.Minute})
	x.step(fire)
	x.step(inBand) // not firing any more (inside the band also ends pending)
	if x.state(RuleKey) != StateOK || len(x.notes) != 0 {
		t.Fatalf("state %v, notes %d", x.state(RuleKey), len(x.notes))
	}
	if last := x.trans[len(x.trans)-1]; last.From != StatePending || last.To != StateOK || last.Reason == "" {
		t.Fatalf("transition %+v", last)
	}
}

func TestResolveNeedsClearPastTheBandForClearFor(t *testing.T) {
	x := newRun(t, Rule{ClearFor: 2 * time.Minute})
	x.step(fire)   // 10:00 firing
	x.step(inBand) // 10:01 inside the band: stays firing
	x.step(clear)  // 10:02 clear starts
	x.step(fire)   // 10:03 back: the clear timer restarts
	x.step(clear)  // 10:04 clear starts again
	x.step(clear)  // 10:05 one minute clear
	if x.state(RuleKey) != StateFiring || len(x.notes) != 1 {
		t.Fatalf("resolved too early: %v %d", x.state(RuleKey), len(x.notes))
	}
	x.step(clear) // 10:06 two minutes clear
	if x.state(RuleKey) != StateResolved || len(x.notes) != 2 {
		t.Fatalf("did not resolve: %v", x.states)
	}
	n := x.notes[1]
	if n.Event != EventResolved || n.ResolvedAt == nil || !n.ResolvedAt.Equal(t0.Add(6*time.Minute)) || !n.FiredAt.Equal(t0) {
		t.Fatalf("resolved notice %+v", n)
	}
	// Firing again later is a new firing notification.
	x.step(fire)
	if x.state(RuleKey) != StateFiring || len(x.notes) != 3 || x.notes[2].Event != EventFiring || x.notes[2].Repeat != 0 {
		t.Fatalf("refire %+v", x.notes)
	}
}

func TestClearForZeroResolvesAtOnce(t *testing.T) {
	x := newRun(t, Rule{})
	x.step(fire)
	x.step(clear)
	if x.state(RuleKey) != StateResolved {
		t.Fatal("clear_for 0 must resolve on the first clear observation")
	}
}

func TestRepeatInterval(t *testing.T) {
	x := newRun(t, Rule{Repeat: 5 * time.Minute})
	for i := 0; i < 11; i++ { // 10:00 .. 10:10
		x.step(fire)
	}
	if len(x.notes) != 3 {
		t.Fatalf("%d notices, want the first and two repeats", len(x.notes))
	}
	for i, want := range []int{0, 1, 2} {
		if x.notes[i].Repeat != want || x.notes[i].Event != EventFiring || !x.notes[i].FiredAt.Equal(t0) {
			t.Fatalf("notice %d %+v", i, x.notes[i])
		}
	}
	if x.notes[1].Transition != -1 || len(x.trans) != 1 {
		t.Fatal("a repeat is not a transition")
	}
	if !x.notes[2].FiredAt.Equal(t0) || x.states[RuleKey].NotifyCount != 3 {
		t.Fatalf("state %+v", x.states[RuleKey])
	}
}

func TestGroupKeysFireOnceEach(t *testing.T) {
	x := newRun(t, Rule{Type: NewGroup})
	a := Observation{Key: GroupKey(1), Value: 3, Fire: true, Fingerprint: 1, Template: "a <n>"}
	b := Observation{Key: GroupKey(2), Value: 9, Fire: true, Fingerprint: 2, Template: "b"}
	x.step(a)
	x.step(a, b) // a stays firing without a new notice; b fires
	if len(x.notes) != 2 || x.notes[0].Key != GroupKey(1) || x.notes[1].Key != GroupKey(2) || x.notes[1].Template != "b" {
		t.Fatalf("notices %+v", x.notes)
	}
	// a leaves the window (no longer observed): it resolves; b stays.
	x.step(b)
	if x.state(GroupKey(1)) != StateResolved || x.state(GroupKey(2)) != StateFiring {
		t.Fatalf("states %v", x.states)
	}
	if n := x.notes[len(x.notes)-1]; n.Event != EventResolved || n.Fingerprint != 1 || n.Template != "a <n>" {
		t.Fatalf("resolved notice keeps the group: %+v", n)
	}
}

func TestStepIsDeterministicAndOrdered(t *testing.T) {
	obs := []Observation{
		{Key: GroupKey(9), Fire: true},
		{Key: GroupKey(3), Fire: true},
		{Key: GroupKey(5), Fire: true},
	}
	a := Step(Rule{}, nil, obs, t0)
	b := Step(Rule{}, nil, []Observation{obs[2], obs[0], obs[1]}, t0)
	if len(a.Notices) != 3 || a.Notices[0].Key != GroupKey(3) || a.Notices[2].Key != GroupKey(9) {
		t.Fatalf("order %+v", a.Notices)
	}
	for i := range a.Notices {
		if a.Notices[i] != b.Notices[i] {
			t.Fatalf("not deterministic at %d", i)
		}
	}
}

// After a restart the evaluator reads the stored states: a firing rule stays
// firing and sends nothing new until its repeat interval.
func TestRestartKeepsFiringWithoutNewNotice(t *testing.T) {
	x := newRun(t, Rule{Repeat: 10 * time.Minute})
	x.step(fire)
	stored := map[string]State{}
	for k, v := range x.states {
		stored[k] = v
	}
	// "restart": a fresh run over the stored states, three minutes later.
	out := Step(x.rule, stored, []Observation{fire}, t0.Add(3*time.Minute))
	if len(out.Notices) != 0 || len(out.Transitions) != 0 {
		t.Fatalf("restart re-notified: %+v", out)
	}
	out = Step(x.rule, stored, []Observation{fire}, t0.Add(10*time.Minute))
	if len(out.Notices) != 1 || out.Notices[0].Repeat != 1 {
		t.Fatalf("repeat after restart: %+v", out.Notices)
	}
}

// An edit that changes what a rule measures deletes its states (the store
// does that); the next step starts from ok, so it can fire again.
func TestEditResetStartsFromOK(t *testing.T) {
	x := newRun(t, Rule{})
	x.step(fire)
	x.states = map[string]State{} // reset by the edit
	out := x.step(fire)
	if len(out.Notices) != 1 || out.Transitions[0].From != StateOK {
		t.Fatalf("after reset %+v", out)
	}
}

func TestValueIsKeptWhileFiring(t *testing.T) {
	x := newRun(t, Rule{})
	x.step(fire)
	more := fire
	more.Value = 20
	out := x.step(more)
	if len(out.States) != 1 || out.States[0].LastValue != 20 || len(out.Notices) != 0 {
		t.Fatalf("value update %+v", out)
	}
	// An ok rule with an unchanged non-firing observation writes nothing.
	y := newRun(t, Rule{})
	if out := y.step(clear); len(out.States) != 0 {
		t.Fatalf("ok rule wrote %+v", out.States)
	}
}

func TestGroupKeyNeverFiresTwice(t *testing.T) {
	for _, tp := range []Type{NewGroup, Regression} {
		x := newRun(t, Rule{Type: tp})
		a := Observation{Key: GroupKey(1), Fire: true, Fingerprint: 1}
		x.step(a)
		x.step()  // gone from the window: resolved
		x.step(a) // seen again inside a later window
		if len(x.notes) != 2 || x.state(GroupKey(1)) != StateResolved {
			t.Fatalf("%s: %d notices, state %v", tp, len(x.notes), x.state(GroupKey(1)))
		}
	}
	// A rule-level key fires again after it resolved.
	x := newRun(t, Rule{})
	x.step(fire)
	x.step(clear)
	x.step(fire)
	if len(x.notes) != 3 {
		t.Fatalf("rule key: %d notices", len(x.notes))
	}
}
