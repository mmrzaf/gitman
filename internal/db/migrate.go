package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type migrationFile struct {
	version int
	up      string
	down    string
	name    string
}

type migrationConn interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// runMigrations serializes schema changes with BEGIN IMMEDIATE. This prevents
// web and worker processes from racing while upgrading the same SQLite file.
func (db *DB) runMigrations(ctx context.Context, migrationsFS embed.FS, dir string) error {
	files, err := loadMigrationFiles(migrationsFS, dir)
	if err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := beginImmediate(ctx, conn); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			rollbackConn(conn)
		}
	}()

	if err := ensureMigrationTable(ctx, conn); err != nil {
		return err
	}
	current, err := currentVersion(ctx, conn)
	if err != nil {
		return err
	}
	for _, m := range files {
		if m.version <= current {
			continue
		}
		slog.Info("applying migration", "version", m.version, "name", m.name)
		if err := applyMigration(ctx, conn, m); err != nil {
			return fmt.Errorf("migration %d (%s) failed: %w", m.version, m.name, err)
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO schema_migrations (version) VALUES (?)", m.version); err != nil {
			return fmt.Errorf("record migration %d: %w", m.version, err)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

func applyMigration(ctx context.Context, conn migrationConn, migration migrationFile) error {
	// An intermediate beta build added lease columns outside the migration
	// ledger. Apply version 2 defensively so those databases can converge on
	// the versioned schema without duplicate-column failures.
	if migration.version == 2 && migration.name == "ci_run_leases" {
		return ensureCIRunLeaseSchema(ctx, conn)
	}
	_, err := conn.ExecContext(ctx, migration.up)
	return err
}

func ensureCIRunLeaseSchema(ctx context.Context, conn migrationConn) error {
	columns := []struct {
		name       string
		definition string
	}{
		{name: "started_at", definition: "INTEGER"},
		{name: "heartbeat_at", definition: "INTEGER"},
		{name: "attempt_id", definition: "TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range columns {
		exists, err := tableColumnExists(ctx, conn, "ci_runs", column.name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		query := fmt.Sprintf("ALTER TABLE ci_runs ADD COLUMN %s %s", column.name, column.definition)
		if _, err := conn.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_ci_runs_heartbeat_at ON ci_runs(heartbeat_at)"); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_ci_runs_attempt_id ON ci_runs(attempt_id)")
	return err
}

func tableColumnExists(ctx context.Context, conn migrationConn, table, column string) (bool, error) {
	if table != "ci_runs" {
		return false, fmt.Errorf("unsupported migration table %q", table)
	}
	var count int
	err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM pragma_table_info('ci_runs') WHERE name = ?",
		column,
	).Scan(&count)
	return count > 0, err
}

// rollbackTo rolls back to a specific version (down migrations) while holding
// an immediate write lock for the complete operation.
func (db *DB) rollbackTo(ctx context.Context, migrationsFS embed.FS, dir string, targetVersion int) error {
	files, err := loadMigrationFiles(migrationsFS, dir)
	if err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := beginImmediate(ctx, conn); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			rollbackConn(conn)
		}
	}()

	if err := ensureMigrationTable(ctx, conn); err != nil {
		return err
	}
	current, err := currentVersion(ctx, conn)
	if err != nil {
		return err
	}
	if current <= targetVersion {
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			return err
		}
		committed = true
		return nil
	}
	for i := len(files) - 1; i >= 0; i-- {
		m := files[i]
		if m.version > current || m.version <= targetVersion {
			continue
		}
		if m.down == "" {
			return fmt.Errorf("no down migration for version %d", m.version)
		}
		slog.Info("rolling back migration", "version", m.version, "name", m.name)
		if _, err := conn.ExecContext(ctx, m.down); err != nil {
			return fmt.Errorf("rollback of %d failed: %w", m.version, err)
		}
		if _, err := conn.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version = ?", m.version); err != nil {
			return fmt.Errorf("delete migration record %d: %w", m.version, err)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

func beginImmediate(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE")
	if err != nil {
		return fmt.Errorf("begin migration lock: %w", err)
	}
	return nil
}

func rollbackConn(conn *sql.Conn) {
	if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil && !strings.Contains(strings.ToLower(err.Error()), "no transaction") {
		slog.Warn("failed to rollback migration transaction", "error", err)
	}
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
	var v int
	err := conn.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations",
	).Scan(&v)
	return v, err
}

func loadMigrationFiles(fsys embed.FS, dir string) ([]migrationFile, error) {
	entries, err := fsys.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	upMap := make(map[int]string)
	downMap := make(map[int]string)
	nameMap := make(map[int]string)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		parts := strings.SplitN(name, "_", 2)
		if len(parts) < 2 {
			continue
		}
		version, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		rest := parts[1]
		var suffix string
		if strings.HasSuffix(rest, ".up.sql") {
			suffix = ".up.sql"
		} else if strings.HasSuffix(rest, ".down.sql") {
			suffix = ".down.sql"
		} else {
			continue
		}
		desc := strings.TrimSuffix(rest, suffix)
		content, err := fsys.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if suffix == ".up.sql" {
			if _, exists := upMap[version]; exists {
				return nil, fmt.Errorf("duplicate up migration version %d", version)
			}
			upMap[version] = string(content)
			nameMap[version] = desc
		} else {
			if _, exists := downMap[version]; exists {
				return nil, fmt.Errorf("duplicate down migration version %d", version)
			}
			downMap[version] = string(content)
		}
	}

	var files []migrationFile
	for version, upSQL := range upMap {
		files = append(files, migrationFile{
			version: version,
			up:      upSQL,
			down:    downMap[version],
			name:    nameMap[version],
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].version < files[j].version })
	return files, nil
}
