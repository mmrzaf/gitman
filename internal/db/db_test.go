package db

import (
	"context"
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
