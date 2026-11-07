package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/sinanturdu/logsage/backend/internal/password"
	"github.com/sinanturdu/logsage/backend/internal/storage/postgres"
)

const (
	passwordEnvName  = "LOGSAGE_PASSWORD"
	maxPasswordInput = 4096
)

// readPassword takes the password from LOGSAGE_PASSWORD or the first line of
// stdin, never from a flag (shell history, process lists), and never
// prints it. Only the line ending is stripped: spaces are significant.
func readPassword(e *env) (string, error) {
	if pw := e.getenv(passwordEnvName); pw != "" {
		return pw, nil
	}
	line, err := bufio.NewReader(io.LimitReader(e.stdin, maxPasswordInput)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read password from stdin: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (e *env) hasher() (*password.Hasher, error) {
	a := e.cfg.API
	p := password.DefaultParams()
	p.MemoryKiB, p.Iterations, p.Parallelism = uint32(a.Argon2MemoryKiB), uint32(a.Argon2Iterations), uint8(a.Argon2Parallelism)
	return password.NewHasher(p, 1)
}

// hashNewPassword reads, checks the policy and hashes a new password.
func hashNewPassword(e *env, email string) (string, error) {
	pw, err := readPassword(e)
	if err != nil {
		return "", err
	}
	if err := password.CheckPolicy(pw, email); err != nil {
		return "", err
	}
	h, err := e.hasher()
	if err != nil {
		return "", err
	}
	return h.Hash(e.ctx, pw)
}

func emailFlag(fs *flag.FlagSet) *string { return fs.String("email", "", "user email") }

func requireEmail(email string) error {
	if email == "" {
		return usagef("--email is required")
	}
	return postgres.ValidateEmail(strings.TrimSpace(email))
}

// ---- create-user

type createUserCmd struct{ email *string }

func (c *createUserCmd) validate() error { return requireEmail(*c.email) }

func (c *createUserCmd) run(e *env) error {
	h, err := hashNewPassword(e, strings.TrimSpace(*c.email))
	if err != nil {
		return err
	}
	u, err := e.store.CreateUser(e.ctx, *c.email, h)
	if err != nil {
		return err
	}
	_ = e.store.Audit(e.ctx, postgres.AuditEntry{UserID: u.ID, Action: "user.create", Detail: "admin cli"})
	fmt.Fprintf(e.stdout, "created user %s (id %s)\n", u.Email, u.ID)
	return nil
}

// ---- set-password

type setPasswordCmd struct{ email *string }

func (c *setPasswordCmd) validate() error { return requireEmail(*c.email) }

func (c *setPasswordCmd) run(e *env) error {
	u, err := e.store.UserByEmail(e.ctx, *c.email)
	if err != nil {
		return fmt.Errorf("user: %w", err)
	}
	h, err := hashNewPassword(e, u.Email)
	if err != nil {
		return err
	}
	if err := e.store.SetPassword(e.ctx, u.ID, h, ""); err != nil {
		return err
	}
	_ = e.store.Audit(e.ctx, postgres.AuditEntry{UserID: u.ID, Action: "password.set", Detail: "admin cli; all sessions revoked"})
	fmt.Fprintf(e.stdout, "password set for %s; all sessions revoked\n", u.Email)
	return nil
}

// ---- disable-user

type disableUserCmd struct{ email *string }

func (c *disableUserCmd) validate() error { return requireEmail(*c.email) }

func (c *disableUserCmd) run(e *env) error {
	u, err := e.store.DisableUser(e.ctx, *c.email)
	if err != nil {
		return fmt.Errorf("user: %w", err)
	}
	_ = e.store.Audit(e.ctx, postgres.AuditEntry{UserID: u.ID, Action: "user.disable", Detail: "admin cli"})
	fmt.Fprintf(e.stdout, "disabled user %s at %s; all sessions revoked\n", u.Email, fmtTime(u.DisabledAt))
	return nil
}

// ---- add-member

type addMemberCmd struct{ project, email, role *string }

func newAddMember(fs *flag.FlagSet) options {
	return &addMemberCmd{
		project: fs.String("project", "", "project slug"),
		email:   emailFlag(fs),
		role:    fs.String("role", "", "owner, admin or viewer"),
	}
}

func (c *addMemberCmd) validate() error {
	if *c.project == "" || *c.role == "" {
		return usagef("--project, --email and --role are required")
	}
	if err := postgres.ValidateSlug(*c.project); err != nil {
		return err
	}
	if _, err := postgres.ParseRole(*c.role); err != nil {
		return err
	}
	return requireEmail(*c.email)
}

func (c *addMemberCmd) run(e *env) error {
	m, err := e.store.AddMember(e.ctx, *c.project, *c.email, postgres.Role(*c.role))
	if err != nil {
		return err
	}
	_ = e.store.Audit(e.ctx, postgres.AuditEntry{Action: "member.set", Target: *c.project, Detail: "admin cli; role " + string(m.Role)})
	fmt.Fprintf(e.stdout, "%s is now %s of project %s\n", m.Email, m.Role, *c.project)
	return nil
}

// ---- list-members

type listMembersCmd struct{ project *string }

func (c *listMembersCmd) validate() error {
	if *c.project == "" {
		return usagef("--project is required")
	}
	return postgres.ValidateSlug(*c.project)
}

func (c *listMembersCmd) run(e *env) error {
	ms, err := e.store.ListMembers(e.ctx, *c.project)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "EMAIL\tROLE\tADDED\tUSER")
	for _, m := range ms {
		status := "active"
		if m.UserDisabled {
			status = "disabled"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", m.Email, m.Role, fmtTime(&m.CreatedAt), status)
	}
	return tw.Flush()
}
