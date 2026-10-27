package postgres_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
)

// TestSchemaGolden pins the migrated schema. A deliberate migration updates
// the golden with UPDATE_SCHEMA_GOLDEN=1 go test ./internal/postgres; an
// accidental drift fails instead of reaching a release.
func TestSchemaGolden(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)

	var schema string
	if err := database.Pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	rows, err := database.Pool.Query(ctx, `
		SELECT table_name, column_name, data_type, is_nullable, COALESCE(column_default, '')
		FROM information_schema.columns
		WHERE table_schema = $1
		ORDER BY table_name, ordinal_position
	`, schema)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var table, column, typ, nullable, def string
		if err := rows.Scan(&table, &column, &typ, &nullable, &def); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "column %s.%s %s null=%s default=%s\n", table, column, typ, nullable, def)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	indexes, err := database.Pool.Query(ctx, `
		SELECT tablename, indexname FROM pg_indexes
		WHERE schemaname = $1 ORDER BY tablename, indexname
	`, schema)
	if err != nil {
		t.Fatal(err)
	}
	for indexes.Next() {
		var table, index string
		if err := indexes.Scan(&table, &index); err != nil {
			indexes.Close()
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "index %s %s\n", table, index)
	}
	indexes.Close()
	if err := indexes.Err(); err != nil {
		t.Fatal(err)
	}

	golden := filepath.Join("testdata", "schema.golden")
	if os.Getenv("UPDATE_SCHEMA_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if b.String() != string(want) {
		t.Fatalf("schema differs from %s; if this migration is intentional, update it with UPDATE_SCHEMA_GOLDEN=1", golden)
	}
}
