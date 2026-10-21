package collab

import (
	_ "embed"
	"fmt"
	"strings"

	"windshift/internal/database"
)

//go:embed schema_sqlite.sql
var schemaSQLite string

//go:embed schema_postgres.sql
var schemaPostgres string

// ensureSchema creates the co-editing tables if they do not exist. The tables
// are additive and only referenced by this package, so creating them at
// startup instead of through the migration catalog keeps co-editing out of
// upstream schema files. Later changes must stay idempotent (IF NOT EXISTS,
// additive columns).
func ensureSchema(db database.Database) error {
	ddl := schemaSQLite
	if database.IsPostgresDriver(db.GetDriverName()) {
		ddl = schemaPostgres
	}
	for _, statement := range splitStatements(ddl) {
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("collab schema: %w", err)
		}
	}
	return nil
}

// splitStatements splits DDL on semicolons, dropping comments and blanks.
func splitStatements(ddl string) []string {
	var lines []string
	for _, line := range strings.Split(ddl, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "--") {
			lines = append(lines, line)
		}
	}
	var statements []string
	for _, part := range strings.Split(strings.Join(lines, "\n"), ";") {
		if s := strings.TrimSpace(part); s != "" {
			statements = append(statements, s)
		}
	}
	return statements
}
