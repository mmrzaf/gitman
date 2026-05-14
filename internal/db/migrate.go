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

// runMigrations applies all pending up migrations.
func (db *DB) runMigrations(ctx context.Context, migrationsFS embed.FS, dir string) error {
	if err := ensureMigrationTable(ctx, db.DB); err != nil {
		return err
	}

	current, err := currentVersion(ctx, db.DB)
	if err != nil {
		return err
	}

	files, err := loadMigrationFiles(migrationsFS, dir)
	if err != nil {
		return err
	}

	for _, m := range files {
		if m.version <= current {
			continue
		}
		slog.Info("applying migration", "version", m.version, "name", m.name)
		if err := db.applyUp(ctx, m); err != nil {
			return fmt.Errorf("migration %d (%s) failed: %w", m.version, m.name, err)
		}
		if err := recordMigration(ctx, db.DB, m.version); err != nil {
			return err
		}
	}
	return nil
}

// rollbackTo rolls back to a specific version (down migrations).
func (db *DB) rollbackTo(ctx context.Context, migrationsFS embed.FS, dir string, targetVersion int) error {
	if err := ensureMigrationTable(ctx, db.DB); err != nil {
		return err
	}

	current, err := currentVersion(ctx, db.DB)
	if err != nil {
		return err
	}

	if current <= targetVersion {
		return nil
	}

	files, err := loadMigrationFiles(migrationsFS, dir)
	if err != nil {
		return err
	}

	for i := len(files) - 1; i >= 0; i-- {
		m := files[i]
		if m.version > current || m.version <= targetVersion {
			continue
		}
		slog.Info("rolling back migration", "version", m.version, "name", m.name)
		if err := db.applyDown(ctx, m); err != nil {
			return fmt.Errorf("rollback of %d failed: %w", m.version, err)
		}
		if err := deleteMigration(ctx, db.DB, m.version); err != nil {
			return err
		}
	}
	return nil
}

func ensureMigrationTable(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
        CREATE TABLE IF NOT EXISTS schema_migrations (
            version    INTEGER PRIMARY KEY,
            applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
        )
    `)
	return err
}

func currentVersion(ctx context.Context, db *sql.DB) (int, error) {
	var v int
	err := db.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations",
	).Scan(&v)
	return v, err
}

func recordMigration(ctx context.Context, db *sql.DB, version int) error {
	_, err := db.ExecContext(ctx,
		"INSERT INTO schema_migrations (version) VALUES (?)", version)
	return err
}

func deleteMigration(ctx context.Context, db *sql.DB, version int) error {
	_, err := db.ExecContext(ctx,
		"DELETE FROM schema_migrations WHERE version = ?", version)
	return err
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
		downSQL := downMap[v]
		files = append(files, migrationFile{
			version: v,
			up:      upSQL,
			down:    downSQL,
			name:    nameMap[v],
		})
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].version < files[j].version
	})
	return files, nil
}

func (db *DB) applyUp(ctx context.Context, m migrationFile) error {
	return db.execInTransaction(ctx, m.up)
}

func (db *DB) applyDown(ctx context.Context, m migrationFile) error {
	if m.down == "" {
		return fmt.Errorf("no down migration for version %d", m.version)
	}
	return db.execInTransaction(ctx, m.down)
}

func (db *DB) execInTransaction(ctx context.Context, sqlStr string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	if _, err := tx.ExecContext(ctx, sqlStr); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
