package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/mmrzaf/gitman"
)

func TestInitDBNew(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer db.Close()

	var mode string
	err = db.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&mode)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("expected journal_mode=wal, got %q", mode)
	}

	var count int
	err = db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM schema_migrations").Scan(&count)
	if err != nil {
		t.Fatal(err)
	}

	migrations, err := loadMigrationFiles(gitman.FS, "migrations")
	if err != nil {
		t.Fatalf("loadMigrationFiles failed: %v", err)
	}
	if count != len(migrations) {
		t.Errorf("expected %d applied migrations, got %d", len(migrations), count)
	}
}

func TestInitDBReopen(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db1, err := InitDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	db2, err := InitDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
}

func TestInitDBInvalidPath(t *testing.T) {
	_, err := InitDB("/nonexistent/dir/db.sqlite")
	if err == nil {
		t.Error("expected error for invalid path")
	}
}

func TestPing(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	if err := db.Ping(); err != nil {
		t.Errorf("ping failed: %v", err)
	}
}

func TestRollbackTo(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer db.Close()

	if err := db.rollbackTo(context.Background(), gitman.FS, "migrations", 0); err != nil {
		t.Fatalf("rollbackTo failed: %v", err)
	}

	var count int
	if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected 0 applied migrations after rollback, got %d", count)
	}
}

func setupTestDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	return db
}

func TestInitDBBasenameDoesNotChmodWorkingDirectory(t *testing.T) {
	cwd, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	database, err := InitDB("gitman.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	_ = database.Close()
	info, err := os.Stat(".")
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("working directory mode changed to %o", got)
	}
}

func TestInitDBMemoryDSN(t *testing.T) {
	database, err := InitDB(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Ping(); err != nil {
		t.Fatal(err)
	}
}

func TestInitDBConcurrentMigration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concurrent.sqlite")
	start := make(chan struct{})
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			database, err := InitDB(dbPath)
			if err == nil {
				err = database.Close()
			}
			errs <- err
		}()
	}
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent InitDB failed: %v", err)
		}
	}
}

func TestInitDBAdoptsIntermediateLeaseSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "intermediate.sqlite")
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	initSQL, err := gitman.FS.ReadFile("migrations/001_init.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(string(initSQL)); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at INTEGER DEFAULT (strftime('%s', 'now'))
		);
		INSERT INTO schema_migrations (version) VALUES (1);
		ALTER TABLE ci_runs ADD COLUMN started_at INTEGER;
		ALTER TABLE ci_runs ADD COLUMN heartbeat_at INTEGER;
		ALTER TABLE ci_runs ADD COLUMN attempt_id TEXT NOT NULL DEFAULT '';
	`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB failed to adopt intermediate schema: %v", err)
	}
	defer database.Close()
	var version int
	if err := database.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 3 {
		t.Fatalf("expected schema version 3, got %d", version)
	}
	for _, column := range []string{"started_at", "heartbeat_at", "attempt_id", "cancel_reason"} {
		var count int
		if err := database.QueryRow("SELECT COUNT(*) FROM pragma_table_info('ci_runs') WHERE name = ?", column).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("expected column %s exactly once, got %d", column, count)
		}
	}
}
