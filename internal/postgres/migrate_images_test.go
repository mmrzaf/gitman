package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mmrzaf/gitman/internal/postgres/testschema"
)

// TestImageMigrationUpgradesThePreviousSchema applies the migrations that
// existed before images, then runs Migrate as a real upgrade would. Existing
// repository operations must survive the widened constraint and the new
// nullable repository column.
func TestImageMigrationUpgradesThePreviousSchema(t *testing.T) {
	ctx := context.Background()
	dsn := testschema.DSN(t)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, `CREATE TABLE schema_migrations (
		version bigint PRIMARY KEY, name text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.version > 4 {
			break
		}
		if _, err := pool.Exec(ctx, migration.sql); err != nil {
			t.Fatalf("apply %s: %v", migration.name, err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, migration.version, migration.name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO repos (id, name) VALUES ('r1', 'demo')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO repository_operations (id, repo_id, kind, name, payload)
		VALUES ('op1', 'r1', 'head', '', '{}')
	`); err != nil {
		t.Fatal(err)
	}

	database, err := Connect(ctx, dsn, Options{MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Migrate on the previous schema: %v", err)
	}

	var version int64
	if err := database.Q.QueryRow(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version < 5 {
		t.Fatalf("migrated version = %d, want at least 5", version)
	}
	var repoID string
	if err := database.Q.QueryRow(ctx, `SELECT repo_id FROM repository_operations WHERE id = 'op1'`).Scan(&repoID); err != nil {
		t.Fatal(err)
	}
	if repoID != "r1" {
		t.Fatalf("existing operation repo = %q, want r1", repoID)
	}
	if _, err := database.Q.Exec(ctx, `
		INSERT INTO repository_operations (id, repo_id, kind, name, payload, engine_id)
		VALUES ('op2', NULL, 'image.remove', 'engine', '{}', 'engine')
	`); err != nil {
		t.Fatalf("insert image operation after upgrade: %v", err)
	}
	if _, err := database.Q.Exec(ctx, `
		INSERT INTO repository_operations (id, repo_id, kind, name, payload)
		VALUES ('op3', NULL, 'head', '', '{}')
	`); err == nil {
		t.Fatal("a repository operation without a repository was accepted")
	}
	var hosts int
	if err := database.Q.QueryRow(ctx, `SELECT count(*) FROM image_hosts`).Scan(&hosts); err != nil || hosts != 0 {
		t.Fatalf("image_hosts count = %d, %v", hosts, err)
	}
}
