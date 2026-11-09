package alert

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sinanturdu/logsage/backend/internal/fingerprint"
)

// PayloadVersion is the version of the JSON body sent to channels.
const PayloadVersion = 1

// Reasons a resolved notification gives when the rule ended it.
const (
	ReasonRuleDisabled = "rule_disabled"
	ReasonRuleDeleted  = "rule_deleted"
)

// Payload is the versioned JSON body of a notification (ADR 0024). It never
// holds a secret, a webhook URL or, unless the channel opted in, a raw log
// message.
type Payload struct {
	Version        int           `json:"version"`
	Event          string        `json:"event"`
	IdempotencyKey string        `json:"idempotency_key"`
	CreatedAt      time.Time     `json:"created_at"`
	Test           bool          `json:"test"`
	Project        string        `json:"project"`
	Rule           PayloadRule   `json:"rule"`
	State          string        `json:"state"`
	Reason         string        `json:"reason,omitempty"`
	Value          float64       `json:"value"`
	Threshold      float64       `json:"threshold"`
	Unit           string        `json:"unit"`
	Window         PayloadWindow `json:"window"`
	FiredAt        time.Time     `json:"fired_at"`
	ResolvedAt     *time.Time    `json:"resolved_at"`
	Repeat         int           `json:"repeat"`
	Group          *PayloadGroup `json:"group,omitempty"`
	Sample         *string       `json:"sample"`
	URL            string        `json:"url,omitempty"`
}

type PayloadRule struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type PayloadWindow struct {
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	Seconds int       `json:"seconds"`
}

type PayloadGroup struct {
	Fingerprint string `json:"fingerprint"`
	Template    string `json:"template"`
}

// MaxSample is the longest sample message sent, in bytes.
const MaxSample = 1024

// Unit names what a rule's value counts.
func Unit(t Type) string {
	switch t {
	case ErrorRate:
		return "percent"
	case LogVolume:
		return "logs"
	case NewGroupsPerHour:
		return "groups"
	}
	return "events"
}

// IdempotencyKey identifies one notification of one alert on one channel;
// a redelivery carries the same key.
func IdempotencyKey(ruleID, dedupKey, event string, firedAt time.Time, repeat int, channelID string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%d\x00%s", ruleID, dedupKey, event, firedAt.UnixMilli(), repeat, channelID)))
	return hex.EncodeToString(h[:])
}

// DashboardLink points at the group (group rules) or the explorer over the
// window; empty without PUBLIC_DASHBOARD_URL.
func DashboardLink(base string, r Rule, win Window, fp uint64) string {
	if base == "" {
		return ""
	}
	p := "/projects/" + url.PathEscape(r.ProjectSlug)
	q := url.Values{}
	q.Set("from", win.From.UTC().Format(time.RFC3339))
	q.Set("to", win.To.UTC().Format(time.RFC3339))
	if fp != 0 {
		return strings.TrimRight(base, "/") + p + "/groups/" + fingerprint.Hex(fp) + "?" + q.Encode()
	}
	if r.Type == ErrorRate {
		q.Set("level", "error,fatal")
	}
	return strings.TrimRight(base, "/") + p + "/logs?" + q.Encode()
}

func cutSample(s string) string {
	if len(s) <= MaxSample {
		return s
	}
	s = s[:MaxSample]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// Notifications builds one outbox row per channel for a notice.
func Notifications(r Rule, win Window, n Notice, channels []Channel, reason, dashboard string, now time.Time) ([]NewNotification, error) {
	out := make([]NewNotification, 0, len(channels))
	state := "firing"
	if n.Event == EventResolved {
		state = "resolved"
	}
	for _, ch := range channels {
		key := IdempotencyKey(r.ID, n.Key, n.Event, n.FiredAt, n.Repeat, ch.ID)
		p := Payload{
			Version: PayloadVersion, Event: n.Event, IdempotencyKey: key, CreatedAt: now.UTC(),
			Project: r.ProjectSlug, Rule: PayloadRule{ID: r.ID, Name: r.Name, Type: string(r.Type)},
			State: state, Reason: reason, Value: n.Value, Threshold: n.Threshold, Unit: Unit(r.Type),
			Window:  PayloadWindow{From: win.From.UTC(), To: win.To.UTC(), Seconds: int(win.To.Sub(win.From) / time.Second)},
			FiredAt: n.FiredAt.UTC(), ResolvedAt: n.ResolvedAt, Repeat: n.Repeat,
			URL: DashboardLink(dashboard, r, win, n.Fingerprint),
		}
		if n.Fingerprint != 0 {
			p.Group = &PayloadGroup{Fingerprint: fingerprint.Hex(n.Fingerprint), Template: n.Template}
		}
		if ch.IncludeSample && n.Sample != "" {
			s := cutSample(n.Sample)
			p.Sample = &s
		}
		body, err := json.Marshal(p)
		if err != nil {
			return nil, fmt.Errorf("alert: encode payload: %w", err)
		}
		out = append(out, NewNotification{ChannelID: ch.ID, ChannelName: ch.Name, IdempotencyKey: key, Event: n.Event, Payload: body, Transition: n.Transition})
	}
	return out, nil
}

// TestPayload is the body of a channel test send.
func TestPayload(projectSlug string, ch Channel, key string, now time.Time) ([]byte, error) {
	p := Payload{Version: PayloadVersion, Event: EventTest, IdempotencyKey: key, CreatedAt: now.UTC(), Test: true, Project: projectSlug,
		Rule: PayloadRule{Name: "Test notification from LogSage", Type: "test"}, State: "test", Unit: "events", FiredAt: now.UTC(),
		Window: PayloadWindow{From: now.UTC(), To: now.UTC()}}
	return json.Marshal(p)
}

// CloseNotices are the resolved notices for a rule's firing states when the
// rule is disabled or deleted: one per firing key, with the reason.
func CloseNotices(states []State, now time.Time) []Notice {
	var out []Notice
	for i, s := range states {
		if s.State != StateFiring {
			continue
		}
		fired := now
		if s.FiredAt != nil {
			fired = *s.FiredAt
		}
		at := now
		out = append(out, Notice{Key: s.DedupKey, Event: EventResolved, Value: s.LastValue, FiredAt: fired, ResolvedAt: &at,
			Repeat: s.NotifyCount, Fingerprint: s.Fingerprint, Template: s.Template, Transition: i})
	}
	return out
}

// FinalNotifications builds the closing alert.resolved notifications of a
// rule that is disabled or deleted while firing (passed to the store, which
// calls it inside its transaction).
func FinalNotifications(reason, dashboard string, now func() time.Time) func(Rule, []Channel, []Notice) ([]NewNotification, error) {
	return func(r Rule, channels []Channel, notices []Notice) ([]NewNotification, error) {
		var out []NewNotification
		at := now()
		for _, n := range notices {
			ns, err := Notifications(r, Window{From: at, To: at}, n, channels, reason, dashboard, at)
			if err != nil {
				return nil, err
			}
			out = append(out, ns...)
		}
		return out, nil
	}
}
