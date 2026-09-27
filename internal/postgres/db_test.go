package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
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

// TestAcquireTimeoutFailsFastWhenThePoolIsExhausted is a caller that
// cannot even get a connection within AcquireTimeout: it fails with
// ErrUnavailable in bounded time, on both Q and Tx, rather than waiting
// as long as its own (here, unbounded) context would otherwise allow.
func TestAcquireTimeoutFailsFastWhenThePoolIsExhausted(t *testing.T) {
	ctx := context.Background()
	url := os.Getenv("GITMAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GITMAN_TEST_DATABASE_URL not set; skipping a test that requires PostgreSQL")
	}
	database, err := Connect(ctx, url, Options{MaxConns: 1, AcquireTimeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(database.Close)

	held, err := database.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	start := time.Now()
	_, err = database.Q.Query(ctx, "SELECT 1")
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Query took %s to fail, want it bounded by the acquire timeout", elapsed)
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Query while the pool is exhausted = %v, want ErrUnavailable", err)
	}

	if err := database.Tx(ctx, func(tx Tx) error { return nil }); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Tx while the pool is exhausted = %v, want ErrUnavailable", err)
	}
}

// TestAcquireTimeoutStaysBoundedUnderSustainedContention covers a real
// past bug: acquiring a connection just to check one was available, then
// releasing it and asking Pool for a separate one to actually use, left a
// gap between the two acquires for another waiter to queue ahead of the
// real one — so under sustained contention, a caller could be repeatedly
// bumped to the back of Pool's own wait queue by its own probe-then-
// reacquire dance, adding unbounded extra latency on top of what
// AcquireTimeout was supposed to cap. That doesn't hang outright — Pool
// still eventually serves everyone — so the regression this guards
// against is a throughput one: measured against the pre-fix code, this
// workload reliably took upward of 2.5s (every waiter repeatedly
// re-queuing behind the others), against ~1.1-1.2s fixed. secondsBudget
// sits with margin on both sides of that gap.
func TestAcquireTimeoutStaysBoundedUnderSustainedContention(t *testing.T) {
	const secondsBudget = 2 * time.Second

	ctx := context.Background()
	url := os.Getenv("GITMAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GITMAN_TEST_DATABASE_URL not set; skipping a test that requires PostgreSQL")
	}
	database, err := Connect(ctx, url, Options{MaxConns: 2, AcquireTimeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(database.Close)

	const goroutines, roundsEach = 30, 4
	done := make(chan error, goroutines)
	start := time.Now()
	for i := 0; i < goroutines; i++ {
		go func() {
			for j := 0; j < roundsEach; j++ {
				// context.Background() on purpose: nothing about the
				// caller's own context should be what bounds this.
				_, err := database.Q.Exec(context.Background(), "SELECT pg_sleep(0.05)")
				if err != nil && !errors.Is(err, ErrUnavailable) {
					done <- fmt.Errorf("round %d: %w", j, err)
					return
				}
			}
			done <- nil
		}()
	}
	for i := 0; i < goroutines; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(15 * time.Second):
			t.Fatalf("still waiting on goroutines after 15s under a 200ms acquire timeout: the fix regressed")
		}
	}
	if elapsed := time.Since(start); elapsed > secondsBudget {
		t.Fatalf("%d goroutines x %d rounds took %s, want under %s: probe-then-reacquire latency may have regressed",
			goroutines, roundsEach, elapsed, secondsBudget)
	}
	t.Logf("%d goroutines x %d rounds against 2 connections finished in %s", goroutines, roundsEach, time.Since(start))
}
