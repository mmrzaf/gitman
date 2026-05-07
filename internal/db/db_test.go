package db

import (
	"context"
	"path/filepath"
	"testing"
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
	if count < 2 {
		t.Errorf("expected at least 2 migrations, got %d", count)
	}
}

func TestInitDBReopen(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db1, err := InitDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db1.Close()

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
