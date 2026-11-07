package main

import (
	"context"
	"strings"
	"testing"

	"github.com/sinanturdu/logsage/backend/internal/storage/postgres/pgtest"
)

func TestUserCommandsValidation(t *testing.T) {
	env := map[string]string{"DATABASE_URL": "postgres://u:p@localhost:1/db"}
	for _, args := range [][]string{
		{"create-user"},
		{"create-user", "--email", "not-an-email"},
		{"create-user", "--email", "a@b.c", "--password", "x"}, // no password flag exists
		{"set-password"},
		{"disable-user"},
		{"add-member", "--project", "alpha", "--email", "a@b.c"},
		{"add-member", "--project", "alpha", "--email", "a@b.c", "--role", "root"},
		{"list-members"},
	} {
		if code, _, _ := runCLI(t, env, "", args...); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
}

func TestIntegrationUserCommands(t *testing.T) {
	pool, st, url := pgtest.New(t)
	env := map[string]string{"DATABASE_URL": url, "ARGON2_MEMORY_KIB": "19456", "ARGON2_ITERATIONS": "1", "ARGON2_PARALLELISM": "1"}
	const pw = "correct horse battery staple"
	ctx := context.Background()

	// Weak password and email-as-password are refused.
	if code, _, _ := runCLI(t, env, "short\n", "create-user", "--email", "alice@example.com"); code != exitFailed {
		t.Errorf("weak password: exit %d", code)
	}
	if code, _, _ := runCLI(t, env, "alice@example.com\n", "create-user", "--email", "alice@example.com"); code != exitFailed {
		t.Errorf("email as password: exit %d", code)
	}
	code, out, errOut := runCLI(t, env, pw+"\r\n", "create-user", "--email", "alice@example.com")
	if code != exitOK || strings.Contains(out+errOut, pw) {
		t.Fatalf("create-user: exit %d out=%q err=%q", code, out, errOut)
	}
	u, err := st.UserByEmail(ctx, "alice@example.com")
	if err != nil || !strings.HasPrefix(u.PasswordHash, "$argon2id$") || strings.Contains(u.PasswordHash, pw) {
		t.Fatalf("stored user = %v", err)
	}
	if code, _, _ := runCLI(t, env, pw+"\n", "create-user", "--email", "ALICE@example.com"); code != exitFailed {
		t.Errorf("duplicate user: exit %d", code)
	}

	// set-password via the environment variable.
	env2 := map[string]string{}
	for k, v := range env {
		env2[k] = v
	}
	env2["LOGSAGE_PASSWORD"] = "another long passphrase"
	if code, out, errOut := runCLI(t, env2, "", "set-password", "--email", "alice@example.com"); code != exitOK || strings.Contains(out+errOut, "another long") {
		t.Fatalf("set-password: exit %d out=%q err=%q", code, out, errOut)
	}
	if u2, _ := st.UserByEmail(ctx, "alice@example.com"); u2.PasswordHash == u.PasswordHash {
		t.Error("set-password did not change the hash")
	}

	if code, _, errOut := runCLI(t, env, "", "create-project", "--name", "Alpha", "--slug", "alpha"); code != exitOK {
		t.Fatalf("create-project: %s", errOut)
	}
	if code, out, errOut := runCLI(t, env, "", "add-member", "--project", "alpha", "--email", "alice@example.com", "--role", "owner"); code != exitOK || !strings.Contains(out, "owner") {
		t.Fatalf("add-member: %d %q %q", code, out, errOut)
	}
	if code, _, _ := runCLI(t, env, "", "add-member", "--project", "alpha", "--email", "ghost@example.com", "--role", "viewer"); code != exitFailed {
		t.Errorf("add-member unknown user: exit %d", code)
	}
	if code, out, _ := runCLI(t, env, "", "list-members", "--project", "alpha"); code != exitOK || !strings.Contains(out, "alice@example.com") || !strings.Contains(out, "owner") {
		t.Errorf("list-members: %d %q", code, out)
	}
	if code, out, _ := runCLI(t, env, "", "disable-user", "--email", "alice@example.com"); code != exitOK || !strings.Contains(out, "disabled") {
		t.Errorf("disable-user: %d %q", code, out)
	}

	var audits int
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM audit_log WHERE action IN ('user.create','password.set','member.set','user.disable')").Scan(&audits)
	if audits != 4 {
		t.Errorf("audit rows = %d, want 4", audits)
	}
}
