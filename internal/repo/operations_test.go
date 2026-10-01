package repo

import (
	"context"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
	"testing"
)

func TestRecoverCreateAfterFilesystemCommit(t *testing.T) {
	ctx := context.Background()
	db := pgtest.Open(t)
	store := newStore(t)
	s := NewService(db, store, "")
	op, err := s.BeginOperation(ctx, "recover-create", "create", "recovered", "", repositoryIntent{Name: "recovered", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, op.RepoID, "main"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.RecoverOperations(ctx, nil); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.GetByName(ctx, "recovered")
	if err != nil || r.ID != op.RepoID {
		t.Fatalf("recovered=%+v error=%v", r, err)
	}
	ops, err := s.PendingOperations(ctx)
	if err != nil || len(ops) != 0 {
		t.Fatalf("pending=%v error=%v", ops, err)
	}
}

func TestRecoverDeleteAfterFilesystemRemoval(t *testing.T) {
	ctx := context.Background()
	db := pgtest.Open(t)
	store := newStore(t)
	s := NewService(db, store, "")
	r, err := s.Create(ctx, "deleted", "", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.BeginOperation(ctx, r.ID, "delete", "", "", repositoryIntent{Name: r.Name})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(r.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverOperations(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetByID(ctx, r.ID); err == nil {
		t.Fatal("deleted repository row survives recovery")
	}
}
