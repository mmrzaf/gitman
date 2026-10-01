package postgres

import (
	"os"
	"path/filepath"
	"testing"
)

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
