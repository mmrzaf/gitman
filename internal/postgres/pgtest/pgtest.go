// Package pgtest gives tests a clean, migrated PostgreSQL database. It
// is imported only by tests, so the testing package never reaches the
// gitman binary.
package pgtest

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/postgres"
)

// Open connects to the database named by GITMAN_TEST_DATABASE_URL,
// applies migrations, and empties every application table but instance,
// whose one row the migration writes, or skips the calling test when
// that variable is unset. The connection is closed
// automatically when the test ends.
//
// Tests that use Open must not run in parallel with each other: they
// share one database and each starts by emptying it.
func Open(t *testing.T) *postgres.DB {
	t.Helper()
	url := os.Getenv("GITMAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GITMAN_TEST_DATABASE_URL not set; skipping a test that requires PostgreSQL")
	}
	ctx := context.Background()
	database, err := postgres.Connect(ctx, url, postgres.Options{MaxConns: 8})
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(database.Close)
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	reset(t, database)
	return database
}

func reset(t *testing.T, database *postgres.DB) {
	t.Helper()
	ctx := context.Background()
	rows, err := database.Pool.Query(ctx, `
		SELECT tablename FROM pg_tables
		WHERE schemaname = current_schema() AND tablename NOT IN ('schema_migrations', 'instance')
	`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatalf("scan table name: %v", err)
		}
		tables = append(tables, `"`+name+`"`)
	}
	rows.Close()
	if len(tables) == 0 {
		return
	}
	if _, err := database.Pool.Exec(ctx, "TRUNCATE TABLE "+strings.Join(tables, ", ")+" CASCADE"); err != nil {
		t.Fatalf("truncate tables: %v", err)
	}
}
