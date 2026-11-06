// Package migrations embeds the SQL migrations applied by LogSage binaries.
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed clickhouse/*.sql
var clickhouseFS embed.FS

// ClickHouse returns the ClickHouse migrations, one statement per file,
// applied in name order.
func ClickHouse() fs.FS {
	sub, err := fs.Sub(clickhouseFS, "clickhouse")
	if err != nil {
		// Unreachable: the directory is embedded at compile time.
		panic(err)
	}
	return sub
}

//go:embed postgres/*.sql
var postgresFS embed.FS

// Postgres returns the PostgreSQL migrations (NNNN_name.sql, several
// statements per file allowed), applied in version order by
// internal/storage/postgres.Migrate.
func Postgres() fs.FS {
	sub, err := fs.Sub(postgresFS, "postgres")
	if err != nil {
		panic(err) // unreachable: embedded at compile time
	}
	return sub
}
