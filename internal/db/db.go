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
	sql *sql.DB
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
		return nil, errors.Join(err, conn.Close())
	}
	if err := conn.Ping(); err != nil {
		return nil, errors.Join(fmt.Errorf("failed to ping database: %w", err), conn.Close())
	}
	if !isDSN {
		if err := os.Chmod(dbPath, 0o600); err != nil {
			return nil, errors.Join(fmt.Errorf("failed to secure database file: %w", err), conn.Close())
		}
	}

	database := &DB{sql: conn}
	if err := database.runMigrations(context.Background(), gitman.FS, "migrations"); err != nil {
		return nil, errors.Join(fmt.Errorf("migrations failed: %w", err), database.Close())
	}
	return database, nil
}

func (db *DB) Close() error {
	return db.sql.Close()
}

func (db *DB) PingContext(ctx context.Context) error {
	return db.sql.PingContext(ctx)
}

func (db *DB) SchemaVersion(ctx context.Context) (int, error) {
	return currentVersion(ctx, db.sql)
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

func isSQLiteUniqueConstraint(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	code := sqliteErr.Code()
	return code == sqlite3.SQLITE_CONSTRAINT_UNIQUE || code == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
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

// BackupTo creates a SQLite snapshot at destination using VACUUM INTO.
// The destination must not already exist.
func (db *DB) BackupTo(ctx context.Context, destination string) error {
	if _, err := os.Stat(destination); err == nil {
		return fmt.Errorf("destination database already exists: %s", destination)
	} else if !os.IsNotExist(err) {
		return err
	}
	literal := "'" + strings.ReplaceAll(destination, "'", "''") + "'"
	_, err := db.sql.ExecContext(ctx, "VACUUM INTO "+literal)
	return err
}
