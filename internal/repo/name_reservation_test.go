package repo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/apperr"
	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
)

func TestUnfinishedRepositoryCreateReservesItsName(t *testing.T) {
	ctx := context.Background()
	db := pgtest.Open(t)
	store := newStore(t)
	s := NewService(db, store, "")
	const originalID = "pending-create"
	_, err := s.BeginOperation(ctx, originalID, "create", "example", "", repositoryIntent{Name: "example", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "Example", "", "main", ""); apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("name was reused while creation was pending: %v", err)
	}
	var operations int
	if err := db.Q.QueryRow(ctx, `SELECT count(*) FROM repository_operations WHERE kind='create' AND name='example'`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if operations != 1 {
		t.Fatalf("second create journaled a competing operation: %d", operations)
	}
	if err := s.RecoverOperations(ctx, nil); err != nil {
		t.Fatal(err)
	}
	r, err := s.GetByName(ctx, "example")
	if err != nil || r.ID != originalID {
		t.Fatalf("incorrect recovered owner: %+v %v", r, err)
	}
	if _, err := s.Create(ctx, "example", "", "main", ""); !errors.Is(err, postgres.ErrAlreadyExists) {
		t.Fatalf("name reusable after recovery: %v", err)
	}
}

func TestRepositoryCreateRecoveryWaitsForNameLock(t *testing.T) {
	ctx := context.Background()
	db := pgtest.Open(t)
	store := newStore(t)
	s := NewService(db, store, "")
	_, err := s.BeginOperation(ctx, "recovery-lock", "create", "locked", "", repositoryIntent{Name: "locked", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := store.MutationLock(ctx, createNameLockKey("locked"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.RecoverOperations(ctx, nil) }()
	select {
	case err := <-done:
		unlock()
		t.Fatalf("recovery bypassed live creator's name lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	r, err := s.GetByName(ctx, "locked")
	if err != nil || r.ID != "recovery-lock" {
		t.Fatalf("recovered name after lock release: %+v %v", r, err)
	}
}
