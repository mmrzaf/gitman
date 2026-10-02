package repo

import (
	"context"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
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

func TestRecoverPushUsesExecutionBudgetAfterLock(t *testing.T) {
	ctx := context.Background()
	db := pgtest.Open(t)
	store := newStore(t)
	s := NewService(db, store, "")
	op, err := s.BeginOperation(ctx, "recover-push", "push", "", "", map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	err = s.RecoverOperations(ctx, func(recoverCtx context.Context, recovered Operation) error {
		called = true
		if recovered.ID != op.ID {
			t.Fatalf("recovered operation = %s, want %s", recovered.ID, op.ID)
		}
		deadline, ok := recoverCtx.Deadline()
		if !ok {
			t.Fatal("push recovery context has no deadline")
		}
		if remaining := time.Until(deadline); remaining < 4*time.Minute {
			t.Fatalf("push recovery budget = %v, want several minutes after lock acquisition", remaining)
		}
		_, err := db.Pool.Exec(recoverCtx, `UPDATE repository_operations SET completed_at=now() WHERE id=$1`, recovered.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("push recovery callback was not called")
	}
}
