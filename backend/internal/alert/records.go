package alert

import (
	"errors"
	"time"
)

// Channel kinds.
const (
	KindWebhook = "webhook"
	KindSlack   = "slack"
)

// Channel is a notification channel as the API shows it: never the URL's
// path or query, never the secret.
type Channel struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Kind          string    `json:"kind"`
	URLHost       string    `json:"url_host"` // scheme and host only
	IncludeSample bool      `json:"include_sample"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// SealedChannel is a channel with its sealed URL and secret (AES-GCM).
type SealedChannel struct {
	Channel
	ProjectID string
	URL       []byte
	Secret    []byte // nil for slack
}

// EventRecord is one alert_events row.
type EventRecord struct {
	ID          int64     `json:"id"`
	RuleID      *string   `json:"rule_id"`
	RuleName    string    `json:"rule_name"`
	RuleType    string    `json:"rule_type"`
	DedupKey    string    `json:"dedup_key"`
	From        string    `json:"from"`
	To          string    `json:"to"`
	Value       float64   `json:"value"`
	Threshold   float64   `json:"threshold"`
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`
	Fingerprint string    `json:"fingerprint"` // 16 hex, empty for rule-level events
	Template    string    `json:"template"`
	Reason      string    `json:"reason"`
	At          time.Time `json:"at"`
}

// StateRecord is a current pending or firing state.
type StateRecord struct {
	RuleID      string     `json:"rule_id"`
	RuleName    string     `json:"rule_name"`
	DedupKey    string     `json:"dedup_key"`
	State       string     `json:"state"`
	Since       time.Time  `json:"since"`
	Value       float64    `json:"value"`
	FiredAt     *time.Time `json:"fired_at"`
	Fingerprint string     `json:"fingerprint"`
	Template    string     `json:"template"`
}

// Summary is a rule's current state for the dashboard: firing when any key
// fires, else pending when any is pending, else the latest transition's
// target state (ok when there is none). LastChangeAt is that transition.
type Summary struct {
	State        string
	Firing       int
	Pending      int
	LastChangeAt *time.Time
	// The evaluator's heartbeat (K2.2).
	LastEvaluatedAt *time.Time
	LastEvalError   string
}

// NotificationRecord is one delivery in the outbox history.
type NotificationRecord struct {
	ID             int64      `json:"id"`
	ChannelID      *string    `json:"channel_id"`
	ChannelName    string     `json:"channel_name"`
	RuleID         string     `json:"rule_id"`   // empty for tests and deleted rules
	RuleName       string     `json:"rule_name"` // empty for tests
	EventID        *int64     `json:"event_id"`
	Event          string     `json:"event"`
	Status         string     `json:"status"`
	Attempts       int        `json:"attempts"`
	ResponseStatus *int       `json:"response_status"`
	ErrorCategory  string     `json:"error_category"` // one of the Err* categories, or empty
	Test           bool       `json:"test"`
	IdempotencyKey string     `json:"idempotency_key"`
	CreatedAt      time.Time  `json:"created_at"`
	NextAttemptAt  time.Time  `json:"next_attempt_at"`
	SentAt         *time.Time `json:"sent_at"`
}

// Outbox row statuses.
const (
	OutPending   = "pending"
	OutSending   = "sending"
	OutSent      = "sent"
	OutFailed    = "failed"
	OutCancelled = "cancelled"
)

// NewNotification is an outbox row to insert with an outcome. Transition is
// the index of its transition in Outcome.Transitions, or -1.
type NewNotification struct {
	ChannelID      string
	ChannelName    string
	IdempotencyKey string
	Event          string
	Payload        []byte
	Transition     int
	Key            string // the state key (to find a repeat's incident)
}

// Claimed is an outbox row a worker owns until its lease ends.
type Claimed struct {
	ID             int64
	ProjectID      string
	Event          string
	IdempotencyKey string
	Payload        []byte
	Attempts       int // before this attempt
	Test           bool
	Channel        SealedChannel
}

// ErrStale means the rule or a state changed under the evaluator (another
// writer, or an edit): nothing was written; the next cycle starts over.
var ErrStale = errors.New("alert: rule or state changed concurrently")

// DeliveryResult is how a delivery attempt ended.
type DeliveryResult struct {
	Status         string // sent, pending (retry or deferral) or failed
	ResponseStatus *int
	ErrorCategory  string // a closed category, never text from the receiver
	NextAttemptAt  time.Time
	CountAttempt   bool // false for a rate-limit deferral
}
