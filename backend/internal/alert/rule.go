package alert

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Type is what a rule measures (ADR 0024).
type Type string

const (
	ErrorRate        Type = "error_rate"
	LogVolume        Type = "log_volume"
	NewGroup         Type = "new_group"
	Regression       Type = "regression"
	NewGroupsPerHour Type = "new_groups_per_hour"
)

// Types lists the rule types in a stable order.
var Types = []Type{ErrorRate, LogVolume, NewGroup, Regression, NewGroupsPerHour}

// Limits (ADR 0024).
const (
	MaxRulesPerProject    = 50
	MaxChannelsPerProject = 10
	MaxChannelsPerRule    = 10
	MaxName               = 100
	MaxNote               = 500
	MaxServices           = 20
	MinInterval           = 30 * time.Second
	MaxInterval           = time.Hour
	MinWindow             = time.Minute
	MaxWindow             = 24 * time.Hour
	MaxFor                = 24 * time.Hour
	MaxClearFor           = time.Hour
	MinRepeat             = 5 * time.Minute
	MaxRepeat             = 7 * 24 * time.Hour
	DefaultHysteresis     = 10.0 // percent
)

// Params are the type-specific parameters; unused ones stay zero.
type Params struct {
	ThresholdPercent float64  `json:"threshold_percent,omitempty"`
	MinLogs          uint64   `json:"min_logs,omitempty"`
	Op               string   `json:"op,omitempty"` // log_volume: above or below
	Count            uint64   `json:"count,omitempty"`
	MinEvents        uint64   `json:"min_events,omitempty"`
	MaxGroups        uint64   `json:"max_groups,omitempty"`
	Services         []string `json:"services,omitempty"`
	Levels           []string `json:"levels,omitempty"`
	// HysteresisPercent widens the band the value must cross back before a
	// firing rule resolves (rate, volume and guard rules).
	HysteresisPercent *float64 `json:"hysteresis_percent,omitempty"`
}

// Rule is a stored alert rule.
type Rule struct {
	ID          string
	ProjectID   string
	ProjectSlug string
	Name        string
	Note        string
	Type        Type
	Params      Params
	Interval    time.Duration
	Window      time.Duration
	For         time.Duration
	ClearFor    time.Duration
	Repeat      time.Duration
	Enabled     bool
	Channels    []string
	// Version changes when what the rule measures changes (type, params,
	// window): states of an older version are reset.
	Version    int
	NextEvalAt time.Time
}

// Hysteresis returns the band in percent (default 10).
func (r Rule) Hysteresis() float64 {
	if r.Params.HysteresisPercent == nil {
		return DefaultHysteresis
	}
	return *r.Params.HysteresisPercent
}

// EffectiveWindow is the window used for queries: new_groups_per_hour is
// always one hour.
func (r Rule) EffectiveWindow() time.Duration {
	if r.Type == NewGroupsPerHour {
		return time.Hour
	}
	return r.Window
}

// IsGroupRule reports whether the rule keeps one state per group.
func (r Rule) IsGroupRule() bool { return r.Type == NewGroup || r.Type == Regression }

var levels = []string{"debug", "info", "warn", "error", "fatal"}

// Normalize fills defaults: min_logs 50, min_events 1, clear_for = interval.
func (r *Rule) Normalize() {
	r.Name = strings.TrimSpace(r.Name)
	r.Note = strings.TrimSpace(r.Note)
	if r.Type == ErrorRate && r.Params.MinLogs == 0 {
		r.Params.MinLogs = 50
	}
	if r.Type == NewGroup && r.Params.MinEvents == 0 {
		r.Params.MinEvents = 1
	}
	if r.ClearFor == 0 {
		r.ClearFor = min(r.Interval, MaxClearFor)
	}
	if r.Type == NewGroupsPerHour {
		r.Window = time.Hour
	}
	r.Params.Op = strings.ToLower(r.Params.Op)
}

// ValidationError is a 400 with a fixed, safe message.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, a ...any) error { return &ValidationError{Msg: fmt.Sprintf(format, a...)} }

// IsValidation reports whether err is a ValidationError.
func IsValidation(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}

// Validate checks a normalized rule against the limits.
func Validate(r Rule) error {
	switch n := utf8.RuneCountInString(r.Name); {
	case n == 0:
		return invalid("name: required")
	case n > MaxName:
		return invalid("name: at most %d characters", MaxName)
	}
	if utf8.RuneCountInString(r.Note) > MaxNote {
		return invalid("note: at most %d characters", MaxNote)
	}
	if !slices.Contains(Types, r.Type) {
		return invalid("type: must be one of error_rate, log_volume, new_group, regression, new_groups_per_hour")
	}
	if r.Interval < MinInterval || r.Interval > MaxInterval || r.Interval%time.Second != 0 {
		return invalid("interval: must be between 30s and 1h, in whole seconds")
	}
	if r.Window < MinWindow || r.Window > MaxWindow || r.Window%time.Second != 0 {
		return invalid("window: must be between 1m and 24h, in whole seconds")
	}
	if r.For < 0 || r.For > MaxFor || r.For%time.Second != 0 {
		return invalid("for: must be between 0 and 24h")
	}
	if r.ClearFor < 0 || r.ClearFor > MaxClearFor || r.ClearFor%time.Second != 0 {
		return invalid("clear_for: must be between 0 and 1h")
	}
	if r.Repeat != 0 && (r.Repeat < MinRepeat || r.Repeat > MaxRepeat || r.Repeat%time.Second != 0) {
		return invalid("repeat: must be 0 or between 5m and 7d")
	}
	if len(r.Channels) > MaxChannelsPerRule {
		return invalid("channels: at most %d", MaxChannelsPerRule)
	}
	if h := r.Hysteresis(); h < 0 || h > 50 {
		return invalid("hysteresis_percent: must be between 0 and 50")
	}
	p := r.Params
	if len(p.Services) > MaxServices {
		return invalid("services: at most %d", MaxServices)
	}
	for _, s := range p.Services {
		if s == "" || utf8.RuneCountInString(s) > 256 || !utf8.ValidString(s) {
			return invalid("services: each name must be 1 to 256 characters")
		}
	}
	for _, l := range p.Levels {
		if !slices.Contains(levels, l) {
			return invalid("levels: must be debug, info, warn, error or fatal")
		}
	}
	switch r.Type {
	case ErrorRate:
		if p.ThresholdPercent < 0.1 || p.ThresholdPercent > 100 {
			return invalid("threshold_percent: must be between 0.1 and 100")
		}
		if p.MinLogs < 10 || p.MinLogs > 1_000_000 {
			return invalid("min_logs: must be between 10 and 1000000")
		}
		if len(p.Levels) > 0 {
			return invalid("levels: not used by error_rate")
		}
	case LogVolume:
		if p.Op != "above" && p.Op != "below" {
			return invalid("op: must be above or below")
		}
		if p.Count > 1_000_000_000 || (p.Op == "below" && p.Count == 0) {
			return invalid("count: must be between 0 and 1000000000 (at least 1 for below)")
		}
	case NewGroup:
		if p.MinEvents < 1 || p.MinEvents > 1_000_000 {
			return invalid("min_events: must be between 1 and 1000000")
		}
		if len(p.Levels) > 0 {
			return invalid("levels: not used by new_group")
		}
	case Regression:
		if len(p.Levels) > 0 {
			return invalid("levels: not used by regression")
		}
	case NewGroupsPerHour:
		if p.MaxGroups < 1 || p.MaxGroups > 10_000 {
			return invalid("max_groups: must be between 1 and 10000")
		}
		if len(p.Services) > 0 || len(p.Levels) > 0 {
			return invalid("services: not used by new_groups_per_hour")
		}
	}
	return nil
}
