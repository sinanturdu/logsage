package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestGroupStatus(t *testing.T) {
	pool, st := migrated(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "owner@example.com", fakePHC)
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateProjectWithOwner(ctx, "Shop", "shop", u.ID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateProjectWithOwner(ctx, "Other", "other", u.ID)
	if err != nil {
		t.Fatal(err)
	}
	const fp = uint64(0xfedcba9876543210) // above math.MaxInt64: stored as a negative bigint
	audit := AuditEntry{Action: "group.status", Target: "shop/fedcba9876543210", Detail: "resolved"}

	if _, err := st.GroupState(ctx, p.ID, fp); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no row yet means open: %v", err)
	}
	g, err := st.SetGroupStatus(ctx, p.ID, fp, "resolved", "fixed in 1.4.2", u.ID, audit)
	if err != nil {
		t.Fatal(err)
	}
	if g.Fingerprint != fp || g.Status != "resolved" || g.ResolvedAt == nil || g.Note != "fixed in 1.4.2" || g.UpdatedBy != "owner@example.com" {
		t.Fatalf("after resolve: %+v", g)
	}
	first := *g.ResolvedAt

	// Resolving again keeps the first resolved_at; reopening clears it.
	g, _ = st.SetGroupStatus(ctx, p.ID, fp, "resolved", "still fixed", u.ID, audit)
	if g.ResolvedAt == nil || !g.ResolvedAt.Equal(first) || g.Note != "still fixed" {
		t.Fatalf("resolved twice: %+v", g)
	}
	g, _ = st.SetGroupStatus(ctx, p.ID, fp, "open", "", u.ID, audit)
	if g.Status != "open" || g.ResolvedAt != nil {
		t.Fatalf("reopened: %+v", g)
	}
	if _, err := st.SetGroupStatus(ctx, p.ID, fp, "ignored", strings.Repeat("n", 900), u.ID, audit); err != nil {
		t.Fatal(err)
	}

	// Persisted, project-scoped, listed.
	g, err = st.GroupState(ctx, p.ID, fp)
	if err != nil || g.Status != "ignored" || len([]rune(g.Note)) != MaxGroupNote {
		t.Fatalf("stored: %v %+v", err, g)
	}
	if _, err := st.GroupState(ctx, other.ID, fp); !errors.Is(err, ErrNotFound) {
		t.Fatal("another project must not see this state")
	}
	all, err := st.GroupStates(ctx, p.ID)
	if err != nil || len(all) != 1 || all[0].Fingerprint != fp {
		t.Fatalf("states: %v %+v", err, all)
	}

	// Every change wrote an audit row in the same transaction.
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM audit_log WHERE action = 'group.status' AND user_id = $1", u.ID).Scan(&n); err != nil || n != 4 {
		t.Fatalf("audit rows = %d (%v), want 4", n, err)
	}

	// Rejected writes leave nothing behind.
	if _, err := st.SetGroupStatus(ctx, p.ID, fp, "deleted", "", u.ID, audit); err == nil {
		t.Fatal("an unknown status must be refused by the table")
	}
	if _, err := st.SetGroupStatus(ctx, p.ID, 0, "open", "", u.ID, audit); err == nil {
		t.Fatal("fingerprint 0 is not a group")
	}
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM audit_log WHERE action = 'group.status'").Scan(&n)
	if n != 4 {
		t.Fatalf("a refused change wrote an audit row: %d", n)
	}
}
