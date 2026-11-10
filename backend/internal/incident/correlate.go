package incident

import (
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Status of an incident.
type Status string

const (
	StatusOpen         Status = "open"
	StatusAcknowledged Status = "acknowledged"
	StatusResolved     Status = "resolved"
)

// Severity of an incident.
type Severity string

const (
	Critical Severity = "critical"
	High     Severity = "high"
	Medium   Severity = "medium"
	Low      Severity = "low"
)

// Severities is the closed list, most severe first.
var Severities = []Severity{Critical, High, Medium, Low}

// Limits (ADR 0026 section 1).
const (
	MaxTitle         = 200
	MaxSummary       = 2000
	MaxComment       = 2000
	MaxServices      = 20
	MaxGroups        = 50
	MaxOpenIncidents = 1000
	MaxEvents        = 1000
	FlapWindow       = 10 * time.Minute
)

// Config holds the correlation windows (INCIDENT_MERGE_WINDOW and
// INCIDENT_REOPEN_WINDOW) and the cap.
type Config struct {
	MergeWindow  time.Duration
	ReopenWindow time.Duration
	MaxOpen      int
	Retention    time.Duration // resolved incidents older than this are deleted
	DashboardURL string        // PUBLIC_DASHBOARD_URL, for incident links in payloads
}

// DefaultConfig is the documented default.
var DefaultConfig = Config{MergeWindow: 30 * time.Minute, ReopenWindow: 60 * time.Minute, MaxOpen: MaxOpenIncidents, Retention: 400 * 24 * time.Hour}

// Alert is one alert state entering firing, for a rule with open_incident.
type Alert struct {
	RuleID   string
	Key      string // the state key: "rule" or "fp:<hex>"...
	RuleName string
	// Services: the rule's filter plus, for group keys, where the group was
	// seen. Empty means a project-wide alert.
	Services []string
}

// ProjectWide reports whether the alert has no services.
func (a Alert) ProjectWide() bool { return len(a.Services) == 0 }

// Candidate is an incident the alert might join.
type Candidate struct {
	ID           string
	Number       int
	Status       Status
	Services     []string // empty: created by a project-wide rule
	LastActivity time.Time
	ResolvedAt   *time.Time
	// LinkedHere: this alert's rule and key are linked to the incident.
	LinkedHere bool
}

// Kind of decision.
type Kind string

const (
	Attach Kind = "attach"
	Reopen Kind = "reopen"
	Create Kind = "create"
	Skip   Kind = "skip"
)

// Decision is what to do with a firing alert.
type Decision struct {
	Kind         Kind
	IncidentID   string // attach and reopen
	RecurrenceOf string // create: the latest resolved incident of this rule and key, if any
	Reason       string // skip: why
	Rule         int    // which rule of ADR 0026 section 2 decided (1 to 5), for tests and logs
}

// Correlate decides, purely, which incident a firing alert joins.
//
//   - open: the project's non-resolved incidents.
//   - last: the latest incident this rule and key were ever linked to (any
//     status), or nil.
//   - openCount: how many non-resolved incidents the project has.
//
// The order is ADR 0026 section 2: (1) already linked, (2) service overlap
// within the merge window, (3) project-wide alerts join only project-wide
// incidents within the window, (4) reopen a recently resolved incident of
// this rule and key, (5) create, unless the project is at its cap.
func Correlate(a Alert, open []Candidate, last *Candidate, openCount int, now time.Time, cfg Config) Decision {
	if cfg.MaxOpen <= 0 {
		cfg.MaxOpen = MaxOpenIncidents
	}
	// The most recently active candidate wins every tie.
	best := func(match func(Candidate) bool) *Candidate {
		var out *Candidate
		for i := range open {
			c := open[i]
			if c.Status == StatusResolved || !match(c) {
				continue
			}
			if out == nil || c.LastActivity.After(out.LastActivity) || (c.LastActivity.Equal(out.LastActivity) && c.Number > out.Number) {
				out = &open[i]
			}
		}
		return out
	}
	recent := func(c Candidate) bool { return now.Sub(c.LastActivity) <= cfg.MergeWindow }

	if c := best(func(c Candidate) bool { return c.LinkedHere }); c != nil {
		return Decision{Kind: Attach, IncidentID: c.ID, Rule: 1}
	}
	if !a.ProjectWide() {
		if c := best(func(c Candidate) bool { return recent(c) && overlaps(a.Services, c.Services) }); c != nil {
			return Decision{Kind: Attach, IncidentID: c.ID, Rule: 2}
		}
	} else if c := best(func(c Candidate) bool { return recent(c) && len(c.Services) == 0 }); c != nil {
		return Decision{Kind: Attach, IncidentID: c.ID, Rule: 3}
	}
	if openCount >= cfg.MaxOpen {
		return Decision{Kind: Skip, Reason: "open incident limit reached", Rule: 5}
	}
	if last != nil && last.Status == StatusResolved && last.ResolvedAt != nil && now.Sub(*last.ResolvedAt) <= cfg.ReopenWindow {
		return Decision{Kind: Reopen, IncidentID: last.ID, Rule: 4}
	}
	d := Decision{Kind: Create, Rule: 5}
	if last != nil && last.Status == StatusResolved {
		d.RecurrenceOf = last.ID
	}
	return d
}

func overlaps(a, b []string) bool {
	for _, s := range a {
		if slices.Contains(b, s) {
			return true
		}
	}
	return false
}

// Title is "<rule name> in <services>" (or the rule name), at most MaxTitle characters.
func Title(ruleName string, services []string) string {
	t := strings.TrimSpace(ruleName)
	if len(services) > 0 {
		t += " in " + strings.Join(services, ", ")
	}
	return Clip(t, MaxTitle)
}

// Clip cuts s to at most n characters (runes).
func Clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// MergeServices adds the alert's services to the incident's, sorted, without
// duplicates, at most MaxServices.
func MergeServices(have, add []string) []string {
	out := append([]string{}, have...)
	for _, s := range add {
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	if len(out) > MaxServices {
		out = out[:MaxServices]
	}
	return out
}

// Collapse reports whether a repeat alert_firing or alert_resolved for the
// same incident, rule, key and type within FlapWindow of the previous one
// only counts on that event instead of adding a new one.
func Collapse(prevAt time.Time, havePrev bool, now time.Time) bool {
	return havePrev && now.Sub(prevAt) <= FlapWindow
}

// Transition checks a status change asked by a person. Resolve from open is
// allowed (no acknowledgement needed); reopening applies to resolved ones.
func Transition(from Status, action string) (Status, bool) {
	switch action {
	case "acknowledge":
		return StatusAcknowledged, from == StatusOpen
	case "resolve":
		return StatusResolved, from == StatusOpen || from == StatusAcknowledged
	case "reopen":
		return StatusOpen, from == StatusResolved
	}
	return from, false
}
