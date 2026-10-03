// Package pgtest gives each test an isolated disposable PostgreSQL schema.
package pgtest

import (
	"context"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/postgres/testschema"
	"testing"
	"time"
)

func Open(t *testing.T) *postgres.DB {
	t.Helper()
	dsn := testschema.DSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	database, err := postgres.Connect(ctx, dsn, postgres.Options{MaxConns: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return database
}
