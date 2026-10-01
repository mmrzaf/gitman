package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
)

func TestSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)

	p, err := svc.Create(ctx, "darius", "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}

	token, expiresAt, err := svc.CreateSession(ctx, p, time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if token == "" {
		t.Fatal("expected a non-empty token")
	}
	if time.Until(expiresAt) <= 0 {
		t.Fatal("expected expiresAt to be in the future")
	}

	got, err := svc.SessionPerson(ctx, token)
	if err != nil {
		t.Fatalf("SessionPerson: %v", err)
	}
	if got.ID != p.ID {
		t.Error("SessionPerson returned a different person")
	}

	if err := svc.DeleteSession(ctx, token); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := svc.SessionPerson(ctx, token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("SessionPerson after delete = %v, want ErrInvalidSession", err)
	}
}

func TestDeleteSessionOfUnknownTokenIsNotAnError(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)

	if err := svc.DeleteSession(ctx, "not-a-real-token"); err != nil {
		t.Fatalf("DeleteSession(unknown token) = %v, want nil", err)
	}
}

func TestSessionPersonUnknownToken(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)

	if _, err := svc.SessionPerson(ctx, "not-a-real-token"); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("SessionPerson(unknown) = %v, want ErrInvalidSession", err)
	}
}

func TestSessionExpires(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)

	p, err := svc.Create(ctx, "darius", "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := svc.CreateSession(ctx, p, -time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SessionPerson(ctx, token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("SessionPerson(already expired) = %v, want ErrInvalidSession", err)
	}
}
