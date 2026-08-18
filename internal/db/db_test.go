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
	err = db.sql.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&mode)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("expected journal_mode=wal, got %q", mode)
	}

	var count int
	err = db.sql.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM schema_migrations").Scan(&count)
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

	var webhookColumns int
	if err := db.sql.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM pragma_table_info('repositories') WHERE name = 'webhook_secret'",
	).Scan(&webhookColumns); err != nil {
		t.Fatal(err)
	}
	if webhookColumns != 0 {
		t.Fatalf("obsolete webhook_secret column still present")
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

func TestHistoricalDataUpgradePreservesCIRuns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "historical.sqlite")
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrationFiles(gitman.FS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.version > 3 {
			break
		}
		if _, err := raw.Exec(migration.up); err != nil {
			t.Fatalf("apply historical migration %d: %v", migration.version, err)
		}
	}
	if _, err := raw.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at INTEGER DEFAULT (strftime('%s', 'now'))
		);
		INSERT INTO schema_migrations (version) VALUES (1), (2), (3);
		INSERT INTO users (id, username, password_hash) VALUES ('user-1', 'historical_user', 'hash');
		INSERT INTO repositories (id, owner_id, name, description) VALUES ('repo-1', 'user-1', 'project', 'kept');
		INSERT INTO ci_runs (
			id, repo_id, commit_hash, branch, event, status, log_file,
			created_at, completed_at, started_at, heartbeat_at, attempt_id, cancel_reason
		) VALUES (
			'run-1', 'repo-1', '0123456789012345678901234567890123456789', 'main',
			'push', 'failed', '/logs/run-1.log', 1700000000, 1700000030,
			1700000010, NULL, 'attempt-1', ''
		);
	`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("upgrade historical database: %v", err)
	}
	defer database.Close()
	var commit, status, logFile, attemptID, statusReason, retryOf, triggerKey string
	if err := database.sql.QueryRow(`
		SELECT commit_hash, status, log_file, attempt_id, status_reason, retry_of_run_id, trigger_key
		FROM ci_runs WHERE id = 'run-1'
	`).Scan(&commit, &status, &logFile, &attemptID, &statusReason, &retryOf, &triggerKey); err != nil {
		t.Fatal(err)
	}
	if commit != "0123456789012345678901234567890123456789" || status != "failed" ||
		logFile != "/logs/run-1.log" || attemptID != "attempt-1" || statusReason != "" || retryOf != "" || triggerKey != "" {
		t.Fatalf("historical CI run changed during upgrade: commit=%q status=%q log=%q attempt=%q reason=%q retry=%q trigger=%q",
			commit, status, logFile, attemptID, statusReason, retryOf, triggerKey)
	}
	var webhookColumns int
	if err := database.sql.QueryRow(
		"SELECT COUNT(*) FROM pragma_table_info('repositories') WHERE name = 'webhook_secret'",
	).Scan(&webhookColumns); err != nil {
		t.Fatal(err)
	}
	if webhookColumns != 0 {
		t.Fatalf("obsolete webhook_secret column survived historical upgrade")
	}
}

func TestInitDBRejectsNewerSchemaVersion(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "newer.sqlite")
	database, err := InitDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.sql.Exec("INSERT INTO schema_migrations (version) VALUES (999)"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := InitDB(dbPath); err == nil {
		t.Fatal("InitDB accepted a database created by a newer schema")
	}
}

func TestInitDBRejectsIncompleteMigrationHistory(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gap.sqlite")
	database, err := InitDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.sql.Exec("DELETE FROM schema_migrations WHERE version = 3"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := InitDB(dbPath); err == nil {
		t.Fatal("InitDB accepted an incomplete migration history")
	}
}
