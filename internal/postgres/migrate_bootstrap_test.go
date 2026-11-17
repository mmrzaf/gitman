package postgres

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/mmrzaf/gitman/internal/postgres/testschema"
)

// Installation must not overwrite unrelated schemas.
func TestRejectNonemptySchemaWithoutModifyingIt(t *testing.T) {
	database := testDB(t)
	ctx := context.Background()
	if _, err := database.Pool.Exec(ctx, `CREATE TABLE operator_data (id text PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("expected nonempty-schema refusal, got %v", err)
	}
	if _, err := database.MigrationStatus(ctx); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("status accepted foreign schema: %v", err)
	}
	var hasLedger, hasOriginal bool
	if err := database.Pool.QueryRow(ctx, `SELECT to_regclass('schema_migrations') IS NOT NULL, to_regclass('operator_data') IS NOT NULL`).Scan(&hasLedger, &hasOriginal); err != nil {
		t.Fatal(err)
	}
	if hasLedger || !hasOriginal {
		t.Fatalf("refusal mutated original schema: ledger=%v original=%v", hasLedger, hasOriginal)
	}
}

func TestRejectChangedMigrationFingerprint(t *testing.T) {
	database := testDB(t)
	ctx := context.Background()
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `UPDATE schema_migrations SET checksum = 'tampered' WHERE version = (SELECT min(version) FROM schema_migrations)`); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("accepted modified migration fingerprint: %v", err)
	}
	if _, err := database.MigrationStatus(ctx); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("status accepted modified migration fingerprint: %v", err)
	}
}

// Uses independent connections to the same isolated PostgreSQL schema,
// just like independently starting web and worker processes do.
func TestConcurrentMigrationAcrossPools(t *testing.T) {
	ctx := context.Background()
	dsn := testschema.DSN(t)
	first, err := Connect(ctx, dsn, Options{MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(first.Close)
	second, err := Connect(ctx, dsn, Options{MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(second.Close)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, db := range []*DB{first, second} {
		wg.Add(1)
		go func(db *DB) { defer wg.Done(); errs <- db.Migrate(ctx) }(db)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("simultaneous database startup: %v", err)
		}
	}
	var count int
	if err := first.Pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if count != len(migrations) {
		t.Fatalf("concurrent processes applied %d migrations, want %d", count, len(migrations))
	}
}

func TestInitialSchemaSupportsRepositoryAndImageOperations(t *testing.T) {
	database := testDB(t)
	ctx := context.Background()
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO repos (id, name) VALUES ('r1', 'demo')`,
		`INSERT INTO workers (id, hostname) VALUES ('w1', 'worker')`,
		`INSERT INTO repository_operations (id, repo_id, kind, name, payload) VALUES ('op1', 'r1', 'head', '', '{}')`,
		`INSERT INTO repository_operations (id, kind, name, payload, engine_id) VALUES ('op2', 'image.remove', 'sample', '{}', 'engine')`,
		`INSERT INTO image_hosts (host_id, worker_id) VALUES ('engine', 'w1')`,
	} {
		if _, err := database.Pool.Exec(ctx, query); err != nil {
			t.Fatalf("baseline failed for %s: %v", query, err)
		}
	}
	for _, query := range []string{
		`INSERT INTO repository_operations (id, kind, payload) VALUES ('bad1', 'head', '{}')`,
		`INSERT INTO repository_operations (id, repo_id, kind, payload) VALUES ('bad2', 'r1', 'image.prune', '{}')`,
		`INSERT INTO repository_operations (id, kind, payload) VALUES ('bad3', 'unknown', '{}')`,
	} {
		if _, err := database.Pool.Exec(ctx, query); err == nil {
			t.Fatalf("accepted invalid operation: %s", query)
		}
	}
}
