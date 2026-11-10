package incident

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// EventType is a timeline event type from the closed list of ADR 0026.
type EventType string

const (
	EvOpened            EventType = "opened"
	EvAlertFiring       EventType = "alert_firing"
	EvAlertResolved     EventType = "alert_resolved"
	EvAlertClosed       EventType = "alert_closed"
	EvAllAlertsResolved EventType = "all_alerts_resolved"
	EvGroupLinked       EventType = "group_linked"
	EvGroupUnlinked     EventType = "group_unlinked"
	EvStatusChanged     EventType = "status_changed"
	EvSeverityChanged   EventType = "severity_changed"
	EvAssigned          EventType = "assigned"
	EvComment           EventType = "comment"
	EvCommentDeleted    EventType = "comment_deleted"
	EvReopened          EventType = "reopened"
)

// EventTypes is the closed list (the database checks it too).
var EventTypes = []EventType{EvOpened, EvAlertFiring, EvAlertResolved, EvAlertClosed, EvAllAlertsResolved, EvGroupLinked, EvGroupUnlinked,
	EvStatusChanged, EvSeverityChanged, EvAssigned, EvComment, EvCommentDeleted, EvReopened}

// Payloads: ids, numbers, enums, rule names and normalized templates only,
// never a raw log message or a sample.
type (
	OpenedPayload struct {
		Source       string `json:"source"` // auto or manual
		RuleID       string `json:"rule_id,omitempty"`
		RuleName     string `json:"rule_name,omitempty"`
		RecurrenceOf int    `json:"recurrence_of,omitempty"` // the earlier incident's number
	}
	AlertPayload struct {
		RuleID    string  `json:"rule_id"`
		RuleName  string  `json:"rule_name"`
		RuleType  string  `json:"rule_type"`
		Key       string  `json:"key"`
		Value     float64 `json:"value"`
		Threshold float64 `json:"threshold"`
		// Group keys: the 16-hex fingerprint and its normalized template.
		Fingerprint string `json:"fingerprint,omitempty"`
		Template    string `json:"template,omitempty"`
	}
	AlertClosedPayload struct {
		RuleID   string `json:"rule_id"`
		RuleName string `json:"rule_name"`
		Key      string `json:"key"`
		Reason   string `json:"reason"` // rule_disabled, rule_deleted or rule_changed
	}
	EmptyPayload struct{}
	GroupPayload struct {
		Fingerprint string `json:"fingerprint"`
		Template    string `json:"template,omitempty"`
		Source      string `json:"source"` // auto or manual
	}
	StatusPayload struct {
		From Status `json:"from"`
		To   Status `json:"to"`
	}
	SeverityPayload struct {
		From Severity `json:"from"`
		To   Severity `json:"to"`
	}
	AssignedPayload struct {
		UserID string `json:"user_id,omitempty"` // empty: unassigned
	}
	CommentPayload struct {
		CommentID int64 `json:"comment_id,omitempty"` // comment_deleted: the deleted comment's event id
	}
	ReopenedPayload struct {
		RuleID string `json:"rule_id,omitempty"` // empty: reopened by a person
	}
)

// Close reasons of alert_closed.
var CloseReasons = []string{"rule_disabled", "rule_deleted", "rule_changed"}

func payloadFor(t EventType) (any, bool) {
	switch t {
	case EvOpened:
		return &OpenedPayload{}, true
	case EvAlertFiring, EvAlertResolved:
		return &AlertPayload{}, true
	case EvAlertClosed:
		return &AlertClosedPayload{}, true
	case EvAllAlertsResolved:
		return &EmptyPayload{}, true
	case EvGroupLinked, EvGroupUnlinked:
		return &GroupPayload{}, true
	case EvStatusChanged:
		return &StatusPayload{}, true
	case EvSeverityChanged:
		return &SeverityPayload{}, true
	case EvAssigned:
		return &AssignedPayload{}, true
	case EvComment, EvCommentDeleted:
		return &CommentPayload{}, true
	case EvReopened:
		return &ReopenedPayload{}, true
	}
	return nil, false
}

// ErrSchema is a payload that does not match its type's schema.
var ErrSchema = errors.New("incident: event payload does not match its schema")

// EncodePayload checks that p is the schema of t and encodes it.
func EncodePayload(t EventType, p any) ([]byte, error) {
	want, ok := payloadFor(t)
	if !ok {
		return nil, fmt.Errorf("%w: unknown type %q", ErrSchema, t)
	}
	if p == nil {
		return nil, fmt.Errorf("%w: no payload for %s", ErrSchema, t)
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	if reflect.TypeOf(want).Elem() != reflect.Indirect(reflect.ValueOf(p)).Type() {
		return nil, fmt.Errorf("%w: %T for %s", ErrSchema, p, t)
	}
	return b, nil
}

// DecodePayload decodes a stored payload strictly: unknown fields and
// unknown types are errors.
func DecodePayload(t EventType, raw []byte) (any, error) {
	p, ok := payloadFor(t)
	if !ok {
		return nil, fmt.Errorf("%w: unknown type %q", ErrSchema, t)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(p); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSchema, err)
	}
	return p, nil
}
