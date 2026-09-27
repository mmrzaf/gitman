package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mmrzaf/gitman/internal/postgres"
	"github.com/mmrzaf/gitman/internal/postgres/pgtest"
)

func TestCreateAndAuthenticateToken(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)

	p, err := svc.Create(ctx, "darius", "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}

	plain, token, err := svc.CreateToken(ctx, p.ID, "CI", ScopeRead, nil)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if plain == "" {
		t.Fatal("expected a non-empty plain token")
	}
	if token.ExpiresAt != nil {
		t.Fatal("expected no expiry when ttl is nil")
	}

	got, scope, err := svc.Authenticate(ctx, plain)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got.ID != p.ID || scope != ScopeRead {
		t.Errorf("Authenticate = %s, %s", got.ID, scope)
	}
	listedAll, err := svc.ListTokens(ctx, p.ID)
	if err != nil || len(listedAll) != 1 {
		t.Fatalf("ListTokens = %v, %v", listedAll, err)
	}
	listed := listedAll[0]
	if err != nil || listed.LastUsedAt == nil {
		t.Errorf("expected last_used_at to be set after authenticating, got %+v, %v", listed, err)
	}
}

func TestCreateTokenRejectsBadName(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)
	p, err := svc.Create(ctx, "darius", "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateToken(ctx, p.ID, "   ", ScopeRead, nil); err == nil {
		t.Error("expected an error for a blank token name")
	}
}

func TestExpiredTokenFails(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)

	p, err := svc.Create(ctx, "darius", "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}
	past := -time.Hour
	plain, _, err := svc.CreateToken(ctx, p.ID, "CI", ScopeRead, &past)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Authenticate(ctx, plain); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Authenticate(expired) = %v, want ErrInvalidToken", err)
	}
}

func TestTokenForDisabledPersonFails(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)

	p, err := svc.Create(ctx, "darius", "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}
	plain, _, err := svc.CreateToken(ctx, p.ID, "CI", ScopeRead, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Disable(ctx, p.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Authenticate(ctx, plain); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Authenticate(disabled owner) = %v, want ErrInvalidToken", err)
	}
}

func TestAuthenticateUnknownToken(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)

	if _, _, err := svc.Authenticate(ctx, "not-a-real-token"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Authenticate(unknown) = %v, want ErrInvalidToken", err)
	}
}

func TestListAndRevokeToken(t *testing.T) {
	ctx := context.Background()
	database := pgtest.Open(t)
	svc := NewService(database)

	p, err := svc.Create(ctx, "darius", "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := svc.Create(ctx, "sara", "correct-horse-battery", false, "")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := svc.CreateToken(ctx, p.ID, "CI", ScopeRead, nil)
	if err != nil {
		t.Fatal(err)
	}

	tokens, err := svc.ListTokens(ctx, p.ID)
	if err != nil || len(tokens) != 1 || tokens[0].ID != token.ID {
		t.Fatalf("ListTokens = %v, %v", tokens, err)
	}

	if _, err := svc.RevokeToken(ctx, other.ID, token.ID); !errors.Is(err, ErrNotTokenOwner) {
		t.Fatalf("RevokeToken by someone else = %v, want ErrNotTokenOwner", err)
	}
	revoked, err := svc.RevokeToken(ctx, p.ID, token.ID)
	if err != nil || revoked.Name != "CI" {
		t.Fatalf("RevokeToken = %+v, %v", revoked, err)
	}
	if _, err := svc.RevokeToken(ctx, p.ID, token.ID); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatalf("second RevokeToken = %v, want postgres.ErrNotFound", err)
	}
}

func TestScopeSatisfies(t *testing.T) {
	if !ScopeWrite.Satisfies(ScopeRead) {
		t.Error("expected write to satisfy read")
	}
	if !ScopeWrite.Satisfies(ScopeWrite) {
		t.Error("expected write to satisfy write")
	}
	if !ScopeRead.Satisfies(ScopeRead) {
		t.Error("expected read to satisfy read")
	}
	if ScopeRead.Satisfies(ScopeWrite) {
		t.Error("expected read not to satisfy write")
	}
}
