// Package migrations embeds the SQL migration files so that cmd/migrate and the
// API's optional DB_AUTO_MIGRATE path work from a compiled binary, with no
// dependency on the source tree or on the working directory.
//
// The .sql files stay at the repository root (docs/DATA-MODEL.md §5) and are
// embedded from this package because go:embed cannot reach parent directories.
package migrations

import "embed"

// FS holds every migration pair, e.g. 000001_init.up.sql / .down.sql.
//
//go:embed *.sql
var FS embed.FS
