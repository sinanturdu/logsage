// Command admin manages LogSage projects, API keys, users and members in
// PostgreSQL.
//
//	admin [--allow-remote] <command> [flags]
//
//	migrate                                   apply PostgreSQL migrations
//	create-project  --name N --slug S         create a project
//	list-projects                             list projects
//	disable-project --slug S                  disable a project (its keys stop working)
//	create-key      --project S --kind ingest|query --name N [--expires-in 720h]
//	                                          create a key; printed ONCE on stdout
//	list-keys       --project S               list keys (prefix, kind, name, dates, status)
//	revoke-key      --prefix P                revoke a key
//	verify-key      [--kind ingest|query]     verify a key read from LOGSAGE_API_KEY or stdin
//	create-user     --email E                 create a user (password from LOGSAGE_PASSWORD or stdin)
//	set-password    --email E                 set a password (same input); revokes all sessions
//	disable-user    --email E                 disable a user; revokes all sessions
//	add-member      --project S --email E --role owner|admin|viewer
//	                                          add a member or change their role
//	list-members    --project S               list members
//
// The database comes from DATABASE_URL (or POSTGRES_*) in the environment
// only. Non-local database hosts are refused unless --allow-remote is passed.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sinanturdu/logsage/backend/internal/config"
	"github.com/sinanturdu/logsage/backend/internal/keys"
	"github.com/sinanturdu/logsage/backend/internal/storage/postgres"
	"github.com/sinanturdu/logsage/backend/migrations"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr))
}

// Exit codes.
const (
	exitOK      = 0
	exitFailed  = 1 // operation failed, or verify-key found the key invalid
	exitUsage   = 2
	keyEnvName  = "LOGSAGE_API_KEY"
	maxKeyInput = 4096
)

const usage = `usage: admin [--allow-remote] <command> [flags]

commands:
  migrate
  create-project  --name NAME --slug SLUG
  list-projects
  disable-project --slug SLUG
  create-key      --project SLUG --kind ingest|query --name NAME [--expires-in 720h]
  list-keys       --project SLUG
  revoke-key      --prefix PREFIX
  verify-key      [--kind ingest|query]   (key from LOGSAGE_API_KEY or stdin)
  create-user     --email EMAIL           (password from LOGSAGE_PASSWORD or stdin)
  set-password    --email EMAIL           (same; revokes all sessions)
  disable-user    --email EMAIL
  add-member      --project SLUG --email EMAIL --role owner|admin|viewer
  list-members    --project SLUG

The database is read from DATABASE_URL (or POSTGRES_*) in the environment.`

// usageError marks errors that should print the usage and exit 2.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return usageError{fmt.Sprintf(format, a...)} }

// run is main without os globals, for tests.
func run(ctx context.Context, args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	global := flag.NewFlagSet("admin", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	allowRemote := global.Bool("allow-remote", false, "allow a non-local database host")
	if err := global.Parse(args); err != nil || global.NArg() == 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	cmd, rest := global.Arg(0), global.Args()[1:]

	c, ok := commands[cmd]
	if !ok {
		fmt.Fprintf(stderr, "admin: unknown command %q\n\n%s\n", cmd, usage)
		return exitUsage
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	localRemote := fs.Bool("allow-remote", false, "allow a non-local database host")
	opts := c.flags(fs)
	if err := fs.Parse(rest); err != nil {
		fmt.Fprintf(stderr, "admin %s: %v\n\n%s\n", cmd, err, usage)
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "admin %s: unexpected arguments %q\n", cmd, fs.Args())
		return exitUsage
	}
	if err := opts.validate(); err != nil {
		fmt.Fprintf(stderr, "admin %s: %v\n", cmd, err)
		return exitUsage
	}

	cfg, err := config.Load(getenv)
	if err != nil {
		fmt.Fprintf(stderr, "admin: %v\n", err)
		return exitUsage
	}
	if err := cfg.ValidateDatabase(); err != nil {
		fmt.Fprintf(stderr, "admin: %v\n", err)
		return exitUsage
	}
	if !cfg.Postgres.IsLocal() && !*allowRemote && !*localRemote {
		fmt.Fprintf(stderr, "admin: refusing non-local database host %q; pass --allow-remote if you mean it\n", cfg.Postgres.Host())
		return exitUsage
	}

	pool, err := postgres.Open(ctx, cfg.Postgres)
	if err != nil {
		fmt.Fprintf(stderr, "admin: %v\n", err)
		return exitFailed
	}
	defer pool.Close()
	env := &env{
		ctx: ctx, getenv: getenv, stdin: stdin, stdout: stdout, stderr: stderr,
		pool: pool, store: postgres.NewStore(pool, cfg.Postgres.QueryTimeout), now: time.Now, cfg: cfg,
	}
	if err := opts.run(env); err != nil {
		if errors.Is(err, errInvalid) {
			return exitFailed // verify-key already printed "invalid"
		}
		fmt.Fprintf(stderr, "admin %s: %v\n", cmd, err)
		return exitFailed
	}
	return exitOK
}

// env is what a command needs at run time.
type env struct {
	ctx            context.Context
	getenv         func(string) string
	stdin          io.Reader
	stdout, stderr io.Writer
	pool           *pgxpool.Pool
	store          *postgres.Store
	now            func() time.Time
	cfg            config.Config
}

// command parses its flags into options that validate and run.
type command struct {
	flags func(fs *flag.FlagSet) options
}

type options interface {
	validate() error
	run(e *env) error
}

var commands = map[string]command{
	"migrate":         {flags: func(*flag.FlagSet) options { return &migrateCmd{} }},
	"create-project":  {flags: newCreateProject},
	"list-projects":   {flags: func(*flag.FlagSet) options { return &listProjectsCmd{} }},
	"disable-project": {flags: newDisableProject},
	"create-key":      {flags: newCreateKey},
	"list-keys":       {flags: newListKeys},
	"revoke-key":      {flags: newRevokeKey},
	"verify-key":      {flags: newVerifyKey},
	"create-user":     {flags: func(fs *flag.FlagSet) options { return &createUserCmd{email: emailFlag(fs)} }},
	"set-password":    {flags: func(fs *flag.FlagSet) options { return &setPasswordCmd{email: emailFlag(fs)} }},
	"disable-user":    {flags: func(fs *flag.FlagSet) options { return &disableUserCmd{email: emailFlag(fs)} }},
	"add-member":      {flags: newAddMember},
	"list-members": {flags: func(fs *flag.FlagSet) options {
		return &listMembersCmd{project: fs.String("project", "", "project slug")}
	}},
}

// ---- migrate

type migrateCmd struct{}

func (*migrateCmd) validate() error { return nil }

func (*migrateCmd) run(e *env) error {
	res, err := postgres.Migrate(e.ctx, e.pool, migrations.Postgres())
	if err != nil {
		return err
	}
	if len(res.Applied) == 0 {
		fmt.Fprintf(e.stdout, "database is up to date (version %04d)\n", res.Current)
		return nil
	}
	for _, name := range res.Applied {
		fmt.Fprintf(e.stdout, "applied %s\n", name)
	}
	fmt.Fprintf(e.stdout, "database is at version %04d\n", res.Current)
	return nil
}

// ---- projects

type createProjectCmd struct{ name, slug *string }

func newCreateProject(fs *flag.FlagSet) options {
	return &createProjectCmd{name: fs.String("name", "", "display name"), slug: fs.String("slug", "", "url-safe id")}
}

func (c *createProjectCmd) validate() error {
	if *c.name == "" || *c.slug == "" {
		return usagef("--name and --slug are required")
	}
	if err := postgres.ValidateName(*c.name); err != nil {
		return usagef("--name: %v", err)
	}
	if err := postgres.ValidateSlug(*c.slug); err != nil {
		return usagef("--slug: %v", err)
	}
	return nil
}

func (c *createProjectCmd) run(e *env) error {
	p, err := e.store.CreateProject(e.ctx, *c.name, *c.slug)
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "created project %s (%s) id %s\n", p.Slug, p.Name, p.ID)
	return nil
}

type listProjectsCmd struct{}

func (*listProjectsCmd) validate() error { return nil }

func (*listProjectsCmd) run(e *env) error {
	ps, err := e.store.ListProjects(e.ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SLUG\tNAME\tID\tCREATED\tSTATUS")
	for _, p := range ps {
		status := "active"
		if p.DisabledAt != nil {
			status = "disabled " + fmtTime(p.DisabledAt)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.Slug, p.Name, p.ID, fmtTime(&p.CreatedAt), status)
	}
	return tw.Flush()
}

type disableProjectCmd struct{ slug *string }

func newDisableProject(fs *flag.FlagSet) options {
	return &disableProjectCmd{slug: fs.String("slug", "", "project slug")}
}

func (c *disableProjectCmd) validate() error {
	if *c.slug == "" {
		return usagef("--slug is required")
	}
	return postgres.ValidateSlug(*c.slug)
}

func (c *disableProjectCmd) run(e *env) error {
	p, err := e.store.DisableProject(e.ctx, *c.slug)
	if err != nil {
		return fmt.Errorf("project %q: %w", *c.slug, err)
	}
	fmt.Fprintf(e.stdout, "disabled project %s at %s; its keys no longer verify\n", p.Slug, fmtTime(p.DisabledAt))
	return nil
}

// ---- keys

type createKeyCmd struct {
	project, kind, name *string
	expiresIn           *time.Duration
}

func newCreateKey(fs *flag.FlagSet) options {
	return &createKeyCmd{
		project:   fs.String("project", "", "project slug"),
		kind:      fs.String("kind", "", "ingest or query"),
		name:      fs.String("name", "", "what the key is for"),
		expiresIn: fs.Duration("expires-in", 0, "expire after this long (e.g. 720h); 0 = never"),
	}
}

func (c *createKeyCmd) validate() error {
	if *c.project == "" || *c.kind == "" || *c.name == "" {
		return usagef("--project, --kind and --name are required")
	}
	if err := postgres.ValidateSlug(*c.project); err != nil {
		return usagef("--project: %v", err)
	}
	if _, err := keys.ParseKind(*c.kind); err != nil {
		return usagef("--kind: %v", err)
	}
	if err := postgres.ValidateName(*c.name); err != nil {
		return usagef("--name: %v", err)
	}
	if *c.expiresIn < 0 {
		return usagef("--expires-in must not be negative")
	}
	return nil
}

func (c *createKeyCmd) run(e *env) error {
	kind, _ := keys.ParseKind(*c.kind)
	var expires *time.Time
	if *c.expiresIn > 0 {
		t := e.now().Add(*c.expiresIn).UTC()
		expires = &t
	}
	g, info, err := e.store.IssueKey(e.ctx, *c.project, kind, *c.name, expires)
	if err != nil {
		return err
	}
	// The warning goes to stderr and the key alone to stdout, so
	// KEY=$(admin create-key ...) captures only the key.
	fmt.Fprintf(e.stderr, "Created %s key %q for project %s (prefix %s, expires %s).\n", info.Kind, info.Name, info.ProjectSlug, info.Prefix, fmtTime(info.ExpiresAt))
	fmt.Fprintln(e.stderr, "WARNING: the key is shown only this once and cannot be recovered. Store it in a secret manager now.")
	fmt.Fprintln(e.stdout, g.Plaintext)
	return nil
}

type listKeysCmd struct{ project *string }

func newListKeys(fs *flag.FlagSet) options {
	return &listKeysCmd{project: fs.String("project", "", "project slug")}
}

func (c *listKeysCmd) validate() error {
	if *c.project == "" {
		return usagef("--project is required")
	}
	return postgres.ValidateSlug(*c.project)
}

func (c *listKeysCmd) run(e *env) error {
	ks, err := e.store.ListKeys(e.ctx, *c.project)
	if err != nil {
		return err
	}
	now := e.now()
	tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PREFIX\tKIND\tNAME\tCREATED\tEXPIRES\tLAST USED\tSTATUS")
	for _, k := range ks {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", k.Prefix, k.Kind, k.Name, fmtTime(&k.CreatedAt), fmtTime(k.ExpiresAt), fmtTime(k.LastUsedAt), k.Status(now))
	}
	return tw.Flush()
}

type revokeKeyCmd struct{ prefix *string }

func newRevokeKey(fs *flag.FlagSet) options {
	return &revokeKeyCmd{prefix: fs.String("prefix", "", "key prefix (the 8 characters after lsk_<kind>_)")}
}

func (c *revokeKeyCmd) validate() error {
	if *c.prefix == "" {
		return usagef("--prefix is required")
	}
	if strings.HasPrefix(*c.prefix, "lsk_") {
		return usagef("--prefix takes only the 8-character prefix, not the key")
	}
	if len(*c.prefix) != 8 || strings.Trim(*c.prefix, "abcdefghijklmnopqrstuvwxyz234567") != "" {
		return usagef("--prefix must be 8 characters a-z, 2-7")
	}
	return nil
}

func (c *revokeKeyCmd) run(e *env) error {
	k, err := e.store.RevokeKey(e.ctx, *c.prefix)
	if err != nil {
		return fmt.Errorf("key %s: %w", *c.prefix, err)
	}
	fmt.Fprintf(e.stdout, "revoked %s key %s (%q) of project %s at %s\n", k.Kind, k.Prefix, k.Name, k.ProjectSlug, fmtTime(k.RevokedAt))
	return nil
}

var errInvalid = errors.New("invalid")

type verifyKeyCmd struct{ kind *string }

func newVerifyKey(fs *flag.FlagSet) options {
	return &verifyKeyCmd{kind: fs.String("kind", "", "require this kind (ingest or query)")}
}

func (c *verifyKeyCmd) validate() error {
	if *c.kind != "" {
		if _, err := keys.ParseKind(*c.kind); err != nil {
			return usagef("--kind: %v", err)
		}
	}
	return nil
}

// run reads the key from LOGSAGE_API_KEY or the first line of stdin (never
// from a flag, so it stays out of shell history) and never prints it.
func (c *verifyKeyCmd) run(e *env) error {
	key := strings.TrimSpace(e.getenv(keyEnvName))
	if key == "" {
		line, err := bufio.NewReader(io.LimitReader(e.stdin, maxKeyInput)).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read key from stdin: %w", err)
		}
		key = strings.TrimSpace(line)
	}
	id, err := keys.NewVerifier(e.store, nil).Verify(e.ctx, key, keys.Kind(*c.kind))
	if errors.Is(err, keys.ErrInvalidKey) {
		fmt.Fprintln(e.stdout, "invalid")
		return errInvalid
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "valid project=%s kind=%s\n", id.ProjectSlug, id.Kind)
	return nil
}

func fmtTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04Z")
}
