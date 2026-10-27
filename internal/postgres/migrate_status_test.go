package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
)

func TestMigrationStatusReportsAppliedPendingAndAhead(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	status, err := database.MigrationStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Applied) == 0 || len(status.Pending) != 0 || len(status.DatabaseAhead) != 0 {
		t.Fatalf("fresh status = %+v", status)
	}
	if _, err := database.Pool.Exec(ctx, `INSERT INTO schema_migrations (version, name) VALUES (999, '999_from_the_future.sql')`); err != nil {
		t.Fatal(err)
	}
	status, err = database.MigrationStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.DatabaseAhead) != 1 || status.DatabaseAhead[0].Version != 999 {
		t.Fatalf("ahead = %+v", status.DatabaseAhead)
	}
	err = database.Migrate(ctx)
	if err == nil || !strings.Contains(err.Error(), "database schema is ahead") {
		t.Fatalf("Migrate with a newer schema = %v", err)
	}
}
