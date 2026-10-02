// Package testschema owns disposable schemas in an explicitly named test database.
package testschema

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mmrzaf/gitman/internal/id"
)

// DSN creates a schema and registers its removal. Every returned DSN also gets
// a unique application_name so tests can identify and administer only the
// connections they own. Register application pool cleanup after this call so
// its connections close before the schema is removed.
func DSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("GITMAN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("GITMAN_TEST_DATABASE_URL not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(cfg.ConnConfig.Database, "_test") {
		t.Fatal("test database name must end in _test")
	}
	cfg.MaxConns = 1
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	control, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	testID := strings.ReplaceAll(id.New(), "-", "_")
	schema := pgx.Identifier{"test_" + testID}.Sanitize()
	applicationName := "gitman_test_" + testID
	if _, err := control.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		control.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		defer control.Close()
		if _, err := control.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})
	name := strings.Trim(schema, "\"")
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", name)
		q.Set("application_name", applicationName)
		u.RawQuery = q.Encode()
		return u.String()
	}
	return dsn + " search_path=" + name + " application_name=" + applicationName
}
