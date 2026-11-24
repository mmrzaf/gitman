package postgres

import (
	"context"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationLockKey is the PostgreSQL advisory lock that serializes
// migration across processes, so a web process and a worker starting at
// the same moment never apply the same migration twice.
const migrationLockKey int64 = 0x676974_6d616e // "gitman"

// migrationLockTimeout bounds waiting for another process to finish
// migrating, and lock_timeout bounds DDL waiting behind a long transaction.
// Startup must fail with a diagnosis rather than hang forever.
const migrationLockTimeout = 30 * time.Second

// ErrLegacyLedger marks a database created by a pre-1.0 beta. The beta
// ledger has no checksum and the consolidated 1.0 schema added columns the
// beta never recorded; 1.x refuses it rather than guessing.
var ErrLegacyLedger = errors.New("this database uses the pre-1.0 beta migration ledger")

type migration struct {
	version  int64
	name     string
	sql      string
	checksum string
}

// MigrationInfo names one applied, pending or newer-than-this-binary
// schema migration.
type MigrationInfo struct {
	Version int64
	Name    string
}

// MigrationStatus compares the schema recorded in the database with the
// migrations this binary embeds: applied, pending, and database-ahead
// (applied by a newer binary and unknown here). It is read-only, so a
// diagnostic can run beside a live process.
type MigrationStatus struct {
	Applied       []MigrationInfo
	Pending       []MigrationInfo
	DatabaseAhead []MigrationInfo
}

// migrationSchemaReader is the query surface shared by pooled and locked
// connections. Unknown schemas are refused without mutation.
type migrationSchemaReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// inspectMigrationSchema rejects unsupported schemas before the migration
// ledger is created. An existing installation must have a valid migration ledger.
func inspectMigrationSchema(ctx context.Context, q migrationSchemaReader) (bool, error) {
	var present bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_catalog.pg_class c
			JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = current_schema()
			  AND c.relname = 'schema_migrations' AND c.relkind IN ('r', 'p')
		)
	`).Scan(&present); err != nil {
		return false, fmt.Errorf("inspect migration ledger: %w", err)
	}
	if !present {
		var populated bool
		if err := q.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_catalog.pg_class c
				JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
				WHERE n.nspname = current_schema()
				  AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
			)
		`).Scan(&populated); err != nil {
			return false, fmt.Errorf("inspect existing schema: %w", err)
		}
		if populated {
			return false, fmt.Errorf("database schema is not empty and has no migration ledger; use an empty database or restore a compatible backup")
		}
		return false, nil
	}
	var hasChecksum bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = current_schema()
			  AND table_name = 'schema_migrations' AND column_name = 'checksum'
		)
	`).Scan(&hasChecksum); err != nil {
		return false, fmt.Errorf("inspect migration ledger columns: %w", err)
	}
	if !hasChecksum {
		return false, fmt.Errorf("%w; 1.x cannot upgrade it in place. Take a maintenance backup with the beta binary, then either install clean and push the repositories again, or follow docs/operator/backups-and-upgrades.md#upgrading-from-a-beta", ErrLegacyLedger)
	}
	return true, nil
}

func validateAppliedMigration(m migration, name, checksum string) error {
	if name != m.name || checksum != m.checksum {
		return fmt.Errorf("migration %d differs from the Gitman binary (recorded as %s); do not reuse or edit an applied migration", m.version, name)
	}
	return nil
}

// MigrationStatus reports the schema's relationship to this binary.
func (d *DB) MigrationStatus(ctx context.Context) (*MigrationStatus, error) {
	migrations, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	known := make(map[int64]migration, len(migrations))
	for _, m := range migrations {
		known[m.version] = m
	}
	status := &MigrationStatus{}
	present, err := inspectMigrationSchema(ctx, d.Q)
	if err != nil {
		return nil, err
	}
	applied := make(map[int64]bool, len(migrations))
	if present {
		rows, err := d.Q.Query(ctx, `SELECT version, name, checksum FROM schema_migrations ORDER BY version`)
		if err != nil {
			return nil, fmt.Errorf("read applied migrations: %w", err)
		}
		for rows.Next() {
			var version int64
			var name, checksum string
			if err := rows.Scan(&version, &name, &checksum); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan applied migration: %w", err)
			}
			applied[version] = true
			info := MigrationInfo{Version: version, Name: name}
			if migration, ok := known[version]; ok {
				if err := validateAppliedMigration(migration, name, checksum); err != nil {
					rows.Close()
					return nil, err
				}
				status.Applied = append(status.Applied, info)
			} else {
				status.DatabaseAhead = append(status.DatabaseAhead, info)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("read applied migrations: %w", err)
		}
	}
	for _, m := range migrations {
		if !applied[m.version] {
			status.Pending = append(status.Pending, MigrationInfo{Version: m.version, Name: m.name})
		}
	}
	return status, nil
}

// loadMigrations reads every embedded migration file and orders it by the
// numeric prefix in its filename (e.g. "0001_init.sql").
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}

	migrations := make([]migration, 0, len(entries))
	seen := make(map[int64]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := parseMigrationVersion(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("migration %s: %w", entry.Name(), err)
		}
		if other, dup := seen[version]; dup {
			return nil, fmt.Errorf("migrations %s and %s share version %d", other, entry.Name(), version)
		}
		seen[version] = entry.Name()
		content, err := fs.ReadFile(migrationFiles, "migrations/"+entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		migrations = append(migrations, migration{
			version: version, name: entry.Name(), sql: string(content),
			checksum: fmt.Sprintf("%x", sha256.Sum256(content)),
		})
	}

	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	return migrations, nil
}

func parseMigrationVersion(filename string) (int64, error) {
	prefix, _, ok := strings.Cut(filename, "_")
	if !ok {
		return 0, fmt.Errorf("filename must start with a numeric version followed by '_'")
	}
	version, err := strconv.ParseInt(prefix, 10, 64)
	if err != nil || version <= 0 {
		return 0, fmt.Errorf("version prefix %q is not a positive integer", prefix)
	}
	return version, nil
}

// Migrate applies every embedded migration not yet recorded in
// schema_migrations, in ascending order, each in its own transaction,
// while holding an advisory lock so concurrent callers apply each
// migration exactly once.
func (d *DB) Migrate(ctx context.Context) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}

	conn, err := d.Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for migration: %w", err)
	}
	defer conn.Release()

	lockCtx, cancelLock := context.WithTimeout(ctx, migrationLockTimeout)
	defer cancelLock()
	if _, err := conn.Exec(lockCtx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		return fmt.Errorf("take migration lock within %s: %w", migrationLockTimeout, err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := conn.Exec(cleanup, `SELECT pg_advisory_unlock($1)`, migrationLockKey); err != nil {
			_ = conn.Conn().Close(cleanup)
		}
	}()

	present, err := inspectMigrationSchema(ctx, conn)
	if err != nil {
		return err
	}
	if !present {
		if _, err := conn.Exec(ctx, `
			CREATE TABLE schema_migrations (
				version bigint PRIMARY KEY,
				name text NOT NULL,
				checksum text NOT NULL,
				applied_at timestamptz NOT NULL DEFAULT now()
			)
		`); err != nil {
			return fmt.Errorf("create schema_migrations: %w", err)
		}
	}

	applied := make(map[int64]bool)
	known := make(map[int64]migration, len(migrations))
	for _, m := range migrations {
		known[m.version] = m
	}
	rows, err := conn.Query(ctx, `SELECT version, name, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}
	for rows.Next() {
		var version int64
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum); err != nil {
			rows.Close()
			return fmt.Errorf("scan applied migration: %w", err)
		}
		applied[version] = true
		migration, ok := known[version]
		if !ok {
			rows.Close()
			return fmt.Errorf("database schema is ahead of this Gitman binary: migration %d (%s) is not embedded here; run a newer binary or restore a backup made by this version", version, name)
		}
		if err := validateAppliedMigration(migration, name, checksum); err != nil {
			rows.Close()
			return err
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", m.name, err)
		}
		if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '30s'`); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("set migration lock timeout: %w", err)
		}
		if _, err := tx.Exec(ctx, m.sql); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", m.name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)`, m.version, m.name, m.checksum); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", m.name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", m.name, err)
		}
	}
	return nil
}

// Tx runs fn inside a transaction, committing if fn returns nil and
// rolling back otherwise. Acquiring the connection it runs on is bounded
// the same way DB.Q is, via the same acquireConn; the transaction itself,
// once it has a connection, is not — it holds that one connection for its
// whole duration and releases it when done, same as DB.Q's own results do.
func (d *DB) Tx(ctx context.Context, fn func(tx Tx) error) error {
	conn, err := d.acquireConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}
