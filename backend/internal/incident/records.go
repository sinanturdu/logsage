package incident

import (
	"encoding/json"
	"time"
)

// Incident is one incident as the API returns it.
type Incident struct {
	ID                string     `json:"id"`
	Number            int        `json:"number"`
	Title             string     `json:"title"`
	Severity          Severity   `json:"severity"`
	Status            Status     `json:"status"`
	Source            string     `json:"source"`
	Assignee          *string    `json:"assignee"`
	AssigneeEmail     string     `json:"assignee_email,omitempty"`
	Summary           string     `json:"summary"`
	Services          []string   `json:"services"`
	OpenedAt          time.Time  `json:"opened_at"`
	AcknowledgedAt    *time.Time `json:"acknowledged_at"`
	AcknowledgedBy    *string    `json:"acknowledged_by"`
	ResolvedAt        *time.Time `json:"resolved_at"`
	ResolvedBy        *string    `json:"resolved_by"`
	LastActivityAt    time.Time  `json:"last_activity_at"`
	RecurrenceOf      *int       `json:"recurrence_of"` // the earlier incident's number
	AllAlertsResolved bool       `json:"all_alerts_resolved"`
	EventsDropped     int        `json:"events_dropped"`
	Version           int        `json:"version"`
	ProjectID         string     `json:"-"`
}

// LinkedAlert is an alert state linked to an incident.
type LinkedAlert struct {
	RuleID      *string    `json:"rule_id"` // null after the rule was deleted
	RuleName    string     `json:"rule_name"`
	StateKey    string     `json:"state_key"`
	Services    []string   `json:"services"`
	LinkedAt    time.Time  `json:"linked_at"`
	ClosedAt    *time.Time `json:"closed_at"`
	CloseReason string     `json:"close_reason"`
	// State is the alert state now (pending, firing, ok or resolved), empty
	// when the rule or state is gone.
	State string `json:"state"`
}

// LinkedGroup is an error group linked to an incident.
type LinkedGroup struct {
	Fingerprint string    `json:"fingerprint"` // 16 hex
	Template    string    `json:"template"`
	Source      string    `json:"source"` // auto or manual
	LinkedAt    time.Time `json:"linked_at"`
	LinkedBy    *string   `json:"linked_by"`
}

// Event is one timeline entry. Body is set for comments only (null once
// deleted); Payload follows the type's schema.
type Event struct {
	ID         int64           `json:"id"`
	Type       EventType       `json:"type"`
	Actor      *string         `json:"actor"` // null: the system
	ActorEmail string          `json:"actor_email,omitempty"`
	At         time.Time       `json:"at"`
	LastAt     time.Time       `json:"last_at"`
	Count      int             `json:"count"`
	Payload    json.RawMessage `json:"payload"`
	Body       *string         `json:"body"`
}

// New is a manual incident.
type New struct {
	Title    string
	Severity Severity
	Summary  string
	Services []string
	Assignee string // empty: nobody
}

// Patch changes the editable fields; nil leaves a field as it is.
type Patch struct {
	Title    *string
	Severity *Severity
	Summary  *string
}

// Filter of the incident list.
type Filter struct {
	Status   []Status
	Severity []Severity
	Assignee string // "", "me" resolved by the caller to a user id, or "none"
	Service  string
	From, To *time.Time // opened_at range
	Q        string     // title contains
}

// Summary counts incidents per status.
type Summary struct {
	Open         int `json:"open"`
	Acknowledged int `json:"acknowledged"`
	Resolved     int `json:"resolved"`
}
