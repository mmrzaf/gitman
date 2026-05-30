package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mmrzaf/gitman"
	_ "modernc.org/sqlite"
)

type DB struct {
	*sql.DB
}

// InitDB opens the SQLite database, applies connection-local safety pragmas,
// and runs any pending migrations. Gitman intentionally uses one pooled
// SQLite connection so every statement observes the same foreign-key and
// busy-timeout settings.
func InitDB(dbPath string) (*DB, error) {
	isDSN := dbPath == ":memory:" || strings.HasPrefix(dbPath, "file:")
	if !isDSN {
		if err := ensureDatabaseParent(dbPath); err != nil {
			return nil, err
		}
	}

	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)

	pragmas := []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
	}
	for _, pragma := range pragmas {
		if _, err := conn.ExecContext(context.Background(), pragma); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("failed to set %q: %w", pragma, err)
		}
	}
	if err := conn.Ping(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}
	if !isDSN {
		if err := os.Chmod(dbPath, 0o600); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("failed to secure database file: %w", err)
		}
	}

	database := &DB{conn}
	if err := database.runMigrations(context.Background(), gitman.FS, "migrations"); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("migrations failed: %w", err)
	}
	return database, nil
}

func ensureDatabaseParent(dbPath string) error {
	dir := filepath.Dir(dbPath)
	if dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create db directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("failed to secure db directory: %w", err)
	}
	return nil
}
