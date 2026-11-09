package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sinanturdu/logsage/backend/internal/alert"
	"github.com/sinanturdu/logsage/backend/internal/alert/outbound"
	"github.com/sinanturdu/logsage/backend/internal/alert/secret"
	"github.com/sinanturdu/logsage/backend/internal/storage/postgres"
)

// Alerts endpoints (ADR 0024): rules and channels; viewers read, owners
// and admins write; every write leaves an audit row. Channel URLs and
// secrets are sealed here before they reach the store, and a webhook
// secret is returned only by create and rotate.

// AlertStore is what the alert endpoints need from storage.
type AlertStore interface {
	CreateChannel(ctx context.Context, c alert.SealedChannel, userID string, a postgres.AuditEntry) (alert.Channel, error)
	ListChannels(ctx context.Context, projectID string) ([]alert.Channel, error)
	Channel(ctx context.Context, projectID, id string) (alert.Channel, error)
	UpdateChannel(ctx context.Context, projectID, id string, u postgres.ChannelUpdate, a postgres.AuditEntry) (alert.Channel, error)
	DeleteChannel(ctx context.Context, projectID, id string, a postgres.AuditEntry) error
	QueueTestNotification(ctx context.Context, projectID string, ch alert.Channel, key string, payload []byte, now time.Time, a postgres.AuditEntry) (int64, error)
	CreateRule(ctx context.Context, r alert.Rule, userID string, a postgres.AuditEntry) (alert.Rule, error)
	ListRules(ctx context.Context, projectID string) ([]alert.Rule, error)
	Rule(ctx context.Context, projectID, id string) (alert.Rule, error)
	UpdateRule(ctx context.Context, r alert.Rule, a postgres.AuditEntry) (alert.Rule, error)
	SetRuleEnabled(ctx context.Context, projectID, id string, enabled bool, close postgres.Closer, a postgres.AuditEntry) (alert.Rule, error)
	DeleteRule(ctx context.Context, projectID, id string, close postgres.Closer, a postgres.AuditEntry) error
	AlertStates(ctx context.Context, projectID string) ([]alert.StateRecord, error)
	AlertEvents(ctx context.Context, projectID, ruleID string, before int64, limit int) ([]alert.EventRecord, error)
	Notifications(ctx context.Context, projectID, channelID, ruleID, status string, before int64, limit int) ([]alert.NotificationRecord, error)
}

// URLChecker validates webhook URLs (outbound.Client in production).
type URLChecker interface {
	Validate(ctx context.Context, raw string) error
}

// AlertsOptions turns the alert endpoints on. Nil, or a key error, makes
// them answer 503 with the reason.
type AlertsOptions struct {
	Store        AlertStore
	Sealer       *secret.Sealer
	URLs         URLChecker
	KeyErr       error  // ALERTS_SECRET_KEY missing or malformed
	DashboardURL string // PUBLIC_DASHBOARD_URL
}

const (
	alertsDisabled = "alerts are disabled: set ALERTS_ENABLED=true and ALERTS_SECRET_KEY"
	maxChannelName = 100
)

// alertsReady answers 503 when alerts cannot work and reports whether to go on.
func (s *Server) alertsReady(w http.ResponseWriter) bool {
	a := s.opts.Alerts
	switch {
	case a == nil:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": alertsDisabled})
		return false
	case a.KeyErr != nil:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "alerts are not configured: " + a.KeyErr.Error()})
		return false
	}
	return true
}

// alertMember checks alerts are on, then membership and role.
func (s *Server) alertMember(w http.ResponseWriter, r *http.Request, sess postgres.Session, act action) (postgres.Membership, bool) {
	m, ok := s.member(w, r, sess, act)
	if !ok {
		return m, false
	}
	return m, s.alertsReady(w)
}

func (s *Server) alertStoreError(w http.ResponseWriter, op string, err error) {
	var rl *postgres.RateLimitError
	switch {
	case errors.As(err, &rl):
		w.Header().Set("Retry-After", strconv.Itoa(int(rl.RetryAfter.Seconds())))
		writeError(w, http.StatusTooManyRequests)
	case errors.Is(err, postgres.ErrLimit):
		writeJSON(w, http.StatusConflict, map[string]string{"error": limitMessage(err)})
	case errors.Is(err, postgres.ErrBadChannel):
		writeInvalid(w, "channels: unknown channel for this project")
	case errors.Is(err, alert.ErrStale):
		writeError(w, http.StatusConflict)
	default:
		s.storeError(w, op, err)
	}
}

func limitMessage(err error) string {
	if strings.HasPrefix(err.Error(), "channels") {
		return "channels: at most " + strconv.Itoa(alert.MaxChannelsPerProject) + " per project"
	}
	return "rules: at most " + strconv.Itoa(alert.MaxRulesPerProject) + " per project"
}

// --- rules ---

type ruleJSON struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Note       string       `json:"note"`
	Type       alert.Type   `json:"type"`
	Params     alert.Params `json:"params"`
	Interval   string       `json:"interval"`
	Window     string       `json:"window"`
	For        string       `json:"for"`
	ClearFor   string       `json:"clear_for"`
	Repeat     string       `json:"repeat"`
	Enabled    bool         `json:"enabled"`
	Channels   []string     `json:"channels"`
	Version    int          `json:"version"`
	NextEvalAt time.Time    `json:"next_eval_at"`
	// Current state summary (ADR 0024 section 8): ok, pending, firing or
	// resolved; the dashboard shows disabled rules from enabled.
	State        string     `json:"state"`
	Firing       int        `json:"firing"`
	Pending      int        `json:"pending"`
	LastChangeAt *time.Time `json:"last_change_at"`
}

func toRuleJSON(r alert.Rule) ruleJSON {
	ch := r.Channels
	if ch == nil {
		ch = []string{}
	}
	out := ruleJSON{ID: r.ID, Name: r.Name, Note: r.Note, Type: r.Type, Params: r.Params, Interval: r.Interval.String(), Window: r.Window.String(),
		For: r.For.String(), ClearFor: r.ClearFor.String(), Repeat: r.Repeat.String(), Enabled: r.Enabled, Channels: ch, Version: r.Version,
		NextEvalAt: r.NextEvalAt.UTC(), State: r.Summary.State, Firing: r.Summary.Firing, Pending: r.Summary.Pending}
	if out.State == "" {
		out.State = string(alert.StateOK)
	}
	if r.Summary.LastChangeAt != nil {
		t := r.Summary.LastChangeAt.UTC()
		out.LastChangeAt = &t
	}
	return out
}

type ruleInput struct {
	Name     string       `json:"name"`
	Note     string       `json:"note"`
	Type     alert.Type   `json:"type"`
	Params   alert.Params `json:"params"`
	Interval string       `json:"interval"`
	Window   string       `json:"window"`
	For      string       `json:"for"`
	ClearFor string       `json:"clear_for"`
	Repeat   string       `json:"repeat"`
	Enabled  *bool        `json:"enabled"`
	Channels []string     `json:"channels"`
}

func duration(name, v string, dst *time.Duration) error {
	if v == "" {
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || len(v) > 20 {
		return &alert.ValidationError{Msg: name + ": must be a duration such as 30s, 5m or 1h"}
	}
	*dst = d
	return nil
}

// rule turns the input into a validated rule.
func (in ruleInput) rule(projectID string) (alert.Rule, error) {
	r := alert.Rule{ProjectID: projectID, Name: in.Name, Note: in.Note, Type: in.Type, Params: in.Params, Enabled: true, Channels: in.Channels,
		Interval: time.Minute, Window: 5 * time.Minute}
	if in.Enabled != nil {
		r.Enabled = *in.Enabled
	}
	for _, d := range []struct {
		name, v string
		dst     *time.Duration
	}{{"interval", in.Interval, &r.Interval}, {"window", in.Window, &r.Window}, {"for", in.For, &r.For}, {"clear_for", in.ClearFor, &r.ClearFor}, {"repeat", in.Repeat, &r.Repeat}} {
		if err := duration(d.name, d.v, d.dst); err != nil {
			return alert.Rule{}, err
		}
	}
	if len(r.Channels) == 0 {
		return alert.Rule{}, &alert.ValidationError{Msg: "channels: at least one channel is required"}
	}
	for _, c := range r.Channels {
		if len(c) > 64 {
			return alert.Rule{}, &alert.ValidationError{Msg: "channels: unknown channel for this project"}
		}
	}
	r.Normalize()
	return r, alert.Validate(r)
}

func (s *Server) handleListRules(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, ok := s.alertMember(w, r, sess, actView)
	if !ok {
		return
	}
	rules, err := s.opts.Alerts.Store.ListRules(r.Context(), m.ID)
	if err != nil {
		s.alertStoreError(w, "list rules", err)
		return
	}
	out := make([]ruleJSON, 0, len(rules))
	for _, rl := range rules {
		out = append(out, toRuleJSON(rl))
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": out})
}

func (s *Server) handleGetRule(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, ok := s.alertMember(w, r, sess, actView)
	if !ok {
		return
	}
	rl, err := s.opts.Alerts.Store.Rule(r.Context(), m.ID, r.PathValue("id"))
	if err != nil {
		s.alertStoreError(w, "rule", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rule": toRuleJSON(rl)})
}

func (s *Server) handleCreateRule(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, ok := s.alertMember(w, r, sess, actManageAlerts)
	if !ok {
		return
	}
	var in ruleInput
	if !decode(w, r, &in) {
		return
	}
	rl, err := in.rule(m.ID)
	if err != nil {
		writeInvalid(w, err.Error())
		return
	}
	out, err := s.opts.Alerts.Store.CreateRule(r.Context(), rl, sess.UserID,
		postgres.AuditEntry{UserID: sess.UserID, Action: "alert_rule.create", Target: m.Slug, Detail: string(rl.Type)})
	if err != nil {
		s.alertStoreError(w, "create rule", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"rule": toRuleJSON(out)})
}

func (s *Server) handleUpdateRule(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, ok := s.alertMember(w, r, sess, actManageAlerts)
	if !ok {
		return
	}
	var in ruleInput
	if !decode(w, r, &in) {
		return
	}
	old, err := s.opts.Alerts.Store.Rule(r.Context(), m.ID, r.PathValue("id"))
	if err != nil {
		s.alertStoreError(w, "rule", err)
		return
	}
	rl, err := in.rule(m.ID)
	if err != nil {
		writeInvalid(w, err.Error())
		return
	}
	rl.ID = old.ID
	out, err := s.opts.Alerts.Store.UpdateRule(r.Context(), rl,
		postgres.AuditEntry{UserID: sess.UserID, Action: "alert_rule.update", Target: m.Slug + "/" + old.ID, Detail: string(rl.Type)})
	if err != nil {
		s.alertStoreError(w, "update rule", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rule": toRuleJSON(out)})
}

func (s *Server) closer(reason string) postgres.Closer {
	return postgres.Closer(alert.FinalNotifications(reason, s.opts.Alerts.DashboardURL, func() time.Time { return s.opts.Now().UTC() }))
}

func (s *Server) handleEnableRule(enabled bool) func(http.ResponseWriter, *http.Request, postgres.Session) {
	return func(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
		m, ok := s.alertMember(w, r, sess, actManageAlerts)
		if !ok {
			return
		}
		act := "alert_rule.enable"
		if !enabled {
			act = "alert_rule.disable"
		}
		out, err := s.opts.Alerts.Store.SetRuleEnabled(r.Context(), m.ID, r.PathValue("id"), enabled, s.closer(alert.ReasonRuleDisabled),
			postgres.AuditEntry{UserID: sess.UserID, Action: act, Target: m.Slug + "/" + r.PathValue("id")})
		if err != nil {
			s.alertStoreError(w, act, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"rule": toRuleJSON(out)})
	}
}

func (s *Server) handleDeleteRule(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, ok := s.alertMember(w, r, sess, actManageAlerts)
	if !ok {
		return
	}
	if err := s.opts.Alerts.Store.DeleteRule(r.Context(), m.ID, r.PathValue("id"), s.closer(alert.ReasonRuleDeleted),
		postgres.AuditEntry{UserID: sess.UserID, Action: "alert_rule.delete", Target: m.Slug + "/" + r.PathValue("id")}); err != nil {
		s.alertStoreError(w, "delete rule", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- channels ---

type channelInput struct {
	Name          *string `json:"name"`
	Kind          string  `json:"kind"`
	URL           *string `json:"url"`
	IncludeSample *bool   `json:"include_sample"`
}

func validName(n string) bool {
	n = strings.TrimSpace(n)
	return n != "" && utf8.RuneCountInString(n) <= maxChannelName && utf8.ValidString(n)
}

// urlError is the 400 message for a refused webhook URL (fixed strings).
func urlError(err error) string {
	for _, known := range []error{outbound.ErrScheme, outbound.ErrUser, outbound.ErrFrag, outbound.ErrPort, outbound.ErrHost, outbound.ErrAddress, outbound.ErrResolve} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return outbound.ErrURL.Error()
}

func urlHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

func (s *Server) checkURL(w http.ResponseWriter, r *http.Request, raw string) bool {
	if err := s.opts.Alerts.URLs.Validate(r.Context(), raw); err != nil {
		writeInvalid(w, urlError(err))
		return false
	}
	return true
}

func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, ok := s.alertMember(w, r, sess, actView)
	if !ok {
		return
	}
	chs, err := s.opts.Alerts.Store.ListChannels(r.Context(), m.ID)
	if err != nil {
		s.alertStoreError(w, "list channels", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": chs})
}

func (s *Server) handleGetChannel(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, ok := s.alertMember(w, r, sess, actView)
	if !ok {
		return
	}
	ch, err := s.opts.Alerts.Store.Channel(r.Context(), m.ID, r.PathValue("id"))
	if err != nil {
		s.alertStoreError(w, "channel", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"channel": ch})
}

func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, ok := s.alertMember(w, r, sess, actManageAlerts)
	if !ok {
		return
	}
	var in channelInput
	if !decode(w, r, &in) {
		return
	}
	switch {
	case in.Name == nil || !validName(*in.Name):
		writeInvalid(w, "name: 1 to 100 characters")
		return
	case in.Kind != alert.KindWebhook && in.Kind != alert.KindSlack:
		writeInvalid(w, "kind: must be webhook or slack")
		return
	case in.URL == nil:
		writeInvalid(w, "url: required")
		return
	}
	if !s.checkURL(w, r, *in.URL) {
		return
	}
	id, err := postgres.NewID()
	if err != nil {
		s.storeError(w, "channel id", err)
		return
	}
	sealer := s.opts.Alerts.Sealer
	sealedURL, err := sealer.Seal([]byte(*in.URL), secret.AD(m.ID, id, "url"))
	if err != nil {
		s.storeError(w, "seal", err)
		return
	}
	c := alert.SealedChannel{Channel: alert.Channel{ID: id, Name: strings.TrimSpace(*in.Name), Kind: in.Kind, URLHost: urlHost(*in.URL)},
		ProjectID: m.ID, URL: sealedURL}
	if in.IncludeSample != nil {
		c.IncludeSample = *in.IncludeSample
	}
	var plainSecret string
	if in.Kind == alert.KindWebhook {
		if plainSecret, err = secret.NewWebhookSecret(); err == nil {
			c.Secret, err = sealer.Seal([]byte(plainSecret), secret.AD(m.ID, id, "secret"))
		}
		if err != nil {
			s.storeError(w, "secret", err)
			return
		}
	}
	out, err := s.opts.Alerts.Store.CreateChannel(r.Context(), c, sess.UserID,
		postgres.AuditEntry{UserID: sess.UserID, Action: "channel.create", Target: m.Slug + "/" + id, Detail: in.Kind + " " + c.URLHost})
	if err != nil {
		s.alertStoreError(w, "create channel", err)
		return
	}
	body := map[string]any{"channel": out}
	if plainSecret != "" {
		body["secret"] = plainSecret // shown this once; stored sealed
	}
	writeJSON(w, http.StatusCreated, body)
}

func (s *Server) handleUpdateChannel(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, ok := s.alertMember(w, r, sess, actManageAlerts)
	if !ok {
		return
	}
	var in channelInput
	if !decode(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	cur, err := s.opts.Alerts.Store.Channel(r.Context(), m.ID, id)
	if err != nil {
		s.alertStoreError(w, "channel", err)
		return
	}
	if in.Kind != "" && in.Kind != cur.Kind {
		writeInvalid(w, "kind: cannot be changed; create a new channel")
		return
	}
	u := postgres.ChannelUpdate{IncludeSample: in.IncludeSample}
	if in.Name != nil {
		if !validName(*in.Name) {
			writeInvalid(w, "name: 1 to 100 characters")
			return
		}
		n := strings.TrimSpace(*in.Name)
		u.Name = &n
	}
	if in.URL != nil {
		if !s.checkURL(w, r, *in.URL) {
			return
		}
		sealed, err := s.opts.Alerts.Sealer.Seal([]byte(*in.URL), secret.AD(m.ID, id, "url"))
		if err != nil {
			s.storeError(w, "seal", err)
			return
		}
		host := urlHost(*in.URL)
		u.URL, u.URLHost = sealed, &host
	}
	out, err := s.opts.Alerts.Store.UpdateChannel(r.Context(), m.ID, id, u, postgres.AuditEntry{UserID: sess.UserID, Action: "channel.update", Target: m.Slug + "/" + id})
	if err != nil {
		s.alertStoreError(w, "update channel", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"channel": out})
}

func (s *Server) handleRotateSecret(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, ok := s.alertMember(w, r, sess, actManageAlerts)
	if !ok {
		return
	}
	id := r.PathValue("id")
	cur, err := s.opts.Alerts.Store.Channel(r.Context(), m.ID, id)
	if err != nil {
		s.alertStoreError(w, "channel", err)
		return
	}
	if cur.Kind != alert.KindWebhook {
		writeInvalid(w, "kind: only webhook channels have a signing secret")
		return
	}
	plain, err := secret.NewWebhookSecret()
	if err != nil {
		s.storeError(w, "secret", err)
		return
	}
	sealed, err := s.opts.Alerts.Sealer.Seal([]byte(plain), secret.AD(m.ID, id, "secret"))
	if err != nil {
		s.storeError(w, "seal", err)
		return
	}
	out, err := s.opts.Alerts.Store.UpdateChannel(r.Context(), m.ID, id, postgres.ChannelUpdate{Secret: sealed},
		postgres.AuditEntry{UserID: sess.UserID, Action: "channel.rotate", Target: m.Slug + "/" + id})
	if err != nil {
		s.alertStoreError(w, "rotate secret", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"channel": out, "secret": plain})
}

func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, ok := s.alertMember(w, r, sess, actManageAlerts)
	if !ok {
		return
	}
	if err := s.opts.Alerts.Store.DeleteChannel(r.Context(), m.ID, r.PathValue("id"),
		postgres.AuditEntry{UserID: sess.UserID, Action: "channel.delete", Target: m.Slug + "/" + r.PathValue("id")}); err != nil {
		s.alertStoreError(w, "delete channel", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestChannel(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, ok := s.alertMember(w, r, sess, actManageAlerts)
	if !ok {
		return
	}
	id := r.PathValue("id")
	ch, err := s.opts.Alerts.Store.Channel(r.Context(), m.ID, id)
	if err != nil {
		s.alertStoreError(w, "channel", err)
		return
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		s.storeError(w, "test key", err)
		return
	}
	key := "test-" + hex.EncodeToString(b[:])
	now := s.opts.Now().UTC()
	payload, err := alert.TestPayload(m.Slug, ch, key, now)
	if err != nil {
		s.storeError(w, "test payload", err)
		return
	}
	nid, err := s.opts.Alerts.Store.QueueTestNotification(r.Context(), m.ID, ch, key, payload, now,
		postgres.AuditEntry{UserID: sess.UserID, Action: "channel.test", Target: m.Slug + "/" + id})
	if err != nil {
		s.alertStoreError(w, "test channel", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"notification_id": nid, "idempotency_key": key})
}

// --- history ---

func pageParams(w http.ResponseWriter, r *http.Request) (before int64, limit int, ok bool) {
	q := r.URL.Query()
	limit = 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > postgres.MaxHistory {
			writeInvalid(w, "limit: must be between 1 and 100")
			return 0, 0, false
		}
		limit = n
	}
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			writeInvalid(w, "before: must be a positive id from next_before")
			return 0, 0, false
		}
		before = n
	}
	return before, limit, true
}

func (s *Server) handleAlertStates(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, ok := s.alertMember(w, r, sess, actView)
	if !ok {
		return
	}
	st, err := s.opts.Alerts.Store.AlertStates(r.Context(), m.ID)
	if err != nil {
		s.alertStoreError(w, "states", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"states": st})
}

func (s *Server) handleAlertEvents(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, ok := s.alertMember(w, r, sess, actView)
	if !ok {
		return
	}
	before, limit, ok := pageParams(w, r)
	if !ok {
		return
	}
	rule := r.URL.Query().Get("rule")
	if len(rule) > 64 {
		writeInvalid(w, "rule: not a rule id")
		return
	}
	ev, err := s.opts.Alerts.Store.AlertEvents(r.Context(), m.ID, rule, before, limit)
	if err != nil {
		s.alertStoreError(w, "events", err)
		return
	}
	var next *int64
	if len(ev) == limit {
		next = &ev[len(ev)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": ev, "next_before": next})
}

var outboxStatuses = []string{alert.OutPending, alert.OutSending, alert.OutSent, alert.OutFailed, alert.OutCancelled}

func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, ok := s.alertMember(w, r, sess, actView)
	if !ok {
		return
	}
	before, limit, ok := pageParams(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	status, channel, rule := q.Get("status"), q.Get("channel"), q.Get("rule")
	if status != "" && !slices.Contains(outboxStatuses, status) {
		writeInvalid(w, "status: must be pending, sending, sent, failed or cancelled")
		return
	}
	if len(channel) > 64 {
		writeInvalid(w, "channel: not a channel id")
		return
	}
	if len(rule) > 64 {
		writeInvalid(w, "rule: not a rule id")
		return
	}
	ns, err := s.opts.Alerts.Store.Notifications(r.Context(), m.ID, channel, rule, status, before, limit)
	if err != nil {
		s.alertStoreError(w, "notifications", err)
		return
	}
	var next *int64
	if len(ns) == limit {
		next = &ns[len(ns)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifications": ns, "next_before": next})
}
