package repo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/activity"
	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/postgres"
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

func TestMaintainWaitsForTheMutationLockAndRecordsActivity(t *testing.T) {
	ctx := context.Background()
	db := pgtest.Open(t)
	store := newStore(t)
	s := NewService(db, store, "")
	r, err := s.Create(ctx, "maintained", "", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := s.git.MutationLock(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.Maintain(ctx, r.ID, "")
		done <- err
	}()
	select {
	case err := <-done:
		unlock()
		t.Fatalf("Maintain finished while another mutation held the lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatalf("Maintain after the lock was released: %v", err)
	}
	entries, err := activity.NewService(db).Recent(ctx, &r.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Action == activity.RepoGC {
			return
		}
	}
	t.Fatal("maintenance was not recorded in Activity")
}

func TestAbandonOperationRequiresAFailureAndUnblocksTheRepository(t *testing.T) {
	ctx := context.Background()
	db := pgtest.Open(t)
	store := newStore(t)
	s := NewService(db, store, "")
	op, err := s.BeginOperation(ctx, "abandon-me", "create", "abandon-me", "", repositoryIntent{Name: "abandon-me", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AbandonOperation(ctx, op.ID, "", "directory is corrupt"); !errors.Is(err, ErrOperationNotFailed) {
		t.Fatalf("abandon before a failure = %v, want ErrOperationNotFailed", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE repository_operations SET error='permission denied' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	abandoned, err := s.AbandonOperation(ctx, op.ID, "", "directory is corrupt")
	if err != nil {
		t.Fatal(err)
	}
	if abandoned.Error != "abandoned by the command line: directory is corrupt" {
		t.Fatalf("recorded reason = %q", abandoned.Error)
	}
	ops, err := s.PendingOperations(ctx)
	if err != nil || len(ops) != 0 {
		t.Fatalf("pending after abandon = %+v, %v", ops, err)
	}
	if err := s.WithMutation(ctx, op.RepoID, func() error { return nil }); err != nil {
		t.Fatalf("repository is still blocked: %v", err)
	}
	if _, err := s.AbandonOperation(ctx, "no-such-operation", "", "why"); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatalf("unknown operation = %v, want postgres.ErrNotFound", err)
	}
}

func TestAbandonOperationRefusesWhileTheRepositoryLockIsHeld(t *testing.T) {
	ctx := context.Background()
	db := pgtest.Open(t)
	store := newStore(t)
	s := NewService(db, store, "")
	op, err := s.BeginOperation(ctx, "abandon-live", "create", "abandon-live", "", repositoryIntent{Name: "abandon-live", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE repository_operations SET error='permission denied' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	unlock, err := s.git.MutationLock(ctx, op.RepoID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AbandonOperation(ctx, op.ID, "", "directory is corrupt"); apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("abandon while locked = %v, want conflict", err)
	}
	unlock()
	abandoned, err := s.AbandonOperation(ctx, op.ID, "", "directory is corrupt")
	if err != nil || abandoned == nil {
		t.Fatalf("abandon after unlock = %+v, %v", abandoned, err)
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
