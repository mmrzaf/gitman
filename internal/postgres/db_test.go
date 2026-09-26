package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
)

func testDB(t *testing.T) *DB {
	t.Helper()
	url := os.Getenv("GITMAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GITMAN_TEST_DATABASE_URL not set; skipping a test that requires PostgreSQL")
	}
	database, err := Connect(context.Background(), url, Options{})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(database.Close)
	return database
}

func TestMigrateAppliesEverythingAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	database := testDB(t)

	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}

	var version int64
	if err := database.Pool.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if want := migrations[len(migrations)-1].version; version != want {
		t.Fatalf("schema version = %d, want %d", version, want)
	}
}

func TestConcurrentMigrate(t *testing.T) {
	ctx := context.Background()
	database := testDB(t)

	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() { errs <- database.Migrate(ctx) }()
	}
	for i := 0; i < 4; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent Migrate: %v", err)
		}
	}
}

func TestErrorNormalization(t *testing.T) {
	ctx := context.Background()
	database := testDB(t)

	conn, err := database.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `CREATE TEMP TABLE db_test_uniques2 (id text PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO db_test_uniques2 (id) VALUES ('a')`); err != nil {
		t.Fatal(err)
	}
	_, dupErr := conn.Exec(ctx, `INSERT INTO db_test_uniques2 (id) VALUES ('a')`)
	if !IsUniqueViolation(dupErr) {
		t.Fatalf("IsUniqueViolation(%v) = false", dupErr)
	}
	if got := NormalizeWrite(dupErr); !errors.Is(got, ErrAlreadyExists) {
		t.Fatalf("NormalizeWrite = %v, want ErrAlreadyExists", got)
	}

	var scanned string
	scanErr := conn.QueryRow(ctx, `SELECT id FROM db_test_uniques2 WHERE id = 'missing'`).Scan(&scanned)
	if got := NormalizeNotFound(scanErr); !errors.Is(got, ErrNotFound) {
		t.Fatalf("NormalizeNotFound = %v, want ErrNotFound", got)
	}
}

func TestTxRollsBackOnError(t *testing.T) {
	ctx := context.Background()
	database := testDB(t)
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	sentinel := errors.New("stop")
	err := database.Tx(ctx, func(tx Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO events (id, action) VALUES ('tx-rollback-test', 'test')`); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Tx error = %v, want the sentinel", err)
	}
	var n int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE id = 'tx-rollback-test'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("expected the insert to be rolled back")
	}
}
