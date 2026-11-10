// Package api is the management API (cmd/api): login with server-side
// sessions, projects, membership roles and API keys. See ADR 0013.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sinanturdu/logsage/backend/internal/keys"
	"github.com/sinanturdu/logsage/backend/internal/password"
	"github.com/sinanturdu/logsage/backend/internal/storage/postgres"
)

const (
	maxBodyBytes = 16 << 10
	// DefaultTouchInterval bounds last_seen_at writes per session.
	DefaultTouchInterval = time.Minute
	maxKeyLifetime       = 10 * 365 * 24 * time.Hour
)

// Store is what the API needs from PostgreSQL (*postgres.Store).
type Store interface {
	CreateUser(ctx context.Context, email, passwordHash string) (postgres.User, error)
	UserByEmail(ctx context.Context, email string) (postgres.User, error)
	UserByID(ctx context.Context, id string) (postgres.User, error)
	UpdatePasswordHash(ctx context.Context, userID, oldHash, newHash string) error
	SetPassword(ctx context.Context, userID, newHash, keepSessionID string) error

	CreateSession(ctx context.Context, n postgres.NewSession) (string, error)
	LookupSession(ctx context.Context, tokenHash []byte) (postgres.Session, error)
	TouchSession(ctx context.Context, id string, now, idleExpiresAt time.Time) error
	RevokeSession(ctx context.Context, id string) error
	RevokeUserSessions(ctx context.Context, userID, exceptID string) error

	ProjectsForUser(ctx context.Context, userID string) ([]postgres.Membership, error)
	MembershipFor(ctx context.Context, userID, slug string) (postgres.Membership, error)
	CreateProjectWithOwner(ctx context.Context, name, slug, userID string) (postgres.Project, error)
	DisableProject(ctx context.Context, slug string) (postgres.Project, error)

	ListKeys(ctx context.Context, slug string) ([]postgres.KeyInfo, error)
	IssueKey(ctx context.Context, slug string, kind keys.Kind, name string, expiresAt *time.Time) (keys.Generated, postgres.KeyInfo, error)
	RevokeProjectKey(ctx context.Context, slug, prefix string) (postgres.KeyInfo, error)

	Audit(ctx context.Context, e postgres.AuditEntry) error

	// Error group status (ADR 0022); ErrNotFound means open.
	GroupStates(ctx context.Context, projectID string) ([]postgres.GroupState, error)
	GroupState(ctx context.Context, projectID string, fp uint64) (postgres.GroupState, error)
	SetGroupStatus(ctx context.Context, projectID string, fp uint64, status, note, userID string, audit postgres.AuditEntry) (postgres.GroupState, error)
}

// Options configure a Server.
type Options struct {
	IdleTTL             time.Duration
	AbsoluteTTL         time.Duration
	CookieSecure        bool
	AllowedOrigins      []string
	RegistrationEnabled bool
	TouchInterval       time.Duration // default DefaultTouchInterval
	// Logs serves the read endpoints; nil leaves them answering 404.
	Logs LogReader
	// Tail serves the live tail; nil leaves it answering 404.
	Tail TailReader
	// TailOptions configure the live tail (zero values take defaults).
	TailOptions TailOptions
	// MaxConcurrentQueries caps running log reads per user (default
	// DefaultMaxConcurrentQueries); above it the answer is 429.
	MaxConcurrentQueries int
	// Alerts turns the alert endpoints on (ADR 0024); nil answers 503.
	Alerts *AlertsOptions
	// Incidents serves the incident and members endpoints (ADR 0026); nil leaves them answering 404.
	Incidents IncidentStore
	Now    func() time.Time // default time.Now
}

// Server serves the management API.
type Server struct {
	store   Store
	hasher  *password.Hasher
	limiter *Limiter
	log     *slog.Logger
	opts    Options
	origins originChecker
	logs    LogReader
	gate    *queryGate

	tail         TailReader
	tailOpts     TailOptions
	tailGate     *tailGate
	resume       *resumeStore
	shutdown     chan struct{}
	shutdownOnce sync.Once
}

// New returns a Server.
func New(store Store, hasher *password.Hasher, limiter *Limiter, opts Options, log *slog.Logger) (*Server, error) {
	if store == nil || hasher == nil || limiter == nil || log == nil {
		return nil, errors.New("api: store, hasher, limiter and logger are required")
	}
	if opts.IdleTTL <= 0 || opts.AbsoluteTTL < opts.IdleTTL {
		return nil, errors.New("api: need 0 < idle TTL <= absolute TTL")
	}
	if opts.TouchInterval <= 0 {
		opts.TouchInterval = DefaultTouchInterval
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	tailOpts := opts.TailOptions.withDefaults()
	return &Server{store: store, hasher: hasher, limiter: limiter, log: log, opts: opts,
		origins: newOriginChecker(opts.AllowedOrigins), logs: opts.Logs, gate: newQueryGate(opts.MaxConcurrentQueries),
		tail: opts.Tail, tailOpts: tailOpts, tailGate: newTailGate(tailOpts),
		resume:   &resumeStore{max: 4 * tailOpts.MaxConnTotal, ttl: tailOpts.MaxReplay, states: map[string]*tailState{}},
		shutdown: make(chan struct{})}, nil
}

// Register adds the API routes to mux behind the security middleware.
func (s *Server) Register(mux *http.ServeMux) {
	m := http.NewServeMux()
	m.HandleFunc("POST /api/auth/login", s.handleLogin)
	m.HandleFunc("POST /api/auth/register", s.handleRegister)
	m.HandleFunc("POST /api/auth/logout", s.authed(s.handleLogout))
	m.HandleFunc("POST /api/auth/logout-all", s.authed(s.handleLogoutAll))
	m.HandleFunc("GET /api/auth/me", s.authed(s.handleMe))
	m.HandleFunc("POST /api/auth/change-password", s.authed(s.handleChangePassword))
	m.HandleFunc("GET /api/projects", s.authed(s.handleListProjects))
	m.HandleFunc("POST /api/projects", s.authed(s.handleCreateProject))
	m.HandleFunc("GET /api/projects/{slug}", s.authed(s.handleGetProject))
	m.HandleFunc("POST /api/projects/{slug}/disable", s.authed(s.handleDisableProject))
	m.HandleFunc("GET /api/projects/{slug}/keys", s.authed(s.handleListKeys))
	m.HandleFunc("POST /api/projects/{slug}/keys", s.authed(s.handleCreateKey))
	m.HandleFunc("POST /api/projects/{slug}/keys/{prefix}/revoke", s.authed(s.handleRevokeKey))
	m.HandleFunc("GET /api/projects/{slug}/logs", s.authed(s.readLogs(s.prepareLogs)))
	m.HandleFunc("GET /api/projects/{slug}/histogram", s.authed(s.readLogs(s.prepareHistogram)))
	m.HandleFunc("GET /api/projects/{slug}/facets", s.authed(s.readLogs(s.prepareFacets)))
	m.HandleFunc("GET /api/projects/{slug}/tail", s.authed(s.handleTail))
	m.HandleFunc("GET /api/projects/{slug}/groups", s.authed(s.readLogs(s.prepareGroups)))
	m.HandleFunc("GET /api/projects/{slug}/groups/{fingerprint}", s.authed(s.readLogs(s.prepareGroupDetail)))
	m.HandleFunc("POST /api/projects/{slug}/groups/{fingerprint}/status", s.authed(s.handleGroupStatus))
	a := "/api/projects/{slug}/alerts"
	m.HandleFunc("GET "+a+"/rules", s.authed(s.handleListRules))
	m.HandleFunc("POST "+a+"/rules", s.authed(s.handleCreateRule))
	m.HandleFunc("GET "+a+"/rules/{id}", s.authed(s.handleGetRule))
	m.HandleFunc("PUT "+a+"/rules/{id}", s.authed(s.handleUpdateRule))
	m.HandleFunc("DELETE "+a+"/rules/{id}", s.authed(s.handleDeleteRule))
	m.HandleFunc("POST "+a+"/rules/{id}/enable", s.authed(s.handleEnableRule(true)))
	m.HandleFunc("POST "+a+"/rules/{id}/disable", s.authed(s.handleEnableRule(false)))
	m.HandleFunc("GET "+a+"/channels", s.authed(s.handleListChannels))
	m.HandleFunc("POST "+a+"/channels", s.authed(s.handleCreateChannel))
	m.HandleFunc("GET "+a+"/channels/{id}", s.authed(s.handleGetChannel))
	m.HandleFunc("PUT "+a+"/channels/{id}", s.authed(s.handleUpdateChannel))
	m.HandleFunc("DELETE "+a+"/channels/{id}", s.authed(s.handleDeleteChannel))
	m.HandleFunc("POST "+a+"/channels/{id}/rotate-secret", s.authed(s.handleRotateSecret))
	m.HandleFunc("POST "+a+"/channels/{id}/test", s.authed(s.handleTestChannel))
	m.HandleFunc("GET "+a+"/states", s.authed(s.handleAlertStates))
	m.HandleFunc("GET "+a+"/events", s.authed(s.handleAlertEvents))
	m.HandleFunc("GET "+a+"/notifications", s.authed(s.handleNotifications))
	s.routeIncidents(m)
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeError(w, http.StatusNotFound) })
	mux.Handle("/api/", s.secure(m))
}

// ---- responses ----

var statusText = map[int]string{
	http.StatusBadRequest:            "bad request",
	http.StatusUnauthorized:          "unauthorized",
	http.StatusForbidden:             "forbidden",
	http.StatusNotFound:              "not found",
	http.StatusConflict:              "conflict",
	http.StatusRequestEntityTooLarge: "request body too large",
	http.StatusTooManyRequests:       "too many requests",
	http.StatusServiceUnavailable:    "service unavailable",
	http.StatusInternalServerError:   "internal error",
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a generic body that never carries internal details.
func writeError(w http.ResponseWriter, status int) {
	msg, ok := statusText[status]
	if !ok {
		msg = strings.ToLower(http.StatusText(status))
	}
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeInvalid is a 400 with a fixed, safe message from this package.
func writeInvalid(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
}

// decode reads one JSON object with no unknown fields. It writes the error
// response itself and reports whether to continue.
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(dst)
	if err == nil && dec.Decode(&struct{}{}) != io.EOF {
		err = errors.New("trailing data")
	}
	var mbe *http.MaxBytesError
	switch {
	case errors.As(err, &mbe):
		writeError(w, http.StatusRequestEntityTooLarge)
		return false
	case err != nil:
		writeError(w, http.StatusBadRequest)
		return false
	}
	return true
}

// storeError maps a store error to a status: missing rows are 404, the
// rest are logged and 503 (PostgreSQL trouble) without details.
func (s *Server) storeError(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, postgres.ErrNotFound):
		writeError(w, http.StatusNotFound)
	case errors.Is(err, postgres.ErrConflict), errors.Is(err, postgres.ErrProjectDisabled):
		writeError(w, http.StatusConflict)
	default:
		s.log.Error("store call failed", "op", op, "err", err)
		writeError(w, http.StatusServiceUnavailable)
	}
}

func (s *Server) audit(ctx context.Context, e postgres.AuditEntry) {
	if err := s.store.Audit(context.WithoutCancel(ctx), e); err != nil {
		s.log.Error("audit write failed", "action", e.Action, "err", err)
	}
}

// ---- sessions ----

type sessionKey struct{}

// authed resolves the session cookie or answers 401 (503 when PostgreSQL
// fails). Revoked, expired and disabled-user sessions are all 401.
func (s *Server) authed(h func(http.ResponseWriter, *http.Request, postgres.Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, status := s.session(r)
		if status != 0 {
			if status == http.StatusUnauthorized {
				http.SetCookie(w, sessionCookie(s.opts.CookieSecure, "", -1))
			}
			writeError(w, status)
			return
		}
		h(w, r, sess)
	}
}

func (s *Server) session(r *http.Request) (postgres.Session, int) {
	c, err := r.Cookie(cookieName(s.opts.CookieSecure))
	if err != nil {
		return postgres.Session{}, http.StatusUnauthorized
	}
	hash, ok := hashToken(c.Value)
	if !ok {
		return postgres.Session{}, http.StatusUnauthorized
	}
	sess, err := s.store.LookupSession(r.Context(), hash)
	if errors.Is(err, postgres.ErrNotFound) {
		return postgres.Session{}, http.StatusUnauthorized
	}
	if err != nil {
		s.log.Error("session lookup failed", "err", err)
		return postgres.Session{}, http.StatusServiceUnavailable
	}
	now := s.opts.Now()
	if !sess.Valid(now) {
		return postgres.Session{}, http.StatusUnauthorized
	}
	if now.Sub(sess.LastSeenAt) >= s.opts.TouchInterval {
		if err := s.store.TouchSession(r.Context(), sess.ID, now, now.Add(s.opts.IdleTTL)); err != nil {
			s.log.Warn("session touch failed", "err", err)
		}
	}
	return sess, 0
}

func clientIP(r *http.Request) string {
	// RemoteAddr only: X-Forwarded-For is not trusted (ADR 0013).
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func emailKey(email string) string {
	h := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return "e:" + hex.EncodeToString(h[:])
}

// emailRef identifies an attempted email in the audit log without storing
// what was typed (people sometimes type a password into the email field).
func emailRef(email string) string {
	return "email_sha256:" + emailKey(email)[2:18]
}

func (s *Server) throttled(w http.ResponseWriter, keys ...string) bool {
	wait, ok := s.limiter.Check(s.opts.Now(), keys...)
	if ok {
		return false
	}
	secs := int((wait + time.Second - 1) / time.Second)
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, http.StatusTooManyRequests)
	return true
}

type userJSON struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// startSession creates a fresh session (new token on every login, so a
// planted cookie is never upgraded) and sets the cookie.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, userID string) error {
	token, hash, err := newToken()
	if err != nil {
		return err
	}
	now := s.opts.Now()
	_, err = s.store.CreateSession(r.Context(), postgres.NewSession{
		UserID: userID, TokenHash: hash, UserAgent: r.UserAgent(), CreatedAt: now,
		IdleExpiresAt: now.Add(s.opts.IdleTTL), AbsoluteExpiresAt: now.Add(s.opts.AbsoluteTTL),
	})
	if err != nil {
		return err
	}
	http.SetCookie(w, sessionCookie(s.opts.CookieSecure, token, s.opts.AbsoluteTTL))
	return nil
}

// ---- auth handlers ----

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if !decode(w, r, &in) {
		return
	}
	ipKey, eKey := "i:"+clientIP(r), emailKey(in.Email)
	if s.throttled(w, ipKey, eKey) {
		return
	}
	ctx := r.Context()
	fail := func(userID, why string) {
		s.limiter.Fail(s.opts.Now(), ipKey, eKey)
		s.audit(ctx, postgres.AuditEntry{UserID: userID, Action: "login.failure", Target: emailRef(in.Email), Detail: why})
		writeError(w, http.StatusUnauthorized)
	}
	if in.Email == "" || len(in.Email) > 254 || utf8.RuneCountInString(in.Password) > password.MaxLength {
		fail("", "invalid input")
		return
	}
	u, err := s.store.UserByEmail(ctx, in.Email)
	if errors.Is(err, postgres.ErrNotFound) {
		s.hasher.VerifyDummy(ctx, in.Password) // same work as a real check
		fail("", "unknown user")
		return
	}
	if err != nil {
		s.storeError(w, "user by email", err)
		return
	}
	ok, rehash, err := s.hasher.Verify(ctx, in.Password, u.PasswordHash)
	if err != nil && !errors.Is(err, password.ErrMalformed) {
		writeError(w, http.StatusServiceUnavailable) // context canceled while waiting for a hash slot
		return
	}
	if err != nil {
		s.log.Error("stored password hash is malformed", "user_id", u.ID)
	}
	if !ok {
		fail(u.ID, "wrong password")
		return
	}
	if u.DisabledAt != nil {
		fail(u.ID, "user disabled")
		return
	}
	s.limiter.Reset(eKey)
	if rehash {
		if h, err := s.hasher.Hash(ctx, in.Password); err == nil {
			if err := s.store.UpdatePasswordHash(ctx, u.ID, u.PasswordHash, h); err != nil {
				s.log.Warn("password rehash failed", "err", err)
			}
		}
	}
	// Drop a session the client may already hold; the new one replaces it.
	if old, status := s.session(r); status == 0 {
		_ = s.store.RevokeSession(ctx, old.ID)
	}
	if err := s.startSession(w, r, u.ID); err != nil {
		s.storeError(w, "create session", err)
		return
	}
	s.audit(ctx, postgres.AuditEntry{UserID: u.ID, Action: "login.success"})
	writeJSON(w, http.StatusOK, map[string]any{"user": userJSON{ID: u.ID, Email: u.Email}})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.opts.RegistrationEnabled {
		writeError(w, http.StatusNotFound)
		return
	}
	var in credentials
	if !decode(w, r, &in) {
		return
	}
	ipKey := "i:" + clientIP(r)
	if s.throttled(w, ipKey) {
		return
	}
	email := strings.TrimSpace(in.Email)
	if postgres.ValidateEmail(email) != nil {
		writeInvalid(w, "invalid email address")
		return
	}
	if err := password.CheckPolicy(in.Password, email); err != nil {
		writeInvalid(w, err.Error())
		return
	}
	h, err := s.hasher.Hash(r.Context(), in.Password)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable)
		return
	}
	u, err := s.store.CreateUser(r.Context(), email, h)
	if err != nil {
		if errors.Is(err, postgres.ErrConflict) {
			s.limiter.Fail(s.opts.Now(), ipKey) // slows down email probing
		}
		s.storeError(w, "create user", err)
		return
	}
	s.audit(r.Context(), postgres.AuditEntry{UserID: u.ID, Action: "user.register"})
	writeJSON(w, http.StatusCreated, map[string]any{"user": userJSON{ID: u.ID, Email: u.Email}})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	if err := s.store.RevokeSession(r.Context(), sess.ID); err != nil {
		s.storeError(w, "revoke session", err)
		return
	}
	http.SetCookie(w, sessionCookie(s.opts.CookieSecure, "", -1))
	s.audit(r.Context(), postgres.AuditEntry{UserID: sess.UserID, Action: "logout"})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLogoutAll(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	if err := s.store.RevokeUserSessions(r.Context(), sess.UserID, ""); err != nil {
		s.storeError(w, "revoke sessions", err)
		return
	}
	http.SetCookie(w, sessionCookie(s.opts.CookieSecure, "", -1))
	s.audit(r.Context(), postgres.AuditEntry{UserID: sess.UserID, Action: "logout.all"})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	writeJSON(w, http.StatusOK, map[string]any{
		"user":    userJSON{ID: sess.UserID, Email: sess.Email},
		"session": map[string]any{"created_at": sess.CreatedAt, "expires_at": sess.AbsoluteExpiresAt},
	})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	var in struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if !decode(w, r, &in) {
		return
	}
	eKey := emailKey(sess.Email)
	if s.throttled(w, eKey) {
		return
	}
	u, err := s.store.UserByID(r.Context(), sess.UserID)
	if err != nil {
		s.storeError(w, "user by id", err)
		return
	}
	ok := false
	if utf8.RuneCountInString(in.Current) <= password.MaxLength {
		ok, _, _ = s.hasher.Verify(r.Context(), in.Current, u.PasswordHash)
	}
	if !ok {
		s.limiter.Fail(s.opts.Now(), eKey)
		s.audit(r.Context(), postgres.AuditEntry{UserID: u.ID, Action: "password.change.failure", Detail: "wrong current password"})
		writeError(w, http.StatusForbidden)
		return
	}
	if err := password.CheckPolicy(in.New, u.Email); err != nil {
		writeInvalid(w, err.Error())
		return
	}
	h, err := s.hasher.Hash(r.Context(), in.New)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable)
		return
	}
	if err := s.store.SetPassword(r.Context(), u.ID, h, sess.ID); err != nil {
		s.storeError(w, "set password", err)
		return
	}
	s.audit(r.Context(), postgres.AuditEntry{UserID: u.ID, Action: "password.change", Detail: "other sessions revoked"})
	w.WriteHeader(http.StatusNoContent)
}

// ---- project handlers ----

type projectJSON struct {
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	Disabled  bool      `json:"disabled"`
}

func toProjectJSON(m postgres.Membership) projectJSON {
	return projectJSON{ID: m.ID, Slug: m.Slug, Name: m.Name, Role: string(m.Role), CreatedAt: m.CreatedAt, Disabled: m.DisabledAt != nil}
}

// member loads the caller's membership in {slug} and checks a. Non-members
// and unknown projects get 404; members without the right get 403.
func (s *Server) member(w http.ResponseWriter, r *http.Request, sess postgres.Session, a action) (postgres.Membership, bool) {
	slug := r.PathValue("slug")
	if postgres.ValidateSlug(slug) != nil {
		writeError(w, http.StatusNotFound)
		return postgres.Membership{}, false
	}
	m, err := s.store.MembershipFor(r.Context(), sess.UserID, slug)
	if err != nil {
		s.storeError(w, "membership", err)
		return postgres.Membership{}, false
	}
	if !can(m.Role, a) {
		writeError(w, http.StatusForbidden)
		return postgres.Membership{}, false
	}
	return m, true
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	ms, err := s.store.ProjectsForUser(r.Context(), sess.UserID)
	if err != nil {
		s.storeError(w, "list projects", err)
		return
	}
	out := make([]projectJSON, 0, len(ms))
	for _, m := range ms {
		out = append(out, toProjectJSON(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out})
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	var in struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	if !decode(w, r, &in) {
		return
	}
	if postgres.ValidateSlug(in.Slug) != nil {
		writeInvalid(w, "invalid slug: use 3-40 lowercase letters, digits or dashes")
		return
	}
	if postgres.ValidateName(in.Name) != nil {
		writeInvalid(w, "name must be 1-200 characters")
		return
	}
	p, err := s.store.CreateProjectWithOwner(r.Context(), in.Name, in.Slug, sess.UserID)
	if err != nil {
		s.storeError(w, "create project", err)
		return
	}
	s.audit(r.Context(), postgres.AuditEntry{UserID: sess.UserID, Action: "project.create", Target: p.Slug})
	writeJSON(w, http.StatusCreated, map[string]any{"project": toProjectJSON(postgres.Membership{Project: p, Role: postgres.RoleOwner})})
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, ok := s.member(w, r, sess, actView)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": toProjectJSON(m)})
}

func (s *Server) handleDisableProject(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, ok := s.member(w, r, sess, actDisable)
	if !ok {
		return
	}
	p, err := s.store.DisableProject(r.Context(), m.Slug)
	if err != nil {
		s.storeError(w, "disable project", err)
		return
	}
	s.audit(r.Context(), postgres.AuditEntry{UserID: sess.UserID, Action: "project.disable", Target: p.Slug})
	writeJSON(w, http.StatusOK, map[string]any{"project": toProjectJSON(postgres.Membership{Project: p, Role: m.Role})})
}

type keyJSON struct {
	Prefix     string     `json:"prefix"`
	Kind       string     `json:"kind"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	Status     string     `json:"status"`
}

func (s *Server) toKeyJSON(k postgres.KeyInfo) keyJSON {
	return keyJSON{Prefix: k.Prefix, Kind: string(k.Kind), Name: k.Name, CreatedAt: k.CreatedAt, ExpiresAt: k.ExpiresAt,
		RevokedAt: k.RevokedAt, LastUsedAt: k.LastUsedAt, Status: k.Status(s.opts.Now())}
}

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, ok := s.member(w, r, sess, actView)
	if !ok {
		return
	}
	ks, err := s.store.ListKeys(r.Context(), m.Slug)
	if err != nil {
		s.storeError(w, "list keys", err)
		return
	}
	out := make([]keyJSON, 0, len(ks))
	for _, k := range ks {
		out = append(out, s.toKeyJSON(k))
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, ok := s.member(w, r, sess, actManageKeys)
	if !ok {
		return
	}
	var in struct {
		Kind      string `json:"kind"`
		Name      string `json:"name"`
		ExpiresIn string `json:"expires_in"` // Go duration, optional
	}
	if !decode(w, r, &in) {
		return
	}
	kind, err := keys.ParseKind(in.Kind)
	if err != nil {
		writeInvalid(w, "kind must be ingest or query")
		return
	}
	if postgres.ValidateName(in.Name) != nil {
		writeInvalid(w, "name must be 1-200 characters")
		return
	}
	var expires *time.Time
	if in.ExpiresIn != "" {
		d, err := time.ParseDuration(in.ExpiresIn)
		if err != nil || d <= 0 || d > maxKeyLifetime {
			writeInvalid(w, "expires_in must be a positive duration such as 720h, at most 10 years")
			return
		}
		t := s.opts.Now().Add(d).UTC()
		expires = &t
	}
	g, info, err := s.store.IssueKey(r.Context(), m.Slug, kind, in.Name, expires)
	if err != nil {
		s.storeError(w, "issue key", err)
		return
	}
	s.audit(r.Context(), postgres.AuditEntry{UserID: sess.UserID, Action: "key.create", Target: m.Slug + "/" + info.Prefix, Detail: string(info.Kind)})
	// The plaintext key is returned once and never stored or logged.
	writeJSON(w, http.StatusCreated, map[string]any{"key": g.Plaintext, "info": s.toKeyJSON(info)})
}

func (s *Server) handleRevokeKey(w http.ResponseWriter, r *http.Request, sess postgres.Session) {
	m, ok := s.member(w, r, sess, actManageKeys)
	if !ok {
		return
	}
	prefix := r.PathValue("prefix")
	info, err := s.store.RevokeProjectKey(r.Context(), m.Slug, prefix)
	if err != nil {
		s.storeError(w, "revoke key", err)
		return
	}
	s.audit(r.Context(), postgres.AuditEntry{UserID: sess.UserID, Action: "key.revoke", Target: m.Slug + "/" + info.Prefix})
	writeJSON(w, http.StatusOK, map[string]any{"info": s.toKeyJSON(info)})
}
