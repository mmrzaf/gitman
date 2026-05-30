package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
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

// runMigrations applies all pending up migrations inside one transaction.
// The schema change and schema_migrations insert are committed together, so a
// crash cannot leave an applied-but-unrecorded migration.
func (db *DB) runMigrations(ctx context.Context, migrationsFS embed.FS, dir string) error {
	files, err := loadMigrationFiles(migrationsFS, dir)
	if err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx)

	if err := ensureMigrationTableTx(ctx, tx); err != nil {
		return err
	}

	current, err := currentVersionTx(ctx, tx)
	if err != nil {
		return err
	}

	for _, m := range files {
		if m.version <= current {
			continue
		}
		slog.Info("applying migration", "version", m.version, "name", m.name)
		if _, err := tx.ExecContext(ctx, m.up); err != nil {
			return fmt.Errorf("migration %d (%s) failed: %w", m.version, m.name, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version) VALUES (?)", m.version); err != nil {
			return fmt.Errorf("record migration %d: %w", m.version, err)
		}
	}

	return tx.Commit()
}

// rollbackTo rolls back to a specific version (down migrations) inside one transaction.
func (db *DB) rollbackTo(ctx context.Context, migrationsFS embed.FS, dir string, targetVersion int) error {
	files, err := loadMigrationFiles(migrationsFS, dir)
	if err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx)

	if err := ensureMigrationTableTx(ctx, tx); err != nil {
		return err
	}

	current, err := currentVersionTx(ctx, tx)
	if err != nil {
		return err
	}
	if current <= targetVersion {
		return tx.Commit()
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
		if _, err := tx.ExecContext(ctx, m.down); err != nil {
			return fmt.Errorf("rollback of %d failed: %w", m.version, err)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version = ?", m.version); err != nil {
			return fmt.Errorf("delete migration record %d: %w", m.version, err)
		}
	}

	return tx.Commit()
}

func rollbackUnlessDone(tx *sql.Tx) {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		slog.Warn("failed to rollback transaction", "error", err)
	}
}

func ensureMigrationTableTx(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at INTEGER DEFAULT (strftime('%s', 'now'))
		)
	`)
	return err
}

func currentVersionTx(ctx context.Context, tx *sql.Tx) (int, error) {
	var v int
	err := tx.QueryRowContext(ctx,
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
			upMap[version] = string(content)
			nameMap[version] = desc
		} else {
			downMap[version] = string(content)
		}
	}

	var files []migrationFile
	for v, upSQL := range upMap {
		files = append(files, migrationFile{
			version: v,
			up:      upSQL,
			down:    downMap[v],
			name:    nameMap[v],
		})
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].version < files[j].version
	})
	return files, nil
}
