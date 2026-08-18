package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type migrationFile struct {
	version int
	up      string
	name    string
}

type migrationConn interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// runMigrations applies forward-only schema changes while holding an immediate
// SQLite write lock. Gitman rollback is restore-from-backup, not schema rewind.
func (db *DB) runMigrations(ctx context.Context, migrationsFS embed.FS, dir string) (err error) {
	files, err := loadMigrationFiles(migrationsFS, dir)
	if err != nil {
		return err
	}
	conn, err := db.sql.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin migration lock: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if _, rollbackErr := conn.ExecContext(context.Background(), "ROLLBACK"); rollbackErr != nil && !strings.Contains(strings.ToLower(rollbackErr.Error()), "no transaction") {
			err = errors.Join(err, fmt.Errorf("rollback migration transaction: %w", rollbackErr))
		}
	}()

	if err := ensureMigrationTable(ctx, conn); err != nil {
		return err
	}
	current, err := currentVersion(ctx, conn)
	if err != nil {
		return err
	}
	if err := validateMigrationHistory(ctx, conn, current, files); err != nil {
		return err
	}
	for _, migration := range files {
		if migration.version <= current {
			continue
		}
		if _, err := conn.ExecContext(ctx, migration.up); err != nil {
			return fmt.Errorf("migration %d (%s) failed: %w", migration.version, migration.name, err)
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO schema_migrations (version) VALUES (?)", migration.version); err != nil {
			return fmt.Errorf("record migration %d: %w", migration.version, err)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

func ensureMigrationTable(ctx context.Context, conn migrationConn) error {
	_, err := conn.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at INTEGER DEFAULT (strftime('%s', 'now'))
		)
	`)
	return err
}

func currentVersion(ctx context.Context, conn migrationConn) (int, error) {
	var version int
	err := conn.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version)
	return version, err
}

func validateMigrationHistory(ctx context.Context, conn migrationConn, current int, files []migrationFile) error {
	latest := 0
	if len(files) > 0 {
		latest = files[len(files)-1].version
	}
	if current > latest {
		return fmt.Errorf("database schema version %d is newer than this Gitman binary (latest %d)", current, latest)
	}
	if current == 0 {
		return nil
	}
	var applied int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM schema_migrations WHERE version BETWEEN 1 AND ?", current,
	).Scan(&applied); err != nil {
		return fmt.Errorf("validate migration history: %w", err)
	}
	if applied != current {
		return fmt.Errorf("database migration history is incomplete: highest version is %d but only %d version(s) are recorded", current, applied)
	}
	return nil
}

func loadMigrationFiles(fsys embed.FS, dir string) ([]migrationFile, error) {
	entries, err := fsys.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	seen := make(map[int]struct{})
	files := make([]migrationFile, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(name, ".up.sql") {
			if strings.HasSuffix(name, ".sql") {
				return nil, fmt.Errorf("unsupported migration file %q; migrations are forward-only .up.sql files", name)
			}
			continue
		}
		parts := strings.SplitN(name, "_", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid migration filename %q", name)
		}
		version, err := strconv.Atoi(parts[0])
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("invalid migration version in %q", name)
		}
		if _, exists := seen[version]; exists {
			return nil, fmt.Errorf("duplicate migration version %d", version)
		}
		content, err := fsys.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		seen[version] = struct{}{}
		files = append(files, migrationFile{
			version: version,
			up:      string(content),
			name:    strings.TrimSuffix(parts[1], ".up.sql"),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].version < files[j].version })
	for i, file := range files {
		expected := i + 1
		if file.version != expected {
			return nil, fmt.Errorf("migration sequence is incomplete: expected version %d, found %d", expected, file.version)
		}
	}
	return files, nil
}
