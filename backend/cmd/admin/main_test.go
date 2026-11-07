package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

const localDB = "postgres://admin:pw-local-123@127.0.0.1:1/logsage?sslmode=disable"

func runCLI(t *testing.T, env map[string]string, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(context.Background(), args, func(k string) string { return env[k] }, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

// TestArgumentValidation: every case fails before any database connection
// (DATABASE_URL points at a closed port; a connection attempt would exit 1).
func TestArgumentValidation(t *testing.T) {
	env := map[string]string{"DATABASE_URL": localDB}
	tests := []struct {
		name string
		args []string
		want string // substring of stderr
	}{
		{"no command", nil, "usage: admin"},
		{"unknown command", []string{"drop-everything"}, `unknown command "drop-everything"`},
		{"unknown flag", []string{"list-projects", "--verbose"}, "flag provided but not defined"},
		{"extra arguments", []string{"list-projects", "extra"}, "unexpected arguments"},
		{"create-project without flags", []string{"create-project"}, "--name and --slug are required"},
		{"slug uppercase", []string{"create-project", "--name", "Demo", "--slug", "Demo"}, "--slug: invalid slug"},
		{"slug too short", []string{"create-project", "--name", "Demo", "--slug", "ab"}, "--slug: invalid slug"},
		{"slug with underscore", []string{"create-project", "--name", "Demo", "--slug", "my_app"}, "--slug: invalid slug"},
		{"blank name", []string{"create-project", "--name", "   ", "--slug", "demo"}, "--name"},
		{"create-key missing kind", []string{"create-key", "--project", "demo", "--name", "ci"}, "are required"},
		{"create-key bad kind", []string{"create-key", "--project", "demo", "--kind", "admin", "--name", "ci"}, "--kind: unknown key kind"},
		{"create-key negative expiry", []string{"create-key", "--project", "demo", "--kind", "ingest", "--name", "ci", "--expires-in", "-1h"}, "must not be negative"},
		{"create-key bad duration", []string{"create-key", "--project", "demo", "--kind", "ingest", "--name", "ci", "--expires-in", "30d"}, "invalid value"},
		{"keys never come from flags", []string{"verify-key", "--key", "lsk_ing_abcdefgh_x"}, "flag provided but not defined"},
		{"verify-key bad kind", []string{"verify-key", "--kind", "both"}, "--kind"},
		{"revoke-key without prefix", []string{"revoke-key"}, "--prefix is required"},
		{"revoke-key given the whole key", []string{"revoke-key", "--prefix", "lsk_ing_abcdefgh_secret"}, "only the 8-character prefix"},
		{"revoke-key bad prefix", []string{"revoke-key", "--prefix", "ABCDEFGH"}, "8 characters"},
		{"list-keys without project", []string{"list-keys"}, "--project is required"},
		{"disable-project bad slug", []string{"disable-project", "--slug", "x"}, "invalid slug"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out, errOut := runCLI(t, env, "", tt.args...)
			if code != exitUsage {
				t.Fatalf("exit = %d, want %d (stderr %q)", code, exitUsage, errOut)
			}
			if !strings.Contains(errOut, tt.want) {
				t.Errorf("stderr = %q, want %q", errOut, tt.want)
			}
			if out != "" {
				t.Errorf("stdout = %q, want nothing", out)
			}
		})
	}
}

func TestDatabaseGuards(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		args []string
		code int
		want string
	}{
		{"no database configured", map[string]string{}, []string{"list-projects"}, exitUsage, "DATABASE_URL"},
		{"remote host refused", map[string]string{"DATABASE_URL": "postgres://u:remote-pw-9@db.example.com/logsage"}, []string{"list-projects"}, exitUsage, `refusing non-local database host "db.example.com"`},
		{"compose service name is remote", map[string]string{"DATABASE_URL": "postgres://u:remote-pw-9@postgres:5432/logsage"}, []string{"migrate"}, exitUsage, "refusing non-local"},
		{"invalid url", map[string]string{"DATABASE_URL": "postgres://u:remote-pw-9@/nodb"}, []string{"migrate"}, exitUsage, "no host"},
		{
			name: "connection failure does not print the password",
			env:  map[string]string{"DATABASE_URL": localDB, "DATABASE_CONNECT_TIMEOUT": "1s"},
			args: []string{"list-projects"}, code: exitFailed, want: "postgres: connect to postgres://admin:xxxxx@127.0.0.1:1/logsage",
		},
		{
			name: "--allow-remote passes the guard (then fails to connect)",
			env:  map[string]string{"DATABASE_URL": "postgres://u:remote-pw-9@203.0.113.1:5432/logsage", "DATABASE_CONNECT_TIMEOUT": "1s"},
			args: []string{"--allow-remote", "list-projects"}, code: exitFailed, want: "u:xxxxx@203.0.113.1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _, errOut := runCLI(t, tt.env, "", tt.args...)
			if code != tt.code || !strings.Contains(errOut, tt.want) {
				t.Fatalf("exit %d stderr %q; want %d and %q", code, errOut, tt.code, tt.want)
			}
			for _, secret := range []string{"pw-local-123", "remote-pw-9"} {
				if strings.Contains(errOut, secret) {
					t.Errorf("stderr leaks the password: %q", errOut)
				}
			}
		})
	}
}
