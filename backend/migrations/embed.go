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
