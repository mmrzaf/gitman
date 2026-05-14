package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mmrzaf/gitman"
	_ "modernc.org/sqlite"
)

type DB struct {
	*sql.DB
}

// InitDB opens the SQLite database, applies WAL mode, and runs any pending migrations.
func InitDB(dbPath string) (*DB, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create db directory: %w", err)
	}

	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// WAL mode allows concurrent readers alongside the single writer.
	// busy_timeout prevents immediate SQLITE_BUSY errors under write contention.
	// synchronous=NORMAL is safe with WAL and faster than the default FULL.
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=NORMAL",
	}
	for _, p := range pragmas {
		if _, err := conn.ExecContext(context.Background(), p); err != nil {
			if closeErr := conn.Close(); closeErr != nil {
				return nil, fmt.Errorf("failed to set %q: %w (close: %v)", p, err, closeErr)
			}
			return nil, fmt.Errorf("failed to set %q: %w", p, err)
		}
	}

	if err := conn.Ping(); err != nil {
		if closeErr := conn.Close(); closeErr != nil {
			return nil, fmt.Errorf("failed to ping database: %w (close: %v)", err, closeErr)
		}
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	db := &DB{conn}
	if err := db.runMigrations(context.Background(), gitman.FS, "migrations"); err != nil {
		if closeErr := db.Close(); closeErr != nil {
			return nil, fmt.Errorf("migrations failed: %w (close: %v)", err, closeErr)
		}
		return nil, fmt.Errorf("migrations failed: %w", err)
	}
	return db, nil
}
