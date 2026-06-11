package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mmrzaf/gitman"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
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

	if err := applyConnectionPragmas(context.Background(), conn); err != nil {
		_ = conn.Close()
		return nil, err
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

func applyConnectionPragmas(ctx context.Context, conn *sql.DB) error {
	pragmas := []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
	}
	for _, pragma := range pragmas {
		if err := execPragmaWithBusyRetry(ctx, conn, pragma); err != nil {
			return fmt.Errorf("failed to set %q: %w", pragma, err)
		}
	}
	return nil
}

func execPragmaWithBusyRetry(ctx context.Context, conn *sql.DB, pragma string) error {
	const retryFor = 5 * time.Second

	deadline := time.Now().Add(retryFor)
	delay := 10 * time.Millisecond
	for {
		if _, err := conn.ExecContext(ctx, pragma); err != nil {
			if !isSQLiteBusy(err) || time.Now().After(deadline) {
				return err
			}
		} else {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay < 100*time.Millisecond {
			delay *= 2
		}
	}
}

func isSQLiteBusy(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	code := sqliteErr.Code()
	return code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED
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
