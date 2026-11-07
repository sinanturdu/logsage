package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sinanturdu/logsage/backend/internal/keys"
)

const fakePHC = "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g"

func TestIntegrationUsers(t *testing.T) {
	_, st := migrated(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "Alice@Example.com", fakePHC)
	if err != nil || u.PasswordHash != "" || u.Email != "Alice@Example.com" {
		t.Fatalf("CreateUser = %+v, %v", u, err)
	}
	if _, err := st.CreateUser(ctx, "alice@example.COM", fakePHC); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate email (case) = %v, want ErrConflict", err)
	}
	for _, bad := range []string{"", "a", "noat", "@x.y", "x@", "a b@c.d"} {
		if _, err := st.CreateUser(ctx, bad, fakePHC); err == nil {
			t.Errorf("CreateUser(%q) accepted", bad)
		}
	}
	if _, err := st.CreateUser(ctx, "bob@example.com", "plaintext-password"); err == nil {
		t.Error("non-PHC password_hash accepted by the CHECK")
	}
	got, err := st.UserByEmail(ctx, " ALICE@example.com ")
	if err != nil || got.ID != u.ID || got.PasswordHash != fakePHC {
		t.Fatalf("UserByEmail = %+v, %v", got, err)
	}
	if _, err := st.UserByEmail(ctx, "nobody@example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown email = %v", err)
	}
	newPHC := strings.Replace(fakePHC, "t=2", "t=3", 1)
	if err := st.UpdatePasswordHash(ctx, u.ID, "stale", newPHC); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.UserByID(ctx, u.ID); got.PasswordHash != fakePHC {
		t.Error("compare-and-set rehash overwrote a hash it did not read")
	}
	d, err := st.DisableUser(ctx, "alice@example.com")
	if err != nil || d.DisabledAt == nil {
		t.Fatalf("DisableUser = %+v, %v", d, err)
	}
}

func TestIntegrationSessions(t *testing.T) {
	_, st := migrated(t)
	ctx := context.Background()
	u, _ := st.CreateUser(ctx, "s@example.com", fakePHC)
	now := time.Now().UTC().Truncate(time.Microsecond)
	mk := func(tok string) string {
		h := sha256.Sum256([]byte(tok))
		id, err := st.CreateSession(ctx, NewSession{UserID: u.ID, TokenHash: h[:], UserAgent: strings.Repeat("ü", 300),
			CreatedAt: now, IdleExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(2 * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	a, b, c := mk("a"), mk("b"), mk("c")
	ha := sha256.Sum256([]byte("a"))
	s, err := st.LookupSession(ctx, ha[:])
	if err != nil || s.ID != a || s.UserID != u.ID || !s.Valid(now) || s.Valid(now.Add(time.Hour)) {
		t.Fatalf("LookupSession = %+v, %v", s, err)
	}
	// Idle expiry slides but never past the absolute expiry.
	if err := st.TouchSession(ctx, a, now.Add(time.Minute), now.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	s, _ = st.LookupSession(ctx, ha[:])
	if !s.IdleExpiresAt.Equal(s.AbsoluteExpiresAt) || !s.LastSeenAt.Equal(now.Add(time.Minute)) {
		t.Errorf("after touch: idle %v abs %v seen %v", s.IdleExpiresAt, s.AbsoluteExpiresAt, s.LastSeenAt)
	}
	if err := st.RevokeUserSessions(ctx, u.ID, b); err != nil {
		t.Fatal(err)
	}
	for tok, wantValid := range map[string]bool{"a": false, "b": true, "c": false} {
		h := sha256.Sum256([]byte(tok))
		s, _ := st.LookupSession(ctx, h[:])
		if s.Valid(now) != wantValid {
			t.Errorf("session %s valid = %v, want %v", tok, s.Valid(now), wantValid)
		}
	}
	_ = c
	if err := st.SetPassword(ctx, u.ID, fakePHC, ""); err != nil {
		t.Fatal(err)
	}
	hb := sha256.Sum256([]byte("b"))
	if s, _ := st.LookupSession(ctx, hb[:]); s.Valid(now) {
		t.Error("SetPassword with no kept session left a session active")
	}
	// Disabled users are rejected through the join.
	d := mk("d")
	hd := sha256.Sum256([]byte("d"))
	_, _ = st.DisableUser(ctx, "s@example.com")
	if s, _ := st.LookupSession(ctx, hd[:]); s.ID != d || s.Valid(now) || !s.UserDisabled {
		t.Errorf("disabled user's session = %+v", s)
	}
	var ua string
	_ = st.pool.QueryRow(ctx, "SELECT user_agent FROM sessions WHERE id = $1", d).Scan(&ua)
	if len(ua) > MaxUserAgent {
		t.Errorf("user agent stored with %d bytes", len(ua))
	}
}

func TestIntegrationMembersAndKeys(t *testing.T) {
	_, st := migrated(t)
	ctx := context.Background()
	alice, _ := st.CreateUser(ctx, "alice@example.com", fakePHC)
	bob, _ := st.CreateUser(ctx, "bob@example.com", fakePHC)
	pa, err := st.CreateProjectWithOwner(ctx, "Alpha", "alpha", alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateProjectWithOwner(ctx, "Again", "alpha", bob.ID); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate slug = %v", err)
	}
	if _, err := st.CreateProjectWithOwner(ctx, "Beta", "beta", bob.ID); err != nil {
		t.Fatal(err)
	}
	m, err := st.MembershipFor(ctx, alice.ID, "alpha")
	if err != nil || m.Role != RoleOwner || m.ID != pa.ID {
		t.Fatalf("MembershipFor = %+v, %v", m, err)
	}
	if _, err := st.MembershipFor(ctx, alice.ID, "beta"); !errors.Is(err, ErrNotFound) {
		t.Errorf("non-member = %v, want ErrNotFound", err)
	}
	if _, err := st.MembershipFor(ctx, alice.ID, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing project = %v, want ErrNotFound", err)
	}
	if ps, _ := st.ProjectsForUser(ctx, alice.ID); len(ps) != 1 || ps[0].Slug != "alpha" {
		t.Errorf("ProjectsForUser = %+v", ps)
	}
	if _, err := st.AddMember(ctx, "alpha", "BOB@example.com", RoleViewer); err != nil {
		t.Fatal(err)
	}
	if mb, err := st.AddMember(ctx, "alpha", "bob@example.com", RoleAdmin); err != nil || mb.Role != RoleAdmin {
		t.Errorf("AddMember upsert = %+v, %v", mb, err)
	}
	if _, err := st.AddMember(ctx, "alpha", "ghost@example.com", RoleAdmin); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user = %v", err)
	}
	if _, err := st.AddMember(ctx, "alpha", "bob@example.com", Role("root")); err == nil {
		t.Error("invalid role accepted")
	}
	if ms, _ := st.ListMembers(ctx, "alpha"); len(ms) != 2 || ms[0].Email != "alice@example.com" || ms[1].Role != RoleAdmin {
		t.Errorf("ListMembers = %+v", ms)
	}
	if ms, err := st.ListMembers(ctx, "default"); err != nil || len(ms) != 0 {
		t.Errorf("default project members = %+v, %v; want none", ms, err)
	}

	g, info, err := st.IssueKey(ctx, "beta", keys.KindIngest, "ci", nil)
	if err != nil || info.Prefix != g.Prefix || !strings.HasPrefix(g.Plaintext, "lsk_ing_") {
		t.Fatalf("IssueKey = %+v, %v", info, err)
	}
	if _, err := st.RevokeProjectKey(ctx, "alpha", g.Prefix); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoke through another project = %v, want ErrNotFound", err)
	}
	if k, err := st.RevokeProjectKey(ctx, "beta", g.Prefix); err != nil || k.RevokedAt == nil {
		t.Errorf("RevokeProjectKey = %+v, %v", k, err)
	}

	if err := st.Audit(ctx, AuditEntry{Action: "login.failure", Target: strings.Repeat("x", 400)}); err != nil {
		t.Fatal(err)
	}
	if err := st.Audit(ctx, AuditEntry{UserID: alice.ID, Action: "key.create", Target: "beta/" + g.Prefix}); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = st.pool.QueryRow(ctx, "SELECT count(*) FROM audit_log").Scan(&n)
	if n != 2 {
		t.Errorf("audit rows = %d", n)
	}
}
