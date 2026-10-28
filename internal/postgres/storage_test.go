package postgres

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBindRepositoryStorageAllowsPendingDeleteWithoutDirectory(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO repos (id, name) VALUES ('gone', 'gone')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO repository_operations (id, repo_id, kind, name, payload) VALUES ('op', 'gone', 'delete', 'gone', '{}'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	if err := db.BindRepositoryStorage(ctx, filepath.Join(t.TempDir(), "repos")); err != nil {
		t.Fatalf("binding with a pending delete: %v", err)
	}

	other := testDB(t)
	if err := other.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Pool.Exec(ctx, `INSERT INTO repos (id, name) VALUES ('kept', 'kept')`); err != nil {
		t.Fatal(err)
	}
	if err := other.BindRepositoryStorage(ctx, filepath.Join(t.TempDir(), "repos")); err == nil {
		t.Fatal("accepted a missing repository without a pending delete")
	}
}

func TestRepositoryStorageCanMoveAndRejectsAnotherInstance(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	root := filepath.Join(parent, "original")
	if err := db.BindRepositoryStorage(ctx, root); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(parent, "moved")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := db.BindRepositoryStorage(ctx, moved); err != nil {
		t.Fatalf("moving storage: %v", err)
	}
	other := testDB(t)
	if err := other.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := other.BindRepositoryStorage(ctx, moved); err == nil {
		t.Fatal("accepted another instance's storage")
	}
}
