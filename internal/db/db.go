package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

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
	if err := db.migrate(); err != nil {
		if closeErr := conn.Close(); closeErr != nil {
			return nil, fmt.Errorf("failed to run migrations: %w (close: %v)", err, closeErr)
		}
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	return db, nil
}

// migrations is an ordered, append-only list of SQL migration steps.
// The index+1 is the version number. Never remove or reorder entries.
var migrations = []string{

	// ── Version 1 ── base schema ────────────────────────────────────────────
	`CREATE TABLE IF NOT EXISTS users (
		id           TEXT PRIMARY KEY,
		username     TEXT UNIQUE NOT NULL,
		password_hash TEXT NOT NULL,
		created_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at   DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS sessions (
		token      TEXT PRIMARY KEY,
		user_id    TEXT NOT NULL,
		expires_at INTEGER NOT NULL,
		FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS repositories (
		id          TEXT PRIMARY KEY,
		owner_id    TEXT NOT NULL,
		name        TEXT NOT NULL,
		description TEXT,
		is_private  BOOLEAN DEFAULT 0,
		created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(owner_id) REFERENCES users(id),
		UNIQUE(owner_id, name)
	);

	CREATE TABLE IF NOT EXISTS repo_collaborators (
		repo_id      TEXT NOT NULL,
		user_id      TEXT NOT NULL,
		access_level TEXT NOT NULL,
		created_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (repo_id, user_id),
		FOREIGN KEY(repo_id) REFERENCES repositories(id) ON DELETE CASCADE,
		FOREIGN KEY(user_id) REFERENCES users(id)      ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS access_tokens (
		id         TEXT PRIMARY KEY,
		user_id    TEXT NOT NULL,
		name       TEXT NOT NULL,
		token_hash TEXT NOT NULL UNIQUE,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS ssh_keys (
		id         TEXT PRIMARY KEY,
		user_id    TEXT NOT NULL,
		name       TEXT NOT NULL,
		public_key TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
	);`,

	// ── Version 2 ── CI/CD tables ────────────────────────────────────────────
	`CREATE TABLE IF NOT EXISTS ci_runs (
		id           TEXT PRIMARY KEY,
		repo_id      TEXT NOT NULL,
		commit_hash  TEXT NOT NULL DEFAULT '',
		branch       TEXT NOT NULL DEFAULT '',
		tag          TEXT NOT NULL DEFAULT '',
		event        TEXT NOT NULL DEFAULT 'push',
		status       TEXT NOT NULL DEFAULT 'pending',
		log_file     TEXT NOT NULL DEFAULT '',
		created_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
		completed_at DATETIME,
		FOREIGN KEY(repo_id) REFERENCES repositories(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS repo_secrets (
		id              TEXT PRIMARY KEY,
		repo_id         TEXT NOT NULL,
		key             TEXT NOT NULL,
		encrypted_value TEXT NOT NULL,
		created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(repo_id, key),
		FOREIGN KEY(repo_id) REFERENCES repositories(id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_ci_runs_repo_status  ON ci_runs(repo_id, status);
	CREATE INDEX IF NOT EXISTS idx_ci_runs_branch       ON ci_runs(repo_id, branch);
	CREATE INDEX IF NOT EXISTS idx_ci_runs_tag          ON ci_runs(repo_id, tag);
	CREATE INDEX IF NOT EXISTS idx_ci_runs_commit       ON ci_runs(repo_id, commit_hash);
	CREATE INDEX IF NOT EXISTS idx_ci_runs_created      ON ci_runs(created_at DESC);`,
}

// migrate creates the schema_migrations tracking table and applies any
// unapplied migrations in order.
func (db *DB) migrate() error {
	ctx := context.Background()

	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	var currentVersion int
	if err := db.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations",
	).Scan(&currentVersion); err != nil {
		return fmt.Errorf("query schema version: %w", err)
	}

	for i, sql := range migrations {
		version := i + 1
		if version <= currentVersion {
			continue
		}
		if _, err := db.ExecContext(ctx, sql); err != nil {
			return fmt.Errorf("apply migration v%d: %w", version, err)
		}
		if _, err := db.ExecContext(ctx,
			"INSERT INTO schema_migrations (version) VALUES (?)", version,
		); err != nil {
			return fmt.Errorf("record migration v%d: %w", version, err)
		}
	}

	return nil
}
