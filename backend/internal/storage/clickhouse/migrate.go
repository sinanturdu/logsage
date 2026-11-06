package clickhouse

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"text/template"
)

// MigrationData is the template data available to migration files.
type MigrationData struct {
	Database string // validated identifier
	TTLDays  int
}

// Migrate executes every *.sql file in fsys in name order. Each file holds
// one statement and is a text/template rendered with data. Files must be
// idempotent (IF NOT EXISTS), so Migrate can run on every startup.
func (c *Client) Migrate(ctx context.Context, fsys fs.FS, data MigrationData) error {
	if !identifier.MatchString(data.Database) {
		return fmt.Errorf("clickhouse: invalid database name %q", data.Database)
	}
	if data.TTLDays <= 0 {
		return errors.New("clickhouse: TTL days must be positive")
	}
	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return fmt.Errorf("clickhouse: list migrations: %w", err)
	}
	if len(names) == 0 {
		return errors.New("clickhouse: no migrations found")
	}
	sort.Strings(names)

	for _, name := range names {
		stmt, err := renderMigration(fsys, name, data)
		if err != nil {
			return err
		}
		if err := c.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("clickhouse: migration %s: %w", name, err)
		}
	}
	return nil
}

func renderMigration(fsys fs.FS, name string, data MigrationData) (string, error) {
	raw, err := fs.ReadFile(fsys, name)
	if err != nil {
		return "", fmt.Errorf("clickhouse: read migration %s: %w", name, err)
	}
	tmpl, err := template.New(name).Option("missingkey=error").Parse(string(raw))
	if err != nil {
		return "", fmt.Errorf("clickhouse: parse migration %s: %w", name, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("clickhouse: render migration %s: %w", name, err)
	}
	stmt := strings.TrimSpace(buf.String())
	stmt = strings.TrimSuffix(stmt, ";")
	if stmt == "" {
		return "", fmt.Errorf("clickhouse: migration %s is empty", name)
	}
	return stmt, nil
}
